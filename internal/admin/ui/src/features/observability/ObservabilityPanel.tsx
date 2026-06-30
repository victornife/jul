import { useEffect, useRef, useState } from "react";
import { subscribeEvents, type SseEvent } from "@/api/client.ts";

const MAX_EVENTS = 200;

const EVENT_COLORS: Record<string, string> = {
  reload: "text-jul-accent",
  config_change: "text-jul-warning",
  connected: "text-jul-success",
  ping: "text-jul-muted",
};

function EventRow({ ev }: { readonly ev: SseEvent & { seq: number } }) {
  const color = EVENT_COLORS[ev.type] ?? "text-jul-text";
  const time = new Date(ev.time).toLocaleTimeString();
  return (
    <li className="flex gap-3 border-b border-jul-border px-4 py-2 font-mono text-xs last:border-b-0">
      <span className="shrink-0 text-jul-muted">{time}</span>
      <span className={`shrink-0 w-24 ${color}`}>{ev.type}</span>
      {ev.data !== undefined && (
        <span className="text-jul-muted truncate">
          {typeof ev.data === "string" ? ev.data : JSON.stringify(ev.data)}
        </span>
      )}
    </li>
  );
}

export function ObservabilityPanel() {
  const [events, setEvents] = useState<Array<SseEvent & { seq: number }>>([]);
  const [connected, setConnected] = useState(false);
  const [filter, setFilter] = useState("");
  const seqRef = useRef(0);
  const listRef = useRef<HTMLUListElement>(null);

  useEffect(() => {
    const cleanup = subscribeEvents(
      (ev) => {
        if (ev.type === "connected") setConnected(true);
        setEvents((prev) => {
          const next = [
            ...prev,
            { ...ev, seq: ++seqRef.current },
          ].slice(-MAX_EVENTS);
          return next;
        });
        // Auto-scroll to bottom.
        requestAnimationFrame(() => {
          if (listRef.current) {
            listRef.current.scrollTop = listRef.current.scrollHeight;
          }
        });
      },
      () => {
        setConnected(false);
      },
    );
    return cleanup;
  }, []);

  const filtered = filter
    ? events.filter((e) => e.type.includes(filter) || JSON.stringify(e.data ?? "").includes(filter))
    : events;

  return (
    <div className="flex h-full flex-col space-y-4">
      <div className="space-y-1">
        <div className="flex items-center gap-4">
          <h1 className="text-xl font-semibold">Observability</h1>
          <span
            className={`rounded-full px-2 py-0.5 text-xs font-medium ${
              connected
                ? "bg-jul-success/15 text-jul-success"
                : "bg-jul-danger/15 text-jul-danger"
            }`}
          >
            {connected ? "connected" : "disconnected"}
          </span>
        </div>
        <p className="max-w-3xl text-sm text-jul-muted">
          Live event stream from the gateway: config changes, reloads, and runtime anomalies.
          Filter by type or clear the buffer to focus on what matters right now.
        </p>
      </div>

      <div className="flex items-center gap-3">
        <input
          type="text"
          placeholder="Filter by type…"
          value={filter}
          onChange={(e) => {
            setFilter(e.target.value);
          }}
          className="rounded-md border border-jul-border bg-jul-surface px-3 py-1 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
        />
        <button
          onClick={() => {
            setEvents([]);
          }}
          className="rounded-md border border-jul-border px-3 py-1 text-xs text-jul-muted hover:text-jul-text"
        >
          Clear
        </button>
      </div>

      <div className="flex-1 overflow-hidden rounded-lg border border-jul-border bg-jul-surface">
        {filtered.length === 0 ? (
          <p className="px-4 py-6 text-center text-xs text-jul-muted">
            {connected ? "Waiting for events…" : "Not connected to event stream."}
          </p>
        ) : (
          <ul
            ref={listRef}
            className="h-full overflow-y-auto max-h-[calc(100vh-220px)]"
          >
            {filtered.map((ev) => (
              <EventRow key={ev.seq} ev={ev} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
