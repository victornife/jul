/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Tests for the centralized exact-ID managed-apply polling contract (WS04
 * Slice 1): the discriminated fetchManagedApply lookup, the pure
 * time/deadline/grace/expiry helpers, and the useManagedApplyRecord hook wiring.
 * The core invariant proven throughout: a missing, expired, or errored lookup is
 * NEVER reported as terminal (success).
 */
import type { ReactNode } from "react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ManagedApplyRecordSchema, fetchManagedApply } from "@/api/client.ts";
import {
  COMPAT_CEILING_MS,
  DEADLINE_MARGIN_MS,
  MISSING_GRACE_ATTEMPTS,
  POLL_FAST_INTERVAL_MS,
  POLL_FAST_WINDOW_MS,
  POLL_SLOW_INTERVAL_MS,
  computeEffectiveDeadlineMs,
  computePollDelayMs,
  deriveManagedApplyStatus,
  isBootComponentMismatched,
  parseApplyInstance,
  useManagedApplyRecord,
} from "@/lib/useManagedApplyRecord.ts";

const realFetch = globalThis.fetch;

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const BOOT_ID = "rl_9f01c0b451d2_42";

function terminalRecord(id: string, deadline?: string): unknown {
  return {
    id,
    state: "terminal",
    operation: "config.apply",
    ...(deadline !== undefined ? { deadline } : {}),
    result: { ok: true, apply_id: id, mode: "hot" },
  };
}

function pendingRecord(id: string, deadline?: string): unknown {
  return {
    id,
    state: "pending",
    operation: "config.apply",
    ...(deadline !== undefined ? { deadline } : {}),
    result: { ok: true, apply_id: id, mode: "hot" },
  };
}

function finalizingRecord(id: string, deadline?: string): unknown {
  return {
    id,
    state: "finalizing",
    operation: "config.apply",
    ...(deadline !== undefined ? { deadline } : {}),
    result: { ok: true, apply_id: id, mode: "hot" },
  };
}

// parsedRecord runs a raw fixture through the real schema so the derive-status
// tests hold a full ManagedApplyRecord rather than a hand-typed partial.
function parsedRecord(raw: unknown) {
  return ManagedApplyRecordSchema.parse(raw);
}

beforeEach(() => {
  globalThis.fetch = realFetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("fetchManagedApply — discriminated lookup", () => {
  it("returns kind=record for a terminal (200) transaction", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(json(terminalRecord(BOOT_ID))));
    const lookup = await fetchManagedApply(BOOT_ID);
    expect(lookup.kind).toBe("record");
    if (lookup.kind === "record") {
      expect(lookup.record.id).toBe(BOOT_ID);
      expect(lookup.record.state).toBe("terminal");
    }
  });

  it("returns kind=record for a pending (202) transaction", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(json(pendingRecord(BOOT_ID), 202)));
    const lookup = await fetchManagedApply(BOOT_ID);
    expect(lookup.kind).toBe("record");
    if (lookup.kind === "record") expect(lookup.record.state).toBe("pending");
  });

  it("returns kind=record for a finalizing (202) transaction", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(json(finalizingRecord(BOOT_ID), 202)));
    const lookup = await fetchManagedApply(BOOT_ID);
    expect(lookup.kind).toBe("record");
    if (lookup.kind === "record") expect(lookup.record.state).toBe("finalizing");
  });

  it("returns kind=missing for a 404 — never a record", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(new Response(null, { status: 404 })));
    const lookup = await fetchManagedApply(BOOT_ID);
    expect(lookup).toEqual({ kind: "missing" });
  });

  it("still throws for a non-404 transport failure", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(json({ error: "bad id" }, 400)));
    await expect(fetchManagedApply(BOOT_ID)).rejects.toThrow();
  });
});

describe("ManagedApplyRecordSchema — finalization panic fallback", () => {
  it("parses a terminal record that carries a finalization_error", () => {
    // The panic fallback preserves the operation and complete result and adds a
    // finalization_error; it must remain parseable by the wire schema.
    const raw = {
      id: BOOT_ID,
      state: "terminal",
      operation: "config.apply",
      result: { ok: true, apply_id: BOOT_ID, mode: "hot" },
      finalization_error: "managed apply finalization panic: boom",
    };
    const record = ManagedApplyRecordSchema.parse(raw);
    expect(record.state).toBe("terminal");
    expect(record.operation).toBe("config.apply");
    expect(record.result.apply_id).toBe(BOOT_ID);
    expect(record.finalization_error).toContain("finalization panic");
    for (const forbidden of ["owner_token_id", "token_digest", "source_ip", "previous_raw"]) {
      expect(Object.keys(record)).not.toContain(forbidden);
    }
  });
});

