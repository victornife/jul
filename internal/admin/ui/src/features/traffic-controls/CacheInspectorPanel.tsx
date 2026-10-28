/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { useEffect, useId, useRef, useState } from "react";
import {
  ApiError,
  FORBIDDEN_EVENT,
  UNAUTHORIZED_EVENT,
  inspectCache,
  type CacheInspectionPage,
  type CacheSelector,
} from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";

function evidenceDate(value: string | undefined, unknown: string): string {
  return value && value !== "0001-01-01T00:00:00Z" ? value : unknown;
}

export function CacheInspectorPanel({ serverNames }: { readonly serverNames: string[] }) {
  const { has } = usePermission();
  const id = useId();
  const [host, setHost] = useState(serverNames[0] ?? "");
  const [kind, setKind] = useState<"host" | "exact_url" | "prefix" | "glob" | "tag">("host");
  const [filter, setFilter] = useState("");
  const [data, setData] = useState<CacheInspectionPage>();
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [expired, setExpired] = useState(false);
  const request = useRef<AbortController | null>(null);
  const epoch = useRef(0);
  const canInspect = has("cache:inspect");

  function clear() {
    epoch.current++;
    request.current?.abort();
    setData(undefined);
    setLoading(false);
    setError("");
    setExpired(false);
  }
  useEffect(() => {
    const revoke = () => {
      epoch.current++;
      request.current?.abort();
      setData(undefined);
      setLoading(false);
      setError("Inspection access changed. Refresh your credentials.");
    };
    window.addEventListener(FORBIDDEN_EVENT, revoke);
    window.addEventListener(UNAUTHORIZED_EVENT, revoke);
    return () => {
      request.current?.abort();
      window.removeEventListener(FORBIDDEN_EVENT, revoke);
      window.removeEventListener(UNAUTHORIZED_EVENT, revoke);
    };
  }, []);
  useEffect(() => {
    if (!data) return;
    const timer = window.setTimeout(
      () => {
        setExpired(true);
      },
      Math.max(0, Date.parse(data.expires_at) - Date.now()),
    );
    return () => {
      window.clearTimeout(timer);
    };
  }, [data]);

  async function load(cursor?: string) {
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    const version = ++epoch.current;
    setLoading(true);
    setError("");
    setData(undefined);
    setExpired(false);
    const selector: CacheSelector = { kind, host };
    switch (kind) {
      case "exact_url":
        selector.uri = filter;
        break;
      case "prefix":
        selector.prefix = filter;
        break;
      case "glob":
        selector.pattern = filter;
        break;
      case "tag":
        selector.tags = [filter];
        break;
    }
    try {
      const result = await inspectCache(selector, cursor, controller.signal);
      if (epoch.current === version && !controller.signal.aborted) setData(result);
    } catch (e) {
      if (epoch.current === version && !controller.signal.aborted)
        setError(
          e instanceof ApiError && e.status === 403
            ? "Permission denied: cache:inspect is required."
            : "Inspection unavailable. Check access and scope, then retry with a new inspection.",
        );
    } finally {
      if (epoch.current === version && !controller.signal.aborted) setLoading(false);
    }
  }

  return (
    <section aria-label="Cache inspector" className="space-y-3 border-t border-jul-border pt-3">
      <h3 className="font-medium">Inspect cached resources</h3>
      <p className="text-xs text-jul-muted">
        Read-only metadata. This makes no origin request and does not populate the cache. An absent
        entry does not explain its history or guarantee a future hit.
      </p>
      {!canInspect ? (
        <p role="status">Permission denied: ask for an explicit cache:inspect grant.</p>
      ) : (
        <>
          <label htmlFor={`${id}-host`}>Configured hostname</label>
          <input
            id={`${id}-host`}
            value={host}
            list={`${id}-hosts`}
            onChange={(e) => {
              clear();
              setHost(e.target.value);
            }}
          />
          <datalist id={`${id}-hosts`}>
            {serverNames.map((h) => (
              <option key={h} value={h} />
            ))}
          </datalist>
          <label htmlFor={`${id}-kind`}>Filter</label>
          <select
            id={`${id}-kind`}
            value={kind}
            onChange={(e) => {
              clear();
              setKind(e.target.value as typeof kind);
              setFilter("");
            }}
          >
            <option value="host">Hostname</option>
            <option value="exact_url">Exact request URI</option>
            <option value="prefix">Path prefix</option>
            <option value="glob">Path pattern</option>
            <option value="tag">Origin tag</option>
          </select>
          {kind !== "host" && (
            <>
              <label htmlFor={`${id}-filter`}>Filter value</label>
              <input
                id={`${id}-filter`}
                value={filter}
                onChange={(e) => {
                  clear();
                  setFilter(e.target.value);
                }}
              />
            </>
          )}
          <button
            type="button"
            disabled={loading || host.trim() === ""}
            onClick={() => void load()}
          >
            Inspect / refresh
          </button>
          {loading && <p role="status">Loading cache metadata…</p>}
          {error && <p role="alert">{error}</p>}
          {data && (
            <div aria-live="polite" className="space-y-2">
              <p>
                Captured metadata; live entries may have changed. Concurrent-change evidence:{" "}
                {data.concurrent_change}. Result: {data.inspection.state}. Counts:{" "}
                {data.inspection.count_accuracy}. {data.inspection.matched_resources} logical
                resources; {data.inspection.matched_representations} representations;{" "}
                {data.inspection.memory_entries} memory entries; {data.inspection.disk_entries} disk
                entries (including lookup stubs).
              </p>
              <p>
                Captured {data.inspection.captured_at}. Evidence: {data.inspection.evidence_state}.
                Sampling can be enabled in cache settings; it is bounded and loses history.
              </p>
              {expired && (
                <p role="status">
                  Stale evidence: this observation expired. Refresh to inspect current metadata.
                </p>
              )}
              {data.inspection.truncated && (
                <p role="status">
                  Truncated observation: counts may be lower bounds and only a bounded resource set
                  is shown. Narrow your filter.
                </p>
              )}
              {data.inspection.resources.length === 0 && (
                <p>
                  No resource metadata in this result. This does not mean the resource was never
                  cached.
                </p>
              )}
              <p>
                Policy: {data.policy_source}. {data.route_cache_state}. Retained entries can reflect
                prior policy after reload.
              </p>
              {data.inspection.warnings.includes("route_ownership_unavailable") && (
                <p>Inspection cannot establish the route selected by a request or guarantee entry reuse.</p>
              )}
              {data.inspection.warnings.includes("response_hook_bypass_when_subscribed") && (
                <p>Requests using a response-hook subscription bypass shared caching; request-only executions can still cache.</p>
              )}
              <ul>
                {data.inspection.resources.map((r) => (
                  <li key={r.resource_id}>
                    <strong>
                      {r.host}
                      {r.path}
                      {r.has_query ? " (query omitted)" : ""}
                    </strong>
                    : {r.freshness_state}, {r.body_bytes} representation bytes.
                    <p>
                      Vary fields: {(r.vary_fields ?? []).join(", ") || "none"}. Tags:{" "}
                      {(r.tags ?? []).join(", ") || "none / unavailable"}.
                    </p>
                    {r.details_truncated && <p>Representation details truncated.</p>}
                    <ul>
                      {r.details.map((d) => (
                        <li key={d.id}>
                          {d.method}, {d.tiers.join(" + ")}, {d.body_bytes} bytes; origin age{" "}
                          {d.origin_age_seconds.toFixed(1)}s; {d.freshness_state}. Provenance:{" "}
                          {d.evidence_state}.
                          {d.evidence_state === "recorded" && (
                            <p>
                              Inserted {evidenceDate(d.evidence.inserted_at, "unknown")}; validated{" "}
                              {evidenceDate(d.evidence.validated_at, "no recorded validation")}.
                              Freshness {d.evidence.freshness_seconds}s from{" "}
                              {d.evidence.freshness_source}; stale-while-revalidate{" "}
                              {d.evidence.swr_seconds}s ({d.evidence.swr_source}); recorded
                              stale-if-error {d.evidence.sie_seconds}s ({d.evidence.sie_source});
                              current effective stale-if-error{" "}
                              {d.effective_sie_seconds ?? "unavailable"}s.{" "}
                              {d.evidence.requires_validation || d.evidence.must_revalidate
                                ? "Validation restrictions apply."
                                : "Reuse still depends on the request."}{" "}
                              {d.retained_policy ? "Entry retained from a different policy." : ""}
                            </p>
                          )}
                        </li>
                      ))}
                    </ul>
                  </li>
                ))}
              </ul>
              <p>
                Current global fallback: freshness {data.inspection.policy.default_ttl_seconds}s;
                stale-while-revalidate {data.inspection.policy.swr_seconds}s; stale-if-error{" "}
                {data.inspection.policy.sie_seconds}s. Maximum entry{" "}
                {data.inspection.policy.max_entry_bytes} bytes. Origin and validation restrictions
                take precedence.
              </p>
              <p>
                Memory occupancy {data.inspection.memory_occupancy.bytes} / configured{" "}
                {data.inspection.memory_occupancy.max_bytes} bytes;{" "}
                {data.inspection.memory_occupancy.evictions} capacity evictions.{" "}
                {data.inspection.disk_occupancy
                  ? `Disk occupancy ${String(data.inspection.disk_occupancy.bytes)} / configured ${String(data.inspection.disk_occupancy.max_bytes)} bytes.`
                  : "Disk tier disabled / unavailable."}
              </p>
              <h4>Observed decision stages</h4>
              <ul>
                {data.inspection.decisions.map((d, i) => (
                  <li key={`${d.resource_id}-${String(i)}`}>
                    {d.summary ?? d.reason}: {d.host}
                    {d.path}, observed {d.observed_at}, expires {d.expires_at}. {d.next_step}
                  </li>
                ))}
              </ul>
              {data.next_cursor && (
                <button
                  type="button"
                  disabled={expired || loading}
                  onClick={() => void load(data.next_cursor)}
                >
                  Next metadata page
                </button>
              )}
            </div>
          )}
        </>
      )}
      <a
        href="https://github.com/victornife/jul/blob/main/docs/cache.md#bounded-cache-inspection"
        target="_blank"
        rel="noreferrer"
      >
        Policy, limits and evidence documentation
      </a>
    </section>
  );
}
