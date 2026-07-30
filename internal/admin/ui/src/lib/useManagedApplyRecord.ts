/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useCallback, useEffect, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchManagedApply,
  type ManagedApplyLookup,
  type ManagedApplyRecord,
} from "@/api/client.ts";

// useManagedApplyRecord centralizes the exact-ID managed-apply polling contract
// (AC-08/AC-09) so ConfigPanel and HistoryPanel observe the same lifecycle
// through one reusable hook instead of two divergent, hand-rolled poll loops.
// The time/deadline/grace/expiry decisions live in the pure functions below so
// they are unit-testable without React or a fake HTTP layer; the hook only wires
// those decisions to React Query.

// Poll cadence (AC-09): a fast 1 s cadence for the first 10 s, then a 2 s cadence
// thereafter, bounded by the transaction deadline.
export const POLL_FAST_INTERVAL_MS = 1000;
export const POLL_SLOW_INTERVAL_MS = 2000;
export const POLL_FAST_WINDOW_MS = 10_000;

// Stop margin past the ledger deadline so an in-flight finalization that lands
// right at the deadline is still observed before the console gives up.
export const DEADLINE_MARGIN_MS = 5000;

// Conservative compatibility ceiling used only when the server recorded no
// bounded deadline for the transaction, so polling can never run unbounded.
export const COMPAT_CEILING_MS = 60_000;

// A short visibility grace period: the first three 404s after an accepted apply
// are treated as "not yet recorded", not as a missing/expired transaction.
export const MISSING_GRACE_ATTEMPTS = 3;

export type ManagedApplyPollStatus =
  | "idle"
  | "polling"
  | "terminal"
  | "missing-grace"
  | "expired"
  | "error";

export interface ManagedApplyPollState {
  readonly status: ManagedApplyPollStatus;
  readonly record: ManagedApplyRecord | null;
  readonly error: Error | null;
  readonly retry: () => void;
}

// APPLY_INSTANCE_RE matches the boot-scoped managed apply ID grammar
// rl_<12-hex-instance>_<sequence>. Legacy rl_<sequence> IDs have no instance
// component and are intentionally not matched.
const APPLY_INSTANCE_RE = /^rl_([0-9a-f]{12})_[0-9]+$/;

/**
 * parseApplyInstance extracts the 12-hex boot-scoped instance component of a
 * managed apply ID, or null for a legacy (rl_<sequence>) or malformed ID whose
 * process origin cannot be determined.
 */
export function parseApplyInstance(id: string): string | null {
  const match = APPLY_INSTANCE_RE.exec(id);
  return match?.[1] ?? null;
}

/**
 * isBootComponentMismatched reports whether the awaited apply ID was minted by a
 * different process instance than the one currently serving. It is used only to
 * decide, after the visibility grace period, that a persistently missing record
 * belongs to a process that has since restarted (so the record is gone for good
 * rather than merely not yet visible). It returns false — never claiming a
 * mismatch — when either ID is absent or the apply ID is legacy/unparseable, so
 * an unknown origin degrades to deadline-bounded polling instead of a premature
 * expiry.
 */
export function isBootComponentMismatched(
  applyID: string | undefined,
  currentInstanceID: string | undefined,
): boolean {
  if (applyID === undefined || currentInstanceID === undefined) return false;
  const instance = parseApplyInstance(applyID);
  if (instance === null) return false;
  return instance !== currentInstanceID;
}

/**
 * computeEffectiveDeadlineMs returns the absolute epoch-ms instant at which
 * polling must stop: the ledger deadline (preferred) or the caller's fallback
 * deadline, plus a fixed margin; or, when no bounded deadline exists, a
 * conservative compatibility ceiling measured from when polling began.
 */
export function computeEffectiveDeadlineMs(input: {
  readonly recordDeadline?: string | undefined;
  readonly fallbackDeadline?: string | undefined;
  readonly startedAtMs: number;
  readonly nowMs: number;
}): number {
  const iso = input.recordDeadline ?? input.fallbackDeadline;
  if (iso !== undefined) {
    const parsed = Date.parse(iso);
    if (!Number.isNaN(parsed)) return parsed + DEADLINE_MARGIN_MS;
  }
  const base = input.startedAtMs > 0 ? input.startedAtMs : input.nowMs;
  return base + COMPAT_CEILING_MS;
}

/**
 * computePollDelayMs returns the next poll delay for a given elapsed time since
 * polling began: the fast cadence within the initial window, the slow cadence
 * afterwards.
 */
