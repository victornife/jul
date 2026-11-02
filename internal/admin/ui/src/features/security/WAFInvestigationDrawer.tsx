/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchWAFEvents, type WAFEventRecord, type WAFException } from "@/api/client.ts";
import { shortDigest } from "@/lib/wafEffective.ts";

export interface WAFInvestigationDrawerProps {
  readonly onClose: () => void;
  readonly onTuneException?: (exc: WAFException, event: WAFEventRecord) => void;
  readonly onTuneGlobalException?: (exc: WAFException, event: WAFEventRecord) => void;
  readonly tuneError?: string | null;
}

function isNarrowTargetCandidate(t: string): boolean {
  if (!t.includes(":")) return false;
  if (t.endsWith(":[redacted]")) return false;
  const col = t.split(":")[0] ?? "";
  return (
    col === "ARGS" ||
    col === "ARGS_GET" ||
    col === "ARGS_POST" ||
    col === "ARGS_NAMES" ||
    col === "REQUEST_HEADERS" ||
    col === "REQUEST_COOKIES"
  );
}

export function WAFInvestigationDrawer({
  onClose,
  onTuneException,
  onTuneGlobalException,
  tuneError,
}: WAFInvestigationDrawerProps) {
  const [eventId, setEventId] = useState("");
  const [outcome, setOutcome] = useState("");
  const [ruleId, setRuleId] = useState("");
  const [routeId, setRouteId] = useState("");

  const query = useQuery({
    queryKey: ["waf-events", eventId, outcome, ruleId, routeId],
    queryFn: () =>
      fetchWAFEvents({
        event_id: eventId,
        outcome,
        rule_id: ruleId,
        route_id: routeId,
        limit: 50,
      }),
    retry: false,
  });

  const data = query.data;

  return (
    <Drawer
      title="WAF outcome investigation"
      subtitle="Bounded, secret-safe transaction outcomes and narrow-tuning handoff (#632)."
      onClose={onClose}
    >
      <div className="space-y-4 text-xs">
        <div className="grid grid-cols-1 gap-2 rounded-md border border-jul-border bg-jul-surface p-3 sm:grid-cols-2">
          <label className="space-y-1">
            <span className="text-jul-muted">Support reference (event_id)</span>
            <input
              type="text"
              aria-label="Filter by event_id"
              value={eventId}
              placeholder="wev_..."
              onChange={(e) => {
                setEventId(e.target.value);
              }}
              className="w-full rounded border border-jul-border bg-jul-bg px-2 py-1 font-mono text-jul-text"
            />
          </label>
          <label className="space-y-1">
            <span className="text-jul-muted">Outcome</span>
            <select
              aria-label="Filter by outcome"
              value={outcome}
              onChange={(e) => {
                setOutcome(e.target.value);
              }}
              className="w-full rounded border border-jul-border bg-jul-bg px-2 py-1 text-jul-text"
            >
              <option value="">All non-clean outcomes</option>
              <option value="interrupted_request">interrupted_request (blocked pre-origin)</option>
              <option value="interrupted_response">interrupted_response (phase 4)</option>
              <option value="matched_observed">matched_observed (detect / non-disruptive)</option>
              <option value="rejected_body_limit">rejected_body_limit (413)</option>
              <option value="inspection_error">inspection_error (400 fail-closed)</option>
            </select>
          </label>
          <label className="space-y-1">
            <span className="text-jul-muted">Rule ID</span>
            <input
              type="text"
              aria-label="Filter by rule_id"
              value={ruleId}
              placeholder="e.g. 942100"
              onChange={(e) => {
                setRuleId(e.target.value);
              }}
              className="w-full rounded border border-jul-border bg-jul-bg px-2 py-1 font-mono text-jul-text"
            />
          </label>
          <label className="space-y-1">
            <span className="text-jul-muted">Route ID</span>
            <input
              type="text"
              aria-label="Filter by route_id"
              value={routeId}
              placeholder="e.g. rt-api"
              onChange={(e) => {
                setRouteId(e.target.value);
              }}
              className="w-full rounded border border-jul-border bg-jul-bg px-2 py-1 font-mono text-jul-text"
            />
          </label>
        </div>

        {tuneError && (
          <div
            role="alert"
            className="rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3 text-jul-danger"
          >
            {tuneError}
          </div>
        )}

        {query.isPending && <p className="text-jul-muted">Loading WAF investigation buffer…</p>}
        {query.isError && (
          <p className="text-jul-danger">
            Failed to load WAF events:{" "}
            {query.error instanceof Error ? query.error.message : "unknown error"}
          </p>
        )}

        {data && (
          <>
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-jul-border bg-jul-bg/40 px-3 py-2">
              <span>
                Collection: <strong>{data.collection_state}</strong> · retained{" "}
                {String(data.stats.retained_events)}/{String(data.retention.max_events)} (TTL{" "}
                {String(Math.round(data.retention.ttl_seconds / 60))}m)
              </span>
              <span className="text-jul-muted">
                recorded {String(data.stats.recorded_total)} · truncated{" "}
                {String(data.stats.truncated_total)} · dropped {String(data.stats.dropped_total)}
              </span>
            </div>

            {data.events.length === 0 ? (
              <p className="rounded-md border border-jul-border bg-jul-surface p-4 text-jul-muted">
                No matching WAF transaction events in the bounded retention window.
              </p>
            ) : (
              <div className="space-y-3">
                {data.events.map((ev) => (
                  <div
                    key={ev.event_id}
                    className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3"
                  >
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <code className="font-mono font-semibold text-jul-text">{ev.event_id}</code>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span
                          className={`rounded-full px-2 py-0.5 font-medium ${
                            ev.outcome === "matched_observed"
                              ? "bg-jul-warning/15 text-jul-warning"
                              : "bg-jul-danger/15 text-jul-danger"
                          }`}
                        >
                          {ev.outcome}
                        </span>
                        <span className="rounded border border-jul-border px-1.5 py-0.5">
                          mode: {ev.mode}
                        </span>
                        {ev.stale_policy && (
                          <span className="rounded bg-jul-warning/15 px-1.5 py-0.5 text-jul-warning">
                            prior policy generation
                          </span>
                        )}
                      </div>
                    </div>

                    <div className="grid grid-cols-1 gap-1 text-jul-muted sm:grid-cols-2">
                      <div>
                        Request:{" "}
                        <span className="font-mono text-jul-text">
                          {ev.method} {ev.host}
                          {ev.path}
                        </span>
                        {ev.query_omitted ? " (query omitted)" : ""}
                      </div>
                      <div>
                        Client HTTP {String(ev.client_status)} · Origin{" "}
                        {ev.origin_reached
                          ? `reached (HTTP ${String(ev.origin_status ?? 200)})`
                          : "not reached"}
                        {ev.partial_response_delivered ? " · partial response delivered" : ""}
                      </div>
                      <div>
                        Route:{" "}
                        <span className="font-mono text-jul-text">
                          {ev.route.route_id || ev.route.path_pattern || ev.route.scope || "global"}
                        </span>{" "}
                        · gen {String(ev.generation)}
                      </div>
                      {ev.policy_digest && (
                        <div>
                          Policy:{" "}
                          <code className="font-mono" title={ev.policy_digest}>
                            {shortDigest(ev.policy_digest)}
                          </code>
                        </div>
                      )}
                    </div>

                    {(ev.matched_rules ?? []).length > 0 && (
                      <div className="space-y-1 border-t border-jul-border pt-2">
                        <div className="font-medium text-jul-text">
                          Matched rules ({String(ev.total_matched_rules)}
                          {ev.matches_truncated ? ", truncated" : ""})
                        </div>
                        {(ev.matched_rules ?? []).map((mr, idx) => (
                          <div
                            key={`${ev.event_id}-r-${String(mr.rule_id)}-${String(idx)}`}
                            className="flex flex-wrap items-center justify-between gap-2 rounded bg-jul-bg/50 px-2 py-1"
                          >
                            <div>
                              <span className="font-mono font-semibold text-jul-text">
                                rule {String(mr.rule_id)}
                              </span>{" "}
                              <span className="text-jul-muted">
                                (phase {String(mr.phase)}, {mr.source_class}
                                {mr.disruptive ? ", disruptive" : ""})
                              </span>
                              {(mr.matched_targets ?? []).length > 0 && (
                                <span className="ml-2 font-mono text-jul-text">
                                  targets: {(mr.matched_targets ?? []).join(", ")}
                                </span>
                              )}
                            </div>
                            {onTuneException &&
                              (mr.matched_targets ?? [])
                                .filter(isNarrowTargetCandidate)
                                .slice(0, 1)
                                .map((tgt) => {
                                  const proposed: WAFException = {
                                    rule_id: mr.rule_id,
                                    scope: "narrow_target",
                                    target: tgt,
                                    methods: [ev.method],
                                    rationale: `Narrow exception from ${ev.event_id}`,
                                  };
                                  return (
                                    <div key={tgt} className="flex items-center gap-1.5">
                                      <button
                                        type="button"
                                        onClick={() => {
                                          onTuneException(proposed, ev);
                                        }}
                                        className="rounded border border-jul-border bg-jul-surface px-2 py-0.5 text-jul-text hover:bg-jul-bg"
                                      >
                                        Tune {tgt} →
                                      </button>
                                      {onTuneGlobalException && (
                                        <button
                                          type="button"
                                          onClick={() => {
                                            onTuneGlobalException(proposed, ev);
                                          }}
                                          className="rounded border border-jul-border bg-jul-bg px-2 py-0.5 text-jul-muted hover:text-jul-text"
                                        >
                                          Tune globally
                                        </button>
                                      )}
                                    </div>
                                  );
                                })}
                          </div>
                        ))}
                      </div>
                    )}
                  </div>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </Drawer>
  );
}
