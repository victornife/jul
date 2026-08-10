/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

export interface ConfigHandoffGuardInput {
  readonly pendingKnown: boolean;
  readonly pendingChanged: boolean;
  readonly baseChanged: boolean;
  readonly refreshing: boolean;
  readonly refreshFailed: boolean;
}

export interface ConfigHandoffGuardDecision {
  readonly blocked: boolean;
  readonly requiresRefresh: boolean;
  readonly reason: string;
}

/**
 * Fail-closed gate shared by structured and raw handoffs. It never chooses a
 * lifecycle action; it only decides whether the server-authoritative assessment
 * is still usable against the exact base and pending-restart state.
 */
export function evaluateConfigHandoffGuard(
  input: ConfigHandoffGuardInput,
): ConfigHandoffGuardDecision {
  if (!input.pendingKnown) {
    return {
      blocked: true,
      requiresRefresh: false,
      reason: "Pending-restart status is still loading.",
    };
  }
  if (input.baseChanged) {
    return {
      blocked: true,
      requiresRefresh: false,
      reason: "The base configuration changed. Regenerate the candidate from the latest source.",
    };
  }
  if (input.refreshFailed) {
    return {
      blocked: true,
      requiresRefresh: true,
      reason: "The authoritative preview could not be refreshed.",
    };
  }
  if (input.refreshing) {
    return {
      blocked: true,
      requiresRefresh: true,
      reason: "Refreshing the authoritative preview for the current pending-restart state.",
    };
  }
  if (input.pendingChanged) {
    return {
      blocked: true,
      requiresRefresh: true,
      reason: "Pending-restart state changed since preview. A fresh authoritative preview is required.",
    };
  }
  return { blocked: false, requiresRefresh: false, reason: "" };
}
