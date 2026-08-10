/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type {
  CacheDraft,
  CompressionDraft,
  RateLimitDraft,
} from "@/lib/trafficPatchBuilders.ts";

export type { CacheDraft, CompressionDraft, RateLimitDraft } from "@/lib/trafficPatchBuilders.ts";

function tomlString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function escapeRe(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

/**
 * Replaces the first occurrence of a top-level table or appends it. Raw editors
 * remain an authorized compatibility path; typed compression and rate-limit
 * editors no longer call this helper. Cache deliberately remains a complete
 * table raw/stage-only path until a cache_set operation exists.
 */
export function upsertTopLevelTable(raw: string, table: string, fragment: string): string {
  const lines = raw.split(/\r?\n/);
  const headerRe = new RegExp(`^\\[+\\s*${escapeRe(table)}(\\.|\\s*\\]\\])`);
  const belongs = (line: string): boolean => {
    const trimmed = line.trim();
    return trimmed === `[${table}]` || headerRe.test(trimmed);
  };

  let start = -1;
  for (let index = 0; index < lines.length; index += 1) {
    if (belongs(lines[index] ?? "")) {
      start = index;
      break;
    }
  }
  if (start === -1) {
    const base = raw.trimEnd();
    return base.length > 0 ? `${base}\n\n${fragment}\n` : `${fragment}\n`;
  }

  let end = lines.length;
  for (let index = start + 1; index < lines.length; index += 1) {
    const line = lines[index] ?? "";
    if (line.startsWith("[") && !belongs(line)) {
      end = index;
      break;
    }
  }

  const before = lines.slice(0, start).join("\n").trim();
  const after = lines.slice(end).join("\n").trim();
  return [before, fragment, after].filter((part) => part.length > 0).join("\n\n").trim() + "\n";
}

/**
 * Generates the complete current [cache] table. Values are emitted independently
 * of enabled so dormant disk/TTL/stale settings survive disable/enable cycles.
 * Empty values represent schema fields that were absent and remain absent.
 */
export function generateCacheToml(draft: CacheDraft): string {
  const lines = ["[cache]", `enabled = ${String(draft.enabled)}`];
  if (draft.memoryMaxSize.trim() !== "") {
    lines.push(`memory_max_size = ${tomlString(draft.memoryMaxSize.trim())}`);
  }
  if (draft.diskPath.trim() !== "") lines.push(`disk_path = ${tomlString(draft.diskPath.trim())}`);
  if (draft.diskMaxSize.trim() !== "") {
    lines.push(`disk_max_size = ${tomlString(draft.diskMaxSize.trim())}`);
  }
  if (draft.defaultTTL.trim() !== "") {
    lines.push(`default_ttl = ${tomlString(draft.defaultTTL.trim())}`);
  }
  if (draft.staleWhileRevalidate.trim() !== "") {
    lines.push(`stale_while_revalidate = ${tomlString(draft.staleWhileRevalidate.trim())}`);
  }
  if (draft.staleIfError.trim() !== "") {
    lines.push(`stale_if_error = ${tomlString(draft.staleIfError.trim())}`);
  }
  return lines.join("\n");
}

export function compressionWarnings(draft: CompressionDraft): string[] {
  if (!draft.enabled) return [];
  const types = draft.types.map((value) => value.toLowerCase());
  const alreadyCompressed = ["image/", "video/", "application/zip", "application/gzip"];
  return types.some((value) => alreadyCompressed.some((prefix) => value.startsWith(prefix)))
    ? ["Compressing already-compressed assets (images, video, archives) wastes CPU for little gain."]
    : [];
}

export function cacheWarnings(draft: CacheDraft): string[] {
  if (!draft.enabled) return [];
  const warnings: string[] = [];
  if (draft.diskPath.trim() === "" && draft.memoryMaxSize.trim() === "") {
    warnings.push("No memory cap or disk path is set; verify the effective cache defaults.");
  }
  if (/^\d+\s*h/.test(draft.defaultTTL.trim()) || /^[1-9]\d{3,}\s*s/.test(draft.defaultTTL.trim())) {
    warnings.push("A long default TTL can serve stale responses for dynamic data — verify per-route TTLs.");
  }
  return warnings;
}

export function rateLimitWarnings(draft: RateLimitDraft): string[] {
  if (!draft.enabled) return [];
  const warnings: string[] = [];
  if (draft.rate <= 0) warnings.push("A rate of 0 rejects every request once the burst is exhausted.");
  if (draft.key.startsWith("header:")) {
    warnings.push("Header-keyed limits are spoofable unless this server sits behind a trusted proxy.");
  }
  return warnings;
}

// The upstream/retry reference-TOML portion remains intentionally unchanged by
// #81; only the four server-level values migrate to a sparse server_set_limits.
export interface LimitsDraft {
  bodyLimit: string;
  readTimeout: string;
  writeTimeout: string;
  idleTimeout: string;
  proxyConnectTimeout: string;
  proxyReadTimeout: string;
  proxySendTimeout: string;
  maxFails: number;
  failTimeout: string;
}

export function generateLimitsToml(draft: LimitsDraft): string {
  const sections: string[] = [];
  const server: string[] = [];
  if (draft.bodyLimit.trim()) server.push(`client_max_body_size = ${tomlString(draft.bodyLimit.trim())}`);
  if (draft.readTimeout.trim()) server.push(`read_timeout = ${tomlString(draft.readTimeout.trim())}`);
  if (draft.writeTimeout.trim()) server.push(`write_timeout = ${tomlString(draft.writeTimeout.trim())}`);
  if (draft.idleTimeout.trim()) server.push(`idle_timeout = ${tomlString(draft.idleTimeout.trim())}`);
  if (server.length > 0) sections.push(["# Under the [[servers]] block:", ...server].join("\n"));

  const proxy: string[] = [];
  if (draft.proxyConnectTimeout.trim()) {
    proxy.push(`proxy_connect_timeout = ${tomlString(draft.proxyConnectTimeout.trim())}`);
  }
  if (draft.proxyReadTimeout.trim()) {
    proxy.push(`proxy_read_timeout = ${tomlString(draft.proxyReadTimeout.trim())}`);
  }
  if (draft.proxySendTimeout.trim()) {
    proxy.push(`proxy_send_timeout = ${tomlString(draft.proxySendTimeout.trim())}`);
  }
  if (proxy.length > 0) {
    sections.push(["# Under the proxied [[servers.locations]] block:", ...proxy].join("\n"));
  }

  const retry: string[] = [];
  if (draft.maxFails > 0) retry.push(`max_fails = ${String(Math.floor(draft.maxFails))}`);
  if (draft.failTimeout.trim()) retry.push(`fail_timeout = ${tomlString(draft.failTimeout.trim())}`);
  if (retry.length > 0) {
    sections.push(["# Under the [[upstreams]] block (passive retry / fail-over):", ...retry].join("\n"));
  }
  return sections.length === 0
    ? "# No limits set — all values left at their defaults."
    : sections.join("\n\n");
}

export function limitsWarnings(draft: LimitsDraft): string[] {
  const warnings: string[] = [];
  if (draft.readTimeout.trim() === "0" || draft.writeTimeout.trim() === "0") {
    warnings.push("A timeout of 0 disables the deadline entirely, which can leak slow-loris connections.");
  }
  if (/^\d+\s*g/i.test(draft.bodyLimit.trim())) {
    warnings.push("A multi-gigabyte body limit can let a single upload exhaust memory or disk.");
  }
  if (draft.proxyReadTimeout.trim() === "0" || draft.proxyConnectTimeout.trim() === "0") {
    warnings.push("An upstream timeout of 0 lets a slow backend hold a connection indefinitely.");
  }
  if (draft.maxFails > 10) {
    warnings.push("A high max_fails keeps sending traffic to a failing backend before retiring it.");
  }
  return warnings;
}
