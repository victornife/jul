/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import {
  fetchRawConfig,
  fetchStats,
  fetchRoutes,
  purgeCache,
  type TrafficControls,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { patchConfig, ConfigRejectedError } from "@/api/client.ts";
import {
  upsertTopLevelTable,
  generateCompressionToml,
  generateCacheToml,
  generateRateLimitToml,
  generateLimitsToml,
  compressionWarnings,
  cacheWarnings,
  rateLimitWarnings,
  limitsWarnings,
  type CompressionDraft,
  type CacheDraft,
  type RateLimitDraft,
  type LimitsDraft,
} from "@/lib/trafficToml.ts";
import { TextField, NumberField, Toggle, CheckboxGroup, AffectedRoutes } from "./TrafficFormFields.tsx";

export type TrafficEditorKind = "compression" | "cache" | "rate_limit" | "limits";

const TITLES: Record<TrafficEditorKind, { title: string; subtitle: string }> = {
  compression: {
    title: "Compression",
    subtitle:
      "Compression reduces response size before sending data to clients. It usually helps HTML, CSS, JavaScript, JSON, and SVG; it usually does not help images, video, or archives.",
  },
  cache: {
    title: "Cache",
    subtitle:
      "The response cache stores upstream responses so repeat requests are served from memory or disk. Avoid caching authenticated or per-user responses.",
  },
  rate_limit: {
    title: "Rate limiting",
    subtitle:
      "Rate limiting bounds how many requests a client may make. Choose a key (client IP, a header, or a JWT claim), a sustained rate, and a burst allowance.",
  },
  limits: {
    title: "Limits & timeouts",
    subtitle:
      "Request limits and timeouts protect the server from slow or oversized requests. These apply per server block, so the generated keys are placed under the [[servers]] block you choose in the editor. The read/write/idle timeouts are listener-level (from the first server block on the address) and require a restart to change on an existing listener.",
  },
};

const DEFAULT_COMPRESSION_TYPES = [
  "text/*",
  "application/json",
  "application/javascript",
  "application/xml",
  "image/svg+xml",
];


export interface TrafficControlEditorProps {
  readonly kind: TrafficEditorKind;
  readonly current: TrafficControls;
  readonly onClose: () => void;
}

/**
 * Guided editor for the global traffic-control tables (Phase 3, Milestones
 * 3.1–3.3). It never writes directly: it upserts the relevant top-level table
 * into the running config and hands the draft to the Config editor where it
 * flows through Validate → Diff → Apply → Rollback.
 */
export function TrafficControlEditor({ kind, current, onClose }: TrafficControlEditorProps) {
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [purgeMsg, setPurgeMsg] = useState<string | null>(null);

  // Live observability for the cache and rate-limit editors (Milestones 3.2/3.3):
  // the cache hit ratio and the rate-limited request counts come from the runtime
  // stats snapshot; the route list resolves which routes opt into each feature.
  const stats = useQuery({
    queryKey: ["stats"],
    queryFn: fetchStats,
    enabled: kind === "cache" || kind === "rate_limit",
    refetchInterval: 5_000,
  });
  // Every editor kind needs the route list now: compression/cache/rate-limit to
  // show affected routes, and limits to pick the target [[servers]] block — so
  // the query is always enabled.
  const routes = useQuery({
    queryKey: ["routes"],
    queryFn: fetchRoutes,
  });

  // The limits editor targets a concrete [[servers]] block (P3-15): the operator
  // picks a listener and the server-level fields are applied in place via the
  // structured patch API, instead of emitting a commented snippet to hand-place.
  const listenAddrs = Array.from(new Set((routes.data ?? []).map((r) => r.listen)));
  const [targetListen, setTargetListen] = useState("");
  const effectiveListen = targetListen || listenAddrs[0] || "";

  async function onPurge(): Promise<void> {
    setPurgeMsg(null);
    try {
      await purgeCache();
      setPurgeMsg("Cache purged.");
    } catch {
      setPurgeMsg("Could not purge the cache.");
    }
  }

  const [compression, setCompression] = useState<CompressionDraft>({
    enabled: current.compression?.enabled ?? false,
    encoders: current.compression?.encoders ?? ["gzip"],
    minSize: "1k",
    types: DEFAULT_COMPRESSION_TYPES,
    precompressed: false,
  });
  const [cache, setCache] = useState<CacheDraft>({
    enabled: current.cache?.enabled ?? false,
    memoryMaxSize: current.cache?.memory_max ?? "64m",
    diskPath: current.cache?.disk_path ?? "",
    defaultTTL: current.cache?.default_ttl ?? "60s",
    staleWhileRevalidate: "",
  });
  const [rateLimit, setRateLimit] = useState<RateLimitDraft>({
    enabled: current.rate_limit?.enabled ?? false,
    key: current.rate_limit?.key ?? "ip",
    rate: current.rate_limit?.rate ?? 100,
    burst: current.rate_limit?.burst ?? 0,
    maxConns: 0,
  });
  const [limits, setLimits] = useState<LimitsDraft>({
    bodyLimit: "10m",
    readTimeout: "30s",
    writeTimeout: "30s",
    idleTimeout: "60s",
    proxyConnectTimeout: "5s",
    proxyReadTimeout: "30s",
    proxySendTimeout: "30s",
    maxFails: 3,
    failTimeout: "10s",
  });

  let fragment = "";
  let table = "";
  let warnings: string[] = [];
  switch (kind) {
    case "compression":
      fragment = generateCompressionToml(compression);
      table = "compression";
      warnings = compressionWarnings(compression);
      break;
    case "cache":
      fragment = generateCacheToml(cache);
      table = "cache";
      warnings = cacheWarnings(cache);
      break;
    case "rate_limit":
      fragment = generateRateLimitToml(rateLimit);
      table = "rate_limit";
      warnings = rateLimitWarnings(rateLimit);
      break;
    case "limits":
      fragment = generateLimitsToml(limits);
      table = "";
      warnings = limitsWarnings(limits);
      break;
  }

  function toggleEncoder(value: string, on: boolean): void {
    setCompression((d) => ({
      ...d,
      encoders: on ? [...d.encoders, value] : d.encoders.filter((e) => e !== value),
    }));
  }
  function toggleType(value: string, on: boolean): void {
    setCompression((d) => ({
      ...d,
      types: on ? [...d.types, value] : d.types.filter((t) => t !== value),
    }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    // The limits editor applies the server-level fields in place to the chosen
    // [[servers]] block via the structured patch API, so there is no snippet to
    // hand-place. The patched candidate is handed to the Config editor for the
    // usual diff review + apply.
    if (kind === "limits") {
      if (effectiveListen === "") {
        setError("No server block is available to apply limits to.");
        return;
      }
      try {
        const res = await patchConfig({
          op: "server_set_limits",
          listen: effectiveListen,
          limits: {
            client_max_body_size: limits.bodyLimit,
            read_timeout: limits.readTimeout,
            write_timeout: limits.writeTimeout,
            idle_timeout: limits.idleTimeout,
          },
        });
        setPendingDraft({
          kind: "patch",
          ops: [
            {
              op: "server_set_limits",
              listen: effectiveListen,
              limits: {
                client_max_body_size: limits.bodyLimit,
                read_timeout: limits.readTimeout,
                write_timeout: limits.writeTimeout,
                idle_timeout: limits.idleTimeout,
              },
            },
          ],
          baseVersion: res.base_version,
          previewDiff: res.diff,
        });
        void navigate("/config");
      } catch (err) {
        setError(
          err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.",
        );
      }
      return;
    }
    try {
      const raw = await fetchRawConfig();
      const next = upsertTopLevelTable(raw.raw ?? "", table, fragment);
      setPendingDraft({ kind: "toml", toml: next });
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this change.");
    }
  }

  const meta = TITLES[kind];

  return (
    <Drawer
      title={`Edit ${meta.title.toLowerCase()}`}
      subtitle="Review and apply safely in the editor."
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          {meta.subtitle}
        </p>

        {kind === "compression" && (
          <>
            <Toggle
              label="Enable compression"
              checked={compression.enabled}
              onChange={(v) => {
                setCompression((d) => ({ ...d, enabled: v }));
              }}
            />
            {compression.enabled && (
              <>
                <CheckboxGroup
                  label="Encoders"
                  options={["gzip", "br", "zstd"]}
                  selected={compression.encoders}
                  onToggle={toggleEncoder}
                />
                <TextField
                  label="Minimum size"
                  hint="Responses smaller than this are not compressed."
                  value={compression.minSize}
                  placeholder="1k"
                  onChange={(v) => {
                    setCompression((d) => ({ ...d, minSize: v }));
                  }}
                />
                <CheckboxGroup
                  label="Content types"
                  options={DEFAULT_COMPRESSION_TYPES}
                  selected={compression.types}
                  onToggle={toggleType}
                />
                <Toggle
                  label="Serve precompressed .br/.gz sidecars for static files"
                  checked={compression.precompressed}
                  onChange={(v) => {
                    setCompression((d) => ({ ...d, precompressed: v }));
                  }}
                />
                <AffectedRoutes
                  title="Routes that opt into compression"
                  paths={(routes.data ?? [])
                    .flatMap((r) => r.locations)
                    .filter((l) => l.compression)
                    .map((l) => l.match)}
                  emptyHint="No route sets a per-location compression override; the global setting applies everywhere."
                />
              </>
            )}
          </>
        )}

        {kind === "cache" && (
          <>
            <Toggle
              label="Enable cache"
              checked={cache.enabled}
              onChange={(v) => {
                setCache((d) => ({ ...d, enabled: v }));
              }}
            />
            {cache.enabled && (
              <>
                <TextField
                  label="Memory max size"
                  hint="In-memory tier cap, e.g. 64m."
                  value={cache.memoryMaxSize}
                  placeholder="64m"
                  onChange={(v) => {
                    setCache((d) => ({ ...d, memoryMaxSize: v }));
                  }}
                />
                <TextField
                  label="Disk path (optional)"
                  hint="Enables a disk overflow tier when set."
                  value={cache.diskPath}
                  placeholder="/var/cache/jul"
                  onChange={(v) => {
                    setCache((d) => ({ ...d, diskPath: v }));
                  }}
                />
                <TextField
                  label="Default TTL"
                  hint="Used when upstream gives no explicit freshness."
                  value={cache.defaultTTL}
                  placeholder="60s"
                  onChange={(v) => {
                    setCache((d) => ({ ...d, defaultTTL: v }));
                  }}
                />
                <TextField
                  label="Stale-while-revalidate (optional)"
                  hint="Serve stale entries this long while refreshing."
                  value={cache.staleWhileRevalidate}
                  placeholder="10s"
                  onChange={(v) => {
                    setCache((d) => ({ ...d, staleWhileRevalidate: v }));
                  }}
                />
                <AffectedRoutes
                  title="Routes that opt into caching"
                  paths={(routes.data ?? [])
                    .flatMap((r) => r.locations)
                    .filter((l) => l.cache)
                    .map((l) => l.match)}
                  emptyHint="No route opts into caching yet — enable it per route from the Route editor."
                />
              </>
            )}

            <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Cache effectiveness
              </span>
              {stats.data?.available ? (
                <div className="flex flex-wrap gap-4 text-sm">
                  <span className="text-jul-text">
                    Hit ratio:{" "}
                    <span className="font-mono text-jul-accent">
                      {((stats.data.cacheHitRatio || 0) * 100).toFixed(1)}%
                    </span>
                  </span>
                  <span className="text-jul-muted">
                    HIT {Math.round(stats.data.cacheEvents?.["HIT"] ?? 0).toLocaleString()}
                  </span>
                  <span className="text-jul-muted">
                    MISS {Math.round(stats.data.cacheEvents?.["MISS"] ?? 0).toLocaleString()}
                  </span>
                  <span className="text-jul-muted">
                    BYPASS {Math.round(stats.data.cacheEvents?.["BYPASS"] ?? 0).toLocaleString()}
                  </span>
                </div>
              ) : (
                <p className="text-xs text-jul-muted">No cache activity recorded yet.</p>
              )}
              <div className="flex items-center gap-3">
                <button
                  type="button"
                  onClick={() => {
                    void onPurge();
                  }}
                  className="rounded-md border border-jul-danger/50 px-3 py-1 text-xs font-medium text-jul-danger hover:bg-jul-danger/10"
                >
                  Purge cache now
                </button>
                {purgeMsg && <span className="text-xs text-jul-muted">{purgeMsg}</span>}
              </div>
            </div>
          </>
        )}

        {kind === "rate_limit" && (
          <>
            <Toggle
              label="Enable rate limiting"
              checked={rateLimit.enabled}
              onChange={(v) => {
                setRateLimit((d) => ({ ...d, enabled: v }));
              }}
            />
            {rateLimit.enabled && (
              <>
                <TextField
                  label="Key"
                  hint='Client identity: "ip", "header:X-Api-Key", or "jwt:sub".'
                  value={rateLimit.key}
                  placeholder="ip"
                  onChange={(v) => {
                    setRateLimit((d) => ({ ...d, key: v }));
                  }}
                />
                <div className="grid grid-cols-2 gap-3">
                  <NumberField
                    label="Rate (req/s)"
                    value={rateLimit.rate}
                    onChange={(v) => {
                      setRateLimit((d) => ({ ...d, rate: v }));
                    }}
                  />
                  <NumberField
                    label="Burst"
                    value={rateLimit.burst}
                    onChange={(v) => {
                      setRateLimit((d) => ({ ...d, burst: v }));
                    }}
                  />
                </div>
                <NumberField
                  label="Max connections (0 = unlimited)"
                  value={rateLimit.maxConns}
                  onChange={(v) => {
                    setRateLimit((d) => ({ ...d, maxConns: v }));
                  }}
                />
              </>
            )}

            <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Rate-limited requests
              </span>
              {stats.data?.available && Object.keys(stats.data.rateLimited ?? {}).length > 0 ? (
                <ul className="space-y-1">
                  {Object.entries(stats.data.rateLimited ?? {})
                    .sort((a, b) => b[1] - a[1])
                    .map(([keyKind, count]) => (
                      <li key={keyKind} className="flex justify-between text-sm">
                        <span className="font-mono text-jul-text">{keyKind}</span>
                        <span className="text-jul-muted">
                          {Math.round(count).toLocaleString()} rejected
                        </span>
                      </li>
                    ))}
                </ul>
              ) : (
                <p className="text-xs text-jul-muted">
                  No requests have been rate-limited yet (or rate limiting is inactive).
                </p>
              )}
            </div>

            <AffectedRoutes
              title="Routes that opt into rate limiting"
              paths={(routes.data ?? [])
                .flatMap((r) => r.locations)
                .filter((l) => l.rate_limit)
                .map((l) => l.match)}
              emptyHint="No route sets a per-location rate-limit override; the global setting applies everywhere."
            />
          </>
        )}

        {kind === "limits" && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Apply to server</span>
              <select
                value={effectiveListen}
                onChange={(e) => {
                  setTargetListen(e.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                {listenAddrs.length === 0 ? (
                  <option value="">(no server blocks found)</option>
                ) : (
                  listenAddrs.map((addr) => (
                    <option key={addr} value={addr}>
                      {addr}
                    </option>
                  ))
                )}
              </select>
              <span className="text-xs text-jul-muted">
                The body-size and server timeouts below are applied in place to this [[servers]]
                block via a structured edit — you review the diff before it is saved.
              </span>
            </label>

            <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-warning">
              The read, write, and idle timeouts are <strong>listener-level</strong>: they are
              taken from the first server block on the listen address and fixed when the socket is
              bound. Changing them on an address the server already serves cannot be hot-applied —
              the apply reports <strong>restart required</strong>. The max request body size
              hot-applies normally.
            </p>

            <TextField
              label="Max request body size"
              hint="Rejects bodies larger than this (e.g. 10m)."
              value={limits.bodyLimit}
              placeholder="10m"
              onChange={(v) => {
                setLimits((d) => ({ ...d, bodyLimit: v }));
              }}
            />
            <div className="grid grid-cols-2 gap-3">
              <TextField
                label="Read timeout"
                hint="Max time to read a request."
                value={limits.readTimeout}
                placeholder="30s"
                onChange={(v) => {
                  setLimits((d) => ({ ...d, readTimeout: v }));
                }}
              />
              <TextField
                label="Write timeout"
                hint="Max time to write a response."
                value={limits.writeTimeout}
                placeholder="30s"
                onChange={(v) => {
                  setLimits((d) => ({ ...d, writeTimeout: v }));
                }}
              />
            </div>
            <TextField
              label="Idle timeout"
              hint="Keep-alive idle connection timeout."
              value={limits.idleTimeout}
              placeholder="60s"
              onChange={(v) => {
                setLimits((d) => ({ ...d, idleTimeout: v }));
              }}
            />

            <div className="space-y-1 border-t border-jul-border pt-4">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Upstream timeouts
              </span>
              <p className="text-xs text-jul-muted">
                Timeouts stop Jul from waiting forever for a slow backend. These apply per proxied
                location and are <strong>not</strong> part of the in-place server edit above; copy
                them from the generated TOML below into the relevant [[servers.locations]] block.
              </p>
            </div>
            <div className="grid grid-cols-3 gap-3">
              <TextField
                label="Connect timeout"
                hint="Dialling the backend."
                value={limits.proxyConnectTimeout}
                placeholder="5s"
                onChange={(v) => {
                  setLimits((d) => ({ ...d, proxyConnectTimeout: v }));
                }}
              />
              <TextField
                label="Read timeout"
                hint="Reading the response."
                value={limits.proxyReadTimeout}
                placeholder="30s"
                onChange={(v) => {
                  setLimits((d) => ({ ...d, proxyReadTimeout: v }));
                }}
              />
              <TextField
                label="Send timeout"
                hint="Sending the request."
                value={limits.proxySendTimeout}
                placeholder="30s"
                onChange={(v) => {
                  setLimits((d) => ({ ...d, proxySendTimeout: v }));
                }}
              />
            </div>

            <div className="space-y-1 border-t border-jul-border pt-4">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Retries & fail-over
              </span>
              <p className="text-xs text-jul-muted">
                Retries can help with temporary backend failures, but too many retries can make
                incidents worse. Jul retires a backend after max_fails failures and brings it back
                after fail_timeout. These apply per upstream pool and are <strong>not</strong> part
                of the in-place server edit above; copy them from the generated TOML below.
              </p>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <NumberField
                label="Max fails"
                value={limits.maxFails}
                onChange={(v) => {
                  setLimits((d) => ({ ...d, maxFails: v }));
                }}
              />
              <TextField
                label="Fail timeout"
                hint="How long a failed backend stays retired."
                value={limits.failTimeout}
                placeholder="10s"
                onChange={(v) => {
                  setLimits((d) => ({ ...d, failTimeout: v }));
                }}
              />
            </div>
          </>
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-warning">
              Risk warnings
            </span>
            <ul className="list-disc space-y-1 pl-4">
              {warnings.map((w) => (
                <li key={w} className="text-xs text-jul-warning">
                  {w}
                </li>
              ))}
            </ul>
          </div>
        )}

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            {kind === "limits" ? "Reference TOML (upstream/retry keys)" : "Generated TOML"}
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {fragment}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}
