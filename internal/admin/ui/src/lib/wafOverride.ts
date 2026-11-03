/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { LocationWAF, LocationWAFPatch, WAFException } from "@/api/client.ts";

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
  blockingParanoia?: string; // "" leaves default; otherwise "1".."4"
  detectionParanoia?: string; // "" leaves blocking; otherwise "1".."4"
  exceptions?: WAFException[]; // structured narrow exceptions (#631)
  requestBodyLimit: string; // size string, e.g. "128k"
  responseBodyCheck: boolean;
  directivesFiles: string; // one rule-file path per line
  inlineRules: string;
}

export function seedWAFOverride(w: LocationWAF): WAFOverrideDraft {
  const effectiveParanoia = w.blocking_paranoia
    ? String(w.blocking_paranoia)
    : w.paranoia
      ? String(w.paranoia)
      : "";
  return {
    enabled: w.enabled,
    mode: w.mode === "block" ? "block" : "detect",
    crsEnabled: w.crs_enabled,
    blockStatus: w.block_status ? String(w.block_status) : "",
    paranoia: effectiveParanoia,
    ...(w.blocking_paranoia ? { blockingParanoia: String(w.blocking_paranoia) } : {}),
    ...(w.detection_paranoia ? { detectionParanoia: String(w.detection_paranoia) } : {}),
    ...(w.exceptions && w.exceptions.length > 0 ? { exceptions: w.exceptions } : {}),
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
  const pRaw = d.paranoia.trim();
  const bpRaw = (d.blockingParanoia ?? "").trim();
  const dpRaw = (d.detectionParanoia ?? "").trim();
  const paranoia = Number(pRaw);
  const blockingParanoia = Number(bpRaw);
  const detectionParanoia = Number(dpRaw);
  const hasSplitParanoia =
    (bpRaw !== "" && Number.isInteger(blockingParanoia) && blockingParanoia > 0) ||
    (dpRaw !== "" && Number.isInteger(detectionParanoia) && detectionParanoia > 0);
  const effectiveBP =
    pRaw !== "" && Number.isInteger(paranoia) && paranoia > 0
      ? paranoia
      : bpRaw !== "" && Number.isInteger(blockingParanoia) && blockingParanoia > 0
        ? blockingParanoia
        : 1;
  return {
    enabled: d.enabled,
    mode: d.mode,
    crs_enabled: d.crsEnabled,
    response_body_check: d.responseBodyCheck,
    ...(d.blockStatus.trim() && Number.isInteger(blockStatus) ? { block_status: blockStatus } : {}),
    ...(hasSplitParanoia
      ? {
          blocking_paranoia: effectiveBP,
          ...(dpRaw !== "" && Number.isInteger(detectionParanoia)
            ? { detection_paranoia: detectionParanoia }
            : {}),
        }
      : pRaw !== "" && Number.isInteger(paranoia)
        ? { paranoia }
        : {}),
    ...(d.exceptions && d.exceptions.length > 0 ? { exceptions: d.exceptions } : {}),
    ...(d.requestBodyLimit.trim() ? { request_body_limit: d.requestBodyLimit.trim() } : {}),
    ...(files.length > 0 ? { directives_files: files } : {}),
    ...(d.inlineRules.trim() ? { inline_rules: d.inlineRules } : {}),
  };
}

const VALID_COLLECTIONS = new Set([
  "ARGS",
  "ARGS_GET",
  "ARGS_POST",
  "ARGS_NAMES",
  "REQUEST_HEADERS",
  "REQUEST_COOKIES",
]);

export function validateExceptionTargetInput(raw: string): string | null {
  const trimmed = raw.trim();
  if (!trimmed) return "Target is required for narrow_target scope (e.g. ARGS:profile_bio).";
  const idx = trimmed.indexOf(":");
  if (idx <= 0) {
    return "Target must have form <COLLECTION>:<KEY> (e.g. ARGS:profile_bio).";
  }
  const col = trimmed.slice(0, idx).toUpperCase();
  const key = trimmed.slice(idx + 1);
  if (!VALID_COLLECTIONS.has(col)) {
    return `Unsupported target collection "${col}" (use ARGS, ARGS_GET, ARGS_POST, ARGS_NAMES, REQUEST_HEADERS, or REQUEST_COOKIES).`;
  }
  if (!key || key.length > 64 || !/^[A-Za-z0-9_.\-[\]]+$/.test(key)) {
    return "Target key must be 1–64 characters matching [A-Za-z0-9_.-[]] with no spaces or wildcards.";
  }
  return null;
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
  const bpRaw = (d.blockingParanoia ?? "").trim();
  const dpRaw = (d.detectionParanoia ?? "").trim();
  const bp = bpRaw ? Number(bpRaw) : 0;
  const dp = dpRaw ? Number(dpRaw) : 0;
  if (bpRaw !== "") {
    if (!Number.isInteger(bp) || bp < 1 || bp > 4) {
      w.push("Blocking paranoia level must be between 1 and 4.");
    } else if (!d.crsEnabled) {
      w.push("Blocking paranoia level applies only when the CRS is enabled.");
    }
  }
  if (dpRaw !== "") {
    if (!Number.isInteger(dp) || dp < 1 || dp > 4) {
      w.push("Detection paranoia level must be between 1 and 4.");
    } else if (!d.crsEnabled) {
      w.push("Detection paranoia level applies only when the CRS is enabled.");
    }
  }
  const pTrim = d.paranoia.trim();
  const effBp = pTrim !== "" ? Number(pTrim) : bp > 0 ? bp : 1;
  if (dp > 0 && effBp >= 1 && effBp <= 4 && dp < effBp) {
    w.push("Detection paranoia must be greater than or equal to blocking paranoia.");
  }
  for (let i = 0; i < (d.exceptions ?? []).length; i++) {
    const exc = d.exceptions?.[i];
    if (!exc) continue;
    if (!Number.isInteger(exc.rule_id) || exc.rule_id <= 0) {
      w.push(`Exception #${String(i + 1)}: rule_id must be a positive integer.`);
    }
    const scope = exc.scope ?? (exc.target ? "narrow_target" : "rule_remove");
    if (scope === "narrow_target") {
      const targetErr = validateExceptionTargetInput(exc.target ?? "");
      if (targetErr) w.push(`Exception #${String(i + 1)}: ${targetErr}`);
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
