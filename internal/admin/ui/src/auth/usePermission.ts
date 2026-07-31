/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { createContext, useContext } from "react";
import type { Identity } from "@/api/client.ts";

/**
 * PermissionState is the current-identity view the Console uses to gate
 * controls proactively (P3-03 §33). The server remains authoritative — this is
 * a UX enhancement that hides or disables actions the caller cannot perform and
 * explains why, so operators are not led into a guaranteed 403.
 */
export interface PermissionState {
  /** The authenticated caller, or null while unknown (loading, error, or no RBAC). */
  readonly identity: Identity | null;
  /** True while the identity request is in flight. */
  readonly isLoading: boolean;
  /**
   * True once a concrete identity has been resolved. When false the identity is
   * unknown and {@link PermissionState.has} fails open so the UI is never
   * blanked during load or when running without RBAC.
   */
  readonly ready: boolean;
  /**
   * has reports whether the current identity holds a permission. It fails open
   * (returns true) until {@link PermissionState.ready} is true, keeping the
   * server the single source of truth for authorization.
   */
  readonly has: (permission: string) => boolean;
}

const permitAll = (): boolean => true;

// The default context permits everything and is never "ready", so a component
// rendered outside a PermissionProvider (e.g. an isolated unit test) behaves as
// if RBAC gating is inactive rather than hiding every control.
export const PermissionContext = createContext<PermissionState>({
  identity: null,
  isLoading: false,
  ready: false,
  has: permitAll,
});

/** usePermission returns the current identity and permission predicate. */
export function usePermission(): PermissionState {
  return useContext(PermissionContext);
}