export function computePollDelayMs(elapsedMs: number): number {
  return elapsedMs < POLL_FAST_WINDOW_MS ? POLL_FAST_INTERVAL_MS : POLL_SLOW_INTERVAL_MS;
}

/**
 * deriveManagedApplyStatus maps a lookup snapshot onto the poll status. It is the
 * single source of truth for the invariant that a missing, expired, or errored
 * lookup is NEVER reported as terminal (success): only a `record` whose state is
 * "terminal" yields "terminal".
 */
export function deriveManagedApplyStatus(input: {
  readonly enabled: boolean;
  readonly lookup: ManagedApplyLookup | undefined;
  readonly error: Error | null;
  readonly missingCount: number;
  readonly nowMs: number;
  readonly effectiveDeadlineMs: number;
  readonly bootMismatch: boolean;
}): ManagedApplyPollStatus {
  if (!input.enabled) return "idle";
  if (input.error) return "error";
  const lookup = input.lookup;
  if (lookup?.kind === "record") {
    if (lookup.record.state === "terminal") return "terminal";
    return input.nowMs >= input.effectiveDeadlineMs ? "expired" : "polling";
  }
  if (lookup?.kind === "missing") {
    if (input.missingCount > MISSING_GRACE_ATTEMPTS && input.bootMismatch) return "expired";
    if (input.nowMs >= input.effectiveDeadlineMs) return "expired";
    if (input.missingCount <= MISSING_GRACE_ATTEMPTS) return "missing-grace";
    return "polling";
  }
  return "polling";
}

/**
 * useManagedApplyRecord polls GET /api/config/applies/{id} for the exact awaited
 * transaction until it reaches a terminal record, its deadline elapses, or its
 * originating process has restarted. It fetches immediately, honours the ledger
 * deadline, tolerates a short 404 grace window, and exposes an explicit retry.
 * It never converts a missing, expired, or errored lookup into a success.
 */
export function useManagedApplyRecord(
  applyID: string | undefined,
  fallbackDeadline?: string,
  currentInstanceID?: string,
): ManagedApplyPollState {
  // startedAt anchors both the poll cadence and the no-deadline ceiling; missing
  // counts the consecutive 404s driving the grace/expiry decision. Refs, not
  // state, so reads inside the refetchInterval callback see the latest value
  // without forcing a re-render on every poll.
  const startedAtRef = useRef(0);
  const missingCountRef = useRef(0);
  const enabled = applyID !== undefined;

  useEffect(() => {
    startedAtRef.current = 0;
    missingCountRef.current = 0;
  }, [applyID]);

  const query = useQuery<ManagedApplyLookup>({
    queryKey: ["managed-apply-record", applyID],
    queryFn: async () => {
      if (startedAtRef.current === 0) startedAtRef.current = Date.now();
      const lookup = await fetchManagedApply(applyID as string);
      if (lookup.kind === "missing") missingCountRef.current += 1;
      else missingCountRef.current = 0;
      return lookup;
    },
    enabled,
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false,
    refetchInterval: (q) => {
      const data = q.state.data;
      if (data?.kind === "record" && data.record.state === "terminal") return false;
      const now = Date.now();
      const startedAt = startedAtRef.current || now;
      const deadlineMs = computeEffectiveDeadlineMs({
        recordDeadline: data?.kind === "record" ? data.record.deadline : undefined,
        fallbackDeadline,
        startedAtMs: startedAt,
        nowMs: now,
      });
      if (now >= deadlineMs) return false;
      if (
        data?.kind === "missing" &&
        missingCountRef.current > MISSING_GRACE_ATTEMPTS &&
        isBootComponentMismatched(applyID, currentInstanceID)
      ) {
        return false;
      }
      return computePollDelayMs(now - startedAt);
    },
  });

  const retry = useCallback(() => {
    startedAtRef.current = 0;
    missingCountRef.current = 0;
    void query.refetch();
  }, [query]);

  const lookup = query.data;
  const now = Date.now();
  const startedAt = startedAtRef.current || now;
  const effectiveDeadlineMs = computeEffectiveDeadlineMs({
    recordDeadline: lookup?.kind === "record" ? lookup.record.deadline : undefined,
    fallbackDeadline,
    startedAtMs: startedAt,
    nowMs: now,
  });
  const status = deriveManagedApplyStatus({
    enabled,
    lookup,
    error: query.error ?? null,
    missingCount: missingCountRef.current,
    nowMs: now,
    effectiveDeadlineMs,
    bootMismatch: isBootComponentMismatched(applyID, currentInstanceID),
  });

  return {
    status,
    record: lookup?.kind === "record" ? lookup.record : null,
    error: query.error ?? null,
    retry,
  };
}
