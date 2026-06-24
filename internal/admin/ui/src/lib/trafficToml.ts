// Client-side generators for the guided Traffic Controls editors (Phase 3).
// Compression, cache, and rate limiting map to top-level TOML tables
// ([compression], [cache], [rate_limit]). Each editor emits an updated raw
// document that is routed through the authoritative Validate → Diff → Apply →
// Rollback pipeline in the Config editor — the editors never write directly, so
// a bad draft can never replace the running config.

function tomlString(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlStringArray(items: string[]): string {
  return `[${items.map((i) => tomlString(i)).join(", ")}]`;
}

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// ── Top-level table upsert ───────────────────────────────────────────────────

/**
 * Replaces the first occurrence of a top-level [table] block in raw with
 * fragment, or appends fragment when the table is absent. A "block" runs from
 * its `[table]` header to the next top-level header (a line starting with "[")
 * or EOF. Sub-tables ("[table.sub]") and array tables ("[[table]]") are treated
 * as part of the block so nested keys are replaced atomically.
 */
export function upsertTopLevelTable(raw: string, table: string, fragment: string): string {
  const lines = raw.split("\n");
  const headerRe = new RegExp(`^\\[+\\s*${escapeRe(table)}(\\.|\\s*\\]\\])`);
  const belongs = (line: string): boolean => {
    const t = line.trimStart();
    return t === `[${table}]` || headerRe.test(t);
  };

  let start = -1;
  for (let i = 0; i < lines.length; i++) {
    if (belongs(lines[i] ?? "")) {
      start = i;
      break;
    }
  }
  if (start === -1) {
    const base = raw.trimEnd();
    return base.length > 0 ? `${base}\n\n${fragment}\n` : `${fragment}\n`;
  }

  let end = lines.length;
  for (let i = start + 1; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line.startsWith("[") && !belongs(line)) {
      end = i;
      break;
    }
  }

  const before = lines.slice(0, start).join("\n").trimEnd();
  const after = lines.slice(end).join("\n").trim();
  const parts = [before, fragment, after].filter((p) => p.length > 0);
  return parts.join("\n\n").trimEnd() + "\n";
}

// ── Compression (Milestone 3.1) ──────────────────────────────────────────────

export interface CompressionDraft {
  enabled: boolean;
  encoders: string[]; // gzip, br, zstd
  minSize: string; // e.g. "1k"
  types: string[];
  precompressed: boolean;
}

export function generateCompressionToml(d: CompressionDraft): string {
  const lines: string[] = ["[compression]", `enabled = ${String(d.enabled)}`];
  if (d.enabled) {
    if (d.encoders.length > 0) lines.push(`encoders = ${tomlStringArray(d.encoders)}`);
    if (d.minSize.trim()) lines.push(`min_size = ${tomlString(d.minSize.trim())}`);
    if (d.types.length > 0) lines.push(`types = ${tomlStringArray(d.types)}`);
    if (d.precompressed) lines.push(`precompressed = true`);
  }
  return lines.join("\n");
}

// ── Cache (Milestone 3.2) ────────────────────────────────────────────────────

export interface CacheDraft {
  enabled: boolean;
  memoryMaxSize: string; // e.g. "64m"
  diskPath: string;
  defaultTTL: string; // e.g. "60s"
  staleWhileRevalidate: string;
}

export function generateCacheToml(d: CacheDraft): string {
  const lines: string[] = ["[cache]", `enabled = ${String(d.enabled)}`];
  if (d.enabled) {
    if (d.memoryMaxSize.trim()) lines.push(`memory_max_size = ${tomlString(d.memoryMaxSize.trim())}`);
    if (d.diskPath.trim()) lines.push(`disk_path = ${tomlString(d.diskPath.trim())}`);
    if (d.defaultTTL.trim()) lines.push(`default_ttl = ${tomlString(d.defaultTTL.trim())}`);
    if (d.staleWhileRevalidate.trim()) {
      lines.push(`stale_while_revalidate = ${tomlString(d.staleWhileRevalidate.trim())}`);
    }
  }
  return lines.join("\n");
}

// ── Rate limiting (Milestone 3.3) ────────────────────────────────────────────

export interface RateLimitDraft {
  enabled: boolean;
  key: string; // "ip" | "header:<Name>" | "jwt:<claim>"
  rate: number;
  burst: number;
  maxConns: number;
}

export function generateRateLimitToml(d: RateLimitDraft): string {
  const lines: string[] = ["[rate_limit]", `enabled = ${String(d.enabled)}`];
  if (d.enabled) {
    lines.push(`key = ${tomlString(d.key.trim() || "ip")}`);
    lines.push(`rate = ${String(Math.max(0, Math.floor(d.rate)))}`);
    if (d.burst > 0) lines.push(`burst = ${String(Math.floor(d.burst))}`);
    if (d.maxConns > 0) lines.push(`max_conns = ${String(Math.floor(d.maxConns))}`);
  }
  return lines.join("\n");
}

// ── Risk warnings ────────────────────────────────────────────────────────────

export function compressionWarnings(d: CompressionDraft): string[] {
  const w: string[] = [];
  if (!d.enabled) return w;
  const types = d.types.map((t) => t.toLowerCase());
  const alreadyCompressed = ["image/", "video/", "application/zip", "application/gzip"];
  if (types.some((t) => alreadyCompressed.some((c) => t.startsWith(c)))) {
    w.push(
      "Compressing already-compressed assets (images, video, archives) wastes CPU for little gain.",
    );
  }
  return w;
}

