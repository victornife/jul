/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

// Client-side generator for the guided WAF editor (Wave A). It produces the
// global [waf] top-level table, which the editor upserts into the running
// config and routes through the authoritative Validate → Diff → Apply →
// Rollback pipeline; it never writes directly.
//
// The headline safety concern is the detect→block rollout: enabling blocking
// rules (especially the OWASP CRS) against live traffic without first observing
// what they would have blocked is a fast way to break legitimate requests. The
// editor makes "detect" the default mode and warns whenever block mode is
// combined with a fresh CRS enablement.

export type WAFMode = "detect" | "block";

export interface WAFDraft {
  enabled: boolean;
  mode: WAFMode;
  blockStatus: number; // 0 = server default (403)
  crsEnabled: boolean;
  paranoia: number; // 1..4; only meaningful when crsEnabled
  directivesFiles: string; // newline/comma-separated SecLang file paths
  inlineRules: string; // SecLang snippet appended last
  requestBodyLimit: string; // size, e.g. "128k"; blank = server default
  responseBodyCheck: boolean; // inspect response bodies (CRS phase 4)
}

export function emptyWAFDraft(): WAFDraft {
  return {
    enabled: true,
    mode: "detect",
    blockStatus: 0,
    crsEnabled: true,
    paranoia: 1,
    directivesFiles: "",
    inlineRules: "",
    requestBodyLimit: "",
    responseBodyCheck: false,
  };
}

function tomlString(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlStringArray(items: string[]): string {
  return `[${items.map((i) => tomlString(i)).join(", ")}]`;
}

function splitList(s: string): string[] {
  return s
    .split(/[\n,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

// wafWarnings reports human-readable risks before the operator opens the diff.
// The server validates authoritatively; these are near-side hints centred on
// the detect→block rollout guardrail.
export function wafWarnings(d: WAFDraft): string[] {
  const warn: string[] = [];
  if (!d.enabled) return warn;
  const hasRules =
    d.crsEnabled || splitList(d.directivesFiles).length > 0 || d.inlineRules.trim().length > 0;
  if (!hasRules) {
    warn.push(
      "The WAF is enabled but has no rules: enable the CRS, add directive files, or inline rules.",
    );
  }
  if (d.mode === "block" && d.crsEnabled) {
    warn.push(
      "Block mode with the CRS can reject legitimate traffic. Roll out in detect mode first, review WAF events, then switch to block.",
    );
  }
  if (d.paranoia !== 1 && !d.crsEnabled) {
    warn.push("Paranoia level applies only when the CRS is enabled.");
  }
  if (d.crsEnabled && d.paranoia >= 3 && d.mode === "block") {
    warn.push("CRS paranoia ≥ 3 in block mode is aggressive and prone to false positives.");
  }
  return warn;
}

/** Generates the global [waf] table for the WAF editor. */
export function generateWafToml(d: WAFDraft): string {
  const lines: string[] = ["[waf]"];
  lines.push(`enabled = ${d.enabled ? "true" : "false"}`);
  if (!d.enabled) {
    return lines.join("\n");
  }
  lines.push(`mode = ${tomlString(d.mode)}`);
  if (d.blockStatus > 0) {
    lines.push(`block_status = ${String(d.blockStatus)}`);
  }
  lines.push(`crs_enabled = ${d.crsEnabled ? "true" : "false"}`);
  // Paranoia only applies when the CRS is enabled; the server rejects a
  // non-zero paranoia without crs_enabled, so omit it otherwise.
  if (d.crsEnabled && d.paranoia >= 1 && d.paranoia <= 4) {
    lines.push(`paranoia = ${String(d.paranoia)}`);
  }
  const files = splitList(d.directivesFiles);
  if (files.length > 0) {
    lines.push(`directives_files = ${tomlStringArray(files)}`);
  }
  if (d.requestBodyLimit.trim()) {
    lines.push(`request_body_limit = ${tomlString(d.requestBodyLimit.trim())}`);
  }
  if (d.responseBodyCheck) {
    lines.push(`response_body_check = true`);
  }
  if (d.inlineRules.trim()) {
    // Emit as a TOML multi-line basic string so SecLang directives survive.
    lines.push(`inline_rules = """\n${d.inlineRules.trimEnd()}\n"""`);
  }
  return lines.join("\n");
}
