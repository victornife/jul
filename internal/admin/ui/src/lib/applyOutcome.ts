/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Apply-outcome taxonomy for the configuration write flow (AUX-02).
 *
 * A config apply is not a single yes/no event. The write path validates and
 * persists synchronously, but the swap into the live runtime -' and, for the L4
 * stream proxy, the listener rebind -' completes asynchronously; a handful of
 * settings (the ACME issued-domain set and issuer) cannot be hot-applied at all
 * and need a process restart. Always painting a green "saved" hides the cases
 * where an apply is accepted but not yet, or not fully, live. This module folds
 * the raw signals -' accepted vs restart-required, the server's pending_reload
 * flag, and the post-apply stream_status -' into one explicit, severity-tagged
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
  // config and is still serving the previous one -' a degraded, not failed,
  // apply that needs operator attention.
  | "partial-reload"
  // Accepted, swap completed, but the reload exceeded the operator-configured
  // reload_timeout. The new config is serving; the timeout is advisory and
  // a slow reload should be investigated.
  | "reload-timed-out"
  // Valid but NOT applied: the change is fixed at process start, so nothing was
  // saved and a restart is required for it to take effect.
  | "restart-required"
  // Configuration saved for the next process restart (first stage). The live
  // runtime is unchanged; the operator must restart the process.
  | "staged-for-restart"
  // Staged configuration updated (a further stage_restart apply while a staged
  // restart is already pending). Live runtime is unchanged.
  | "staged-update"
  // A hot apply was blocked because a managed staged restart is already
  // pending. The operator must discard or restart before applying hot changes.
  | "pending-restart-blocks-hot"
  // The staged restart was successfully discarded and the previous configuration
  // was restored. The live runtime was already serving the previous config.
  | "discard-success"
  // Reload enqueue failed; candidate was persisted but never reached the runtime.
  // Restoration was attempted (and may have succeeded).
  | "enqueue-failed";

