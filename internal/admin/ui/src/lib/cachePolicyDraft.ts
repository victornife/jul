/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type {
  ConfigPatch,
  LocationCachePolicyPatch,
  LocationCachePolicyState,
  LocationProjection,
  RouteTarget,
} from "@/api/client.ts";

export type CachePolicyStarterId =
  | "disabled_private"
  | "conservative_dynamic"
  | "static_assets";

export interface CachePolicyStarter {
  readonly id: CachePolicyStarterId;
  readonly label: string;
  readonly summary: string;
  readonly cacheEnabled: boolean;
  readonly defaultTTL: string;
  readonly maxTTL: string;
  readonly staleWhileRevalidate: string;
  readonly staleIfError: string;
  readonly bypassMode: "none" | "named" | "any";
  readonly bypassCookiesInput: string;
}

export const CACHE_POLICY_STARTERS: readonly CachePolicyStarter[] = [
  {
    id: "disabled_private",
    label: "Disabled / private",
    summary:
      "Turns route caching off and removes route cache_policy overrides. Best for login, account, checkout, or personalized endpoints.",
    cacheEnabled: false,
    defaultTTL: "",
    maxTTL: "",
    staleWhileRevalidate: "",
    staleIfError: "",
    bypassMode: "none",
    bypassCookiesInput: "",
  },
  {
    id: "conservative_dynamic",
    label: "Conservative dynamic / API",
    summary:
      "Requires explicit origin freshness (default_ttl = 0s), caps fresh lifetime to 1m, enables 30s SWR / 5m stale-if-error, and bypasses any request with a Cookie header.",
    cacheEnabled: true,
    defaultTTL: "0s",
    maxTTL: "1m",
    staleWhileRevalidate: "30s",
    staleIfError: "5m",
    bypassMode: "any",
    bypassCookiesInput: "",
  },
  {
    id: "static_assets",
    label: "Static / versioned assets",
    summary:
      "Uses a 1h fallback TTL when origin headers omit freshness, caps fresh lifetime at 24h, and serves 5m SWR / 1h stale-if-error without cookie bypass.",
    cacheEnabled: true,
    defaultTTL: "1h",
    maxTTL: "24h",
    staleWhileRevalidate: "5m",
    staleIfError: "1h",
    bypassMode: "none",
    bypassCookiesInput: "",
  },
] as const;

export interface CachePolicyFormState {
  readonly cacheEnabled: boolean;
  readonly defaultTTL: string;
  readonly maxTTL: string;
  readonly staleWhileRevalidate: string;
  readonly staleIfError: string;
  readonly bypassMode: "none" | "named" | "any";
  readonly bypassCookiesInput: string;
}

const GO_DURATION_RE = /^(?:0|(?:\d+(?:\.\d+)?(?:ns|us|µs|μs|ms|s|m|h))+)$/;
const COOKIE_TOKEN_RE = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

export function seedCachePolicyForm(loc: LocationProjection): CachePolicyFormState {
  const cp: LocationCachePolicyState | undefined = loc.cache_policy;
  let bypassMode: "none" | "named" | "any" = "none";
  if (cp?.bypass_any_cookie) {
    bypassMode = "any";
  } else if (cp?.bypass_cookies && cp.bypass_cookies.length > 0) {
    bypassMode = "named";
  }
  return {
    cacheEnabled: loc.cache,
    defaultTTL: cp?.default_ttl ?? "",
    maxTTL: cp?.max_ttl ?? "",
    staleWhileRevalidate: cp?.stale_while_revalidate ?? "",
    staleIfError: cp?.stale_if_error ?? "",
    bypassMode,
    bypassCookiesInput: cp?.bypass_cookies ? cp.bypass_cookies.join(", ") : "",
  };
}

export function parseDurationSecondsApprox(raw: string): number | null {
  const trimmed = raw.trim();
  if (trimmed === "") return null;
  if (trimmed === "0") return 0;
  if (!GO_DURATION_RE.test(trimmed)) return null;
  const partRe = /(\d+(?:\.\d+)?)(ns|us|µs|μs|ms|s|m|h)/g;
  let total = 0;
  let matched = false;
  let m: RegExpExecArray | null;
  while ((m = partRe.exec(trimmed)) !== null) {
    matched = true;
    const val = Number.parseFloat(m[1] ?? "0");
    const unit = m[2];
    switch (unit) {
      case "ns":
        total += val / 1e9;
        break;
      case "us":
      case "µs":
      case "μs":
        total += val / 1e6;
        break;
      case "ms":
        total += val / 1e3;
        break;
      case "s":
        total += val;
        break;
      case "m":
        total += val * 60;
        break;
      case "h":
        total += val * 3600;
        break;
      default:
        return null;
    }
  }
  return matched ? total : null;
}

