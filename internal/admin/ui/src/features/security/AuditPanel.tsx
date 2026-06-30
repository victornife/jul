import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchAudit,
  downloadAuditExport,
  describeApiError,
  type AuditEvent,
  type AuditFilter,
} from "@/api/client.ts";

function resultTone(result: string): string {
  return result === "success" ? "text-jul-success" : "text-jul-danger";
}

function AuditRow({ ev }: { readonly ev: AuditEvent }) {
  return (
    <tr className="border-b border-jul-border last:border-b-0">
      <td className="px-3 py-1.5 text-jul-muted">{new Date(ev.time).toLocaleString()}</td>
      <td className="px-3 py-1.5 font-mono text-jul-text">{ev.operation}</td>
      <td className="px-3 py-1.5 text-jul-muted">{ev.resource ?? "—"}</td>
      <td className={`px-3 py-1.5 font-semibold ${resultTone(ev.result)}`}>{ev.result}</td>
      <td className="px-3 py-1.5 text-jul-muted">{ev.actor}</td>
      <td className="px-3 py-1.5 text-jul-muted">{ev.source_ip ?? "—"}</td>
      <td className="max-w-xs truncate px-3 py-1.5 text-jul-muted">{ev.detail ?? ""}</td>
    </tr>
  );
}

export function AuditPanel() {
  const [op, setOp] = useState("");
  const [result, setResult] = useState("");
  const filter: AuditFilter = {
    op: op || undefined,
    result: result || undefined,
    limit: 500,
  };
  const { data, isError, error, refetch } = useQuery({
    queryKey: ["audit", op, result],
    queryFn: () => fetchAudit(filter),
  });
  const [exporting, setExporting] = useState(false);

  const events = data ?? [];

  const onExport = (format: "json" | "csv") => {
    setExporting(true);
    void downloadAuditExport(format, { op: op || undefined, result: result || undefined }).finally(
      () => {
        setExporting(false);
      },
    );
  };

  return (
    <div className="space-y-1">
      <div>
        <h1 className="text-xl font-semibold">Audit log</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Attributable, append-only record of config and security events. Secrets are never stored.
          Use it for compliance reviews and to trace when a change was introduced.
        </p>
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex flex-col gap-1 text-xs text-jul-muted">
          Operation prefix
          <input
            type="text"
            value={op}
            placeholder="e.g. config."
            onChange={(e) => {
              setOp(e.target.value);
            }}
            className="rounded-md border border-jul-border bg-jul-surface px-2 py-1 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
        </label>
        <label className="flex flex-col gap-1 text-xs text-jul-muted">
          Result
          <select
            value={result}
            onChange={(e) => {
              setResult(e.target.value);
            }}
            className="rounded-md border border-jul-border bg-jul-surface px-2 py-1 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="">All</option>
            <option value="success">success</option>
            <option value="failure">failure</option>
          </select>
        </label>
        <button
          type="button"
          onClick={() => {
            void refetch();
          }}
          className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent"
        >
          Refresh
        </button>
        <div className="ml-auto flex items-center gap-2">
          <button
            type="button"
            disabled={exporting}
            onClick={() => {
              onExport("json");
            }}
            className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent disabled:opacity-50"
          >
            Export JSON
          </button>
          <button
            type="button"
            disabled={exporting}
            onClick={() => {
              onExport("csv");
            }}
            className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg focus:outline-none focus:ring-2 focus:ring-jul-accent disabled:opacity-50"
          >
            Export CSV
          </button>
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
        {events.length === 0 ? (
          <p className="px-4 py-8 text-center text-sm text-jul-muted">
            {isError ? describeApiError(error, "the audit log").message : "No audit events match."}
          </p>
        ) : (
          <div className="max-h-[calc(100vh-260px)] overflow-auto">
            <table className="w-full text-left text-xs">
              <thead className="sticky top-0 bg-jul-surface text-jul-muted">
                <tr className="border-b border-jul-border">
                  <th scope="col" className="px-3 py-2 font-medium">Time</th>
                  <th scope="col" className="px-3 py-2 font-medium">Operation</th>
                  <th scope="col" className="px-3 py-2 font-medium">Resource</th>
                  <th scope="col" className="px-3 py-2 font-medium">Result</th>
                  <th scope="col" className="px-3 py-2 font-medium">Actor</th>
                  <th scope="col" className="px-3 py-2 font-medium">Source IP</th>
                  <th scope="col" className="px-3 py-2 font-medium">Detail</th>
                </tr>
              </thead>
              <tbody>
                {events.map((ev) => (
                  <AuditRow key={ev.id} ev={ev} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
