/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { Link } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  fetchRequestSamples,
  fetchFailingRoutes,
  fetchUpstreamHistory,
  fetchCertHistory,
  fetchConsoleHealth,
  fetchOverview,
  describeApiError,
  type RequestSample,
  type RouteFailure,
  type BackendHealthHistory,
  type CertRenewalHistory,
  type ConsoleHealth,
} from "@/api/client.ts";
import { TimelinePanel } from "@/features/observability/TimelinePanel.tsx";
import { ObservabilityPanel } from "@/features/observability/ObservabilityPanel.tsx";
import { LogTailPanel } from "@/features/observability/LogTailPanel.tsx";

// ── small presentational helpers ─────────────────────────────────────────────

function statusColor(status: number): string {
  if (status >= 500) return "text-jul-danger";
  if (status >= 400) return "text-jul-warning";
  if (status >= 300) return "text-jul-muted";
  return "text-jul-success";
}

function Section({
  title,
  hint,
  children,
}: {
  readonly title: string;
  readonly hint?: string;
  readonly children: React.ReactNode;
}) {
  return (
    <section className="space-y-3" aria-label={title}>
      <div className="flex items-baseline gap-3">
        <h2 className="text-sm font-semibold uppercase tracking-wider text-jul-muted">{title}</h2>
        {hint !== undefined && <span className="text-xs text-jul-muted">{hint}</span>}
      </div>
      <div className="rounded-lg border border-jul-border bg-jul-surface">{children}</div>
    </section>
  );
}

function Empty({ label }: { readonly label: string }) {
  return <p className="px-4 py-6 text-center text-xs text-jul-muted">{label}</p>;
}

// ── Console health (Milestone 5.7) ───────────────────────────────────────────

function ConsoleHealthCards({ health }: { readonly health: ConsoleHealth }) {
  const cells: Array<{ label: string; value: string; tone?: string | undefined }> = [
    {
      label: "Status",
      value: health.status,
      tone: health.status === "ok" ? "text-jul-success" : "text-jul-warning",
    },
    { label: "Requests", value: String(health.requests) },
    {
      label: "Errors",
      value: String(health.errors),
      tone: health.errors > 0 ? "text-jul-danger" : undefined,
    },
    { label: "p50", value: `${health.latency_p50.toFixed(1)} ms` },
    { label: "p95", value: `${health.latency_p95.toFixed(1)} ms` },
    { label: "p99", value: `${health.latency_p99.toFixed(1)} ms` },
    { label: "SSE conns", value: String(health.sse_conns) },
  ];
  return (
    <div className="grid grid-cols-2 gap-px overflow-hidden rounded-lg bg-jul-border sm:grid-cols-4 lg:grid-cols-7">
      {cells.map((c) => (
        <div key={c.label} className="bg-jul-surface px-4 py-3">
          <div className="text-xs text-jul-muted">{c.label}</div>
          <div className={`text-lg font-semibold ${c.tone ?? "text-jul-text"}`}>{c.value}</div>
        </div>
      ))}
    </div>
  );
}

// ── Request samples (Milestone 5.1) ──────────────────────────────────────────

