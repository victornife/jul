/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { SecurityProjection, WAFEffectivePolicy, WAFPolicySummary } from "@/api/client.ts";

export function plural(n: number, word: string): string {
  return `${String(n)} ${word}${n === 1 ? "" : "s"}`;
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
