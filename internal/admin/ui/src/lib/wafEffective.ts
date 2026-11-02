/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type {
  SecurityProjection,
  WAFEffectivePolicy,
  WAFEvaluationSummary,
  WAFPolicySummary,
} from "@/api/client.ts";

export function plural(n: number, word: string): string {
  return `${String(n)} ${word}${n === 1 ? "" : "s"}`;
}

// evaluationStateLabel maps the server-verified evaluation state (#630) to an
// operator-facing badge and description without implying unrun evaluation.
export function evaluationStateLabel(ev: WAFEvaluationSummary | undefined): {
  readonly badge: string;
  readonly tone: "success" | "warning" | "muted";
  readonly detail: string;
} {
  if (!ev || ev.state === "unrun") {
    return {
      badge: "Not yet evaluated",
      tone: "muted",
      detail:
        "No synthetic fixture evaluation is bound to this serving policy. Run `jul waf evaluate` locally before promoting rule or paranoia changes.",
    };
  }
  switch (ev.state) {
    case "current":
      return {
        badge:
          ev.failed_cases > 0
            ? `Evaluated (${String(ev.failed_cases)} failed)`
            : `Verified (${String(ev.passed_cases)}/${String(ev.total_cases)} fixtures)`,
        tone: ev.failed_cases > 0 ? "warning" : "success",
        detail: `${ev.source === "supplied_report" ? "Supplied local report" : "Server-observed evaluation"} (${ev.pack_id || "default pack"}): ${String(ev.passed_cases)}/${String(ev.total_cases)} passed, ${String(ev.legitimate_blocked ?? 0)} legitimate blocked, ${String(ev.malicious_allowed ?? 0)} malicious allowed.`,
      };
    case "failed":
      return {
        badge: `Evaluation failed (${String(ev.failed_cases)} failed)`,
        tone: "warning",
        detail: `${ev.source === "supplied_report" ? "Supplied local report" : "Server-observed evaluation"} (${ev.pack_id || "default pack"}): ${String(ev.failed_cases)}/${String(ev.total_cases)} fixture(s) failed (${String(ev.legitimate_blocked ?? 0)} legitimate blocked, ${String(ev.malicious_allowed ?? 0)} malicious allowed). Inspect failed fixtures and adjust narrow exceptions or paranoia before promoting.`,
      };
    case "stale_asset_changed":
      return {
        badge: "Stale: external asset changed",
        tone: "warning",
        detail:
          "One or more external rule/data files changed on disk after evaluation. Re-run `jul waf evaluate` and `jul waf verify-assets` before relying on the previous report.",
      };
    case "stale_policy_changed":
      return {
        badge: "Stale: policy changed",
        tone: "warning",
        detail:
          "The active or candidate WAF policy digest differs from the evaluated policy. Re-evaluate the current policy against your fixture pack.",
      };
    case "stale_engine_or_crs_changed":
      return {
        badge: "Stale: engine or CRS version changed",
        tone: "warning",
        detail:
          "The linked Coraza engine or embedded CRS version differs from the evaluation report.",
      };
    default:
      return {
        badge: "Evaluation unavailable",
        tone: "muted",
        detail: "WAF fixture evaluation is not available in this build profile.",
      };
  }
}

// ruleSources describes the compiled rules by bounded source class.
export function ruleSources(p: WAFPolicySummary): string {
  const r = p.rules;
  const parts: string[] = [];
  if (r.embedded > 0) parts.push(`${String(r.embedded)} embedded CRS`);
  if (r.external > 0) parts.push(`${String(r.external)} from rule files`);
  if (r.inline > 0) parts.push(`${String(r.inline)} inline`);
  if (r.generated > 0) parts.push(`${String(r.generated)} Jul-generated`);
  return `${plural(r.total, "rule")}${parts.length > 0 ? ` (${parts.join(", ")})` : ""}`;
}

// shortDigest formats a context-keyed "ev1:<ctx>:<tag>" digest so both the
// context prefix and the per-policy HMAC tag prefix are visible at a glance
// (#649), falling back to a simple prefix slice for legacy/other strings.
export function shortDigest(digest: string | undefined): string {
  if (!digest) return "…";
  const parts = digest.split(":");
  if (parts.length === 3 && parts[0] === "ev1" && parts[1] && parts[2]) {
    return `ev1:${parts[1].slice(0, 8)}:${parts[2].slice(0, 12)}…`;
  }
  return `${digest.slice(0, 19)}…`;
}

// servingDiffers reports that the saved global configuration no longer matches
// what the serving generation compiled — a reload is pending or failed.
export function servingDiffers(d: SecurityProjection, e: WAFEffectivePolicy): boolean {
  if (d.waf_global_enabled !== e.global_enabled) return true;
  const g = e.global;
  if (!g) return false;
  const mode = d.waf_global_mode === "detect" ? "detect" : "block";
  return g.mode !== mode || g.crs_enabled !== d.waf_crs_enabled;
}
