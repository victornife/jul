/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type {
  LocationWAF,
  SecurityProjection,
  WAFEffectivePolicy,
  WAFEvaluationSummary,
  WAFEventRecord,
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

// servingDiffers reports that the saved configuration no longer matches
// what the serving generation compiled — a reload is pending or failed.
export function servingDiffers(d: SecurityProjection, e: WAFEffectivePolicy): boolean {
  if (d.waf_global_enabled !== e.global_enabled) return true;
  if (e.block_routes !== undefined && e.detect_routes !== undefined) {
    if (d.waf_locations !== e.block_routes + e.detect_routes) return true;
    if (d.waf_block_locs !== e.block_routes || d.waf_detect_locs !== e.detect_routes) return true;
  }
  if (
    d.location_wafs !== undefined &&
    e.overrides !== undefined &&
    d.location_wafs.length !== e.overrides.length
  ) {
    return true;
  }
  const g = e.global;
  if (!g) return false;
  const mode = d.waf_global_mode === "detect" ? "detect" : "block";
  if (g.mode !== mode || g.crs_enabled !== d.waf_crs_enabled) return true;
  const cfgBP = d.waf_blocking_paranoia ?? d.waf_paranoia;
  const effBP = g.blocking_paranoia ?? g.paranoia;
  if (cfgBP !== undefined && effBP !== undefined && cfgBP !== effBP) return true;
  const cfgDP = d.waf_detection_paranoia;
  const effDP = g.detection_paranoia;
  if (cfgDP !== undefined && effDP !== undefined && cfgDP !== effDP) return true;
  if (
    d.waf_exceptions !== undefined &&
    g.exceptions_count !== undefined &&
    d.waf_exceptions.length !== g.exceptions_count
  ) {
    return true;
  }
  return false;
}

function normalizeMatchType(mt?: string): string {
  const t = (mt ?? "").trim().toLowerCase();
  return t || "prefix";
}

function normalizeList(items?: readonly string[], upper?: boolean): string {
  return [...(items ?? [])]
    .map((s) => (upper ? s.trim().toUpperCase() : s.trim().toLowerCase()))
    .filter((s) => s.length > 0)
    .sort()
    .join(",");
}

function normalizeServerNamesKey(names?: readonly string[]): string {
  return normalizeList(names, false);
}

function normalizeMethodsKey(methods?: readonly string[]): string {
  return normalizeList(methods, true);
}

function methodAcceptedByPredicate(evMethod: string | undefined, allowed?: readonly string[]): boolean {
  if (!allowed || allowed.length === 0 || !evMethod) return true;
  const m = evMethod.trim().toUpperCase();
  if (!m) return true;
  const upper = allowed.map((s) => s.trim().toUpperCase());
  return upper.includes(m) || (m === "HEAD" && upper.includes("GET"));
}

function routePredicatesMatch(
  ev: WAFEventRecord,
  servingRoute:
    | {
        readonly methods?: readonly string[] | undefined;
        readonly predicates?: string | undefined;
        readonly canonical_predicates?: string | undefined;
      }
    | undefined,
  candidate: {
    readonly methods?: readonly string[] | undefined;
    readonly predicates?: string | undefined;
    readonly canonical_predicates?: string | undefined;
  },
  siblingCountAtCoords: number,
): boolean {
  const evCanon = ev.route.canonical_predicates ?? servingRoute?.canonical_predicates;
  const evPred = ev.route.predicates ?? servingRoute?.predicates;
  const evMethods = ev.route.methods ?? servingRoute?.methods;
  if (evCanon !== undefined && candidate.canonical_predicates !== undefined && evCanon !== candidate.canonical_predicates) return false;
  if (evPred !== undefined && candidate.predicates !== undefined && evPred !== candidate.predicates) return false;
  if (evMethods !== undefined && candidate.methods !== undefined && normalizeMethodsKey(evMethods) !== normalizeMethodsKey(candidate.methods)) return false;
  if (!methodAcceptedByPredicate(ev.method, candidate.methods)) return false;
  const hasPreds =
    Boolean(candidate.canonical_predicates && candidate.canonical_predicates !== "methods=*\n") ||
    Boolean(candidate.predicates) ||
    Boolean(candidate.methods && candidate.methods.length > 0);
  return !((siblingCountAtCoords > 1 || hasPreds) && evCanon === undefined && evPred === undefined && evMethods === undefined);
}

function sameCoordGroup(
  a: {
    readonly listen?: string | undefined;
    readonly path?: string | undefined;
    readonly match_type?: string | undefined;
    readonly server_names?: readonly string[] | undefined;
  },
  listen?: string,
  path?: string,
  matchType?: string,
  serverNames?: readonly string[],
): boolean {
  return (
    Boolean(listen && path) &&
    a.listen === listen &&
    (a.path ?? "") === path &&
    normalizeMatchType(a.match_type) === normalizeMatchType(matchType) &&
    normalizeServerNamesKey(a.server_names) === normalizeServerNamesKey(serverNames)
  );
}

const STALE_ERR = "Editable configuration or serving generation changed since this event was recorded; refresh or reload before tuning.";

function reject(reason = STALE_ERR): { readonly kind: "rejected"; readonly reason: string } {
  return { kind: "rejected", reason };
}

export function matchLocationWAFForEvent(
  ev: WAFEventRecord,
  locationWafs: readonly LocationWAF[],
  globalConfig?: SecurityProjection,
):
  | { readonly kind: "matched"; readonly target: LocationWAF; readonly existing: boolean }
  | { readonly kind: "global" }
  | { readonly kind: "rejected"; readonly reason: string } {
  if (ev.stale_policy) return reject();
  const eff = globalConfig?.waf_effective;
  if (
    eff &&
    (servingDiffers(globalConfig, eff) ||
      globalConfig.rbac?.pending ||
      Boolean(globalConfig.base_version && eff.serving_base_version && globalConfig.base_version !== eff.serving_base_version) ||
      (ev.generation > 0 && eff.generation > 0 && ev.generation !== eff.generation))
  ) {
    return reject();
  }
  if (globalConfig && globalConfig.waf_locations === 0 && !globalConfig.waf_global_enabled && locationWafs.length === 0) {
    return reject();
  }

  const route = ev.route;
  const rid = (route.route_id ?? "").trim();
  const matchesEventCoords = (r: {
    readonly listen?: string | undefined;
    readonly path?: string | undefined;
    readonly match_type?: string | undefined;
    readonly match_ordinal?: number | undefined;
    readonly server_names?: readonly string[] | undefined;
  }): boolean =>
    sameCoordGroup(r, route.listen, route.path_pattern, route.match_type, route.server_names) &&
    (r.match_ordinal ?? 0) === route.match_ordinal;

  const servingRoute = eff?.serving_routes?.find((sr) => {
    const srRid = (sr.route_id ?? "").trim();
    return rid && srRid ? srRid === rid : matchesEventCoords(sr);
  });

  if (globalConfig?.waf_routes) {
    const editableRoutes = globalConfig.waf_routes;
    if (rid) {
      const byId = editableRoutes.filter((r) => (r.route_id ?? "").trim() === rid);
      const matched = byId[0];
      if (
        byId.length !== 1 ||
        !matched ||
        !matched.enabled ||
        matched.scope === "unprotected" ||
        (route.listen && route.path_pattern && !matchesEventCoords(matched))
      ) {
        return reject();
      }
      const siblings = editableRoutes.filter((r) =>
        sameCoordGroup(r, matched.listen, matched.path ?? "", matched.match_type, matched.server_names),
      );
      if (!routePredicatesMatch(ev, servingRoute, matched, siblings.length)) return reject();
    } else if (route.listen && route.path_pattern) {
      const siblings = editableRoutes.filter((r) =>
        sameCoordGroup(r, route.listen, route.path_pattern, route.match_type, route.server_names),
      );
      const atOrdinal = siblings.filter((r) => r.match_ordinal === route.match_ordinal);
      const editableRoute = atOrdinal[0];
      if (
        atOrdinal.length !== 1 ||
        !editableRoute ||
        (editableRoute.route_id ?? "").trim() !== "" ||
        !editableRoute.enabled ||
        editableRoute.scope === "unprotected" ||
        !routePredicatesMatch(ev, servingRoute, editableRoute, siblings.length)
      ) {
        return reject();
      }
    }
  }

  for (const w of locationWafs) {
    const wRid = (w.route_id ?? "").trim();
    const sameCoords = matchesEventCoords(w);
    if ((rid && wRid === rid && route.listen && route.path_pattern && !sameCoords) || (sameCoords && rid !== wRid && (rid !== "" || wRid !== ""))) {
      return reject();
    }
  }

  const candidates = locationWafs.filter((w) => {
    const wRid = (w.route_id ?? "").trim();
    return rid && wRid ? wRid === rid : matchesEventCoords(w);
  });

  if (candidates.length > 1) return reject("Route identity is ambiguous across multiple location WAF overrides.");

  const first = candidates[0];
  if (first) {
    const sameCoordOverrides = locationWafs.filter((w) =>
      sameCoordGroup(w, first.listen, first.path ?? "", first.match_type, first.server_names),
    );
    if (!routePredicatesMatch(ev, servingRoute, first, sameCoordOverrides.length)) return reject();
  }

  if (route.scope === "override" && !first) return reject();
  if (first) {
    return {
      kind: "matched",
      target: {
        ...first,
        match_ordinal: first.match_ordinal ?? route.match_ordinal,
        selector_version: first.selector_version ?? "global-v1",
      },
      existing: true,
    };
  }

  if (globalConfig && !rid && (!globalConfig.waf_routes || globalConfig.waf_routes.length === 0)) {
    return reject();
  }

  // When an event comes from a concrete route that currently inherits the
  // global WAF policy, default tuning creates a route-local replacement
  // preserving the inherited effective/advanced settings so sibling routes
  // inheriting [waf] are not weakened by default (#631, #632).
  if (route.listen && route.path_pattern) {
    const synthesized: LocationWAF = {
      ...(rid ? { route_id: rid } : {}),
      listen: route.listen,
      ...(route.server_names && route.server_names.length > 0
        ? { server_names: [...route.server_names] }
        : {}),
      match_type: normalizeMatchType(route.match_type),
      path: route.path_pattern,
      match_ordinal: route.match_ordinal,
      selector_version: "global-v1",
      enabled: globalConfig ? globalConfig.waf_global_enabled : true,
      mode: globalConfig?.waf_global_mode ?? ev.mode,
      crs_enabled: globalConfig ? globalConfig.waf_crs_enabled : Boolean(ev.crs_version),
      ...(globalConfig?.waf_block_status ? { block_status: globalConfig.waf_block_status } : {}),
      ...(globalConfig?.waf_paranoia
        ? { paranoia: globalConfig.waf_paranoia }
        : ev.paranoia
          ? { paranoia: ev.paranoia }
          : {}),
      ...(globalConfig?.waf_blocking_paranoia
        ? { blocking_paranoia: globalConfig.waf_blocking_paranoia }
        : {}),
      ...(globalConfig?.waf_detection_paranoia
        ? { detection_paranoia: globalConfig.waf_detection_paranoia }
        : {}),
      ...(globalConfig?.waf_exceptions && globalConfig.waf_exceptions.length > 0
        ? { exceptions: [...globalConfig.waf_exceptions] }
        : {}),
      ...(globalConfig?.waf_request_body_limit
        ? { request_body_limit: globalConfig.waf_request_body_limit }
        : {}),
      ...(globalConfig?.waf_response_body_check !== undefined
        ? { response_body_check: globalConfig.waf_response_body_check }
        : {}),
      ...(globalConfig?.waf_directives_files && globalConfig.waf_directives_files.length > 0
        ? { directives_files: [...globalConfig.waf_directives_files] }
        : {}),
      ...(globalConfig?.waf_inline_rules ? { inline_rules: globalConfig.waf_inline_rules } : {}),
    };
    return { kind: "matched", target: synthesized, existing: false };
  }

  return { kind: "global" };
}
