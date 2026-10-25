/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useId, useMemo, useState } from "react";
import {
  FORBIDDEN_EVENT,
  UNAUTHORIZED_EVENT,
  executeCachePurge,
  fetchCachePurgeOperation,
  previewCachePurge,
  type CacheGuidance,
  type CachePurgePreviewResponse,
  type CachePurgeResponse,
  type CacheSelectorKind,
  type RouteProjection,
} from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { buildCliCommandSnippet, buildSelectorFromDraft } from "@/lib/trafficToml.ts";

export interface CacheInvalidationPanelProps {
  readonly cacheEnabled: boolean;
  readonly tagHeaders: readonly string[];
  readonly serverNames?: readonly string[] | undefined;
  readonly routes?: readonly RouteProjection[] | undefined;
  readonly authorityMode?: string | undefined;
}

const SELECTOR_KIND_OPTIONS: ReadonlyArray<{
  readonly value: CacheSelectorKind;
  readonly label: string;
  readonly summary: string;
}> = [
  {
    value: "exact_url",
    label: "Exact public URL / resource",
    summary:
      "Invalidate one exact hostname + path + query resource across GET, HEAD, and Vary representations.",
  },
  {
    value: "prefix",
    label: "Host-bound path prefix",
    summary:
      "Invalidate all cached paths and query strings under a segment-bounded path prefix on one hostname.",
  },
  {
    value: "glob",
    label: "Host-bound anchored glob",
    summary:
      "Invalidate paths matching a bounded anchored pattern (* for one segment, ** for standalone multi-segment).",
  },
  {
    value: "tag",
    label: "Origin grouping tag",
    summary:
      "Invalidate cached representations carrying trusted origin grouping tags within one hostname namespace.",
  },
  {
    value: "host",
    label: "Whole hostname / service scope",
    summary:
      "Invalidate every cached resource belonging to one hostname while preserving other hosts.",
  },
  {
    value: "all",
    label: "Entire node cache (all hosts)",
    summary:
      "Invalidate every cached resource across all hostnames on this Jul instance.",
  },
];

function formatByteCount(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  if (bytes < 1024) return `${String(bytes)} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MiB`;
}

function generateIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return `console-purge-${crypto.randomUUID()}`;
  }
  return `console-purge-${String(Date.now())}-${Math.random().toString(36).slice(2, 10)}`;
}

