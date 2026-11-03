/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { CacheDraft, CompressionDraft, RateLimitDraft } from "@/lib/trafficPatchBuilders.ts";

export type { CacheDraft, CompressionDraft, RateLimitDraft } from "@/lib/trafficPatchBuilders.ts";

function tomlString(value: string): string {
  return `"${value.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

interface TomlScanState {
  mlState: "none" | "ml_basic" | "ml_literal";
  bracketDepth: number;
}

function advanceTomlLineState(line: string, state: TomlScanState): TomlScanState {
  let cur = state.mlState;
  let depth = state.bracketDepth;
  let i = 0;
  while (i < line.length) {
    if (cur === "none") {
      const ch = line[i];
      if (ch === "#") break;
      if (line.startsWith('"""', i)) {
        cur = "ml_basic";
        i += 3;
        continue;
      }
      if (line.startsWith("'''", i)) {
        cur = "ml_literal";
        i += 3;
        continue;
      }
      if (ch === '"') {
        i += 1;
        while (i < line.length) {
          if (line[i] === "\\") {
            i += 2;
            continue;
          }
          if (line[i] === '"') {
            i += 1;
            break;
          }
          i += 1;
        }
        continue;
      }
      if (ch === "'") {
        i += 1;
        while (i < line.length && line[i] !== "'") i += 1;
        i += 1;
        continue;
      }
      if (ch === "[" || ch === "{") {
        depth += 1;
      } else if ((ch === "]" || ch === "}") && depth > 0) {
        depth -= 1;
      }
      i += 1;
    } else if (cur === "ml_basic") {
      if (line[i] === "\\") {
        i += 2;
        continue;
      }
      if (line.startsWith('"""', i)) {
        cur = "none";
        i += 3;
        continue;
      }
      i += 1;
    } else {
      if (line.startsWith("'''", i)) {
        cur = "none";
        i += 3;
        continue;
      }
      i += 1;
    }
  }
  return { mlState: cur, bracketDepth: depth };
}

function parseTomlHeaderRoot(line: string): string | null {
  const trimmed = line.trim();
  if (!trimmed.startsWith("[")) return null;
  const m = /^(?:\[\[\s*([^[\]]+?)\s*\]\]|\[\s*([^[\]]+?)\s*\])\s*(?:#.*)?$/.exec(trimmed);
  if (!m) return null;
  const path = (m[1] ?? m[2] ?? "").trim();
  const seg = /^(?:"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|([A-Za-z0-9_-]+))\s*(?:\.|$)/.exec(
    path,
  );
  if (!seg) return null;
  const first = seg[1] ?? seg[2] ?? seg[3] ?? "";
  return first.length > 0 ? first : null;
}

function parseRootAssignmentKey(line: string): string | null {
  const trimmed = line.trim();
  if (trimmed.length === 0 || trimmed.startsWith("#") || trimmed.startsWith("[")) {
    return null;
  }
  const m = /^(?:"([^"\\]*(?:\\.[^"\\]*)*)"|'([^']*)'|([A-Za-z0-9_-]+))\s*(?:\.|=)/.exec(
    trimmed,
  );
  if (!m) return null;
  return m[1] ?? m[2] ?? m[3] ?? null;
}

/**
 * Replaces all sections and root-level dotted/inline assignments belonging to
 * a top-level table (`[table]`, `[table.*]`, `[[table.*]]`, `table.* = ...`,
 * `table = { ... }`) even when sub-tables or array-of-tables are dispersed
 * after unrelated top-level sections, while preserving multiline strings and
 * nested tables under other roots (such as `[servers.locations.waf]`).
 */
export function upsertTopLevelTable(raw: string, table: string, fragment: string): string {
  const lines = raw.split(/\r?\n/);
  interface Section {
    root: string | null;
    isHeader: boolean;
    lines: string[];
  }
  const sections: Section[] = [];
  let inHeaderSection = false;
  let current: Section = { root: null, isHeader: false, lines: [] };
  let scanState: TomlScanState = { mlState: "none", bracketDepth: 0 };
  let inRootTargetedStatement = false;

  const flushCurrent = (): void => {
    if (current.lines.length > 0 || current.root !== null) {
      sections.push(current);
    }
  };

  for (const line of lines) {
    const atStatementBoundary = scanState.mlState === "none" && scanState.bracketDepth === 0;
    if (atStatementBoundary) {
      if (inRootTargetedStatement) {
        flushCurrent();
        current = { root: null, isHeader: false, lines: [] };
        inRootTargetedStatement = false;
      }
      const headerRoot = parseTomlHeaderRoot(line);
      if (headerRoot !== null) {
        flushCurrent();
        inHeaderSection = true;
        current = { root: headerRoot, isHeader: true, lines: [line] };
        scanState = advanceTomlLineState(line, scanState);
        continue;
      }
      if (!inHeaderSection) {
        const assignKey = parseRootAssignmentKey(line);
        if (assignKey === table) {
          flushCurrent();
          current = { root: table, isHeader: false, lines: [line] };
          inRootTargetedStatement = true;
          scanState = advanceTomlLineState(line, scanState);
          continue;
        }
      }
    }
    current.lines.push(line);
    scanState = advanceTomlLineState(line, scanState);
  }
  flushCurrent();

  const rootPreambleLines: string[] = [];
  let hadRootTargetedStatement = false;
  const headerSections: Section[] = [];
  for (const sec of sections) {
    if (sec.isHeader) {
      headerSections.push(sec);
    } else if (sec.root === table) {
      hadRootTargetedStatement = true;
    } else {
      rootPreambleLines.push(...sec.lines);
    }
  }

  let replaced = false;
  const outParts: string[] = [];
  const rootPreambleText = rootPreambleLines.join("\n").trim();
  if (rootPreambleText.length > 0) {
    outParts.push(rootPreambleText);
  }
  const hasHeaderTarget = headerSections.some((sec) => sec.root === table);
  if (hadRootTargetedStatement && !hasHeaderTarget) {
    outParts.push(fragment.trim());
    replaced = true;
  }
  for (const sec of headerSections) {
    if (sec.root === table) {
      if (!replaced) {
        outParts.push(fragment.trim());
        replaced = true;
      }
      continue;
    }
    const text = sec.lines.join("\n").trim();
    if (text.length > 0) {
      outParts.push(text);
    }
  }
  if (!replaced) {
    outParts.push(fragment.trim());
  }
  return outParts.filter((part) => part.length > 0).join("\n\n").trim() + "\n";
}

export function isValidCacheMaxRanges(draft: CacheDraft): boolean {
  const maxRanges = draft.maxRanges ?? 0;
  if (!Number.isInteger(maxRanges) || maxRanges < 0 || maxRanges > 16) {
    return false;
  }
  return Boolean(draft.rangeEnabled) || maxRanges === 0;
}

const FORBIDDEN_CACHE_TAG_HEADERS = new Set([
  "age",
  "authorization",
  "cache-control",
  "cdn-cache-control",
  "connection",
  "content-encoding",
  "content-length",
  "content-location",
  "content-range",
  "content-type",
  "date",
  "etag",
  "expires",
  "keep-alive",
  "last-modified",
  "location",
  "proxy-authenticate",
  "proxy-authorization",
  "set-cookie",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
  "vary",
  "www-authenticate",
  "x-cache",
  "x-request-id",
]);

const HTTP_TOKEN_RE = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]{1,64}$/;

export function isValidCacheTagHeaders(draft: CacheDraft): boolean {
  const headers = draft.tagHeaders ?? [];
  if (headers.length > 8) return false;
  const seen = new Set<string>();
  for (const raw of headers) {
    const trimmed = raw.trim();
    if (trimmed === "" || trimmed !== raw || !HTTP_TOKEN_RE.test(trimmed)) {
      return false;
    }
    const lower = trimmed.toLowerCase();
    if (FORBIDDEN_CACHE_TAG_HEADERS.has(lower) || seen.has(lower)) {
      return false;
    }
    seen.add(lower);
  }
  return true;
}

/**
 * Generates the complete current [cache] table. Values are emitted independently
 * of enabled so dormant disk/TTL/stale settings survive disable/enable cycles.
 * Empty values represent schema fields that were absent and remain absent.
 */
export function generateCacheToml(draft: CacheDraft): string {
  if (!isValidCacheMaxRanges(draft)) {
    throw new Error("cache.max_ranges must be an integer between 0 and 16.");
  }
  const lines = ["[cache]", `enabled = ${String(draft.enabled)}`];
  if (draft.decisionSamples !== undefined)
    lines.push(`decision_samples = ${String(draft.decisionSamples)}`);
  if (draft.memoryMaxSize.trim() !== "") {
    lines.push(`memory_max_size = ${tomlString(draft.memoryMaxSize.trim())}`);
  }
  if (draft.diskPath.trim() !== "") lines.push(`disk_path = ${tomlString(draft.diskPath.trim())}`);
  if (draft.diskMaxSize.trim() !== "") {
    lines.push(`disk_max_size = ${tomlString(draft.diskMaxSize.trim())}`);
  }
  const maxEntrySize = (draft.maxEntrySize ?? "").trim();
  if (maxEntrySize !== "" && maxEntrySize !== "0b") {
    lines.push(`max_entry_size = ${tomlString(maxEntrySize)}`);
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
  if (draft.lock === true) {
    lines.push("lock = true");
    const lockTimeout = (draft.lockTimeout ?? "").trim();
    if (lockTimeout !== "" && lockTimeout !== "0s") {
      lines.push(`lock_timeout = ${tomlString(lockTimeout)}`);
    }
  }
  if (draft.rangeEnabled === true) {
    lines.push("range_enabled = true");
    const maxRanges = draft.maxRanges ?? 0;
    if (maxRanges > 0) {
      lines.push(`max_ranges = ${String(maxRanges)}`);
    }
  }
  const tagHeaders = (draft.tagHeaders ?? []).map((h) => h.trim()).filter((h) => h !== "");
  if (tagHeaders.length > 0) {
    if (!isValidCacheTagHeaders({ ...draft, tagHeaders })) {
      throw new Error(
        "cache.tag_headers must contain 1 to 8 unique non-reserved HTTP header token names.",
      );
    }
    lines.push(`tag_headers = [${tagHeaders.map(tomlString).join(", ")}]`);
    lines.push(`strip_tag_headers = ${String(draft.stripTagHeaders ?? true)}`);
  }
  return lines.join("\n");
}

export function compressionWarnings(draft: CompressionDraft): string[] {
  if (!draft.enabled) return [];
  const types = draft.types.map((value) => value.toLowerCase());
  const alreadyCompressed = ["image/", "video/", "application/zip", "application/gzip"];
  return types.some((value) => alreadyCompressed.some((prefix) => value.startsWith(prefix)))
    ? [
        "Compressing already-compressed assets (images, video, archives) wastes CPU for little gain.",
      ]
    : [];
}

export function cacheWarnings(draft: CacheDraft): string[] {
  if (!draft.enabled) return [];
  const warnings: string[] = [];
  if (draft.diskPath.trim() === "" && draft.memoryMaxSize.trim() === "") {
    warnings.push("No memory cap or disk path is set; verify the effective cache defaults.");
  }
  if (
    /^\d+\s*h/.test(draft.defaultTTL.trim()) ||
    /^[1-9]\d{3,}\s*s/.test(draft.defaultTTL.trim())
  ) {
    warnings.push(
      "A long default TTL can serve stale responses for dynamic data — verify per-route TTLs.",
    );
  }
  return warnings;
}

export function rateLimitWarnings(draft: RateLimitDraft): string[] {
  if (!draft.enabled) return [];
  const warnings: string[] = [];
  if (draft.rate <= 0)
    warnings.push("A rate of 0 rejects every request once the burst is exhausted.");
  if (draft.key.startsWith("header:")) {
    warnings.push(
      "Header-keyed limits are spoofable unless this server sits behind a trusted proxy.",
    );
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
  if (draft.bodyLimit.trim())
    server.push(`client_max_body_size = ${tomlString(draft.bodyLimit.trim())}`);
  if (draft.readTimeout.trim())
    server.push(`read_timeout = ${tomlString(draft.readTimeout.trim())}`);
  if (draft.writeTimeout.trim())
    server.push(`write_timeout = ${tomlString(draft.writeTimeout.trim())}`);
  if (draft.idleTimeout.trim())
    server.push(`idle_timeout = ${tomlString(draft.idleTimeout.trim())}`);
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
  if (draft.failTimeout.trim())
    retry.push(`fail_timeout = ${tomlString(draft.failTimeout.trim())}`);
  if (retry.length > 0) {
    sections.push(
      ["# Under the [[upstreams]] block (passive retry / fail-over):", ...retry].join("\n"),
    );
  }
  return sections.length === 0
    ? "# No limits set — all values left at their defaults."
    : sections.join("\n\n");
}

export function limitsWarnings(draft: LimitsDraft): string[] {
  const warnings: string[] = [];
  if (draft.readTimeout.trim() === "0" || draft.writeTimeout.trim() === "0") {
    warnings.push(
      "A timeout of 0 disables the deadline entirely, which can leak slow-loris connections.",
    );
  }
  if (/^\d+\s*g/i.test(draft.bodyLimit.trim())) {
    warnings.push("A multi-gigabyte body limit can let a single upload exhaust memory or disk.");
  }
  if (draft.proxyReadTimeout.trim() === "0" || draft.proxyConnectTimeout.trim() === "0") {
    warnings.push("An upstream timeout of 0 lets a slow backend hold a connection indefinitely.");
  }
  if (draft.maxFails > 10) {
    warnings.push(
      "A high max_fails keeps sending traffic to a failing backend before retiring it.",
    );
  }
  return warnings;
}