/** One subsystem that did not activate the new config during a partial reload. */
export interface SubsystemFailure {
  readonly name: string;
  readonly detail: string | undefined;
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

/** Per-subsystem reload result from the backend. */
export interface SubsystemReloadResult {
  readonly status?: string;
  readonly error?: string;
  readonly duration_ms?: number;
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
   * serving; the timeout is advisory and a slow reload should be investigated.
   * Distinct from restart-required (config not saved) and partial-reload
   * (subsystem failed to activate).
   */
  readonly reloadTimedOut?: boolean;
  /**
   * The apply mode returned by the server (P2-04). "stage_restart" means the
   * config was saved for the next restart; the live runtime is unchanged.
   */
  readonly mode?: "hot" | "stage_restart";
  /**
   * True when a hot apply was blocked because a staged restart is pending
   * (P2-04). The server returns ok=false with this flag set.
   */
  readonly pendingRestartBlocksHot?: boolean;
  /**
   * True when deriving the outcome of a discard operation (P2-04). The server
   * restores the previous configuration; live runtime was already serving it.
   */
  readonly isDiscard?: boolean;
/**
  * True when this is a staged-update (a further stage_restart apply while one
  * is already pending). Derived from pending_restart.staged in the response.
  */
 readonly isStagedUpdate?: boolean;
 /**
  * M-03: Per-subsystem reload results from reload.outcome for complete outcome.
  */
 readonly http?: SubsystemReloadResult;
 readonly stream?: SubsystemReloadResult;
 readonly admin?: SubsystemReloadResult;
 /** M-03: Whether the reload was persisted (saved to disk) vs published to runtime. */
 readonly persisted?: boolean;
 /** M-03: The reload phase that failed or timed out. */
 readonly failedPhase?: string;
 readonly timedOutPhase?: string;
 /** P0-1: Enqueue failure when reload.outcome is "not_applied". */
 readonly enqueueFailed?: boolean;
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
  return detail.length > 0 ? { name: "L4 stream proxy", detail: detail } : { name: "L4 stream proxy", detail: undefined };
}

/**
 * Folds the raw apply signals into a single explicit outcome. Precedence:
 * discard > pending-restart-blocks-hot > stage_restart > restart-required
 * > partial subsystem failure > still reloading > fully live.
 */
export function deriveApplyOutcome(input: ApplyOutcomeInput): ApplyOutcome {
  // Discard: the staged restart was abandoned and the previous config restored.
  if (input.isDiscard) {
    return {
      kind: "discard-success",
      severity: "success",
      blocking: false,
      title: "Staged configuration discarded",
      message:
        "The staged configuration was discarded and the previous configuration was restored. The live runtime was already serving it; no reload was needed.",
      failures: [],
    };
  }

  // Hot apply blocked by pending staged restart.
  if (input.pendingRestartBlocksHot) {
    return {
      kind: "pending-restart-blocks-hot",
      severity: "blocked",
      blocking: true,
      title: "Blocked - a staged restart is pending",
      message:
        "A configuration is staged for the next process restart. Hot applies are blocked until it is discarded or the process is restarted. Discard the staged configuration to resume hot applies, or restart the process to apply it.",
      failures: [],
    };
  }

  // stage_restart mode: configuration saved for the next restart.
  if (input.mode === "stage_restart" && input.accepted) {
    if (input.isStagedUpdate) {
      return {
        kind: "staged-update",
        severity: "info",
        blocking: false,
        title: "Staged configuration updated",
        message:
          "The staged configuration was updated. The live runtime is unchanged; restart the process to apply the staged changes.",
        failures: [],
      };
    }
    return {
      kind: "staged-for-restart",
      severity: "info",
      blocking: false,
      title: "Saved for next restart",
      message:
        "The configuration was validated and saved. It will take effect when you restart the process. The live runtime is unchanged.",
      failures: [],
    };
  }

  if (!input.accepted) {
    // P0-1 M-05: Enqueue failure is a distinct outcome: config was persisted
    // but never reached the runtime. Surface as a warning, not blocked.
    if (input.persisted === true || input.enqueueFailed) {
      return {
        kind: "enqueue-failed",
        severity: "warning",
        blocking: false,
        title: "Reload was not enqueued",
        message:
          "The configuration was saved but could not be applied to the running server. The previous configuration was restored if possible. Check the server logs for the queue/submit error.",
        failures: [],
      };
    }
    const msg = input.restartMessage?.trim();
    return {
      kind: "restart-required",
      severity: "blocked",
      blocking: true,
      title: "Restart required - change not applied",
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
  // M-03: Also surface the timed-out phase.
  if (input.reloadTimedOut || input.timedOutPhase) {
    const phaseInfo = input.timedOutPhase ? ` (phase: ${input.timedOutPhase})` : "";
    return {
      kind: "reload-timed-out",
      severity: "warning",
      blocking: false,
      title: "Applied -' reload exceeded the configured timeout",
      message:
        "The configuration was saved and is now serving, but the reload took longer than the configured reload_timeout" +
        phaseInfo + ". Investigate slow reload paths (WAF rule compilation, WASM plugin loading) or increase reload_timeout in [global].",
      failures: [],
    };
  }

  const failures: SubsystemFailure[] = [];
  // M-03: Extract per-subsystem failures from the correlated reload result.
  if (input.http?.status === "failed") {
    failures.push({ name: "HTTP runtime", detail: input.http.error ?? undefined });
  }
  if (input.stream?.status === "failed") {
    failures.push({ name: "L4 stream proxy", detail: input.stream.error ?? undefined });
  }
  if (input.admin?.status === "failed") {
    failures.push({ name: "admin subsystem", detail: input.admin.error ?? undefined });
  }

  // M-03: Also include stream status from legacy polling for backward compat.
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
      title: "Applied -' runtime reloading",
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
