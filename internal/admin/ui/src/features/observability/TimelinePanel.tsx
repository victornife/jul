import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchTimeline, describeApiError, type TimelineEvent } from "@/api/client.ts";

const SEVERITY_TONE: Record<string, string> = {
  info: "text-jul-accent",
  warning: "text-jul-warning",
  error: "text-jul-danger",
};

const CATEGORY_LABEL: Record<string, string> = {
  config: "Config",
  runtime: "Runtime",
  tls: "TLS",
  upstream: "Upstream",
};

const SEVERITY_LABEL: Record<string, string> = {
  info: "Informational",
  warning: "Warning",
  error: "Error",
};

function dotTone(severity: string): string {
  if (severity === "error") return "bg-jul-danger";
  if (severity === "warning") return "bg-jul-warning";
  return "bg-jul-accent";
}

function TimelineRow({ ev }: { readonly ev: TimelineEvent }) {
  const severityLabel = SEVERITY_LABEL[ev.severity] ?? ev.severity;
  const categoryLabel = CATEGORY_LABEL[ev.category] ?? ev.category;
  return (
    <li className="flex gap-3 px-4 py-3 text-sm">
      <span
        role="img"
        aria-label={`${severityLabel} ${categoryLabel.toLowerCase()} event`}
        title={`${severityLabel} severity — ${categoryLabel.toLowerCase()} event`}
        className={`mt-1.5 h-2 w-2 shrink-0 rounded-full ${dotTone(ev.severity)}`}
      />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-baseline gap-x-2">
          <span className={`text-xs font-semibold uppercase ${SEVERITY_TONE[ev.severity] ?? "text-jul-text"}`}>
            {categoryLabel}
          </span>
          <span className="font-mono text-xs text-jul-muted">{ev.type}</span>
          <span className="ml-auto text-xs text-jul-muted">{new Date(ev.time).toLocaleString()}</span>
        </div>
        <p className="mt-0.5 text-jul-text">{ev.message}</p>
        {ev.ref !== undefined && ev.ref !== "" && (
          <p className="mt-0.5 text-xs text-jul-muted">snapshot {ev.ref}</p>
        )}
      </div>
    </li>
  );
}

export function TimelinePanel() {
  const { data, isError, error } = useQuery({
    queryKey: ["timeline"],
    queryFn: fetchTimeline,
  });
  const [category, setCategory] = useState("");

  const events = data ?? [];
  const filtered = category ? events.filter((e) => e.category === category) : events;
  const categories = Array.from(new Set(events.map((e) => e.category)));

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Timeline</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Merged config and runtime events newest-first: apply, reload, rollback, upstream health,
          and certificate changes. Use it to trace the cause of an incident back to its origin.
        </p>
      </div>

      <div className="flex items-center gap-3">
        <label className="flex items-center gap-2 text-xs text-jul-muted">
          Category
          <select
            value={category}
            onChange={(e) => {
              setCategory(e.target.value);
            }}
            className="rounded-md border border-jul-border bg-jul-surface px-2 py-1 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="">All</option>
            {categories.map((c) => (
              <option key={c} value={c}>
                {CATEGORY_LABEL[c] ?? c}
              </option>
            ))}
          </select>
        </label>
      </div>

      <div className="overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
        {filtered.length === 0 ? (
          <p className="px-4 py-8 text-center text-sm text-jul-muted">
            {isError ? describeApiError(error, "the timeline").message : "No events yet."}
          </p>
        ) : (
          <ul className="divide-y divide-jul-border">
            {filtered.map((ev, i) => (
              <TimelineRow key={`${ev.time}-${String(i)}`} ev={ev} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