function RequestSamplesTable({ samples }: { readonly samples: RequestSample[] }) {
  if (samples.length === 0) return <Empty label="No request samples captured yet." />;
  return (
    <div className="max-h-96 overflow-auto">
      <table className="w-full text-left text-xs">
        <thead className="sticky top-0 bg-jul-surface text-jul-muted">
          <tr className="border-b border-jul-border">
            <th scope="col" className="px-3 py-2 font-medium">
              Time
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Method
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Path
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Status
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Duration
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Flags
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Origin / UA
            </th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {samples.map((s, i) => (
            <tr
              key={`${s.time}-${String(i)}`}
              className="border-b border-jul-border last:border-b-0"
            >
              <td className="px-3 py-1.5 text-jul-muted">
                {new Date(s.time).toLocaleTimeString()}
              </td>
              <td className="px-3 py-1.5 text-jul-text">{s.method}</td>
              <td className="max-w-xs truncate px-3 py-1.5 text-jul-text">{s.path}</td>
              <td className={`px-3 py-1.5 font-semibold ${statusColor(s.status)}`}>{s.status}</td>
              <td className="px-3 py-1.5 text-jul-muted">
                {s.duration_ms !== undefined ? `${s.duration_ms.toFixed(1)} ms` : "—"}
              </td>
              <td className="px-3 py-1.5 text-jul-muted">
                {[
                  s.cache_state ? `cache:${s.cache_state}` : "",
                  s.compressed ? "gzip" : "",
                  s.rate_limited ? "limited" : "",
                ]
                  .filter(Boolean)
                  .join(" ") || "—"}
              </td>
              <td className="max-w-xs truncate px-3 py-1.5 text-jul-muted">
                {[s.origin ?? "", s.user_agent ?? ""].filter(Boolean).join(" · ") || "—"}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ── Top failing routes (Milestone 5.2) ───────────────────────────────────────

function FailingRoutesTable({ routes }: { readonly routes: RouteFailure[] }) {
  if (routes.length === 0) {
    return <Empty label="No failing routes — all recent requests succeeded." />;
  }
  return (
    <div className="max-h-96 overflow-auto">
      <table className="w-full text-left text-xs">
        <thead className="sticky top-0 bg-jul-surface text-jul-muted">
          <tr className="border-b border-jul-border">
            <th scope="col" className="px-3 py-2 font-medium">
              Route
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              4xx
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              5xx
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Error rate
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              p95
            </th>
            <th scope="col" className="px-3 py-2 font-medium">
              Last error
            </th>
          </tr>
        </thead>
        <tbody className="font-mono">
          {routes.map((r) => (
            <tr key={r.path} className="border-b border-jul-border last:border-b-0">
              <td className="max-w-xs truncate px-3 py-1.5 text-jul-text">{r.path}</td>
              <td className="px-3 py-1.5 text-jul-warning">{r.status_4xx}</td>
              <td className="px-3 py-1.5 text-jul-danger">{r.status_5xx}</td>
              <td className="px-3 py-1.5 text-jul-text">{`${(r.error_rate * 100).toFixed(1)}%`}</td>
              <td className="px-3 py-1.5 text-jul-muted">{`${r.latency_p95_ms.toFixed(0)} ms`}</td>
              <td className="px-3 py-1.5 text-jul-muted">{r.last_error_class ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// ── Upstream health history (Milestone 5.5) ──────────────────────────────────

function UpstreamHistoryList({ backends }: { readonly backends: BackendHealthHistory[] }) {
  if (backends.length === 0) {
    return <Empty label="No upstream health transitions recorded." />;
  }
  return (
    <ul className="divide-y divide-jul-border">
      {backends.map((b) => (
        <li
          key={`${b.pool}|${b.backend}`}
          className="flex flex-wrap items-center gap-3 px-4 py-3 text-xs"
        >
          <span
            className={`h-2.5 w-2.5 shrink-0 rounded-full ${b.healthy ? "bg-jul-success" : "bg-jul-danger"}`}
            aria-hidden="true"
          />
          <span className="font-mono text-sm text-jul-text">
            {b.pool}/{b.backend}
          </span>
          {b.flapping && (
            <span className="rounded-full bg-jul-warning/15 px-2 py-0.5 font-medium text-jul-warning">
              flapping
            </span>
          )}
          <span className="text-jul-muted">{b.transitions} transitions</span>
          <span className="ml-auto text-jul-muted">
            {b.last_down ? `last down ${new Date(b.last_down).toLocaleString()}` : "no downtime"}
          </span>
        </li>
      ))}
    </ul>
  );
}

// ── Certificate renewal history (Milestone 5.6) ──────────────────────────────

function CertHistoryList({ domains }: { readonly domains: CertRenewalHistory[] }) {
  if (domains.length === 0) {
    return <Empty label="No certificate renewal activity recorded." />;
  }
  return (
    <ul className="divide-y divide-jul-border">
      {domains.map((d) => (
        <li key={d.domain} className="space-y-1 px-4 py-3 text-xs">
          <div className="flex flex-wrap items-center gap-3">
            <span className="font-mono text-sm text-jul-text">{d.domain}</span>
            {d.staging && (
              <span className="rounded-full bg-jul-warning/15 px-2 py-0.5 font-medium text-jul-warning">
                staging
              </span>
            )}
            <span
              className={`ml-auto font-semibold ${
                d.days_left <= 7
                  ? "text-jul-danger"
                  : d.days_left <= 30
                    ? "text-jul-warning"
                    : "text-jul-success"
              }`}
            >
              {d.days_left} days left
            </span>
          </div>
          <div className="flex flex-wrap gap-x-4 gap-y-0.5 text-jul-muted">
            {d.issuer !== undefined && <span>issuer {d.issuer}</span>}
            {d.last_success !== undefined && (
              <span>last success {new Date(d.last_success).toLocaleString()}</span>
            )}
            {d.last_error !== undefined && (
              <span className="text-jul-danger">last error: {d.last_error}</span>
            )}
          </div>
        </li>
      ))}
    </ul>
  );
}

// ── Origins & CORS (Milestone 5.3, from overview traffic_sources) ────────────

function topEntries(rec: Record<string, number> | undefined, n: number): Array<[string, number]> {
  if (!rec) return [];
  return Object.entries(rec)
    .sort((a, b) => b[1] - a[1])
    .slice(0, n);
}

function OriginsCors() {
  const { data } = useQuery({ queryKey: ["overview"], queryFn: fetchOverview });
  const ts = data?.traffic_sources;
  const origins = topEntries(ts?.origins, 8);
  const referers = topEntries(ts?.referers, 8);

  return (
    <div className="grid gap-px overflow-hidden rounded-lg bg-jul-border sm:grid-cols-2">
      <div className="space-y-2 bg-jul-surface p-4">
        <div className="flex items-center justify-between text-xs text-jul-muted">
          <span className="font-medium">Top origins</span>
          <span>
            preflight {String(ts?.preflight_count ?? 0)} · same {String(ts?.same_origin ?? 0)} ·
            cross {String(ts?.cross_origin ?? 0)}
          </span>
        </div>
        {origins.length === 0 ? (
          <p className="py-2 text-xs text-jul-muted">No cross-origin traffic observed.</p>
        ) : (
          <ul className="space-y-1 font-mono text-xs">
            {origins.map(([host, count]) => (
              <li key={host} className="flex justify-between">
                <span className="truncate text-jul-text">{host}</span>
                <span className="text-jul-muted">{String(count)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
      <div className="space-y-2 bg-jul-surface p-4">
        <span className="text-xs font-medium text-jul-muted">Top referers</span>
        {referers.length === 0 ? (
          <p className="py-2 text-xs text-jul-muted">No referers observed.</p>
        ) : (
          <ul className="space-y-1 font-mono text-xs">
            {referers.map(([host, count]) => (
              <li key={host} className="flex justify-between">
                <span className="truncate text-jul-text">{host}</span>
                <span className="text-jul-muted">{String(count)}</span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}

// ── Diagnostics tab (the request/route/upstream/cert depth) ──────────────────

function DiagnosticsTab() {
  const health = useQuery({ queryKey: ["console-health"], queryFn: fetchConsoleHealth });
  const samples = useQuery({ queryKey: ["request-samples"], queryFn: fetchRequestSamples });
  const failing = useQuery({ queryKey: ["failing-routes"], queryFn: () => fetchFailingRoutes(20) });
  const upstream = useQuery({ queryKey: ["upstream-history"], queryFn: fetchUpstreamHistory });
  const certs = useQuery({ queryKey: ["cert-history"], queryFn: fetchCertHistory });

  return (
    <div className="space-y-8">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Console health</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          The admin API's own vitals: request latency, error rate, and SSE connection health.
          Use this to confirm the console itself is responsive before investigating downstream issues.
        </p>
      </div>

      <Section title="Health cards" hint="p50 / p95 / p99 · requests · errors · SSE conns">
        {health.data ? (
          <div className="p-3">
            <ConsoleHealthCards health={health.data} />
          </div>
        ) : (
          <Empty
            label={
              health.isError ? describeApiError(health.error, "console health").message : "Loading…"
            }
          />
        )}
      </Section>

      <Section title="Request samples" hint="newest first · bounded ring buffer">
        {samples.data ? (
          <RequestSamplesTable samples={samples.data} />
        ) : (
          <Empty
            label={
              samples.isError
                ? describeApiError(samples.error, "request samples").message
                : "Loading…"
            }
          />
        )}
      </Section>

      <Section title="Top failing routes" hint="ranked 5xx → 4xx → volume">
        {failing.data ? (
          <FailingRoutesTable routes={failing.data} />
        ) : (
          <Empty
            label={
              failing.isError
                ? describeApiError(failing.error, "failing routes").message
                : "Loading…"
            }
          />
        )}
      </Section>

      <Section title="Upstream health history" hint="transitions & flapping">
        {upstream.data ? (
          <UpstreamHistoryList backends={upstream.data} />
        ) : (
          <Empty
            label={
              upstream.isError
                ? describeApiError(upstream.error, "upstream history").message
                : "Loading…"
            }
          />
        )}
      </Section>

      <Section title="Certificate renewal history" hint="expiry & ACME outcomes">
        {certs.data ? (
          <CertHistoryList domains={certs.data} />
        ) : (
          <Empty
            label={
              certs.isError
                ? describeApiError(certs.error, "certificate history").message
                : "Loading…"
            }
          />
        )}
      </Section>

      <Section title="Origins & CORS" hint="top origins / referers · preflight counts">
        <OriginsCors />
      </Section>
    </div>
  );
}

// ── Operations workspace (C-4: Events + Timeline folded in as tabs) ──────────

export type OperationsTab = "diagnostics" | "events" | "logs" | "timeline";

const TABS: ReadonlyArray<{ id: OperationsTab; label: string; to: string; hint: string }> = [
  {
    id: "diagnostics",
    label: "Diagnostics",
    to: "/operations",
    hint: "samples, failing routes, health",
  },
  { id: "events", label: "Events", to: "/operations/events", hint: "live SSE stream" },
  { id: "logs", label: "Logs", to: "/operations/logs", hint: "live access-log tail" },
  {
    id: "timeline",
    label: "Timeline",
    to: "/operations/timeline",
    hint: "merged config & runtime history",
  },
];

// OperationsPanel is the single troubleshooting workspace (P2-10): the live
// Events stream and the merged Timeline are folded in as tabs alongside the
// diagnostics depth, so an operator has one obvious place to investigate rather
// than choosing between Events, Timeline, and Operations. Audit stays separate
// because it is a compliance/security surface, not a troubleshooting view.
export function OperationsPanel({ tab = "diagnostics" }: { readonly tab?: OperationsTab }) {
  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Operations</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          The troubleshooting workspace: recent behavior and health, the live event stream, and the
          merged config/runtime timeline. All views are bounded and privacy-preserving.
        </p>
      </div>

      <nav className="flex gap-1 border-b border-jul-border" aria-label="Operations views">
        {TABS.map((t) => {
          const active = t.id === tab;
          return (
            <Link
              key={t.id}
              to={t.to}
              title={t.hint}
              aria-current={active ? "page" : undefined}
              className={`-mb-px border-b-2 px-3 py-2 text-sm transition-colors ${
                active
                  ? "border-jul-accent font-medium text-jul-text"
                  : "border-transparent text-jul-muted hover:text-jul-text"
              }`}
            >
              {t.label}
            </Link>
          );
        })}
      </nav>

      {tab === "diagnostics" && <DiagnosticsTab />}
      {tab === "events" && <ObservabilityPanel />}
      {tab === "logs" && <LogTailPanel />}
      {tab === "timeline" && <TimelinePanel />}
    </div>
  );
}
