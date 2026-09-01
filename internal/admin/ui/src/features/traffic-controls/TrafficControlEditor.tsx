/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import {
  fetchPendingRestart,
  fetchRawConfig,
  fetchRoutes,
  fetchStats,
  previewRawConfig,
  purgeCache,
  type ConfigPatch,
  type RouteProjection,
  type ServerLimitsProjection,
  type TrafficControls,
} from "@/api/client.ts";
import {
  recommendPatchAction,
  setPendingDraft,
  snapshotPendingRestart,
} from "@/lib/configDraftHandoff.ts";
import {
  buildCompressionPatch,
  buildGlobalRateLimitPatch,
  buildServerLimitsPatch,
  seedCache,
  seedCompression,
  seedRateLimit,
  seedServerLimits,
  serverTargetKey,
  type CacheDraft,
  type CompressionDraft,
  type RateLimitDraft,
  type ServerLimitsDraft,
} from "@/lib/trafficPatchBuilders.ts";
import {
  cacheWarnings,
  compressionWarnings,
  generateCacheToml,
  generateLimitsToml,
  limitsWarnings,
  rateLimitWarnings,
  upsertTopLevelTable,
  type LimitsDraft,
} from "@/lib/trafficToml.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";
import { AffectedRoutes, CheckboxGroup, NumberField, TextField, Toggle } from "./TrafficFormFields.tsx";

export type TrafficEditorKind = "compression" | "cache" | "rate_limit" | "limits";

const TITLES: Record<TrafficEditorKind, { title: string; subtitle: string }> = {
  compression: {
    title: "Compression",
    subtitle:
      "Compression reduces response size. This editor sends one sparse compression_set operation and preserves dormant values while disabled.",
  },
  cache: {
    title: "Cache",
    subtitle:
      "Cache remains a complete-table, raw path. A scalar-only change (memory/disk size, TTL, stale windows) applies live; enabling/disabling the cache or changing the disk path stages for the next restart. The candidate is pinned to the exact configuration version used to generate it and never enters browser storage.",
  },
  rate_limit: {
    title: "Rate limiting",
    subtitle:
      "The global request policy uses rate_limit_global_set. max_conns is a listener-level concurrent-connection cap whose final lifecycle is decided by the server.",
  },
  limits: {
    title: "Limits & timeouts",
    subtitle:
      "The selected server is seeded from its persisted projection. Only changed server-level values are sent; upstream timeout/retry reference TOML remains informational.",
  },
};

const DEFAULT_COMPRESSION_TYPES = [
  "text/*",
  "application/json",
  "application/javascript",
  "application/xml",
  "image/svg+xml",
];
const ENCODERS = ["gzip", "br", "zstd"];

function unique(values: readonly string[]): string[] {
  return Array.from(new Set(values));
}

function routeLabel(route: ServerLimitsProjection): string {
  const names = route.server_names ?? [];
  return names.length > 0 ? `${route.listen} — ${names.join(", ")}` : `${route.listen} — (any host)`;
}

function affectedPaths(
  routes: readonly RouteProjection[],
  predicate: (route: RouteProjection, location: RouteProjection["locations"][number]) => boolean,
): string[] {
  const paths: string[] = [];
  for (const route of routes) {
    for (const location of route.locations) {
      if (predicate(route, location)) paths.push(`${route.listen}${location.match}`);
    }
  }
  return unique(paths);
}

function equalCache(left: CacheDraft, right: CacheDraft): boolean {
  return (
    left.enabled === right.enabled &&
    left.memoryMaxSize === right.memoryMaxSize &&
    left.diskPath === right.diskPath &&
    left.diskMaxSize === right.diskMaxSize &&
    left.defaultTTL === right.defaultTTL &&
    left.staleWhileRevalidate === right.staleWhileRevalidate &&
    left.staleIfError === right.staleIfError
  );
}

export interface TrafficControlEditorProps {
  readonly kind: TrafficEditorKind;
  readonly current: TrafficControls;
  readonly onClose: () => void;
}

