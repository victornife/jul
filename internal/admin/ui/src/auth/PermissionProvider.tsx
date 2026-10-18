/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  fetchMe,
  invalidateAdminResponses,
  UNAUTHORIZED_EVENT,
  FORBIDDEN_EVENT,
  type Identity,
} from "@/api/client.ts";
import { PermissionContext, type PermissionState } from "@/auth/usePermission.ts";
import { clearPendingDraft } from "@/lib/configDraftHandoff.ts";

// IDENTITY_KEY names the cached current-identity query.
const IDENTITY_KEY = ["identity"] as const;

/**
 * PermissionProvider fetches the caller's own identity from GET /api/admin/me
 * and exposes it to the tree so controls can be gated proactively. It never
 * relaxes server enforcement: gating is a hint, and every mutation is still
 * authorized by the server.
 *
 * When the API reports a 401 (the same signal the AuthGate listens for) the
 * cached identity is treated as invalid so a stale permission set never lingers
 * after the credential is rejected. It deliberately does NOT force a refetch on
 * that signal — doing so against a rejected credential would spin a 401 loop.
 * A later successful fetch (after the AuthGate reloads the app with a fresh
 * token) clears the rejected flag and restores gating.
 */
export function PermissionProvider({ children }: { readonly children: ReactNode }) {
  const qc = useQueryClient();
  const query = useQuery({
    queryKey: IDENTITY_KEY,
    queryFn: fetchMe,
    // A 401/403 is an authentication state, not a transient failure — do not
    // retry it, and keep the identity fresh for a minute otherwise.
    retry: false,
    staleTime: 60_000,
    // RBAC roles/principals are hot-reloadable, so another operator can change
    // this principal's permissions while the tab stays open. A short background
    // refetch (and a refetch on focus) keeps proactive gating from drifting
    // from the server's authoritative decision (N-02).
    refetchInterval: 60_000,
    refetchOnWindowFocus: true,
  });
  const [rejected, setRejected] = useState(false);
  const [settledIdentity, setSettledIdentity] = useState<string | null>(null);

  useEffect(() => {
    function onUnauthorized(): void {
      invalidateAdminResponses();
      setRejected(true);
      clearPendingDraft();
      void qc.cancelQueries({ predicate: (q) => q.queryKey[0] !== "identity" });
      qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "identity" });
    }
    function onCredentialChanged(): void {
      onUnauthorized();
      void qc.resetQueries({ queryKey: IDENTITY_KEY });
    }
    // A gated action was forbidden (403): the credential is still valid but its
    // permissions may have changed under a hot RBAC reload, so refetch identity
    // to refresh gating rather than trusting the cached set (N-02).
    function onForbidden(): void {
      void qc.invalidateQueries({ queryKey: IDENTITY_KEY });
    }
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    window.addEventListener(FORBIDDEN_EVENT, onForbidden);
    window.addEventListener("jul:credential-changed", onCredentialChanged);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
      window.removeEventListener(FORBIDDEN_EVENT, onForbidden);
      window.removeEventListener("jul:credential-changed", onCredentialChanged);
    };
  }, [qc]);

  // A fresh successful fetch (dataUpdatedAt advances only on success) means the
  // credential is accepted again, so clear any prior rejection.
  const dataUpdatedAt = query.dataUpdatedAt;
  useEffect(() => {
    if (query.isSuccess) setRejected(false);
  }, [query.isSuccess, dataUpdatedAt]);

  const identity: Identity | null = rejected ? null : (query.data ?? null);
  const ready = !rejected && query.isSuccess && identity !== null;
  const identityKey =
    identity === null
      ? null
      : JSON.stringify([
          identity.principal,
          identity.token_id,
          identity.role,
          [...identity.permissions].sort(),
        ]);
  useEffect(() => {
    if (!ready || identityKey === settledIdentity) return;
    invalidateAdminResponses();
    // Never reuse another identity's privileged projections, even when the
    // same token loses permissions through a live policy update.
    clearPendingDraft();
    void qc.cancelQueries({ predicate: (q) => q.queryKey[0] !== "identity" });
    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "identity" });
    setSettledIdentity(identityKey);
  }, [identityKey, ready, settledIdentity, qc]);

  const value = useMemo<PermissionState>(() => {
    const granted = new Set(identity?.permissions ?? []);
    return {
      identity,
      isLoading: query.isLoading,
      ready,
      has: (permission: string): boolean => {
        if (!ready) return true; // fail open until the identity is known
        return granted.has(permission);
      },
    };
  }, [identity, query.isLoading, ready]);

  // Do not mount panels with cached data while identity is unknown or while
  // caches from a different permission set are being removed.
  if (query.isError && !rejected) {
    return (
      <div role="alert">
        Unable to verify admin permissions.{" "}
        <button
          type="button"
          onClick={() => {
            void query.refetch();
          }}
        >
          Retry permission check
        </button>
      </div>
    );
  }
  if (!ready || settledIdentity !== identityKey) {
    return <p role="status">Checking admin permissions…</p>;
  }
  return (
    <PermissionContext.Provider key={identityKey} value={value}>
      {children}
    </PermissionContext.Provider>
  );
}
