/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Apply-outcome taxonomy for the configuration write flow (AUX-02).
 *
 * A config apply is not a single yes/no event. The write path validates and
 * persists synchronously, but the swap into the live runtime — and, for the L4
 * stream proxy, the listener rebind — completes asynchronously; a handful of
 * settings (the ACME issued-domain set and issuer) cannot be hot-applied at all
 * and need a process restart. Always painting a green "saved" hides the cases
 * where an apply is accepted but not yet, or not fully, live. This module folds
 * the raw signals — accepted vs restart-required, the server's pending_reload
 * flag, and the post-apply stream_status — into one explicit, severity-tagged
 * outcome so the panel can render every branch consistently and an operator can
 * tell them apart at a glance.
 */

export type ApplyOutcomeSeverity = "success" | "info" | "warning" | "blocked";

export type ApplyOutcomeKind =
  // Applied and fully live: HTTP swapped and every subsystem reloaded cleanly.
  | "full-live"
  // Accepted and persisted; the asynchronous runtime swap has not been
  // confirmed live yet (the transient state right after an apply).
  | "reload-pending"
  // Accepted and live for HTTP, but a subsystem failed to activate the new
  // config and is still serving the previous one — a degraded, not failed,
  // apply that needs operator attention.
  | "partial-reload"
  // Accepted, swap completed, but the reload exceeded the operator-configured
  // reload_timeout. The new config is serving; the timeout is advisory and
  // a slow reload should be investigated.
  | "reload-timed-out"
  // Valid but NOT applied: the change is fixed at process start, so nothing was
  // saved and a restart is required for it to take effect.
  | "restart-required";

/** One subsystem that did not activate the new config during a partial reload. */
export interface SubsystemFailure {
  readonly name: string;
  readonly detail?: string;
}

export interface ApplyOutcome {
  readonly kind: ApplyOutcomeKind;
  readonly severity: ApplyOutcomeSeverity;
  /** True when nothing was applied, so the operator must take a blocking action. */
  readonly blocking: boolean;
  readonly title: string;
  readonly message: string;
  /** The subsystems that failed to reload; non-empty only for "partial-reload". */
  readonly failures: readonly SubsystemFailure[];
}

export interface ApplyOutcomeInput {
  /** True when the apply was accepted (HTTP 200); false for restart-required. */
  readonly accepted: boolean;
  /** The server's pending_reload flag: the swap into the live runtime is async. */
  readonly pendingReload: boolean;
  /** True once a post-apply runtime snapshot (overview) has been observed. */
  readonly runtimeObserved: boolean;
  /** The most recent overview stream_status, when one has been observed. */
  readonly streamStatus?: string;
  /** Operator-facing message for a restart-required (not accepted) apply. */
  readonly restartMessage?: string;
  /**
   * True when the server's previous_reload.timed_out flag was set: the swap
   * completed but exceeded the configured reload_timeout. The new config is
   * serving; the slow reload should be investigated and the timeout raised if
   * the config has grown. Distinct from restart-required (config not saved)
   * and partial-reload (subsystem failed to activate).
   */
  readonly reloadTimedOut?: boolean;
}

/**
 * Parses an overview `stream_status` into a subsystem failure, or null when the
 * stream reload is healthy or absent. The backend encodes a rejected L4 reload
 * as "failed: <reason>" (the prior listeners keep serving), "ok" when the
 * running set matches the applied config, and "" when no stream is configured.
 */
export function streamReloadFailure(streamStatus: string | undefined): SubsystemFailure | null {
  if (streamStatus === undefined || !streamStatus.startsWith("failed:")) return null;
  const detail = streamStatus.replace(/^failed:\s*/, "").trim();
  return detail.length > 0 ? { name: "L4 stream proxy", detail } : { name: "L4 stream proxy" };
}

/**
 * Folds the raw apply signals into a single explicit outcome. Precedence:
 * restart-required (nothing applied) → partial subsystem failure → still
 * reloading → fully live. A subsystem failure outranks the pending state so a
 * degraded apply is never masked by an in-flight reload.
 */
export function deriveApplyOutcome(input: ApplyOutcomeInput): ApplyOutcome {
  if (!input.accepted) {
    const msg = input.restartMessage?.trim();
    return {
      kind: "restart-required",
      severity: "blocked",
      blocking: true,
      title: "Restart required — change not applied",
      message:
        msg && msg.length > 0
          ? msg
          : "This change is valid but cannot be applied while the server is running. Nothing was saved; update the configuration file and restart the server for it to take effect.",
      failures: [],
    };
  }

  // A timed-out reload: the config was saved and the swap completed, but it
  // exceeded the configured reload_timeout. The new config is serving. Surface
  // this as a warning so the operator investigates slow reload paths or raises
  // the timeout. Takes precedence over partial-reload and reload-pending.
  if (input.reloadTimedOut) {
    return {
      kind: "reload-timed-out",
      severity: "warning",
      blocking: false,
      title: "Applied — reload exceeded the configured timeout",
      message:
        "The configuration was saved and is now serving, but the reload took longer than the configured reload_timeout. " +
        "Investigate slow reload paths (WAF rule compilation, WASM plugin loading) or increase reload_timeout in [global].",
      failures: [],
    };
  }

  const failures: SubsystemFailure[] = [];
  const streamFailure = streamReloadFailure(input.streamStatus);
  if (streamFailure) failures.push(streamFailure);

  if (failures.length > 0) {
    return {
      kind: "partial-reload",
      severity: "warning",
      blocking: false,
      title: "Applied with a partial reload",
      message:
        "The configuration was saved and the HTTP runtime swapped to it, but a subsystem could not activate the new config and is still serving the previous one. Resolve the issue below, then apply again.",
      failures,
    };
  }

  if (!input.runtimeObserved && input.pendingReload) {
    return {
      kind: "reload-pending",
      severity: "info",
      blocking: false,
      title: "Applied — runtime reloading",
      message:
        "The configuration was validated and saved. The live runtime is swapping to it now; this panel confirms once every subsystem reports the new config is live.",
      failures: [],
    };
  }

  return {
    kind: "full-live",
    severity: "success",
    blocking: false,
    title: "Applied and live",
    message: "The configuration was validated, saved, and is now live across every subsystem.",
    failures: [],
  };
}