export function parseBypassCookiesInput(raw: string): {
  readonly cookies: string[];
  readonly errors: string[];
} {
  const parts = raw
    .split(/[\s,]+/)
    .map((p) => p.trim())
    .filter((p) => p.length > 0);
  const errors: string[] = [];
  const seen = new Set<string>();
  const cookies: string[] = [];

  if (parts.length > 32) {
    errors.push("bypass_cookies cannot exceed 32 cookie names.");
  }

  for (const name of parts) {
    if (name.length > 128) {
      errors.push(`Cookie name "${name.slice(0, 24)}…" exceeds 128 bytes.`);
      continue;
    }
    if (!COOKIE_TOKEN_RE.test(name)) {
      errors.push(
        `Cookie name "${name}" must be a valid HTTP token (no "=", ";", spaces, or control characters).`,
      );
      continue;
    }
    const lower = name.toLowerCase();
    if (seen.has(lower)) {
      errors.push(`Duplicate cookie name "${name}" (matched case-insensitively).`);
      continue;
    }
    seen.add(lower);
    cookies.push(lower);
  }

  cookies.sort();
  return { cookies, errors };
}

export interface CachePolicyValidation {
  readonly errors: string[];
  readonly warnings: string[];
  readonly patch: LocationCachePolicyPatch | null;
}

export function validateCachePolicyForm(form: CachePolicyFormState): CachePolicyValidation {
  const errors: string[] = [];
  const warnings: string[] = [];

  const checkDuration = (label: string, raw: string): string | undefined => {
    const trimmed = raw.trim();
    if (trimmed === "") return undefined;
    if (trimmed.startsWith("-")) {
      errors.push(`${label} cannot be negative.`);
      return undefined;
    }
    if (parseDurationSecondsApprox(trimmed) === null) {
      errors.push(`${label} must be a valid Go duration (e.g. "0s", "30s", "5m", "1h").`);
      return undefined;
    }
    return trimmed === "0" ? "0s" : trimmed;
  };

  const defaultTTL = checkDuration("default_ttl", form.defaultTTL);
  const maxTTL = checkDuration("max_ttl", form.maxTTL);
  const swr = checkDuration("stale_while_revalidate", form.staleWhileRevalidate);
  const sie = checkDuration("stale_if_error", form.staleIfError);

  if (defaultTTL !== undefined && maxTTL !== undefined) {
    const dSec = parseDurationSecondsApprox(defaultTTL);
    const mSec = parseDurationSecondsApprox(maxTTL);
    if (dSec !== null && mSec !== null && dSec > mSec) {
      errors.push(`default_ttl (${defaultTTL}) must not exceed max_ttl (${maxTTL}).`);
    }
  }

  let bypassCookies: string[] = [];
  if (form.bypassMode === "named") {
    const parsed = parseBypassCookiesInput(form.bypassCookiesInput);
    bypassCookies = parsed.cookies;
    errors.push(...parsed.errors);
    if (bypassCookies.length === 0 && parsed.errors.length === 0) {
      errors.push("Enter at least one cookie name for named cookie bypass, or select None.");
    }
  }

  const bypassAny = form.bypassMode === "any";
  const hasAnyOverride =
    defaultTTL !== undefined ||
    maxTTL !== undefined ||
    swr !== undefined ||
    sie !== undefined ||
    bypassAny ||
    bypassCookies.length > 0;

  if (!form.cacheEnabled && hasAnyOverride) {
    warnings.push(
      "Route cache is disabled (cache = false); [servers.locations.cache_policy] overrides remain inactive until route caching is enabled.",
    );
  }

  const patch: LocationCachePolicyPatch | null = hasAnyOverride
    ? {
        ...(defaultTTL !== undefined ? { default_ttl: defaultTTL } : {}),
        ...(maxTTL !== undefined ? { max_ttl: maxTTL } : {}),
        ...(swr !== undefined ? { stale_while_revalidate: swr } : {}),
        ...(sie !== undefined ? { stale_if_error: sie } : {}),
        ...(bypassCookies.length > 0 ? { bypass_cookies: bypassCookies } : {}),
        ...(bypassAny ? { bypass_any_cookie: true } : {}),
      }
    : null;

  return { errors, warnings, patch };
}

export function buildCachePolicyPatchOps(
  target: RouteTarget,
  loc: LocationProjection,
  form: CachePolicyFormState,
  patch: LocationCachePolicyPatch | null,
): ConfigPatch[] {
  const ops: ConfigPatch[] = [];
  if (form.cacheEnabled !== loc.cache) {
    ops.push({
      op: "route_toggle_cache",
      ...target,
      enabled: form.cacheEnabled,
    });
  }
  if (patch !== null) {
    ops.push({
      op: "location_cache_policy_set",
      ...target,
      cache_policy: patch,
    });
  } else if (loc.cache_policy !== undefined) {
    ops.push({
      op: "location_cache_policy_clear",
      ...target,
    });
  }
  return ops;
}
