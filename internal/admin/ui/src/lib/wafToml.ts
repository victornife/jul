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

import type { WAFException } from "@/api/client.ts";

export type WAFMode = "detect" | "block";

export interface WAFDraft {
  enabled: boolean;
  mode: WAFMode;
  blockStatus: number; // 0 = server default (403)
  crsEnabled: boolean;
  paranoia: number; // 1..4; only meaningful when crsEnabled
  blockingParanoia?: number; // 0/undefined = use legacy paranoia; 1..4
  detectionParanoia?: number; // 0/undefined = use blockingParanoia; 1..4
  exceptions?: WAFException[]; // structured narrow exceptions (#631)
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

function tomlMultilineString(s: string): string {
  const escaped = s
    .replace(/\\/g, "\\\\")
    .replace(/\r/g, "\\r")
    // eslint-disable-next-line no-control-regex
    .replace(/[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]/g, (ch) =>
      `\\u${ch.charCodeAt(0).toString(16).padStart(4, "0")}`,
    )
    .replace(/"{3,}/g, (m) => m.replace(/"/g, '\\"'));
  return `"""\n${escaped}\n"""`;
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
  const effectiveBP = d.blockingParanoia && d.blockingParanoia > 0 ? d.blockingParanoia : d.paranoia;
  if (effectiveBP !== 1 && !d.crsEnabled) {
    warn.push("Paranoia level applies only when the CRS is enabled.");
  }
  if (d.crsEnabled && effectiveBP >= 3 && d.mode === "block") {
    warn.push("CRS paranoia ≥ 3 in block mode is aggressive and prone to false positives.");
  }
  const dp = d.detectionParanoia ?? 0;
  if (d.crsEnabled && dp > 0 && effectiveBP > 0 && dp < effectiveBP) {
    warn.push("Detection paranoia must be greater than or equal to blocking paranoia.");
  }
  for (const exc of d.exceptions ?? []) {
    if (exc.scope === "rule_remove") {
      warn.push(
        `Exception for rule ${String(exc.rule_id)} uses whole-rule removal (rule_remove); prefer narrow_target (e.g. ARGS:field) when possible.`,
      );
    }
  }
  return warn;
}

/**
 * Generates the global [waf] table for the WAF editor.
 * Dormant policy fields (mode, CRS, paranoia, directive files, inline rules,
 * and structured exceptions) are emitted independently of `enabled` so
 * disabling and re-enabling the WAF preserves the configured policy intact.
 */
export function generateWafToml(d: WAFDraft): string {
  const lines: string[] = ["[waf]"];
  lines.push(`enabled = ${d.enabled ? "true" : "false"}`);
  lines.push(`mode = ${tomlString(d.mode)}`);
  if (d.blockStatus > 0) {
    lines.push(`block_status = ${String(d.blockStatus)}`);
  }
  lines.push(`crs_enabled = ${d.crsEnabled ? "true" : "false"}`);
  // Paranoia only applies when the CRS is enabled; the server rejects a
  // non-zero paranoia without crs_enabled, so omit it otherwise.
  const bp = d.blockingParanoia ?? 0;
  const dp = d.detectionParanoia ?? 0;
  if (d.crsEnabled) {
    if (dp > 0) {
      const effectiveBP = bp >= 1 && bp <= 4 ? bp : d.paranoia >= 1 && d.paranoia <= 4 ? d.paranoia : 1;
      lines.push(`blocking_paranoia = ${String(effectiveBP)}`);
      if (dp >= 1 && dp <= 4) {
        lines.push(`detection_paranoia = ${String(dp)}`);
      }
    } else if (bp >= 1 && bp <= 4) {
      lines.push(`blocking_paranoia = ${String(bp)}`);
    } else if (d.paranoia >= 1 && d.paranoia <= 4) {
      lines.push(`paranoia = ${String(d.paranoia)}`);
    }
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
    // Emit as an escaped TOML multi-line basic string so SecLang regexes (e.g. \d) and quotes survive.
    lines.push(`inline_rules = ${tomlMultilineString(d.inlineRules.trimEnd())}`);
  }
  for (const exc of d.exceptions ?? []) {
    lines.push("");
    lines.push("[[waf.exceptions]]");
    if (exc.id?.trim()) lines.push(`id = ${tomlString(exc.id.trim())}`);
    lines.push(`rule_id = ${String(exc.rule_id)}`);
    if (exc.scope?.trim()) lines.push(`scope = ${tomlString(exc.scope.trim())}`);
    if (exc.target?.trim()) lines.push(`target = ${tomlString(exc.target.trim())}`);
    if (exc.methods && exc.methods.length > 0) {
      lines.push(`methods = ${tomlStringArray(exc.methods)}`);
    }
    if (exc.rationale?.trim()) lines.push(`rationale = ${tomlString(exc.rationale.trim())}`);
    if (exc.owner?.trim()) lines.push(`owner = ${tomlString(exc.owner.trim())}`);
    if (exc.reference?.trim()) lines.push(`reference = ${tomlString(exc.reference.trim())}`);
    if (exc.created_at?.trim()) lines.push(`created_at = ${tomlString(exc.created_at.trim())}`);
    if (exc.review_at?.trim()) lines.push(`review_at = ${tomlString(exc.review_at.trim())}`);
    if (exc.fixtures && exc.fixtures.length > 0) {
      lines.push(`fixtures = ${tomlStringArray(exc.fixtures)}`);
    }
  }
  return lines.join("\n");
}