describe("parseApplyInstance / isBootComponentMismatched", () => {
  it("extracts the 12-hex boot instance from a boot-scoped id", () => {
    expect(parseApplyInstance("rl_9f01c0b451d2_42")).toBe("9f01c0b451d2");
  });

  it("returns null for a legacy or malformed id", () => {
    expect(parseApplyInstance("rl_7")).toBeNull();
    expect(parseApplyInstance("rl_9f01c0b451d_42")).toBeNull(); // 11-char instance
    expect(parseApplyInstance("rl_9f01c0b451dz_42")).toBeNull(); // non-hex
    expect(parseApplyInstance("garbage")).toBeNull();
  });

  it("reports a mismatch only when both instances are known and differ", () => {
    expect(isBootComponentMismatched("rl_9f01c0b451d2_42", "9f01c0b451d2")).toBe(false);
    expect(isBootComponentMismatched("rl_9f01c0b451d2_42", "aaaaaaaaaaaa")).toBe(true);
    // Unknown current instance or legacy id degrades to "no mismatch".
    expect(isBootComponentMismatched("rl_9f01c0b451d2_42", undefined)).toBe(false);
    expect(isBootComponentMismatched("rl_7", "9f01c0b451d2")).toBe(false);
    expect(isBootComponentMismatched(undefined, "9f01c0b451d2")).toBe(false);
  });
});

describe("computeEffectiveDeadlineMs", () => {
  it("prefers the record deadline plus the fixed margin", () => {
    const deadline = new Date(10_000).toISOString();
    const fallback = new Date(50_000).toISOString();
    expect(
      computeEffectiveDeadlineMs({
        recordDeadline: deadline,
        fallbackDeadline: fallback,
        startedAtMs: 1000,
        nowMs: 2000,
      }),
    ).toBe(10_000 + DEADLINE_MARGIN_MS);
  });

  it("falls back to the caller deadline when the record has none", () => {
    const fallback = new Date(50_000).toISOString();
    expect(
      computeEffectiveDeadlineMs({
        recordDeadline: undefined,
        fallbackDeadline: fallback,
        startedAtMs: 1000,
        nowMs: 2000,
      }),
    ).toBe(50_000 + DEADLINE_MARGIN_MS);
  });

  it("uses the compatibility ceiling from start when no bounded deadline exists", () => {
    expect(
      computeEffectiveDeadlineMs({
        recordDeadline: undefined,
        fallbackDeadline: undefined,
        startedAtMs: 1000,
        nowMs: 2000,
      }),
    ).toBe(1000 + COMPAT_CEILING_MS);
  });

  it("anchors the ceiling to now when polling has not started", () => {
    expect(
      computeEffectiveDeadlineMs({
        recordDeadline: undefined,
        fallbackDeadline: "not-a-date",
        startedAtMs: 0,
        nowMs: 2000,
      }),
    ).toBe(2000 + COMPAT_CEILING_MS);
  });
});

describe("computePollDelayMs", () => {
  it("uses the fast cadence inside the initial window", () => {
    expect(computePollDelayMs(0)).toBe(POLL_FAST_INTERVAL_MS);
    expect(computePollDelayMs(POLL_FAST_WINDOW_MS - 1)).toBe(POLL_FAST_INTERVAL_MS);
  });

  it("uses the slow cadence at and beyond the initial window", () => {
    expect(computePollDelayMs(POLL_FAST_WINDOW_MS)).toBe(POLL_SLOW_INTERVAL_MS);
    expect(computePollDelayMs(POLL_FAST_WINDOW_MS + 5000)).toBe(POLL_SLOW_INTERVAL_MS);
  });
});

describe("deriveManagedApplyStatus", () => {
  const base = {
    enabled: true,
    error: null,
    missingCount: 0,
    nowMs: 1000,
    effectiveDeadlineMs: 100_000,
    bootMismatch: false,
  };

  it("is idle when disabled", () => {
    expect(deriveManagedApplyStatus({ ...base, enabled: false, lookup: undefined })).toBe("idle");
  });

  it("is error when the query errored", () => {
    expect(
      deriveManagedApplyStatus({ ...base, error: new Error("boom"), lookup: undefined }),
    ).toBe("error");
  });

  it("is terminal only for a terminal record", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        lookup: { kind: "record", record: parsedRecord(terminalRecord(BOOT_ID)) },
      }),
    ).toBe("terminal");
  });

  it("is polling for a pending record before the deadline", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        lookup: { kind: "record", record: parsedRecord(pendingRecord(BOOT_ID)) },
      }),
    ).toBe("polling");
  });

  it("is polling for a finalizing record before the deadline", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        lookup: { kind: "record", record: parsedRecord(finalizingRecord(BOOT_ID)) },
      }),
    ).toBe("polling");
  });

  it("is expired for a pending record at or past the deadline", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        nowMs: 100_000,
        lookup: { kind: "record", record: parsedRecord(pendingRecord(BOOT_ID)) },
      }),
    ).toBe("expired");
  });

  it("is missing-grace within the grace window", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        missingCount: MISSING_GRACE_ATTEMPTS,
        lookup: { kind: "missing" },
      }),
    ).toBe("missing-grace");
  });

  it("keeps polling after grace when the process has not restarted", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        missingCount: MISSING_GRACE_ATTEMPTS + 1,
        lookup: { kind: "missing" },
      }),
    ).toBe("polling");
  });

  it("is expired after grace when the boot component mismatches", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        missingCount: MISSING_GRACE_ATTEMPTS + 1,
        bootMismatch: true,
        lookup: { kind: "missing" },
      }),
    ).toBe("expired");
  });

  it("is expired for a missing record past the deadline even within grace", () => {
    expect(
      deriveManagedApplyStatus({
        ...base,
        nowMs: 100_000,
        missingCount: 1,
        lookup: { kind: "missing" },
      }),
    ).toBe("expired");
  });
});

