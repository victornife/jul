/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useId, useMemo, useRef, useState } from "react";
import {
  FORBIDDEN_EVENT,
  UNAUTHORIZED_EVENT,
  executeCachePurge,
  fetchCachePurgeOperation,
  previewCachePurge,
  type CacheGuidance,
  type CachePurgePreviewResponse,
  type CachePurgeResponse,
  type CacheSelector,
  type CacheSelectorKind,
  type RouteProjection,
} from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";

// eslint-disable-next-line react-refresh/only-export-components
export function buildSelectorFromDraft(draft: {
  readonly kind: CacheSelectorKind;
  readonly host: string;
  readonly url: string;
  readonly uri: string;
  readonly prefix: string;
  readonly pattern: string;
  readonly tagsText: string;
  readonly tagMatch: "any" | "all";
  readonly routeId: string;
}): { readonly selector?: CacheSelector; readonly error?: string } {
  const host = draft.host.trim();
  const routeId = draft.routeId.trim() || undefined;
  switch (draft.kind) {
    case "exact_url": {
      const url = draft.url.trim();
      const uri = draft.uri.trim();
      if (url !== "") {
        return {
          selector: {
            kind: "exact_url",
            url,
            ...(host !== "" ? { host } : {}),
            ...(routeId ? { route_id: routeId } : {}),
          },
        };
      }
      if (host === "" || uri === "") {
        return {
          error:
            "Enter either a full http(s):// URL or both a target hostname and a request URI starting with /.",
        };
      }
      return {
        selector: {
          kind: "exact_url",
          host,
          uri,
          ...(routeId ? { route_id: routeId } : {}),
        },
      };
    }
    case "prefix": {
      const prefix = draft.prefix.trim();
      if (host === "" || prefix === "") {
        return {
          error:
            "Both hostname and an anchored path prefix starting with / are required.",
        };
      }
      return {
        selector: {
          kind: "prefix",
          host,
          prefix,
          ...(routeId ? { route_id: routeId } : {}),
        },
      };
    }
    case "glob": {
      const pattern = draft.pattern.trim();
      if (host === "" || pattern === "") {
        return {
          error:
            "Both hostname and an anchored glob pattern starting with / are required.",
        };
      }
      return {
        selector: {
          kind: "glob",
          host,
          pattern,
          ...(routeId ? { route_id: routeId } : {}),
        },
      };
    }
    case "tag": {
      const tags = draft.tagsText
        .split(",")
        .map((t) => t.trim())
        .filter((t) => t !== "");
      if (host === "" || tags.length === 0) {
        return {
          error:
            "Both hostname and at least one grouping tag (e.g. article:42) are required.",
        };
      }
      return {
        selector: {
          kind: "tag",
          host,
          tags,
          tag_match: draft.tagMatch,
          ...(routeId ? { route_id: routeId } : {}),
        },
      };
    }
    case "host": {
      if (host === "") {
        return { error: "Target hostname is required for a host-scoped invalidation." };
      }
      return {
        selector: {
          kind: "host",
          host,
          ...(routeId ? { route_id: routeId } : {}),
        },
      };
    }
    case "all":
      return { selector: { kind: "all", all: true } };
    case "key":
      return { error: "Use one of the structured selectors in the Console." };
  }
}

function shellQuote(val: string): string {
  return `'${val.replace(/'/g, "'\\''")}'`;
}

// eslint-disable-next-line react-refresh/only-export-components
export function buildCliCommandSnippet(
  selector: CacheSelector,
  broadScope: boolean,
): string {
  const args: string[] = [
    "jul cache purge",
    "--endpoint https://admin.example.com:9090",
    "--token-file /etc/jul/admin.token",
  ];
  switch (selector.kind) {
    case "exact_url":
      if (selector.url) {
        args.push(`--url ${shellQuote(selector.url)}`);
      } else {
        if (selector.host) args.push(`--host ${shellQuote(selector.host)}`);
        if (selector.uri) args.push(`--uri ${shellQuote(selector.uri)}`);
      }
      break;
    case "prefix":
      if (selector.host) args.push(`--host ${shellQuote(selector.host)}`);
      if (selector.prefix) args.push(`--prefix ${shellQuote(selector.prefix)}`);
      break;
    case "glob":
      if (selector.host) args.push(`--host ${shellQuote(selector.host)}`);
      if (selector.pattern) args.push(`--glob ${shellQuote(selector.pattern)}`);
      break;
    case "tag":
      if (selector.host) args.push(`--host ${shellQuote(selector.host)}`);
      for (const tag of selector.tags ?? []) {
        args.push(`--tag ${shellQuote(tag)}`);
      }
      if (selector.tag_match === "all") {
        args.push("--tag-match all");
      }
      break;
    case "host":
      if (selector.host) args.push(`--host ${shellQuote(selector.host)}`);
      break;
    case "all":
      args.push("--all");
      break;
    case "key":
      if (selector.key) args.push(`--key ${shellQuote(selector.key)}`);
      break;
  }
  if (broadScope) {
    args.push("--confirm-broad");
  }
  return args.join(" \\\n  ");
}

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

