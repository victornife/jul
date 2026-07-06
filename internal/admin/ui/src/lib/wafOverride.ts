/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { LocationWAF, LocationWAFPatch } from "@/api/client.ts";

// wafOverride holds the pure draft <-> patch mapping and warning logic for the
// guided per-location WAF override editor (Phase 4e). Keeping it free of React
// makes the round-trip and validation directly unit testable, mirroring
// appSettings.ts. The override REPLACES the global [waf] policy for its location
// wholesale, so the editor seeds every field and sends the full set back.

export type WAFOverrideMode = "block" | "detect";

export interface WAFOverrideDraft {
  enabled: boolean;
  mode: WAFOverrideMode;
  crsEnabled: boolean;
  blockStatus: string; // "" leaves the 403 default
  paranoia: string; // "" leaves the CRS default; otherwise "1".."4"
  requestBodyLimit: string; // size string, e.g. "128k"
  responseBodyCheck: boolean;
  directivesFiles: string; // one rule-file path per line
  inlineRules: string;
}

export function seedWAFOverride(w: LocationWAF): WAFOverrideDraft {
  return {
    enabled: w.enabled,
    mode: w.mode === "block" ? "block" : "detect",
    crsEnabled: w.crs_enabled,
    blockStatus: w.block_status ? String(w.block_status) : "",
    paranoia: w.paranoia ? String(w.paranoia) : "",
    requestBodyLimit: w.request_body_limit ?? "",
    responseBodyCheck: w.response_body_check ?? false,
    directivesFiles: (w.directives_files ?? []).join("\n"),
    inlineRules: w.inline_rules ?? "",
  };
}

function fileList(s: string): string[] {
  return s
    .split("\n")
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

export function wafOverrideToPatch(d: WAFOverrideDraft): LocationWAFPatch {
  const files = fileList(d.directivesFiles);
  const blockStatus = Number(d.blockStatus);
  const paranoia = Number(d.paranoia);
  return {
    enabled: d.enabled,
    mode: d.mode,
    crs_enabled: d.crsEnabled,
    response_body_check: d.responseBodyCheck,
    ...(d.blockStatus.trim() && Number.isInteger(blockStatus) ? { block_status: blockStatus } : {}),
    ...(d.paranoia.trim() && Number.isInteger(paranoia) ? { paranoia } : {}),
    ...(d.requestBodyLimit.trim() ? { request_body_limit: d.requestBodyLimit.trim() } : {}),
    ...(files.length > 0 ? { directives_files: files } : {}),
    ...(d.inlineRules.trim() ? { inline_rules: d.inlineRules } : {}),
  };
}

// wafOverrideWarnings returns the blocking validation issues — it mirrors the
// server's validateWAF so the editor can refuse a save the backend would reject.
export function wafOverrideWarnings(d: WAFOverrideDraft): string[] {
  const w: string[] = [];
  if (!d.enabled) return w;
  const hasRules =
    d.crsEnabled || fileList(d.directivesFiles).length > 0 || d.inlineRules.trim() !== "";
  if (!hasRules) {
    w.push(
      "This override is enabled but defines no rules — enable the CRS, add a rule file, or write inline rules.",
    );
  }
  if (d.paranoia.trim() !== "") {
    const pn = Number(d.paranoia);
    if (!Number.isInteger(pn) || pn < 1 || pn > 4) {
      w.push("Paranoia level must be between 1 and 4.");
    } else if (!d.crsEnabled) {
      w.push("Paranoia level applies only when the CRS is enabled.");
    }
  }
  if (d.blockStatus.trim() !== "") {
    const bs = Number(d.blockStatus);
    if (!Number.isInteger(bs) || bs < 100 || bs > 599) {
      w.push("Block status must be a valid HTTP status (100–599).");
    }
  }
  return w;
}