function GuidanceList({
  items,
}: {
  readonly items: readonly CacheGuidance[];
}): React.JSX.Element | null {
  if (items.length === 0) return null;
  return (
    <div style={{ display: "grid", gap: "0.5rem" }}>
      {items.map((w) => {
        const isWarn = w.severity.toLowerCase() === "warning";
        return (
          <div
            key={`${w.code}-${w.summary}`}
            role="note"
            style={{
              padding: "0.55rem 0.7rem",
              borderRadius: "6px",
              border: `1px solid ${
                isWarn
                  ? "rgba(245, 158, 11, 0.35)"
                  : "rgba(59, 130, 246, 0.32)"
              }`,
              background: isWarn
                ? "rgba(245, 158, 11, 0.1)"
                : "rgba(59, 130, 246, 0.08)",
              fontSize: "0.78rem",
              lineHeight: 1.45,
            }}
          >
            <div style={{ fontWeight: 600, color: "var(--text)" }}>
              [{isWarn ? "WARNING" : "INFO"} · {w.code}] {w.summary}
            </div>
            {w.detail && (
              <div style={{ color: "var(--muted)", marginTop: "0.2rem" }}>
                {w.detail}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}

export function CacheInvalidationPanel({
  cacheEnabled,
  tagHeaders,
  serverNames = [],
  routes = [],
  authorityMode,
}: CacheInvalidationPanelProps): React.JSX.Element {
  const uid = useId();
  const { has } = usePermission();
  const canReadMetadata = has("cache:purge");
  const canPurge = has("cache:purge");

  const defaultHost = useMemo(() => {
    const clean = serverNames.find((name) => name.trim() !== "" && !name.includes("*"));
    return clean ?? serverNames[0] ?? "";
  }, [serverNames]);

  const [kind, setKind] = useState<CacheSelectorKind>("exact_url");
  const [host, setHost] = useState<string>(defaultHost);
  const [url, setUrl] = useState<string>("");
  const [uri, setUri] = useState<string>("");
  const [prefix, setPrefix] = useState<string>("");
  const [pattern, setPattern] = useState<string>("");
  const [tagsText, setTagsText] = useState<string>("");
  const [tagMatch, setTagMatch] = useState<"any" | "all">("any");
  const [routeId, setRouteId] = useState<string>("");

  const [preview, setPreview] = useState<CachePurgePreviewResponse | null>(null);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [confirmBroad, setConfirmBroad] = useState(false);
  const [executeBusy, setExecuteBusy] = useState(false);
  const [activeIdempotencyKey, setActiveIdempotencyKey] = useState<string>("");
  const [result, setResult] = useState<CachePurgeResponse | null>(null);
  const [errorMsg, setErrorMsg] = useState<string | null>(null);
  const [permissionRevokedNotice, setPermissionRevokedNotice] = useState<string | null>(null);
  const [lookupOpId, setLookupOpId] = useState<string>("");
  const [lookupBusy, setLookupBusy] = useState(false);

  const cacheRoutes = useMemo(() => {
    const items: Array<{ readonly route_id: string; readonly match: string; readonly host?: string }> = [];
    for (const r of routes) {
      const firstHost = (r.server_names ?? []).find((n) => n.trim() !== "" && !n.includes("*"));
      for (const loc of r.locations) {
        const id = loc.route_id ?? `${r.listen}${loc.match}`;
        if (loc.match && loc.match.startsWith("/")) {
          items.push({
            route_id: id,
            match: loc.match,
            ...(firstHost ? { host: firstHost } : {}),
          });
        }
      }
    }
    return items;
  }, [routes]);

  // Any change to selector inputs invalidates a previously generated preview so
  // confirmation always stays bound to the exact previewed selector.
  const clearPreviewState = (): void => {
    setPreview(null);
    setConfirmBroad(false);
    setErrorMsg(null);
    setPermissionRevokedNotice(null);
  };

  // Listen for credential or RBAC permission changes while confirmation is open.
  useEffect(() => {
    const handleAuthOrPermissionChange = (): void => {
      if (preview !== null) {
        setPreview(null);
        setConfirmBroad(false);
        setPermissionRevokedNotice(
          "Your authentication or RBAC permissions changed while confirmation was open. Preview the scope again with your current credentials before executing.",
        );
      }
    };
    window.addEventListener("jul:credential-changed", handleAuthOrPermissionChange);
    window.addEventListener(UNAUTHORIZED_EVENT, handleAuthOrPermissionChange);
    window.addEventListener(FORBIDDEN_EVENT, handleAuthOrPermissionChange);
    return () => {
      window.removeEventListener("jul:credential-changed", handleAuthOrPermissionChange);
      window.removeEventListener(UNAUTHORIZED_EVENT, handleAuthOrPermissionChange);
      window.removeEventListener(FORBIDDEN_EVENT, handleAuthOrPermissionChange);
    };
  }, [preview]);

  useEffect(() => {
    if (!canPurge && preview !== null) {
      setPermissionRevokedNotice(
        "Current principal no longer holds cache:purge permission. Execution is disabled.",
      );
    }
  }, [canPurge, preview]);

  const draftOutcome = useMemo(
    () =>
      buildSelectorFromDraft({
        kind,
        host,
        url,
        uri,
        prefix,
        pattern,
        tagsText,
        tagMatch,
        routeId,
      }),
    [kind, host, url, uri, prefix, pattern, tagsText, tagMatch, routeId],
  );

  const handleSelectRouteHelper = (selectedRouteId: string): void => {
    clearPreviewState();
    setRouteId(selectedRouteId);
    const found = cacheRoutes.find((r) => r.route_id === selectedRouteId);
    if (!found) return;
    if (found.host && host.trim() === "") {
      setHost(found.host);
    }
    const matchPath = found.match.trim();
    if (!matchPath.startsWith("/")) return;
    if (kind === "prefix") {
      setPrefix(matchPath);
    } else if (kind === "glob") {
      setPattern(matchPath.endsWith("/") ? `${matchPath}**` : `${matchPath}/*`);
    } else if (kind === "exact_url" && url.trim() === "") {
      setUri(matchPath);
    }
  };

  const handlePreview = async (): Promise<void> => {
    setErrorMsg(null);
    setPermissionRevokedNotice(null);
    setResult(null);
    if (!draftOutcome.selector) {
      setErrorMsg(draftOutcome.error ?? "Complete the selector fields to preview.");
      return;
    }
    setPreviewBusy(true);
    try {
      const resp = await previewCachePurge(draftOutcome.selector, true);
      setPreview(resp);
      setConfirmBroad(false);
      setActiveIdempotencyKey(generateIdempotencyKey());
    } catch (err) {
      setPreview(null);
      setErrorMsg(err instanceof Error ? err.message : "Failed to preview cache invalidation.");
    } finally {
      setPreviewBusy(false);
    }
  };

  const handleExecute = async (isRetry = false): Promise<void> => {
    setErrorMsg(null);
    if (!canPurge) {
      setErrorMsg("cache:purge permission is required to execute cache invalidation.");
      return;
    }
    const targetSelector = isRetry && result ? result.selector : preview?.selector;
    if (!targetSelector) {
      setErrorMsg("Preview the invalidation scope before executing.");
      return;
    }
    if (!isRetry && preview?.broad_scope && !confirmBroad) {
      setErrorMsg(
        "Broad invalidation requires checking the explicit confirmation box before execution.",
      );
      return;
    }
    setExecuteBusy(true);
    try {
      const idempotencyKey = activeIdempotencyKey || generateIdempotencyKey();
      if (!activeIdempotencyKey) {
        setActiveIdempotencyKey(idempotencyKey);
      }
      const resp = await executeCachePurge({
        selector: targetSelector,
        previewToken: !isRetry ? preview?.preview_token : undefined,
        confirmBroad:
          (isRetry && (result?.selector.kind === "host" || result?.selector.kind === "all")) ||
          confirmBroad ||
          undefined,
        retryOperationId: isRetry && result ? result.operation_id : undefined,
        idempotencyKey,
      });
      setResult(resp);
      if (resp.state === "succeeded") {
        setPreview(null);
        setConfirmBroad(false);
      }
    } catch (err) {
      setErrorMsg(
        err instanceof Error
          ? err.message
          : "Cache invalidation failed with an unexpected error. You can safely retry with the same scope or inspect the operation ID.",
      );
    } finally {
      setExecuteBusy(false);
    }
  };

  const handleLookupOperation = async (): Promise<void> => {
    const trimmed = lookupOpId.trim();
    if (!trimmed) return;
    setLookupBusy(true);
    setErrorMsg(null);
    try {
      const resp = await fetchCachePurgeOperation(trimmed);
      setResult(resp);
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : "Failed to load operation status.");
    } finally {
      setLookupBusy(false);
    }
  };

  const selectedOption =
    SELECTOR_KIND_OPTIONS.find((opt) => opt.value === kind) ??
    SELECTOR_KIND_OPTIONS[0];

  const needsHostInput = kind !== "all";

  return (
    <section
      aria-label="Scoped cache invalidation"
      style={{
        display: "grid",
        gap: "0.9rem",
        padding: "1rem",
        borderRadius: "8px",
        border: "1px solid var(--border)",
        background: "rgba(15, 23, 42, 0.35)",
      }}
    >
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "flex-start",
          flexWrap: "wrap",
          gap: "0.5rem",
        }}
      >
        <div>
          <h4 style={{ margin: 0, fontSize: "0.92rem", color: "var(--text)" }}>
            Scoped Cache Invalidation (Select Scope → Preview → Confirm → Execute)
          </h4>
          <p
            style={{
              margin: "0.25rem 0 0",
              fontSize: "0.78rem",
              color: "var(--muted)",
              lineHeight: 1.45,
            }}
          >
            Preview matching resources and representations non-destructively before
            invalidating. Operations apply only to this Jul instance; external CDNs or
            browser caches are not notified.
          </p>
        </div>
        <span
          style={{
            fontSize: "0.72rem",
            padding: "0.2rem 0.5rem",
            borderRadius: "999px",
            border: "1px solid var(--border)",
            color: "var(--muted)",
          }}
        >
          Authority:{" "}
          {authorityMode === "file_owned"
            ? "file_owned (operational purge allowed)"
            : authorityMode === "managed"
              ? "managed"
              : "operational"}
        </span>
      </div>

      {!cacheEnabled && (
        <div
          role="note"
          style={{
            padding: "0.6rem 0.75rem",
            borderRadius: "6px",
            border: "1px solid rgba(245, 158, 11, 0.35)",
            background: "rgba(245, 158, 11, 0.1)",
            fontSize: "0.78rem",
            color: "var(--text)",
          }}
        >
          <strong>[INFO]</strong> Response caching is currently disabled in configuration.
          You can still preview selector normalization, and executing a purge will return an
          explicit <code>cache_disabled</code> outcome.
        </div>
      )}

      {/* Step 1: Select Scope */}
      <div
        style={{
          display: "grid",
          gap: "0.75rem",
          padding: "0.85rem",
          borderRadius: "6px",
          border: "1px solid var(--border)",
          background: "var(--surface)",
        }}
      >
        <div
          style={{
            display: "grid",
            gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
            gap: "0.75rem",
          }}
        >
          <label
            htmlFor={`${uid}-selector-kind`}
            style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
          >
            <span style={{ fontWeight: 600, color: "var(--text)" }}>
              1. Selector kind
            </span>
            <select
              id={`${uid}-selector-kind`}
              aria-label="Invalidation selector kind"
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as CacheSelectorKind);
                clearPreviewState();
              }}
              style={{
                padding: "0.45rem 0.6rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--bg)",
                color: "var(--text)",
                fontSize: "0.82rem",
              }}
            >
              {SELECTOR_KIND_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>
            <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
              {selectedOption?.summary}
            </span>
          </label>

          {cacheRoutes.length > 0 && kind !== "all" && (
            <label
              htmlFor={`${uid}-route-helper`}
              style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
            >
              <span style={{ fontWeight: 600, color: "var(--text)" }}>
                Configured route scope (optional helper)
              </span>
              <select
                id={`${uid}-route-helper`}
                aria-label="Configured route helper"
                value={routeId}
                onChange={(e) => {
                  handleSelectRouteHelper(e.target.value);
                }}
                style={{
                  padding: "0.45rem 0.6rem",
                  borderRadius: "6px",
                  border: "1px solid var(--border)",
                  background: "var(--bg)",
                  color: "var(--text)",
                  fontSize: "0.82rem",
                }}
              >
                <option value="">All routes on selected hostname</option>
                {cacheRoutes.map((r) => (
                  <option key={r.route_id} value={r.route_id}>
                    {r.route_id} ({r.match})
                  </option>
                ))}
              </select>
              <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
                Pre-fills path scope from authoritative route configuration.
              </span>
            </label>
          )}
        </div>

        {needsHostInput && (
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))",
              gap: "0.75rem",
            }}
          >
            <label
              htmlFor={`${uid}-host`}
              style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
            >
              <span style={{ fontWeight: 600, color: "var(--text)" }}>
                Target hostname {kind === "exact_url" ? "(or included in full URL)" : "(required)"}
              </span>
              <input
                id={`${uid}-host`}
                aria-label="Target hostname"
                type="text"
                value={host}
                placeholder="portal.example.com"
                list={`${uid}-host-datalist`}
                onChange={(e) => {
                  setHost(e.target.value);
                  clearPreviewState();
                }}
                style={{
                  padding: "0.45rem 0.6rem",
                  borderRadius: "6px",
                  border: "1px solid var(--border)",
                  background: "var(--bg)",
                  color: "var(--text)",
                  fontSize: "0.82rem",
                }}
              />
              {serverNames.length > 0 && (
                <datalist id={`${uid}-host-datalist`}>
                  {serverNames.map((name) => (
                    <option key={name} value={name} />
                  ))}
                </datalist>
              )}
              <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
                Case-normalized per RFC 9110. Explicit non-default ports (e.g.{" "}
                <code>example.com:8443</code>) are matched literally.
              </span>
            </label>
          </div>
        )}

        {kind === "exact_url" && (
          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(240px, 1fr))",
              gap: "0.75rem",
            }}
          >
            <label
              htmlFor={`${uid}-exact-url`}
              style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
            >
              <span style={{ fontWeight: 600, color: "var(--text)" }}>
                Full public URL
              </span>
              <input
                id={`${uid}-exact-url`}
                aria-label="Exact public URL"
                type="text"
                value={url}
                placeholder="https://portal.example.com/articles/42?lang=en"
                onChange={(e) => {
                  setUrl(e.target.value);
                  clearPreviewState();
                }}
                style={{
                  padding: "0.45rem 0.6rem",
                  borderRadius: "6px",
                  border: "1px solid var(--border)",
                  background: "var(--bg)",
                  color: "var(--text)",
                  fontSize: "0.82rem",
                }}
              />
              <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
                Exact query string is part of the selector; fragments (#...) are rejected.
              </span>
            </label>

            <label
              htmlFor={`${uid}-exact-uri`}
              style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
            >
              <span style={{ fontWeight: 600, color: "var(--text)" }}>
                Or request URI (with Target hostname)
              </span>
              <input
                id={`${uid}-exact-uri`}
                aria-label="Exact request URI"
                type="text"
                value={uri}
                placeholder="/articles/42?lang=en"
                onChange={(e) => {
                  setUri(e.target.value);
                  clearPreviewState();
                }}
                style={{
                  padding: "0.45rem 0.6rem",
                  borderRadius: "6px",
                  border: "1px solid var(--border)",
                  background: "var(--bg)",
                  color: "var(--text)",
                  fontSize: "0.82rem",
                }}
              />
              <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
                Invalidates GET, HEAD, and all Vary representations of this exact URI.
              </span>
            </label>
          </div>
        )}

        {kind === "prefix" && (
          <label
            htmlFor={`${uid}-prefix`}
            style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
          >
            <span style={{ fontWeight: 600, color: "var(--text)" }}>
              Anchored path prefix
            </span>
            <input
              id={`${uid}-prefix`}
              aria-label="Anchored path prefix"
              type="text"
              value={prefix}
              placeholder="/articles/"
              onChange={(e) => {
                setPrefix(e.target.value);
                clearPreviewState();
              }}
              style={{
                padding: "0.45rem 0.6rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--bg)",
                color: "var(--text)",
                fontSize: "0.82rem",
              }}
            />
            <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
              Segment-bounded: <code>/articles</code> or <code>/articles/</code> matches{" "}
              <code>/articles</code> and <code>/articles/42?page=1</code>, never{" "}
              <code>/articles-archive</code>.
            </span>
          </label>
        )}

        {kind === "glob" && (
          <label
            htmlFor={`${uid}-glob`}
            style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
          >
            <span style={{ fontWeight: 600, color: "var(--text)" }}>
              Anchored glob pattern
            </span>
            <input
              id={`${uid}-glob`}
              aria-label="Anchored glob pattern"
              type="text"
              value={pattern}
              placeholder="/articles/*/comments"
              onChange={(e) => {
                setPattern(e.target.value);
                clearPreviewState();
              }}
              style={{
                padding: "0.45rem 0.6rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--bg)",
                color: "var(--text)",
                fontSize: "0.82rem",
              }}
            />
            <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
              <code>*</code> matches within one path segment; standalone <code>**</code>{" "}
              matches zero or more full path segments. All query strings on matched paths
              are included.
            </span>
          </label>
        )}

        {kind === "tag" && (
          <div style={{ display: "grid", gap: "0.65rem" }}>
            {tagHeaders.length === 0 && (
              <div
                role="note"
                style={{
                  padding: "0.55rem 0.7rem",
                  borderRadius: "6px",
                  border: "1px solid rgba(245, 158, 11, 0.35)",
                  background: "rgba(245, 158, 11, 0.1)",
                  fontSize: "0.76rem",
                  color: "var(--text)",
                }}
              >
                <strong>[WARNING · TAG_INGESTION_DISABLED]</strong> No trusted origin tag
                headers (<code>[cache].tag_headers</code>) are currently configured. Enable
                tag headers above (e.g. <code>Cache-Tag, Surrogate-Key</code>) so Jul
                indexes origin grouping tags on cached responses.
              </div>
            )}
            <div
              style={{
                display: "grid",
                gridTemplateColumns: "2fr 1fr",
                gap: "0.75rem",
              }}
            >
              <label
                htmlFor={`${uid}-tags`}
                style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
              >
                <span style={{ fontWeight: 600, color: "var(--text)" }}>
                  Grouping tags (comma-separated, case-sensitive)
                </span>
                <input
                  id={`${uid}-tags`}
                  aria-label="Origin grouping tags"
                  type="text"
                  value={tagsText}
                  placeholder="article:42, tenant:acme"
                  onChange={(e) => {
                    setTagsText(e.target.value);
                    clearPreviewState();
                  }}
                  style={{
                    padding: "0.45rem 0.6rem",
                    borderRadius: "6px",
                    border: "1px solid var(--border)",
                    background: "var(--bg)",
                    color: "var(--text)",
                    fontSize: "0.82rem",
                  }}
                />
                <span style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
                  Tags are isolated per hostname and distinct from HTTP <code>ETag</code>{" "}
                  validators.
                </span>
              </label>

              <label
                htmlFor={`${uid}-tag-match`}
                style={{ display: "grid", gap: "0.3rem", fontSize: "0.78rem" }}
              >
                <span style={{ fontWeight: 600, color: "var(--text)" }}>
                  Multi-tag match mode
                </span>
                <select
                  id={`${uid}-tag-match`}
                  aria-label="Multi-tag match mode"
                  value={tagMatch}
                  onChange={(e) => {
                    setTagMatch(e.target.value as "any" | "all");
                    clearPreviewState();
                  }}
                  style={{
                    padding: "0.45rem 0.6rem",
                    borderRadius: "6px",
                    border: "1px solid var(--border)",
                    background: "var(--bg)",
                    color: "var(--text)",
                    fontSize: "0.82rem",
                  }}
                >
                  <option value="any">ANY (union — match any listed tag)</option>
                  <option value="all">ALL (intersection — require every tag)</option>
                </select>
              </label>
            </div>
          </div>
        )}

        <div
          style={{
            display: "flex",
            justifyContent: "space-between",
            alignItems: "center",
            flexWrap: "wrap",
            gap: "0.6rem",
          }}
        >
          <div style={{ fontSize: "0.75rem", color: "var(--muted)" }}>
            Step 2: Run a non-destructive preview to inspect matched resources, tiers, and
            warnings before confirming.
          </div>
          <button
            type="button"
            onClick={() => {
              void handlePreview();
            }}
            disabled={!canReadMetadata || previewBusy || executeBusy}
            className="rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-xs font-medium text-jul-text hover:bg-jul-bg disabled:cursor-not-allowed disabled:opacity-40"
          >
            {previewBusy ? "Previewing scope..." : "Preview invalidation scope"}
          </button>
        </div>
      </div>

      {permissionRevokedNotice && (
        <div
          role="alert"
          style={{
            padding: "0.65rem 0.8rem",
            borderRadius: "6px",
            border: "1px solid rgba(239, 68, 68, 0.45)",
            background: "rgba(239, 68, 68, 0.12)",
            fontSize: "0.8rem",
            color: "var(--text)",
          }}
        >
          <strong>[PERMISSION CHANGE]</strong> {permissionRevokedNotice}
        </div>
      )}

      {errorMsg && (
        <div
          role="alert"
          style={{
            padding: "0.65rem 0.8rem",
            borderRadius: "6px",
            border: "1px solid rgba(239, 68, 68, 0.45)",
            background: "rgba(239, 68, 68, 0.12)",
            fontSize: "0.8rem",
            color: "var(--text)",
          }}
        >
          <strong>[ERROR]</strong> {errorMsg}
        </div>
      )}

      {/* Step 2 & 3: Preview & Confirm */}
      {preview && (
        <div
          role="region"
          aria-label="Invalidation preview"
          style={{
            display: "grid",
            gap: "0.8rem",
            padding: "0.9rem",
            borderRadius: "6px",
            border: "1px solid rgba(59, 130, 246, 0.4)",
            background: "rgba(15, 23, 42, 0.55)",
          }}
        >
          <div
            style={{
              display: "flex",
              justifyContent: "space-between",
              alignItems: "center",
              flexWrap: "wrap",
              gap: "0.5rem",
            }}
          >
            <div style={{ fontWeight: 600, fontSize: "0.86rem", color: "var(--text)" }}>
              Preview Summary ({preview.count_accuracy === "exact" ? "EXACT COUNT" : "LOWER BOUND · TRUNCATED"})
            </div>
            <div style={{ fontSize: "0.73rem", color: "var(--muted)" }}>
              Source: <code>{preview.source}</code> · As of: <code>{preview.as_of}</code> · Seq:{" "}
              <code>{String(preview.cache_sequence)}</code>
            </div>
          </div>

          <div
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))",
              gap: "0.6rem",
            }}
          >
            <div
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
              }}
            >
              <div style={{ fontSize: "0.72rem", color: "var(--muted)" }}>
                Matched resources (URLs)
              </div>
              <div style={{ fontSize: "1.05rem", fontWeight: 700, color: "var(--text)" }}>
                {preview.truncated ? `>= ${String(preview.matched_resources)}` : String(preview.matched_resources)}
              </div>
            </div>

            <div
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
              }}
            >
              <div style={{ fontSize: "0.72rem", color: "var(--muted)" }}>
                Representations (GET/HEAD/Vary)
              </div>
              <div style={{ fontSize: "1.05rem", fontWeight: 700, color: "var(--text)" }}>
                {preview.truncated
                  ? `>= ${String(preview.matched_representations)}`
                  : String(preview.matched_representations)}
              </div>
              <div style={{ fontSize: "0.68rem", color: "var(--muted)" }}>
                Deduplicated across memory &amp; disk
              </div>
            </div>

            <div
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
              }}
            >
              <div style={{ fontSize: "0.72rem", color: "var(--muted)" }}>
                Memory tier
              </div>
              <div style={{ fontSize: "0.95rem", fontWeight: 600, color: "var(--text)" }}>
                {String(preview.memory_entries)} entries ({formatByteCount(preview.memory_bytes)})
              </div>
            </div>

            <div
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
              }}
            >
              <div style={{ fontSize: "0.72rem", color: "var(--muted)" }}>
                Disk tier
              </div>
              <div style={{ fontSize: "0.95rem", fontWeight: 600, color: "var(--text)" }}>
                {String(preview.disk_entries)} entries ({formatByteCount(preview.disk_bytes)})
              </div>
            </div>

            <div
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
              }}
            >
              <div style={{ fontSize: "0.72rem", color: "var(--muted)" }}>
                In-flight origin fills
              </div>
              <div style={{ fontSize: "0.95rem", fontWeight: 600, color: "var(--text)" }}>
                {String(preview.inflight_fills_matched)} matched
              </div>
              {preview.inflight_tag_unknown && (
                <div style={{ fontSize: "0.68rem", color: "var(--muted)" }}>
                  Response tags fenced on arrival
                </div>
              )}
            </div>
          </div>

          {preview.matched_representations === 0 && (
            <div
              role="status"
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
                fontSize: "0.78rem",
                color: "var(--text)",
              }}
            >
              <strong>[NO STORED MATCHES]</strong> 0 cached representations currently match
              this selector. Executing will still install a selector fence so any in-flight
              origin fill that started before the purge cannot publish stale content.
            </div>
          )}

          {preview.truncated && (
            <div
              role="note"
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid rgba(245, 158, 11, 0.4)",
                background: "rgba(245, 158, 11, 0.1)",
                fontSize: "0.78rem",
                color: "var(--text)",
              }}
            >
              <strong>[TRUNCATED PREVIEW]</strong> Preview scan reached the bounded limit of{" "}
              <code>{String(preview.scan_limit)}</code> entries. Counts shown above are a
              lower bound; execution will still re-evaluate the selector and invalidate
              matching entries up to the operation limit.
            </div>
          )}

          <GuidanceList items={preview.warnings} />

          {preview.samples.length > 0 && (
            <div style={{ display: "grid", gap: "0.35rem" }}>
              <div style={{ fontSize: "0.76rem", fontWeight: 600, color: "var(--text)" }}>
                Sampled matching resources (query values redacted, up to 20 shown):
              </div>
              <div
                style={{
                  maxHeight: "180px",
                  overflowY: "auto",
                  borderRadius: "6px",
                  border: "1px solid var(--border)",
                  background: "var(--surface)",
                  padding: "0.45rem 0.65rem",
                  fontSize: "0.75rem",
                }}
              >
                {preview.samples.map((sample) => (
                  <div
                    key={`${sample.host}${sample.path}`}
                    style={{
                      display: "flex",
                      justifyContent: "space-between",
                      gap: "0.5rem",
                      padding: "0.25rem 0",
                      borderBottom: "1px solid rgba(148, 163, 184, 0.12)",
                    }}
                  >
                    <code>
                      {sample.host}
                      {sample.path}
                    </code>
                    <span style={{ color: "var(--muted)" }}>
                      {sample.methods.join("/")} · {String(sample.representations)} rep ·{" "}
                      {sample.tiers.join("+")} · {sample.freshness_state}
                      {sample.tags && sample.tags.length > 0
                        ? ` · tags: ${sample.tags.join(", ")}`
                        : ""}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          <div
            style={{
              fontSize: "0.74rem",
              color: "var(--muted)",
              lineHeight: 1.4,
            }}
          >
            Execution contract: <code>{preview.execution_mode_contract}</code> — confirming
            installs a monotonic selector fence and re-evaluates the normalized selector so
            entries filled between preview and execution are also invalidated while
            already-admitted downstream response streams finish uninterrupted.
          </div>

          {preview.broad_scope && (
            <label
              htmlFor={`${uid}-confirm-broad`}
              style={{
                display: "flex",
                alignItems: "flex-start",
                gap: "0.5rem",
                padding: "0.6rem 0.75rem",
                borderRadius: "6px",
                border: "1px solid rgba(245, 158, 11, 0.45)",
                background: "rgba(245, 158, 11, 0.12)",
                fontSize: "0.79rem",
                color: "var(--text)",
                cursor: "pointer",
              }}
            >
              <input
                id={`${uid}-confirm-broad`}
                type="checkbox"
                checked={confirmBroad}
                onChange={(e) => {
                  setConfirmBroad(e.target.checked);
                }}
                style={{ marginTop: "0.2rem" }}
              />
              <span>
                <strong>Confirm broad invalidation:</strong> I understand this scope is
                broad (<code>host</code>, <code>all</code>, &gt;= 100 resources, or
                truncated) and will cause subsequent requests in this scope to refetch from
                the upstream origin.
              </span>
            </label>
          )}

          <div
            style={{
              display: "flex",
              justifyContent: "space-between",
              alignItems: "center",
              flexWrap: "wrap",
              gap: "0.6rem",
            }}
          >
            <details style={{ fontSize: "0.75rem", color: "var(--muted)" }}>
              <summary style={{ cursor: "pointer" }}>
                Automation CLI equivalent (secret-safe)
              </summary>
              <pre
                style={{
                  marginTop: "0.4rem",
                  padding: "0.5rem 0.65rem",
                  borderRadius: "6px",
                  background: "var(--bg)",
                  border: "1px solid var(--border)",
                  color: "var(--text)",
                  overflowX: "auto",
                }}
              >
                {buildCliCommandSnippet(preview.selector, preview.broad_scope)}
              </pre>
            </details>

            <div style={{ display: "flex", alignItems: "center", gap: "0.5rem" }}>
              <button
                type="button"
                onClick={clearPreviewState}
                disabled={executeBusy}
                className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text disabled:opacity-40"
              >
                Cancel preview
              </button>
              <button
                type="button"
                onClick={() => {
                  void handleExecute(false);
                }}
                disabled={
                  !canPurge ||
                  executeBusy ||
                  Boolean(permissionRevokedNotice) ||
                  (preview.broad_scope && !confirmBroad)
                }
                className="rounded-md bg-jul-danger px-3 py-1.5 text-xs font-medium text-white disabled:cursor-not-allowed disabled:opacity-40"
              >
                {executeBusy ? "Invalidating..." : "Confirm & invalidate"}
              </button>
              <ForbiddenAction permission="cache:purge" />
            </div>
          </div>
        </div>
      )}

      {/* Step 4: Observe Result & Recover */}
      {result && (
        <div
          role={result.state === "succeeded" ? "status" : "alert"}
          aria-label="Invalidation execution result"
          style={{
            display: "grid",
            gap: "0.75rem",
            padding: "0.9rem",
            borderRadius: "6px",
            border: `1px solid ${
              result.state === "succeeded"
                ? "rgba(34, 197, 94, 0.45)"
                : "rgba(245, 158, 11, 0.55)"
            }`,
            background:
              result.state === "succeeded"
                ? "rgba(34, 197, 94, 0.08)"
                : "rgba(245, 158, 11, 0.1)",
          }}
        >
          <div
            style={{
              display: "flex",
              justifyContent: "space-between",
              alignItems: "center",
              flexWrap: "wrap",
              gap: "0.5rem",
            }}
          >
            <div style={{ fontWeight: 700, fontSize: "0.86rem", color: "var(--text)" }}>
              [{result.state.toUpperCase()} · {result.outcome}] Effectiveness:{" "}
              <code>{result.invalidation_effective}</code>
              {result.idempotent_replay ? " (Idempotent Replay)" : ""}
            </div>
            <div style={{ fontSize: "0.74rem", color: "var(--muted)" }}>
              Operation ID: <code>{result.operation_id}</code> · Fence seq:{" "}
              <code>{String(result.fence_sequence ?? 0)}</code>
            </div>
          </div>

          <div style={{ fontSize: "0.8rem", color: "var(--text)" }}>
            Invalidated <strong>{String(result.invalidated_resources)}</strong> resources (
            <strong>{String(result.invalidated_representations)}</strong> representations:{" "}
            {String(result.memory_entries_removed)} memory,{" "}
            {String(result.disk_entries_removed)} disk). Retired{" "}
            {String(result.active_readers_retired ?? 0)} active reader leases; fenced{" "}
            {String(result.fenced_inflight_ops ?? 0)} in-flight origin operations.
            {(result.failed_entries ?? 0) > 0 || (result.remaining_entries ?? 0) > 0 ? (
              <span>
                {" "}
                Failed entries: <strong>{String(result.failed_entries ?? 0)}</strong> ·
                Remaining entries: <strong>{String(result.remaining_entries ?? 0)}</strong>.
              </span>
            ) : null}
          </div>

          {result.state_changed_since_preview && (
            <div style={{ fontSize: "0.75rem", color: "var(--muted)" }}>
              Note: Cache state changed between preview and execution; the selector was
              re-evaluated under fence <code>{String(result.fence_sequence ?? 0)}</code>.
            </div>
          )}

          {result.recovery_guidance && (
            <div
              style={{
                padding: "0.55rem 0.7rem",
                borderRadius: "6px",
                border: "1px solid var(--border)",
                background: "var(--surface)",
                fontSize: "0.78rem",
                color: "var(--text)",
              }}
            >
              <strong>Recovery guidance:</strong> {result.recovery_guidance}
            </div>
          )}

          <GuidanceList items={result.warnings} />

          {result.state !== "succeeded" && (
            <div style={{ display: "flex", justifyContent: "flex-end", gap: "0.5rem" }}>
              <button
                type="button"
                onClick={() => {
                  void handleExecute(true);
                }}
                disabled={!canPurge || executeBusy}
                className="rounded-md bg-jul-accent px-3 py-1.5 text-xs font-medium text-jul-bg disabled:opacity-40"
              >
                {executeBusy
                  ? "Retrying invalidation..."
                  : `Retry invalidation (${result.operation_id})`}
              </button>
            </div>
          )}
        </div>
      )}

      {/* Operation ID status lookup for uncertain / disconnected outcomes */}
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          flexWrap: "wrap",
          gap: "0.6rem",
          paddingTop: "0.35rem",
          borderTop: "1px solid var(--border)",
        }}
      >
        <label
          htmlFor={`${uid}-lookup-op`}
          style={{
            display: "flex",
            alignItems: "center",
            gap: "0.5rem",
            fontSize: "0.76rem",
            color: "var(--muted)",
          }}
        >
          <span>Inspect operation ID:</span>
          <input
            id={`${uid}-lookup-op`}
            aria-label="Inspect purge operation ID"
            type="text"
            value={lookupOpId}
            placeholder="cp_1_..."
            onChange={(e) => {
              setLookupOpId(e.target.value);
            }}
            style={{
              padding: "0.3rem 0.5rem",
              borderRadius: "6px",
              border: "1px solid var(--border)",
              background: "var(--bg)",
              color: "var(--text)",
              fontSize: "0.76rem",
            }}
          />
        </label>
        <button
          type="button"
          onClick={() => {
            void handleLookupOperation();
          }}
          disabled={!canReadMetadata || lookupBusy || lookupOpId.trim() === ""}
          className="rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text disabled:opacity-40"
        >
          {lookupBusy ? "Checking..." : "Check operation status"}
        </button>
      </div>
    </section>
  );
}