const INPUT_CLS =
  "rounded-md border border-jul-border bg-jul-bg px-2.5 py-1.5 text-xs text-jul-text";
const LABEL_CLS = "grid gap-1 text-xs";
const TITLE_CLS = "font-semibold text-jul-text";
const HINT_CLS = "text-[11px] text-jul-muted";
const STAT_CLS = "rounded-md border border-jul-border bg-jul-surface p-2";
const BTN_CLS =
  "rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text disabled:opacity-40";

function GuidanceList({
  items,
}: {
  readonly items?: readonly CacheGuidance[] | undefined;
}): React.JSX.Element | null {
  if (!items || items.length === 0) return null;
  return (
    <div className="grid gap-2">
      {items.map((w) => {
        const isWarn = w.severity.toLowerCase() === "warning";
        return (
          <div
            key={`${w.code}-${w.summary}`}
            role="note"
            className="rounded-md border border-jul-border bg-jul-surface p-2 text-xs"
          >
            <div className={TITLE_CLS}>
              [{isWarn ? "WARNING" : "INFO"} · {w.code}] {w.summary}
            </div>
            {w.detail && <div className="mt-0.5 text-jul-muted">{w.detail}</div>}
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
  const [selectedRouteKey, setSelectedRouteKey] = useState<string>("");

  const [preview, setPreview] = useState<CachePurgePreviewResponse | null>(null);
  const [confirmedSelector, setConfirmedSelector] = useState<CacheSelector | null>(null);
  const previewGenRef = useRef(0);
  const opSelectorsRef = useRef<Record<string, CacheSelector>>({});
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
    const items: Array<{
      readonly key: string;
      readonly label: string;
      readonly authoritative_route_id?: string | undefined;
      readonly match: string;
      readonly host?: string;
    }> = [];
    routes.forEach((r, sIdx) => {
      const firstHost = (r.server_names ?? []).find((n) => n.trim() !== "" && !n.includes("*"));
      r.locations.forEach((loc, lIdx) => {
        const authId = loc.route_id && loc.route_id.trim() !== "" ? loc.route_id.trim() : undefined;
        const key = authId ?? `${String(sIdx)}:${firstHost ?? r.listen}:${String(lIdx)}:${loc.match}`;
        const label = authId ?? `${firstHost ?? r.listen}${loc.match}`;
        if (loc.match && loc.match.startsWith("/")) {
          items.push({
            key,
            label,
            ...(authId ? { authoritative_route_id: authId } : {}),
            match: loc.match,
            ...(firstHost ? { host: firstHost } : {}),
          });
        }
      });
    });
    return items;
  }, [routes]);

  // Any change to selector inputs invalidates a previously generated preview so
  // confirmation always stays bound to the exact previewed selector.
  const clearPreviewState = (): void => {
    previewGenRef.current += 1;
    setPreviewBusy(false);
    setPreview(null);
    setConfirmedSelector(null);
    setConfirmBroad(false);
    setErrorMsg(null);
    setPermissionRevokedNotice(null);
  };

  // Listen for credential or RBAC permission changes while confirmation is open.
  useEffect(() => {
    const handleAuthOrPermissionChange = (): void => {
      previewGenRef.current += 1;
      setPreviewBusy(false);
      setConfirmedSelector(null);
      setConfirmBroad(false);
      setPreview((prev) => {
        if (prev !== null) {
          setPermissionRevokedNotice(
            "Your authentication or RBAC permissions changed while confirmation was open. Preview the scope again with your current credentials before executing.",
          );
        }
        return null;
      });
    };
    window.addEventListener("jul:credential-changed", handleAuthOrPermissionChange);
    window.addEventListener(UNAUTHORIZED_EVENT, handleAuthOrPermissionChange);
    window.addEventListener(FORBIDDEN_EVENT, handleAuthOrPermissionChange);
    return () => {
      window.removeEventListener("jul:credential-changed", handleAuthOrPermissionChange);
      window.removeEventListener(UNAUTHORIZED_EVENT, handleAuthOrPermissionChange);
      window.removeEventListener(FORBIDDEN_EVENT, handleAuthOrPermissionChange);
    };
  }, []);

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

  const handleSelectRouteHelper = (selectedKey: string): void => {
    clearPreviewState();
    setSelectedRouteKey(selectedKey);
    const found = cacheRoutes.find((r) => r.key === selectedKey);
    if (!found) {
      setRouteId("");
      return;
    }
    setRouteId(found.authoritative_route_id ?? "");
    if (found.host) {
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
    const requestedSelector = draftOutcome.selector;
    if (!requestedSelector) {
      setErrorMsg(draftOutcome.error ?? "Complete the selector fields to preview.");
      return;
    }
    const reqGen = ++previewGenRef.current;
    setPreviewBusy(true);
    try {
      const resp = await previewCachePurge(requestedSelector, true);
      if (previewGenRef.current !== reqGen) return;
      setConfirmedSelector(requestedSelector);
      setPreview(resp);
      setConfirmBroad(false);
      setActiveIdempotencyKey(generateIdempotencyKey());
    } catch (err) {
      if (previewGenRef.current !== reqGen) return;
      setPreview(null);
      setConfirmedSelector(null);
      setErrorMsg(err instanceof Error ? err.message : "Failed to preview cache invalidation.");
    } finally {
      if (previewGenRef.current === reqGen) {
        setPreviewBusy(false);
      }
    }
  };

  const handleExecute = async (isRetry = false): Promise<void> => {
    setErrorMsg(null);
    if (!canPurge) {
      setErrorMsg("cache:purge permission is required to execute cache invalidation.");
      return;
    }
    let targetSelector: CacheSelector | undefined;
    if (isRetry && result) {
      const bound = opSelectorsRef.current[result.operation_id] ?? confirmedSelector;
      if (bound) {
        targetSelector = bound;
      } else {
        const isRedacted =
          Boolean(result.selector.uri?.includes("?<redacted>")) ||
          Boolean(result.selector.key?.includes("?<redacted>"));
        if (!isRedacted) {
          targetSelector = result.selector;
        } else if (
          draftOutcome.selector &&
          draftOutcome.selector.kind === result.selector.kind &&
          !draftOutcome.selector.uri?.includes("?<redacted>")
        ) {
          targetSelector = draftOutcome.selector;
        } else {
          setErrorMsg(
            "This looked-up operation targets a redacted query string; re-enter the full URL above and preview to retry.",
          );
          return;
        }
      }
    } else {
      targetSelector = confirmedSelector ?? preview?.selector;
    }
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
          (isRetry &&
            (result?.selector.kind === "host" ||
              result?.selector.kind === "all" ||
              (result?.invalidated_resources ?? 0) >= 100 ||
              preview?.broad_scope === true)) ||
          confirmBroad ||
          undefined,
        retryOperationId: isRetry && result ? result.operation_id : undefined,
        idempotencyKey,
      });
      opSelectorsRef.current[resp.operation_id] = targetSelector;
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
      if (!opSelectorsRef.current[resp.operation_id]) {
        setConfirmedSelector(null);
      }
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
      className="grid gap-3 rounded-lg border border-jul-border bg-jul-bg/40 p-4"
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h4 className="m-0 text-sm font-semibold text-jul-text">
            Scoped Cache Invalidation (Select Scope → Preview → Confirm → Execute)
          </h4>
          <p className="mt-1 text-xs text-jul-muted">
            Preview matching resources and representations non-destructively before
            invalidating. Operations apply only to this Jul instance; external CDNs or
            browser caches are not notified.
          </p>
        </div>
        <span className="rounded-full border border-jul-border px-2 py-0.5 text-[11px] text-jul-muted">
          Authority:{" "}
          {authorityMode === "file_owned"
            ? "file_owned (operational purge allowed)"
            : authorityMode === "managed"
              ? "managed"
              : "operational"}
        </span>
      </div>

      {!cacheEnabled && (
        <div role="note" className={STAT_CLS + " text-xs text-jul-text"}>
          <strong>[INFO]</strong> Response caching is currently disabled in configuration.
          You can still preview selector normalization, and executing a purge will return an
          explicit <code>cache_disabled</code> outcome.
        </div>
      )}

      {/* Step 1: Select Scope */}
      <div className="grid gap-3 rounded-md border border-jul-border bg-jul-surface p-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <label htmlFor={`${uid}-selector-kind`} className={LABEL_CLS}>
            <span className={TITLE_CLS}>1. Selector kind</span>
            <select
              id={`${uid}-selector-kind`}
              aria-label="Invalidation selector kind"
              value={kind}
              onChange={(e) => {
                setKind(e.target.value as CacheSelectorKind);
                clearPreviewState();
              }}
              className={INPUT_CLS}
            >
              {SELECTOR_KIND_OPTIONS.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </select>
            <span className={HINT_CLS}>{selectedOption?.summary}</span>
          </label>

          {cacheRoutes.length > 0 && kind !== "all" && (
            <label htmlFor={`${uid}-route-helper`} className={LABEL_CLS}>
              <span className={TITLE_CLS}>Configured route scope (optional helper)</span>
              <select
                id={`${uid}-route-helper`}
                aria-label="Configured route helper"
                value={selectedRouteKey}
                onChange={(e) => {
                  handleSelectRouteHelper(e.target.value);
                }}
                className={INPUT_CLS}
              >
                <option value="">All routes on selected hostname</option>
                {cacheRoutes.map((r) => (
                  <option key={r.key} value={r.key}>
                    {r.label} ({r.match})
                  </option>
                ))}
              </select>
              <span className={HINT_CLS}>
                Pre-fills path scope from authoritative route configuration.
              </span>
            </label>
          )}
        </div>

        {needsHostInput && (
          <label htmlFor={`${uid}-host`} className={LABEL_CLS}>
            <span className={TITLE_CLS}>
              Target hostname{" "}
              {kind === "exact_url" ? "(or included in full URL)" : "(required)"}
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
              className={INPUT_CLS}
            />
            {serverNames.length > 0 && (
              <datalist id={`${uid}-host-datalist`}>
                {serverNames.map((name) => (
                  <option key={name} value={name} />
                ))}
              </datalist>
            )}
            <span className={HINT_CLS}>
              Case-normalized per RFC 9110. Explicit non-default ports (e.g.{" "}
              <code>example.com:8443</code>) are matched literally.
            </span>
          </label>
        )}

        {kind === "exact_url" && (
          <div className="grid gap-3 sm:grid-cols-2">
            <label htmlFor={`${uid}-exact-url`} className={LABEL_CLS}>
              <span className={TITLE_CLS}>Full public URL</span>
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
                className={INPUT_CLS}
              />
              <span className={HINT_CLS}>
                Exact query string is part of the selector; fragments (#...) are rejected.
              </span>
            </label>

            <label htmlFor={`${uid}-exact-uri`} className={LABEL_CLS}>
              <span className={TITLE_CLS}>Or request URI (with Target hostname)</span>
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
                className={INPUT_CLS}
              />
              <span className={HINT_CLS}>
                Invalidates GET, HEAD, and all Vary representations of this exact URI.
              </span>
            </label>
          </div>
        )}

        {kind === "prefix" && (
          <label htmlFor={`${uid}-prefix`} className={LABEL_CLS}>
            <span className={TITLE_CLS}>Anchored path prefix</span>
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
              className={INPUT_CLS}
            />
            <span className={HINT_CLS}>
              Segment-bounded: <code>/articles</code> or <code>/articles/</code> matches{" "}
              <code>/articles</code> and <code>/articles/42?page=1</code>, never{" "}
              <code>/articles-archive</code>.
            </span>
          </label>
        )}

        {kind === "glob" && (
          <label htmlFor={`${uid}-glob`} className={LABEL_CLS}>
            <span className={TITLE_CLS}>Anchored glob pattern</span>
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
              className={INPUT_CLS}
            />
            <span className={HINT_CLS}>
              <code>*</code> matches within one path segment; standalone <code>**</code>{" "}
              matches zero or more full path segments. All query strings on matched paths
              are included.
            </span>
          </label>
        )}

        {kind === "tag" && (
          <div className="grid gap-2.5">
            {tagHeaders.length === 0 && (
              <div role="note" className={STAT_CLS + " text-xs text-jul-text"}>
                <strong>[WARNING · TAG_INGESTION_DISABLED]</strong> No trusted origin tag
                headers (<code>[cache].tag_headers</code>) are currently configured. Enable
                tag headers above (e.g. <code>Cache-Tag, Surrogate-Key</code>) so Jul
                indexes origin grouping tags on cached responses.
              </div>
            )}
            <div className="grid gap-3 sm:grid-cols-3">
              <label htmlFor={`${uid}-tags`} className={LABEL_CLS + " sm:col-span-2"}>
                <span className={TITLE_CLS}>
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
                  className={INPUT_CLS}
                />
                <span className={HINT_CLS}>
                  Tags are isolated per hostname and distinct from HTTP <code>ETag</code>{" "}
                  validators.
                </span>
              </label>

              <label htmlFor={`${uid}-tag-match`} className={LABEL_CLS}>
                <span className={TITLE_CLS}>Multi-tag match mode</span>
                <select
                  id={`${uid}-tag-match`}
                  aria-label="Multi-tag match mode"
                  value={tagMatch}
                  onChange={(e) => {
                    setTagMatch(e.target.value as "any" | "all");
                    clearPreviewState();
                  }}
                  className={INPUT_CLS}
                >
                  <option value="any">ANY (union — match any listed tag)</option>
                  <option value="all">ALL (intersection — require every tag)</option>
                </select>
              </label>
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className={HINT_CLS}>
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
        <div role="alert" className={STAT_CLS + " text-xs text-jul-text"}>
          <strong>[PERMISSION CHANGE]</strong> {permissionRevokedNotice}
        </div>
      )}

      {errorMsg && (
        <div role="alert" className={STAT_CLS + " text-xs text-jul-text"}>
          <strong>[ERROR]</strong> {errorMsg}
        </div>
      )}

      {/* Step 2 & 3: Preview & Confirm */}
      {preview && (
        <div
          role="region"
          aria-label="Invalidation preview"
          className="grid gap-3 rounded-md border border-jul-border bg-jul-bg/60 p-3.5"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-xs font-semibold text-jul-text">
              Preview Summary (
              {preview.count_accuracy === "exact" ? "EXACT COUNT" : "LOWER BOUND · TRUNCATED"}
              )
            </div>
            <div className={HINT_CLS}>
              Source: <code>{preview.source}</code> · As of: <code>{preview.as_of}</code> · Seq:{" "}
              <code>{String(preview.cache_sequence)}</code>
            </div>
          </div>

          <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
            <div className={STAT_CLS}>
              <div className={HINT_CLS}>Matched resources (URLs)</div>
              <div className="text-base font-bold text-jul-text">
                {preview.truncated
                  ? `>= ${String(preview.matched_resources)}`
                  : String(preview.matched_resources)}
              </div>
            </div>

            <div className={STAT_CLS}>
              <div className={HINT_CLS}>Representations (GET/HEAD/Vary)</div>
              <div className="text-base font-bold text-jul-text">
                {preview.truncated
                  ? `>= ${String(preview.matched_representations)}`
                  : String(preview.matched_representations)}
              </div>
              <div className="text-[10px] text-jul-muted">
                Deduplicated across memory &amp; disk
              </div>
            </div>

            <div className={STAT_CLS}>
              <div className={HINT_CLS}>Memory tier</div>
              <div className="text-sm font-semibold text-jul-text">
                {String(preview.memory_entries)} entries ({formatByteCount(preview.memory_bytes)})
              </div>
            </div>

            <div className={STAT_CLS}>
              <div className={HINT_CLS}>Disk tier</div>
              <div className="text-sm font-semibold text-jul-text">
                {String(preview.disk_entries)} entries ({formatByteCount(preview.disk_bytes)})
              </div>
            </div>

            <div className={STAT_CLS}>
              <div className={HINT_CLS}>In-flight origin fills</div>
              <div className="text-sm font-semibold text-jul-text">
                {String(preview.inflight_fills_matched)} matched
              </div>
              {preview.inflight_tag_unknown && (
                <div className="text-[10px] text-jul-muted">
                  Response tags fenced on arrival
                </div>
              )}
            </div>
          </div>

          {preview.matched_representations === 0 && (
            <div role="status" className={STAT_CLS + " text-xs text-jul-text"}>
              <strong>[NO STORED MATCHES]</strong> 0 cached representations currently match
              this selector. Executing will still install a selector fence so any in-flight
              origin fill that started before the purge cannot publish stale content.
            </div>
          )}

          {preview.truncated && (
            <div role="note" className={STAT_CLS + " text-xs text-jul-text"}>
              <strong>[TRUNCATED PREVIEW]</strong> Preview scan reached the bounded limit of{" "}
              <code>{String(preview.scan_limit)}</code> entries. Counts shown above are a
              lower bound; execution will still re-evaluate the selector and invalidate
              matching entries up to the operation limit.
            </div>
          )}

          <GuidanceList items={preview.warnings} />

          {(preview.samples ?? []).length > 0 && (
            <div className="grid gap-1.5">
              <div className="text-xs font-semibold text-jul-text">
                Sampled matching resources (query values redacted, up to 20 shown):
              </div>
              <div className="max-h-44 overflow-y-auto rounded-md border border-jul-border bg-jul-surface p-2 text-xs">
                {(preview.samples ?? []).map((sample) => (
                  <div
                    key={`${sample.host}${sample.path}`}
                    className="flex justify-between gap-2 border-b border-jul-border/40 py-1"
                  >
                    <code>
                      {sample.host}
                      {sample.path}
                    </code>
                    <span className="text-jul-muted">
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

          <div className={HINT_CLS}>
            Execution contract: <code>{preview.execution_mode_contract}</code> — confirming
            installs a monotonic selector fence and re-evaluates the normalized selector so
            entries filled between preview and execution are also invalidated while
            already-admitted downstream response streams finish uninterrupted.
          </div>

          {preview.broad_scope && (
            <label
              htmlFor={`${uid}-confirm-broad`}
              className="flex cursor-pointer items-start gap-2 rounded-md border border-jul-border bg-jul-surface p-2.5 text-xs text-jul-text"
            >
              <input
                id={`${uid}-confirm-broad`}
                type="checkbox"
                checked={confirmBroad}
                onChange={(e) => {
                  setConfirmBroad(e.target.checked);
                }}
                className="mt-0.5"
              />
              <span>
                <strong>Confirm broad invalidation:</strong> I understand this scope is
                broad (<code>host</code>, <code>all</code>, &gt;= 100 resources, or
                truncated) and will cause subsequent requests in this scope to refetch from
                the upstream origin.
              </span>
            </label>
          )}

          <div className="flex flex-wrap items-center justify-between gap-2">
            <details className="text-xs text-jul-muted">
              <summary className="cursor-pointer">
                Automation CLI equivalent (secret-safe)
              </summary>
              <pre className="mt-1.5 overflow-x-auto rounded-md border border-jul-border bg-jul-bg p-2 text-jul-text">
                {buildCliCommandSnippet(confirmedSelector ?? preview.selector, preview.broad_scope)}
              </pre>
            </details>

            <div className="flex items-center gap-2">
              <button
                type="button"
                onClick={clearPreviewState}
                disabled={executeBusy}
                className={BTN_CLS}
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
          className="grid gap-3 rounded-md border border-jul-border bg-jul-surface p-3.5"
        >
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="text-xs font-bold text-jul-text">
              [{result.state.toUpperCase()} · {result.outcome}] Effectiveness:{" "}
              <code>{result.invalidation_effective}</code>
              {result.idempotent_replay ? " (Idempotent Replay)" : ""}
            </div>
            <div className={HINT_CLS}>
              Operation ID: <code>{result.operation_id}</code> · Fence seq:{" "}
              <code>{String(result.fence_sequence ?? 0)}</code>
            </div>
          </div>

          <div className="text-xs text-jul-text">
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
            <div className={HINT_CLS}>
              Note: Cache state changed between preview and execution; the selector was
              re-evaluated under fence <code>{String(result.fence_sequence ?? 0)}</code>.
            </div>
          )}

          {result.recovery_guidance && (
            <div className={STAT_CLS + " text-xs text-jul-text"}>
              <strong>Recovery guidance:</strong> {result.recovery_guidance}
            </div>
          )}

          <GuidanceList items={result.warnings} />

          {result.state !== "succeeded" && (
            <div className="flex justify-end gap-2">
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
      <div className="flex flex-wrap items-center justify-between gap-2 border-t border-jul-border pt-2">
        <label htmlFor={`${uid}-lookup-op`} className="flex items-center gap-2 text-xs text-jul-muted">
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
            className={INPUT_CLS}
          />
        </label>
        <button
          type="button"
          onClick={() => {
            void handleLookupOperation();
          }}
          disabled={!canReadMetadata || lookupBusy || lookupOpId.trim() === ""}
          className={BTN_CLS}
        >
          {lookupBusy ? "Checking..." : "Check operation status"}
        </button>
      </div>
    </section>
  );
}