export function TrafficControlEditor({ kind, current, onClose }: TrafficControlEditorProps) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { has } = usePermission();
  const canWrite = has("config:write");
  const canRaw = has("config:raw");
  const canPurge = has("cache:purge");
  const batch = useRunPatchBatch();
  const [localError, setLocalError] = useState<string | null>(null);
  const [purgeMessage, setPurgeMessage] = useState<string | null>(null);
  const [cacheReviewing, setCacheReviewing] = useState(false);

  const routes = useQuery({ queryKey: ["routes"], queryFn: fetchRoutes });
  const stats = useQuery({
    queryKey: ["stats"],
    queryFn: fetchStats,
    enabled: kind === "cache" || kind === "rate_limit",
    refetchInterval: 5_000,
  });
  const cacheBase = useQuery({
    queryKey: ["raw-config", "cache-editor-base"],
    queryFn: fetchRawConfig,
    enabled: kind === "cache" && canRaw,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: false,
  });

  const compressionInitial = useRef<CompressionDraft>(seedCompression(current));
  const rateLimitInitial = useRef<RateLimitDraft>(seedRateLimit(current));
  const cacheInitial = useRef<CacheDraft>(seedCache(current));
  const [compression, setCompression] = useState(compressionInitial.current);
  const [rateLimit, setRateLimit] = useState(rateLimitInitial.current);
  const [cache, setCache] = useState(cacheInitial.current);

  const [targetServerKey, setTargetServerKey] = useState("");
  const [limitBaseline, setLimitBaseline] = useState<ServerLimitsDraft | null>(null);
  const [serverLimits, setServerLimits] = useState<ServerLimitsDraft>({
    bodyLimit: "",
    readTimeout: "",
    writeTimeout: "",
    idleTimeout: "",
  });
  const [referenceLimits, setReferenceLimits] = useState<LimitsDraft>({
    bodyLimit: "",
    readTimeout: "",
    writeTimeout: "",
    idleTimeout: "",
    proxyConnectTimeout: "5s",
    proxyReadTimeout: "30s",
    proxySendTimeout: "30s",
    maxFails: 3,
    failTimeout: "10s",
  });

  const routeOptions = current.servers ?? [];
  const selectedRoute =
    routeOptions.find((route) => serverTargetKey(route) === targetServerKey) ?? routeOptions[0] ?? null;

  useEffect(() => {
    if (kind !== "limits" || selectedRoute === null || limitBaseline !== null) return;
    const seeded = seedServerLimits(selectedRoute);
    setTargetServerKey(serverTargetKey(selectedRoute));
    setLimitBaseline(seeded);
    setServerLimits(seeded);
    setReferenceLimits((previous) => ({ ...previous, ...seeded }));
  }, [kind, selectedRoute, limitBaseline]);

  const compressionOperation = buildCompressionPatch(compressionInitial.current, compression);
  const rateLimitOperation = buildGlobalRateLimitPatch(rateLimitInitial.current, rateLimit);
  const limitsOperation =
    selectedRoute !== null && limitBaseline !== null
      ? buildServerLimitsPatch(selectedRoute, limitBaseline, serverLimits)
      : null;
  const cacheDirty = !equalCache(cacheInitial.current, cache);

  const warnings = useMemo(() => {
    switch (kind) {
      case "compression":
        return compressionWarnings(compression);
      case "cache":
        return cacheWarnings(cache);
      case "rate_limit":
        return rateLimitWarnings(rateLimit);
      case "limits":
        return limitsWarnings(referenceLimits);
    }
  }, [kind, compression, cache, rateLimit, referenceLimits]);

  const allRoutes = routes.data ?? [];
  const affected =
    kind === "cache"
      ? affectedPaths(allRoutes, (_route, location) => location.cache)
      : kind === "rate_limit"
        ? affectedPaths(allRoutes, (_route, location) => location.rate_limit)
        : kind === "compression"
          ? affectedPaths(allRoutes, () => true)
          : [];

  const clearErrors = (): void => {
    setLocalError(null);
    batch.clearError();
  };

  const runTyped = (operation: ConfigPatch | null): void => {
    clearErrors();
    if (operation === null) return;
    void batch.run([operation]);
  };

  const reviewCache = async (): Promise<void> => {
    clearErrors();
    if (!canRaw) {
      setLocalError("Cache editing requires config:raw because it preserves and stages a complete [cache] table.");
      return;
    }
    const raw = cacheBase.data;
    if (raw?.raw === undefined || raw.base_version === undefined || raw.base_version.trim() === "") {
      setLocalError("The exact raw configuration and base version are not available. Reload the cache editor before reviewing.");
      return;
    }
    setCacheReviewing(true);
    try {
      const candidate = upsertTopLevelTable(raw.raw, "cache", generateCacheToml(cache));
      const pendingResponse = await queryClient.fetchQuery({
        queryKey: ["pending-restart"],
        queryFn: fetchPendingRestart,
        staleTime: 0,
      });
      const pendingSnapshot = snapshotPendingRestart(pendingResponse);
      const preview = await previewRawConfig(candidate, raw.base_version);
      if (preview.base_version !== raw.base_version) {
        throw new Error("The cache preview did not match the source base version.");
      }
      if (!preview.valid || preview.validation_errors.length > 0) {
        const details = preview.validation_errors
          .map((issue) => `${issue.path ? `${issue.path}: ` : ""}${issue.summary}`)
          .join("; ");
        throw new Error(details || "The cache candidate is invalid.");
      }
      const action = recommendPatchAction(preview.lifecycle, pendingSnapshot);
      if (action !== "hot" && action !== "stage_restart" && action !== "update_staged") {
        throw new Error("The server did not offer a safe apply action for this cache candidate.");
      }
      setPendingDraft({
        kind: "toml",
        toml: candidate,
        baseVersion: raw.base_version,
        previewDiff: preview.diff,
        lifecycle: preview.lifecycle,
        recommendedAction: action,
        pendingRestart: pendingSnapshot,
        candidateState: "memory_only",
      });
      void navigate("/config");
    } catch (error) {
      setLocalError(error instanceof Error ? error.message : "The cache candidate could not be previewed.");
    } finally {
      setCacheReviewing(false);
    }
  };

  const selectServer = (next: ServerLimitsProjection): void => {
    const dirty =
      limitBaseline !== null &&
      buildServerLimitsPatch(selectedRoute ?? next, limitBaseline, serverLimits) !== null;
    if (dirty && !window.confirm("Changing server will discard unsaved Limits & Timeouts edits. Continue?")) {
      return;
    }
    const seeded = seedServerLimits(next);
    setTargetServerKey(serverTargetKey(next));
    setLimitBaseline(seeded);
    setServerLimits(seeded);
    setReferenceLimits((previous) => ({ ...previous, ...seeded }));
    clearErrors();
  };

  const meta = TITLES[kind];
  const typedOperation =
    kind === "compression"
      ? compressionOperation
      : kind === "rate_limit"
        ? rateLimitOperation
        : kind === "limits"
          ? limitsOperation
          : null;
  const busy = batch.busy || cacheReviewing;
  const reviewDisabled =
    busy ||
    !canWrite ||
    (kind === "cache"
      ? !cacheDirty || !canRaw || cacheBase.isLoading || cacheBase.isFetching || cacheBase.isError
      : typedOperation === null);

  const toggleListValue = (
    values: readonly string[],
    value: string,
    on: boolean,
  ): string[] => (on ? [...values, value] : values.filter((item) => item !== value));

  return (
    <Drawer title={`Edit ${meta.title.toLowerCase()}`} subtitle="Review the server-authoritative preview before applying." onClose={onClose}>
      <div className="space-y-5 p-4">
        <p className="text-sm text-jul-muted">{meta.subtitle}</p>

        {kind === "compression" && (
          <div className="space-y-4">
            <Toggle
              label="Enable compression"
              checked={compression.enabled}
              onChange={(enabled) => {
                setCompression((previous) => ({ ...previous, enabled }));
                clearErrors();
              }}
            />
            <CheckboxGroup
              label="Encoders (ordered as selected)"
              options={unique([...ENCODERS, ...compressionInitial.current.encoders])}
              selected={compression.encoders}
              onToggle={(value, on) => {
                setCompression((previous) => ({
                  ...previous,
                  encoders: toggleListValue(previous.encoders, value, on),
                }));
                clearErrors();
              }}
            />
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Compression level</span>
              <input
                type="number"
                value={compression.level}
                onChange={(event) => {
                  setCompression((previous) => ({ ...previous, level: Number(event.target.value) }));
                  clearErrors();
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text"
              />
            </label>
            <TextField
              label="Minimum response size"
              value={compression.minSize}
              placeholder="1k"
              onChange={(minSize) => {
                setCompression((previous) => ({ ...previous, minSize }));
                clearErrors();
              }}
            />
            <CheckboxGroup
              label="MIME types"
              options={unique([...DEFAULT_COMPRESSION_TYPES, ...compressionInitial.current.types])}
              selected={compression.types}
              onToggle={(value, on) => {
                setCompression((previous) => ({
                  ...previous,
                  types: toggleListValue(previous.types, value, on),
                }));
                clearErrors();
              }}
            />
            <Toggle
              label="Serve precompressed sidecars"
              checked={compression.precompressed}
              onChange={(precompressed) => {
                setCompression((previous) => ({ ...previous, precompressed }));
                clearErrors();
              }}
            />
            <AffectedRoutes title="Affected routes" paths={affected} emptyHint="No routes are currently projected." />
          </div>
        )}

        {kind === "rate_limit" && (
          <div className="space-y-4">
            <Toggle
              label="Enable global rate limiting"
              checked={rateLimit.enabled}
              onChange={(enabled) => {
                setRateLimit((previous) => ({ ...previous, enabled }));
                clearErrors();
              }}
            />
            <TextField
              label="Key"
              hint="Examples: ip, header:X-Client-ID, jwt:sub"
              value={rateLimit.key}
              onChange={(key) => {
                setRateLimit((previous) => ({ ...previous, key }));
                clearErrors();
              }}
            />
            <NumberField
              label="Requests per second"
              value={rateLimit.rate}
              onChange={(rate) => {
                setRateLimit((previous) => ({ ...previous, rate }));
                clearErrors();
              }}
            />
            <NumberField
              label="Burst"
              value={rateLimit.burst}
              onChange={(burst) => {
                setRateLimit((previous) => ({ ...previous, burst }));
                clearErrors();
              }}
            />
            <NumberField
              label="Maximum concurrent connections"
              value={rateLimit.maxConns}
              onChange={(maxConns) => {
                setRateLimit((previous) => ({ ...previous, maxConns }));
                clearErrors();
              }}
            />
            <p className="text-xs text-jul-muted">
              max_conns is listener-level. The authoritative preview may permit it for listeners that are all new; retained listeners are saved for the next restart.
            </p>
            <AffectedRoutes title="Routes with rate limiting" paths={affected} emptyHint="No locations currently opt into rate limiting." />
            {stats.data?.rateLimited && (
              <p className="text-xs text-jul-muted">
                Recent limited requests: {Object.values(stats.data.rateLimited).reduce((sum, value) => sum + value, 0)}
              </p>
            )}
          </div>
        )}

        {kind === "cache" && (
          <div className="space-y-4">
            {!canRaw && (
              <div role="alert" className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-sm text-jul-warning">
                Cache editing requires <span className="font-mono">config:raw</span> because the complete [cache] table is preserved as a raw, stage-only candidate. Typed compression and rate-limit editing remain available without this permission.
              </div>
            )}
            {canRaw && cacheBase.isError && (
              <div role="alert" className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-sm text-jul-danger">
                The exact raw configuration could not be loaded. Cache review is blocked so a candidate can never be paired with the wrong base version.
              </div>
            )}
            <Toggle
              label="Enable cache"
              checked={cache.enabled}
              onChange={(enabled) => {
                setCache((previous) => ({ ...previous, enabled }));
                clearErrors();
              }}
            />
            <TextField
              label="Memory maximum size"
              value={cache.memoryMaxSize}
              onChange={(memoryMaxSize) => {
                setCache((previous) => ({ ...previous, memoryMaxSize }));
                clearErrors();
              }}
            />
            <TextField
              label="Disk path"
              value={cache.diskPath}
              onChange={(diskPath) => {
                setCache((previous) => ({ ...previous, diskPath }));
                clearErrors();
              }}
            />
            <TextField
              label="Disk maximum size"
              value={cache.diskMaxSize}
              onChange={(diskMaxSize) => {
                setCache((previous) => ({ ...previous, diskMaxSize }));
                clearErrors();
              }}
            />
            <TextField
              label="Default TTL"
              value={cache.defaultTTL}
              onChange={(defaultTTL) => {
                setCache((previous) => ({ ...previous, defaultTTL }));
                clearErrors();
              }}
            />
            <TextField
              label="Stale while revalidate"
              value={cache.staleWhileRevalidate}
              onChange={(staleWhileRevalidate) => {
                setCache((previous) => ({ ...previous, staleWhileRevalidate }));
                clearErrors();
              }}
            />
            <TextField
              label="Stale if error"
              value={cache.staleIfError}
              onChange={(staleIfError) => {
                setCache((previous) => ({ ...previous, staleIfError }));
                clearErrors();
              }}
            />
            <AffectedRoutes title="Cached routes" paths={affected} emptyHint="No locations currently opt into caching." />
            {stats.data && (
              <p className="text-xs text-jul-muted">
                Current cache hit ratio: {(stats.data.cacheHitRatio * 100).toFixed(1)}%
              </p>
            )}
            <div className="flex items-center gap-2">
              <button
                type="button"
                disabled={!canPurge}
                className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text disabled:opacity-40"
                onClick={() => {
                  setPurgeMessage(null);
                  void purgeCache()
                    .then(() => {
                      setPurgeMessage("Cache purged.");
                    })
                    .catch(() => {
                      setPurgeMessage("Could not purge the cache.");
                    });
                }}
              >
                Purge cache
              </button>
              <ForbiddenAction permission="cache:purge" />
              {purgeMessage && <span className="text-xs text-jul-muted">{purgeMessage}</span>}
            </div>
          </div>
        )}

        {kind === "limits" && (
          <div className="space-y-4">
            {routeOptions.length === 0 ? (
              <p className="text-sm text-jul-muted">No server block is available.</p>
            ) : (
              <label className="block space-y-1">
                <span className="text-sm font-medium text-jul-text">Server</span>
                <select
                  value={selectedRoute === null ? "" : serverTargetKey(selectedRoute)}
                  className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text"
                  onChange={(event) => {
                    const next = routeOptions.find((route) => serverTargetKey(route) === event.target.value);
                    if (next !== undefined) selectServer(next);
                  }}
                >
                  {routeOptions.map((route) => (
                    <option key={serverTargetKey(route)} value={serverTargetKey(route)}>
                      {routeLabel(route)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <TextField label="Client maximum body size" value={serverLimits.bodyLimit} onChange={(bodyLimit) => { setServerLimits((previous) => ({ ...previous, bodyLimit })); setReferenceLimits((previous) => ({ ...previous, bodyLimit })); clearErrors(); }} />
            <TextField label="Read timeout" value={serverLimits.readTimeout} onChange={(readTimeout) => { setServerLimits((previous) => ({ ...previous, readTimeout })); setReferenceLimits((previous) => ({ ...previous, readTimeout })); clearErrors(); }} />
            <TextField label="Write timeout" value={serverLimits.writeTimeout} onChange={(writeTimeout) => { setServerLimits((previous) => ({ ...previous, writeTimeout })); setReferenceLimits((previous) => ({ ...previous, writeTimeout })); clearErrors(); }} />
            <TextField label="Idle timeout" value={serverLimits.idleTimeout} onChange={(idleTimeout) => { setServerLimits((previous) => ({ ...previous, idleTimeout })); setReferenceLimits((previous) => ({ ...previous, idleTimeout })); clearErrors(); }} />
            <div className="space-y-3 rounded-md border border-jul-border p-3">
              <p className="text-xs font-semibold uppercase tracking-wider text-jul-muted">Reference TOML only — not part of this structured operation</p>
              <TextField label="Proxy connect timeout" value={referenceLimits.proxyConnectTimeout} onChange={(proxyConnectTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, proxyConnectTimeout }));
              }} />
              <TextField label="Proxy read timeout" value={referenceLimits.proxyReadTimeout} onChange={(proxyReadTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, proxyReadTimeout }));
              }} />
              <TextField label="Proxy send timeout" value={referenceLimits.proxySendTimeout} onChange={(proxySendTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, proxySendTimeout }));
              }} />
              <NumberField label="Maximum failures" value={referenceLimits.maxFails} onChange={(maxFails) => {
                setReferenceLimits((previous) => ({ ...previous, maxFails }));
              }} />
              <TextField label="Failure timeout" value={referenceLimits.failTimeout} onChange={(failTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, failTimeout }));
              }} />
              <pre className="overflow-auto whitespace-pre-wrap rounded bg-jul-bg p-2 text-xs text-jul-muted">{generateLimitsToml(referenceLimits)}</pre>
            </div>
          </div>
        )}

        {warnings.length > 0 && (
          <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-warning">
            {warnings.map((warning) => <p key={warning}>{warning}</p>)}
          </div>
        )}
        {(localError ?? describePatchBatchError(batch.error)) && (
          <div role="alert" className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-xs text-jul-danger">
            {localError ?? describePatchBatchError(batch.error)}
          </div>
        )}

        <div className="flex flex-wrap items-center justify-end gap-3 border-t border-jul-border pt-4">
          <ForbiddenAction permission="config:write" />
          <button type="button" onClick={onClose} className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text">
            Cancel
          </button>
          <button
            type="button"
            disabled={reviewDisabled}
            onClick={() => {
              if (kind === "cache") void reviewCache();
              else runTyped(typedOperation);
            }}
            className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg disabled:opacity-40"
          >
            {busy ? "Reviewing…" : kind === "cache" ? "Review staged change" : "Review changes"}
          </button>
        </div>
      </div>
    </Drawer>
  );
}
