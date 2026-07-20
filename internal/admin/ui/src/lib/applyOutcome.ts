/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { ReloadResult } from "@/api/client.ts";

/**
 * Apply-outcome taxonomy for the configuration write flow (AUX-02 / H-06).
 *
 * A config apply is not a single yes/no event. The write path validates and
 * persists synchronously, but the swap into the live runtime — and, for the L4
 * stream proxy, the listener rebind — completes asynchronously; a handful of
 * settings cannot be hot-applied at all and need a process restart. The server
 * now returns a structured `reload` result that classifies the terminal outcome
 * (applied_live, applied_degraded, not_applied, saved_not_live) plus per-
 * subsystem status, and restoration fields that report when a rejected candidate
 * was rolled back. This module folds those signals into one explicit, severity-
 * tagged outcome so the panel can render every branch consistently.
 */

export type ApplyOutcomeSeverity = "success" | "info" | "warning" | "blocked";

export type ApplyOutcomeKind =
  // Applied and fully live: every subsystem reloaded cleanly.
  | "full-live"
  // Accepted and persisted; the asynchronous runtime swap has not been
  // confirmed live yet (the transient state right after an apply).
  | "reload-pending"
  // Accepted and live, but a subsystem failed to activate the new config and
  // is still serving the previous one — a degraded, not failed, apply.
  | "partial-reload"
  // Accepted, swap completed, but the reload exceeded the operator-configured
  // reload_timeout. The new config is serving; the timeout is advisory.
  | "reload-timed-out"
  // Valid but NOT applied: the change is fixed at process start.
  | "restart-required"
  // Configuration saved for the next process restart (first stage).
  | "staged-for-restart"
  // Staged configuration updated while a staged restart was already pending.
  | "staged-update"
  // A hot apply was blocked because a staged restart is already pending.
  | "pending-restart-blocks-hot"
  // The staged restart was discarded and the previous config restored.
  | "discard-success"
  // The candidate was persisted, the live reload rejected it, and the previous
  // configuration was restored. Nothing is serving the candidate.
  | "restored"
  // The candidate was rejected and restoration was attempted but failed. The
  // on-disk state may not match the running runtime; manual intervention is
  // required.
  | "restoration-failed";

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
  /**
   * The structured reload result for this apply (P2-04 / H-06). Replaces the
   * legacy pending_reload + previous_reload pair. When absent the caller should
   * fall back to pending_reload for backward compatibility.
   */
  readonly reload?: ReloadResult;
  /**
   * The server's legacy pending_reload flag. Used only when `reload` is absent
   * (older server builds).
   */
  readonly pendingReload: boolean;
  /** True once a post-apply runtime snapshot (overview) has been observed. */
  readonly runtimeObserved: boolean;
  /** The most recent overview stream_status, when one has been observed. */
  readonly streamStatus?: string;
  /** Operator-facing message for a restart-required (not accepted) apply. */
  readonly restartMessage?: string;
  /**
   * Restoration fields (F-03). restored is true when the rejected candidate was
   * rolled back to the previous configuration. restoreError is set when the
   * rollback itself failed.
   */
  readonly restored?: boolean;
  readonly restoreError?: string;
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
   * True when deriving the outcome of a discard operation (P2-04).
   */
  readonly isDiscard?: boolean;
  /**
   * True when this is a staged-update.
   */
  readonly isStagedUpdate?: boolean;
}

/**
 * Parses an overview `stream_status` into a subsystem failure, or null when the
 * stream reload is healthy or absent. The backend encodes a rejected L4 reload
 * as "failed: <reason>" (the prior listeners keep serving).
 */
export function streamReloadFailure(streamStatus: string | undefined): SubsystemFailure | null {
  if (streamStatus === undefined || !streamStatus.startsWith("failed:")) return null;
  const detail = streamStatus.replace(/^failed:\s*/, "").trim();
  return detail.length > 0 ? { name: "L4 stream proxy", detail } : { name: "L4 stream proxy" };
}

const SUBSYSTEM_NAMES: Record<string, string> = {
  http: "HTTP proxy",
  stream: "L4 stream proxy",
  admin: "Admin subsystem",
};

