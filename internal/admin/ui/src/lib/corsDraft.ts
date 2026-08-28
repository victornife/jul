/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { CORSPatch, LocationCORSState } from "@/api/client.ts";

export interface CORSDraft {
  enabled: boolean;
  allowedOrigins: string;
  allowedMethods: string;
  allowedHeaders: string;
  exposedHeaders: string;
  allowCredentials: boolean;
  maxAge: string;
}

export function emptyCORSDraft(): CORSDraft {
  return {
    enabled: true,
    allowedOrigins: "",
    allowedMethods: "",
    allowedHeaders: "",
    exposedHeaders: "",
    allowCredentials: false,
    maxAge: "",
  };
}

export function seedCORSDraft(seed: LocationCORSState | undefined): CORSDraft {
  if (!seed) return emptyCORSDraft();
  return {
    enabled: seed.enabled,
    allowedOrigins: (seed.allowed_origins ?? []).join(", "),
    allowedMethods: (seed.allowed_methods ?? []).join(", "),
    allowedHeaders: (seed.allowed_headers ?? []).join(", "),
    exposedHeaders: (seed.exposed_headers ?? []).join(", "),
    allowCredentials: seed.allow_credentials,
    maxAge: seed.max_age ?? "",
  };
}

function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

/**
 * corsWarnings surfaces the ADR 0018 §9 rules the server would reject on: an
 * enabled policy needs at least one origin, "*" must be the only entry, and
 * "*" forbids credentials. It is a client-side hint, not the authority — the
 * validated re-parse still enforces the full grammar (byte-exact origin form,
 * bounds), which this deliberately does not duplicate.
 */
export function corsWarnings(d: CORSDraft): string[] {
  const warnings: string[] = [];
  if (!d.enabled) return warnings;
  const origins = splitList(d.allowedOrigins);
  if (origins.length === 0) {
    warnings.push('At least one allowed origin is required, or "*" for every origin.');
    return warnings;
  }
  const wildcard = origins.includes("*");
  if (wildcard && origins.length > 1) {
    warnings.push('"*" must be the only entry in allowed origins.');
  }
  if (wildcard && d.allowCredentials) {
    warnings.push('allow_credentials cannot be combined with the "*" wildcard origin.');
  }
  return warnings;
}

export function corsDraftToPatch(d: CORSDraft): CORSPatch {
  const maxAge = d.maxAge.trim();
  return {
    enabled: d.enabled,
    allowed_origins: splitList(d.allowedOrigins),
    allowed_methods: splitList(d.allowedMethods),
    allowed_headers: splitList(d.allowedHeaders),
    exposed_headers: splitList(d.exposedHeaders),
    allow_credentials: d.allowCredentials,
    ...(maxAge ? { max_age: maxAge } : {}),
  };
}