function makeWrapper() {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  function Wrapper({ children }: { readonly children: ReactNode }) {
    return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  }
  return Wrapper;
}

describe("useManagedApplyRecord — hook wiring", () => {
  it("is idle when no apply id is awaited", () => {
    globalThis.fetch = vi.fn(() => {
      throw new Error("should not fetch when idle");
    });
    const { result } = renderHook(() => useManagedApplyRecord(undefined), {
      wrapper: makeWrapper(),
    });
    expect(result.current.status).toBe("idle");
    expect(result.current.record).toBeNull();
  });

  it("resolves a terminal record to status terminal", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(json(terminalRecord(BOOT_ID))));
    const { result, unmount } = renderHook(() => useManagedApplyRecord(BOOT_ID), {
      wrapper: makeWrapper(),
    });
    await waitFor(() => {
      expect(result.current.status).toBe("terminal");
    });
    expect(result.current.record?.id).toBe(BOOT_ID);
    unmount();
  });

  it("treats an initial 404 as missing-grace and never claims terminal", async () => {
    globalThis.fetch = vi.fn(() => Promise.resolve(new Response(null, { status: 404 })));
    const { result, unmount } = renderHook(() => useManagedApplyRecord(BOOT_ID), {
      wrapper: makeWrapper(),
    });
    await waitFor(() => {
      expect(result.current.status).toBe("missing-grace");
    });
    expect(result.current.record).toBeNull();
    expect(result.current.status).not.toBe("terminal");
    unmount();
  });

  it("polls through an initial 404 then a pending record to the terminal record", async () => {
    // AC-09: the read-after-write 404 grace, the pending poll, and terminalization
    // are one continuous lifecycle — a first-miss transaction still resolves to
    // terminal without ever being reported as missing/expired along the way.
    let calls = 0;
    globalThis.fetch = vi.fn(() => {
      calls += 1;
      if (calls === 1) return Promise.resolve(new Response(null, { status: 404 }));
      if (calls === 2) return Promise.resolve(json(pendingRecord(BOOT_ID), 202));
      return Promise.resolve(json(terminalRecord(BOOT_ID)));
    });
    const { result, unmount } = renderHook(() => useManagedApplyRecord(BOOT_ID), {
      wrapper: makeWrapper(),
    });
    await waitFor(
      () => {
        expect(result.current.status).toBe("terminal");
      },
      { timeout: 4000 },
    );
    expect(result.current.record?.id).toBe(BOOT_ID);
    expect(calls).toBeGreaterThanOrEqual(3);
    unmount();
  });

  it("polls through pending then finalizing to terminal, never terminal early", async () => {
    // The finalizing state is externally observable but non-terminal: the hook
    // must keep polling through it and only report terminal once the record
    // actually reaches state=terminal.
    let calls = 0;
    const seen: string[] = [];
    globalThis.fetch = vi.fn(() => {
      calls += 1;
      if (calls === 1) return Promise.resolve(json(pendingRecord(BOOT_ID), 202));
      if (calls === 2) return Promise.resolve(json(finalizingRecord(BOOT_ID), 202));
      return Promise.resolve(json(terminalRecord(BOOT_ID)));
    });
    const { result, unmount } = renderHook(() => useManagedApplyRecord(BOOT_ID), {
      wrapper: makeWrapper(),
    });
    await waitFor(
      () => {
        seen.push(result.current.status);
        expect(result.current.status).toBe("terminal");
      },
      { timeout: 4000 },
    );
    // Terminal was never reported while the record was pending or finalizing.
    expect(seen.slice(0, -1)).not.toContain("terminal");
    expect(result.current.record?.state).toBe("terminal");
    expect(calls).toBeGreaterThanOrEqual(3);
    unmount();
  });

  it("manual retry resets terminal lookup state and re-fetches the record", async () => {
    // A completed terminal lookup stops polling; an explicit retry must clear
    // that terminal state and re-observe the exact id — never leaving a stale
    // terminal reported after the operator re-checks.
    let calls = 0;
    globalThis.fetch = vi.fn(() => {
      calls += 1;
      if (calls === 1) return Promise.resolve(json(terminalRecord(BOOT_ID)));
      return Promise.resolve(json(pendingRecord(BOOT_ID), 202));
    });
    const { result, unmount } = renderHook(() => useManagedApplyRecord(BOOT_ID), {
      wrapper: makeWrapper(),
    });
    await waitFor(() => {
      expect(result.current.status).toBe("terminal");
    });
    act(() => {
      result.current.retry();
    });
    await waitFor(() => {
      expect(result.current.status).toBe("polling");
    });
    expect(result.current.record?.state).toBe("pending");
    expect(calls).toBeGreaterThanOrEqual(2);
    unmount();
  });
});