/**
 * Collects subsystem failures from the structured reload result. A subsystem is
 * failed when its status is "failed" or "timed_out". The L4 stream proxy is
 * also polled separately via overview stream_status because stream listeners
 * reload asynchronously.
 */
export function reloadSubsystemFailures(reload: ReloadResult | undefined): SubsystemFailure[] {
  if (!reload) return [];
  const failures: SubsystemFailure[] = [];
  for (const key of ["http", "stream", "admin"] as const) {
    const sub = reload[key];
    if (!sub) continue;
    const status = sub.status;
    const name = SUBSYSTEM_NAMES[key];
    if (!name) continue;
    if (status === "failed" || status === "timed_out") {
      failures.push(sub.error !== undefined ? { name, detail: sub.error } : { name });
    }
  }
  return failures;
}

/**
 * Folds the raw apply signals into a single explicit outcome. Precedence:
 * discard > pending-restart-blocks-hot > stage_restart > restart-required
 * > restoration-failed > restored > timed-out > partial subsystem failure
 * > still reloading > fully live.
 */
export function deriveApplyOutcome(input: ApplyOutcomeInput): ApplyOutcome {
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

  // Restoration takes precedence over reload outcomes: if the candidate was
  // rejected and rolled back, nothing is serving it.
  if (input.restoreError && input.restoreError.length > 0) {
    return {
      kind: "restoration-failed",
      severity: "blocked",
      blocking: true,
      title: "Apply rejected and restoration failed",
      message: `The live reload rejected the candidate and the attempt to restore the previous configuration failed: ${input.restoreError}. Check the server logs and the on-disk configuration.`,
      failures: [],
    };
  }
  if (input.restored) {
    return {
      kind: "restored",
      severity: "warning",
      blocking: false,
      title: "Apply rejected - previous configuration restored",
      message:
        "The candidate was saved but the live reload rejected it. The previous configuration has been restored and is serving.",
      failures: [],
    };
  }

  const reload = input.reload;
  if (reload) {
    // A timed-out reload: the config was saved and the swap completed, but it
    // exceeded reload_timeout. The new config is serving.
    if (reload.timed_out && reload.outcome !== "not_applied") {
      return {
        kind: "reload-timed-out",
        severity: "warning",
        blocking: false,
        title: "Applied - reload exceeded the configured timeout",
        message:
          "The configuration was saved and is now serving, but the reload took longer than the configured reload_timeout. " +
          "Investigate slow reload paths (WAF rule compilation, WASM plugin loading) or increase reload_timeout in [global].",
        failures: [],
      };
    }

    const failures = reloadSubsystemFailures(reload);
    const streamFailure = streamReloadFailure(input.streamStatus);
    if (streamFailure && !failures.some((f) => f.name === streamFailure.name)) {
      failures.push(streamFailure);
    }

    if (reload.outcome === "applied_degraded" || failures.length > 0) {
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

    if (reload.outcome === "saved_not_live") {
      return {
        kind: "reload-pending",
        severity: "info",
        blocking: false,
        title: "Applied - runtime reloading",
        message:
          "The configuration was validated and saved. The live reload is still in flight; this panel confirms once every subsystem reports the new config is live.",
        failures: [],
      };
    }

    if (reload.outcome === "applied_live") {
      return {
        kind: "full-live",
        severity: "success",
        blocking: false,
        title: "Applied and live",
        message: "The configuration was validated, saved, and is now live across every subsystem.",
        failures: [],
      };
    }
  }

  // Legacy fallback for older servers that do not send the structured reload
  // result. Use pending_reload + stream_status to approximate the outcome.
  if (!input.runtimeObserved && input.pendingReload) {
    return {
      kind: "reload-pending",
      severity: "info",
      blocking: false,
      title: "Applied - runtime reloading",
      message:
        "The configuration was validated and saved. The live runtime is swapping to it now; this panel confirms once every subsystem reports the new config is live.",
      failures: [],
    };
  }

  const streamFailure = streamReloadFailure(input.streamStatus);
  if (streamFailure) {
    return {
      kind: "partial-reload",
      severity: "warning",
      blocking: false,
      title: "Applied with a partial reload",
      message:
        "The configuration was saved and the HTTP runtime swapped to it, but a subsystem could not activate the new config and is still serving the previous one. Resolve the issue below, then apply again.",
      failures: [streamFailure],
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
