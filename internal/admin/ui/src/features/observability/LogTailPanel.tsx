/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, useState } from "react";
import { subscribeLogs, type LogEntry } from "@/api/client.ts";

// MAX_LINES bounds what the tab keeps in memory; the backend ring buffer is the
// source of truth, this is just the on-screen tail.
const MAX_LINES = 500;

type Row = LogEntry & { seq: number };

function statusColor(status: number): string {
  if (status >= 500) return "text-jul-danger";
  if (status >= 400) return "text-jul-warning";
  if (status >= 300) return "text-jul-muted";
  return "text-jul-success";
}

function haystack(e: LogEntry): string {
  return [e.method, e.path, e.host, String(e.status), e.remote ?? "", e.proto ?? ""]
    .join(" ")
    .toLowerCase();
}

function LogRow({ row }: { readonly row: Row }) {
  const time = new Date(row.time).toLocaleTimeString();
  const meta = [row.host, row.remote ?? "", row.user_agent ?? ""].filter(Boolean).join(" · ");
  return (
    <li className="flex gap-3 border-b border-jul-border px-4 py-1.5 font-mono text-xs last:border-b-0">
      <span className="shrink-0 text-jul-muted">{time}</span>
      <span className="w-14 shrink-0 text-jul-text">{row.method}</span>
      <span className={`w-10 shrink-0 font-semibold ${statusColor(row.status)}`}>{row.status}</span>
      <span className="min-w-0 flex-1 truncate text-jul-text">{row.path}</span>
      <span className="w-16 shrink-0 text-right text-jul-muted">{`${row.duration_ms.toFixed(1)}ms`}</span>
      <span className="hidden w-64 shrink-0 truncate text-jul-muted sm:block">{meta || "—"}</span>
    </li>
  );
}

// LogTailPanel is the Operations Log tab (Phase 4g): a live access-log tail fed
// by the bounded ring-buffer sink over SSE. It is privacy-preserving by
// construction — paths are redacted, query strings dropped, and User-Agents
// reduced to a coarse family server-side. Pause freezes the view (incoming
// lines are dropped, not buffered) so an operator can read without the tail
// scrolling away.
export function LogTailPanel() {
  const [rows, setRows] = useState<Row[]>([]);
  const [connected, setConnected] = useState(false);
  const [paused, setPaused] = useState(false);
  const [filter, setFilter] = useState("");
  const seqRef = useRef(0);
  const pausedRef = useRef(false);
  const listRef = useRef<HTMLUListElement>(null);

  useEffect(() => {
    pausedRef.current = paused;
  }, [paused]);

  useEffect(() => {
    const stop = subscribeLogs(
      (entry) => {
        if (pausedRef.current) return;
        setRows((prev) => [...prev, { ...entry, seq: ++seqRef.current }].slice(-MAX_LINES));
        requestAnimationFrame(() => {
          if (listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight;
        });
      },
      {
        onOpen: () => {
          setConnected(true);
        },
        onError: () => {
          setConnected(false);
        },
      },
    );
    return stop;
  }, []);

  const needle = filter.trim().toLowerCase();
  const visible = needle ? rows.filter((r) => haystack(r).includes(needle)) : rows;

  return (
    <div className="flex h-full flex-col space-y-6">
      <div className="space-y-1">
        <div className="flex items-center gap-4">
          <h1 className="text-xl font-semibold">Access log</h1>
          <span
            className={`rounded-full px-2 py-0.5 text-xs font-medium ${
              connected
                ? "bg-jul-success/15 text-jul-success"
                : "bg-jul-danger/15 text-jul-danger"
            }`}
          >
            {connected ? "live" : "connecting…"}
          </span>
          {paused && (
            <span className="rounded-full bg-jul-warning/15 px-2 py-0.5 text-xs font-medium text-jul-warning">
              paused
            </span>
          )}
          <span className="text-xs text-jul-muted">{visible.length} lines</span>
        </div>
        <p className="max-w-3xl text-sm text-jul-muted">
          Live tail of processed requests with method, path, status, and latency.
          Filter by any field, pause to inspect a pattern, or clear the buffer to start fresh.
        </p>
      </div>

      <div className="flex items-center gap-3">
        <input
          type="text"
          placeholder="Filter by method, path, status, host…"
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value);
          }}
          className="rounded-md border border-jul-border bg-jul-surface px-3 py-1 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
        />
        <button
          onClick={() => {
            setPaused((p) => !p);
          }}
          className="rounded-md border border-jul-border px-3 py-1 text-xs text-jul-muted hover:text-jul-text"
        >
          {paused ? "Resume" : "Pause"}
        </button>
        <button
          onClick={() => {
            setRows([]);
          }}
          className="rounded-md border border-jul-border px-3 py-1 text-xs text-jul-muted hover:text-jul-text"
        >
          Clear
        </button>
      </div>

      <div className="flex-1 overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
        {visible.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-jul-muted">
            {connected
              ? rows.length === 0
                ? "Waiting for requests…"
                : "No lines match the filter."
              : "Connecting to the log stream…"}
          </p>
        ) : (
          <ul ref={listRef} className="h-full overflow-y-auto max-h-[calc(100vh-220px)]">
            {visible.map((row) => (
              <LogRow key={row.seq} row={row} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
