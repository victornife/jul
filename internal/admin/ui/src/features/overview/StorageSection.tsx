/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { Link } from "react-router-dom";
import type { StorageHeadroom, StorageHints } from "@/api/client.ts";
import { formatBytes } from "@/lib/formatBytes.ts";
import {
  STORAGE_CATEGORY_META,
  STORAGE_REASON_TEXT,
  storageAttention,
  storageLabel,
} from "@/lib/storage.ts";

function StateBadge({ h }: { readonly h: StorageHeadroom }) {
  // Text badges: the state is never conveyed by colour alone.
  const badges: { text: string; tone: string }[] = [];
  if (h.writesFailing)
    badges.push({ text: "Writes failing", tone: "bg-jul-danger/20 text-jul-danger" });
  if (h.state === "critical")
    badges.push({ text: "Critical headroom", tone: "bg-jul-danger/20 text-jul-danger" });
  else if (h.state === "low")
    badges.push({ text: "Low headroom", tone: "bg-jul-warning/20 text-jul-warning" });
  else if (h.state === "error")
    badges.push({ text: "Error", tone: "bg-jul-danger/20 text-jul-danger" });
  if (badges.length === 0) return null;
  return (
    <span className="flex flex-wrap gap-1">
      {badges.map((b) => (
        <span
          key={b.text}
          className={`rounded-full px-2 py-0.5 text-[10px] font-semibold uppercase tracking-wide ${b.tone}`}
        >
          {b.text}
        </span>
      ))}
    </span>
  );
}

function StorageCard({ h }: { readonly h: StorageHeadroom }) {
  const meta = STORAGE_CATEGORY_META[h.category];
  const label = storageLabel(h.category);
  const known = h.state === "ok" || h.state === "low" || h.state === "critical";
  const value =
    known && h.availableBytes !== undefined ? `${formatBytes(h.availableBytes)} free` : undefined;
  const border =
    h.writesFailing || h.state === "critical" || h.state === "error"
      ? "border-jul-danger/50 bg-jul-danger/5"
      : h.state === "low"
        ? "border-jul-warning/50 bg-jul-warning/5"
        : "border-jul-border bg-jul-surface";
  return (
    <li className={`rounded-lg border p-4 ${border}`} aria-label={`${label} storage`}>
      <div className="flex items-start justify-between gap-2">
        <div className="text-xs font-semibold uppercase tracking-wider text-jul-muted">{label}</div>
        <StateBadge h={h} />
      </div>
      <div
        className={`mt-2 text-2xl font-bold ${value === undefined ? "text-jul-muted italic" : "text-jul-text"}`}
      >
        {value ?? (h.state === "error" ? "error" : "unavailable")}
      </div>
      <div className="mt-1 space-y-0.5 text-xs text-jul-muted">
        {known && h.totalBytes !== undefined && h.availableRatio !== undefined && (
          <div>
            of {formatBytes(h.totalBytes)} — {(h.availableRatio * 100).toFixed(1)}% available
          </div>
        )}
        {!known && h.reason && <div>{STORAGE_REASON_TEXT[h.reason] ?? h.reason}</div>}
        {!known && !h.reason && <div>capacity not reported for this location</div>}
        {h.pendingCreation && (
          <div>not created yet — measured on the filesystem it will be created in</div>
        )}
        {h.sharedWith && h.sharedWith.length > 0 && (
          <div>shares a filesystem with {h.sharedWith.map(storageLabel).join(", ")}</div>
        )}
        {h.writesFailing && (
          <div className="font-medium text-jul-danger">
            The most recent write failed — this subsystem is already losing writes.
          </div>
        )}
        {!h.writesFailing && h.writeFailures !== undefined && h.writeFailures > 0 && (
          <div>
            {Math.round(h.writeFailures).toLocaleString()} earlier write failure
            {h.writeFailures === 1 ? "" : "s"} since start; writes currently succeed
          </div>
        )}
        {meta && (
          <div>
            configured by <code className="font-mono">{meta.configKey}</code> ·{" "}
            <Link to={meta.route} className="text-jul-accent underline">
              {meta.where}
              <span className="sr-only"> (configure {label})</span>
            </Link>
          </div>
        )}
      </div>
    </li>
  );
}

// StorageSection surfaces advisory headroom for the filesystems Jul itself is
// configured to write (#437). It is not a host disk view: only Jul-owned
// categories appear, and no path, mount or device is ever shown.
export function StorageSection({
  storage,
  hints,
}: {
  readonly storage: StorageHeadroom[] | undefined;
  readonly hints: StorageHints | undefined;
}) {
  if (!storage || storage.length === 0) return null;
  const attention = storage.some(storageAttention);
  return (
    <section className="space-y-4" aria-labelledby="storage-heading">
      <h2 id="storage-heading" className="text-sm font-semibold text-jul-muted">
        Storage
      </h2>
      <ul className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {storage.map((h) => (
          <StorageCard key={h.category} h={h} />
        ))}
      </ul>
      <p className={`text-xs ${attention ? "text-jul-warning" : "text-jul-muted"}`}>
        {hints
          ? `Low below ${(hints.lowRatio * 100).toFixed(0)}% available, critical below ${(hints.criticalRatio * 100).toFixed(0)}%. `
          : ""}
        These are generic local hints, not SLOs, and never affect readiness. &quot;Writes
        failing&quot; is the subsystem&apos;s own signal and means data is already being lost.
      </p>
    </section>
  );
}
