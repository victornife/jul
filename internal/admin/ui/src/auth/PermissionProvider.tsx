/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchMe, UNAUTHORIZED_EVENT, type Identity } from "@/api/client.ts";
import { PermissionContext, type PermissionState } from "@/auth/usePermission.ts";

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
  const query = useQuery({
    queryKey: IDENTITY_KEY,
    queryFn: fetchMe,
    // A 401/403 is an authentication state, not a transient failure — do not
    // retry it, and keep the identity fresh for a minute otherwise.
    retry: false,
    staleTime: 60_000,
  });
  const [rejected, setRejected] = useState(false);

  useEffect(() => {
    function onUnauthorized(): void {
      setRejected(true);
    }
    window.addEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    return () => {
      window.removeEventListener(UNAUTHORIZED_EVENT, onUnauthorized);
    };
  }, []);

  // A fresh successful fetch (dataUpdatedAt advances only on success) means the
  // credential is accepted again, so clear any prior rejection.
  const dataUpdatedAt = query.dataUpdatedAt;
  useEffect(() => {
    if (query.isSuccess) setRejected(false);
  }, [query.isSuccess, dataUpdatedAt]);

  const identity: Identity | null = rejected ? null : (query.data ?? null);
  const ready = !rejected && query.isFetched && identity !== null;

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

  return <PermissionContext.Provider value={value}>{children}</PermissionContext.Provider>;
}
