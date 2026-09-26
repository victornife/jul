/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { AppProjection, HashPatch } from "@/api/client.ts";

export type { HashPatch };

/** Affinity key sources for strategy consistent_hash (ADR 0021). */
export type HashKeyKind = HashPatch["key"];
/** Strategies that place a request that has no usable key. */
export type HashFallback = NonNullable<HashPatch["fallback"]>;

export interface AppHashDraft {
  readonly key: HashKeyKind;
  readonly name: string;
  readonly fallback: HashFallback;
}

export const emptyHashDraft: AppHashDraft = { key: "client_ip", name: "", fallback: "round_robin" };

export const HASH_KEY_OPTIONS: ReadonlyArray<{ readonly value: HashKeyKind; readonly label: string }> =
  [
    { value: "client_ip", label: "Client IP (canonical client address)" },
    { value: "header", label: "Request header" },
    { value: "cookie", label: "Cookie" },
  ];

export const HASH_FALLBACK_OPTIONS: ReadonlyArray<{
  readonly value: HashFallback;
  readonly label: string;
}> = [
  { value: "round_robin", label: "Round robin" },
  { value: "weighted_round_robin", label: "Weighted round robin" },
  { value: "least_conn", label: "Least connections" },
];

// RFC 9110 token characters: the same rule the server applies to the name.
const TOKEN = /^[!#$%&'*+.^_`|~0-9A-Za-z-]+$/;

/** Local validation mirroring config.Validate; the server remains authoritative. */
export function hashDraftIssues(draft: AppHashDraft): string[] {
  if (draft.key === "client_ip") return [];
  const name = draft.name.trim();
  const noun = draft.key === "header" ? "header" : "cookie";
  if (name === "") return [`Consistent hashing on a ${noun} needs the ${noun} name.`];
  if (name.length > 128) return [`The ${noun} name is longer than 128 characters.`];
  if (!TOKEN.test(name)) return [`"${name}" is not a valid ${noun} name.`];
  if (draft.key === "header" && name.toLowerCase() === "cookie") {
    return ['Hash on a single cookie with key "Cookie" instead of the whole Cookie header.'];
  }
  return [];
}

export function hashDraftToPatch(draft: AppHashDraft): HashPatch {
  const patch: HashPatch = { key: draft.key, fallback: draft.fallback };
  if (draft.key !== "client_ip") patch.name = draft.name.trim();
  return patch;
}

export function hashDraftFromProjection(app: AppProjection): AppHashDraft {
  const h = app.hash;
  if (h === undefined) return emptyHashDraft;
  const key: HashKeyKind = h.key === "header" || h.key === "cookie" ? h.key : "client_ip";
  const fallback: HashFallback =
    h.fallback === "least_conn" || h.fallback === "weighted_round_robin"
      ? h.fallback
      : "round_robin";
  return { key, name: h.name ?? "", fallback };
}

/** Where a key source can be read. */
export function hashApplicability(key: HashKeyKind): string {
  return key === "client_ip"
    ? "HTTP routes and stream (TCP/UDP) routes"
    : "HTTP routes only — stream routes cannot read headers or cookies";
}

/** One-line description of an upstream's affinity policy. */
export function describeHash(app: AppProjection): string | null {
  const h = app.hash;
  if (h === undefined) return null;
  const source = h.key === "client_ip" ? "client IP" : `${h.key} ${h.name ?? ""}`.trim();
  return `${source} · without a key: ${h.fallback} · ${h.algorithm}`;
}

/** Explains the missing-key policy in operator terms. */
export function missingKeyExplanation(key: HashKeyKind, fallback: HashFallback): string {
  const missing =
    key === "client_ip"
      ? "the client address cannot be attributed (an unusable forwarding chain from a trusted proxy)"
      : `the ${key} is absent, empty, repeated or longer than 256 bytes`;
  return `When ${missing}, the request is placed by ${fallback} instead of being hashed. Unhealthy, open-circuit or saturated backends are skipped for the key's next choice.`;
}