export function cacheWarnings(d: CacheDraft): string[] {
  const w: string[] = [];
  if (!d.enabled) return w;
  if (d.diskPath.trim() === "" && d.memoryMaxSize.trim() === "") {
    w.push("No memory cap or disk path set; the cache will use the 64 MiB memory default.");
  }
  const ttl = d.defaultTTL.trim();
  if (/^\d+\s*h/.test(ttl) || /^[1-9]\d{3,}\s*s/.test(ttl)) {
    w.push("A long default TTL can serve stale responses for dynamic data — verify per-route TTLs.");
  }
  return w;
}

export function rateLimitWarnings(d: RateLimitDraft): string[] {
  const w: string[] = [];
  if (!d.enabled) return w;
  if (d.rate <= 0) {
    w.push("A rate of 0 rejects every request once the burst is exhausted.");
  }
  if (d.key.startsWith("header:")) {
    w.push("Header-keyed limits are spoofable unless this server sits behind a trusted proxy.");
  }
  return w;
}

// ── Limits: timeouts, retries, body size (Milestone 3.4) ─────────────────────

export interface LimitsDraft {
  bodyLimit: string; // client_max_body_size, e.g. "10m"
  readTimeout: string; // server read timeout, e.g. "30s"
  writeTimeout: string; // server write timeout
  idleTimeout: string; // keep-alive idle timeout
  // Upstream / proxy timeouts (per-location keys).
  proxyConnectTimeout: string; // proxy_connect_timeout, e.g. "5s"
  proxyReadTimeout: string; // proxy_read_timeout, e.g. "30s"
  proxySendTimeout: string; // proxy_send_timeout, e.g. "30s"
  // Retry behaviour (passive health checks, per-upstream keys).
  maxFails: number; // max_fails — how many failures retire a backend
  failTimeout: string; // fail_timeout — how long a backend stays retired
}

/**
 * Generates a snippet of server-level limits the operator merges into the
 * relevant [[servers]] block. Unlike the global tables, timeouts and the body
 * limit are per-server, so this is emitted as standalone keys for the operator
 * to place under their chosen server block rather than upserted automatically.
 *
 * Upstream timeouts are per-location proxy keys and retries are per-upstream
 * passive-health keys, so each group is emitted under a clearly labelled
 * comment describing where it belongs (Milestone 3.4).
 */
export function generateLimitsToml(d: LimitsDraft): string {
  const sections: string[] = [];

  const server: string[] = [];
  if (d.bodyLimit.trim()) server.push(`client_max_body_size = ${tomlString(d.bodyLimit.trim())}`);
  if (d.readTimeout.trim()) server.push(`read_timeout = ${tomlString(d.readTimeout.trim())}`);
  if (d.writeTimeout.trim()) server.push(`write_timeout = ${tomlString(d.writeTimeout.trim())}`);
  if (d.idleTimeout.trim()) server.push(`idle_timeout = ${tomlString(d.idleTimeout.trim())}`);
  if (server.length > 0) {
    sections.push(["# Under the [[servers]] block:", ...server].join("\n"));
  }

  const proxy: string[] = [];
  if (d.proxyConnectTimeout.trim())
    proxy.push(`proxy_connect_timeout = ${tomlString(d.proxyConnectTimeout.trim())}`);
  if (d.proxyReadTimeout.trim())
    proxy.push(`proxy_read_timeout = ${tomlString(d.proxyReadTimeout.trim())}`);
  if (d.proxySendTimeout.trim())
    proxy.push(`proxy_send_timeout = ${tomlString(d.proxySendTimeout.trim())}`);
  if (proxy.length > 0) {
    sections.push(
      ["# Under the proxied [[servers.locations]] block:", ...proxy].join("\n"),
    );
  }

  const retry: string[] = [];
  if (d.maxFails > 0) retry.push(`max_fails = ${String(Math.floor(d.maxFails))}`);
  if (d.failTimeout.trim()) retry.push(`fail_timeout = ${tomlString(d.failTimeout.trim())}`);
  if (retry.length > 0) {
    sections.push(
      [
        "# Under the [[upstreams]] block (passive retry / fail-over):",
        ...retry,
      ].join("\n"),
    );
  }

  if (sections.length === 0) return "# No limits set — all values left at their defaults.";
  return sections.join("\n\n");
}

export function limitsWarnings(d: LimitsDraft): string[] {
  const w: string[] = [];
  if (d.readTimeout.trim() === "0" || d.writeTimeout.trim() === "0") {
    w.push("A timeout of 0 disables the deadline entirely, which can leak slow-loris connections.");
  }
  if (/^\d+\s*g/i.test(d.bodyLimit.trim())) {
    w.push("A multi-gigabyte body limit can let a single upload exhaust memory or disk.");
  }
  if (d.proxyReadTimeout.trim() === "0" || d.proxyConnectTimeout.trim() === "0") {
    w.push("An upstream timeout of 0 lets a slow backend hold a connection indefinitely.");
  }
  if (d.maxFails > 10) {
    w.push("A high max_fails keeps sending traffic to a failing backend before retiring it.");
  }
  return w;
}
