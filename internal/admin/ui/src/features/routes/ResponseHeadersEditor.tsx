/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { type RouteTarget } from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  emptyResponseHeaderRow,
  responseHeaderRowsToPatch,
  responseHeaderWarnings,
  seedResponseHeaderRows,
  type ResponseHeaderRow,
  type ResponseHeaderRowOp,
} from "@/lib/responseHeadersDraft.ts";

export interface ResponseHeadersEditorProps {
  /** The location whose response-header operations are being edited. */
  readonly target: RouteTarget;
  /** The location's current ordered operations, used to seed the form. */
  readonly seed?: readonly { op: string; name: string; value?: string }[] | undefined;
  /** Whether the location already has response-header operations. */
  readonly existing?: boolean;
  readonly onClose: () => void;
}

const OPS: ResponseHeaderRowOp[] = ["add", "set", "remove"];

function moveRow<T>(rows: readonly T[], from: number, to: number): T[] {
  if (to < 0 || to >= rows.length) return rows.slice();
  const next = rows.slice();
  const [moved] = next.splice(from, 1);
  if (moved === undefined) return next;
  next.splice(to, 0, moved);
  return next;
}

/**
 * Guided editor for a single location's ordered response-header operations
 * (ADR 0018 §8, #147). Order is semantically meaningful — a "set" followed by
 * two "add"s is the canonical deterministic multi-value form — so rows are
 * reordered with explicit move buttons rather than drag-and-drop, which needs
 * no keyboard alternative. It routes the change through
 * location_response_headers_set / _clear, replacing the location's whole
 * ordered list wholesale, the same convention every other per-location editor
 * here uses.
 */
export function ResponseHeadersEditor({
  target,
  seed,
  existing = Boolean(seed && seed.length > 0),
  onClose,
}: ResponseHeadersEditorProps) {
  const [rows, setRows] = useState<ResponseHeaderRow[]>(() => seedResponseHeaderRows(seed));
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;
  const warnings = responseHeaderWarnings(rows);

  function updateRow(i: number, patch: Partial<ResponseHeaderRow>): void {
    setRows((rs) => rs.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  }

  function addRow(): void {
    setRows((rs) => [...rs, emptyResponseHeaderRow()]);
  }

  function removeRow(i: number): void {
    setRows((rs) => rs.filter((_, j) => j !== i));
  }

  function move(i: number, dir: -1 | 1): void {
    setRows((rs) => moveRow(rs, i, i + dir));
  }

  function save(): void {
    runPatch({
      op: "location_response_headers_set",
      ...target,
      response_headers: responseHeaderRowsToPatch(rows),
    });
  }

  function clearOps(): void {
    runPatch({ op: "location_response_headers_clear", ...target });
  }

  return (
    <Drawer
      title={existing ? "Edit response headers" : "Add response headers"}
      subtitle={`Response headers for ${where}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            {existing && (
              <button
                type="button"
                disabled={busy || !canWrite}
                onClick={clearOps}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
              >
                Clear operations
              </button>
            )}
            <button
              type="button"
              disabled={busy || warnings.length > 0 || !canWrite}
              onClick={save}
              className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {busy ? "Previewing…" : "Review lifecycle and diff →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          This <strong>replaces</strong> the whole ordered list for <span className="font-mono">{where}</span>{" "}
          wholesale. Operations apply top to bottom; a later one observes the earlier ones' effect —
          a "set" followed by two "add"s is the way to express a multi-value header.
        </p>

        {existing && seed === undefined && (
          <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
            Existing response-header operations are not read back from the API (they may be
            operator-sensitive) — this route&apos;s current ones are not shown below. Saving
            replaces the whole list, so re-add any operation you want to keep before continuing.
          </p>
        )}

        <div className="space-y-2">
          {rows.map((row, i) => (
            <div
              key={`rh-${String(i)}`}
              className="flex items-start gap-2 rounded-md border border-jul-border bg-jul-surface p-2"
            >
              <div className="flex flex-col gap-1 pt-1">
                <button
                  type="button"
                  aria-label={`Move row ${String(i + 1)} up`}
                  disabled={i === 0}
                  onClick={() => {
                    move(i, -1);
                  }}
                  className="rounded border border-jul-border px-1.5 text-xs text-jul-muted hover:bg-jul-bg disabled:opacity-30"
                >
                  ↑
                </button>
                <button
                  type="button"
                  aria-label={`Move row ${String(i + 1)} down`}
                  disabled={i === rows.length - 1}
                  onClick={() => {
                    move(i, 1);
                  }}
                  className="rounded border border-jul-border px-1.5 text-xs text-jul-muted hover:bg-jul-bg disabled:opacity-30"
                >
                  ↓
                </button>
              </div>
              <select
                aria-label={`Row ${String(i + 1)} operation`}
                value={row.op}
                onChange={(e) => {
                  updateRow(i, { op: e.target.value as ResponseHeaderRowOp });
                }}
                className="rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                {OPS.map((op) => (
                  <option key={op} value={op}>
                    {op}
                  </option>
                ))}
              </select>
              <input
                type="text"
                aria-label={`Row ${String(i + 1)} header name`}
                placeholder="X-Frame-Options"
                value={row.name}
                onChange={(e) => {
                  updateRow(i, { name: e.target.value });
                }}
                className="flex-1 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              {row.op !== "remove" && (
                <input
                  type="text"
                  aria-label={`Row ${String(i + 1)} value`}
                  placeholder="DENY"
                  value={row.value}
                  onChange={(e) => {
                    updateRow(i, { value: e.target.value });
                  }}
                  className="flex-1 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
                />
              )}
              <button
                type="button"
                aria-label={`Remove row ${String(i + 1)}`}
                onClick={() => {
                  removeRow(i);
                }}
                className="rounded-md border border-jul-border px-2 py-1.5 text-xs text-jul-danger hover:bg-jul-bg"
              >
                Remove
              </button>
            </div>
          ))}
          <button
            type="button"
            onClick={addRow}
            className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg"
          >
            + Add operation
          </button>
        </div>

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            {warnings.map((w, i) => (
              <p key={`rhw-${String(i)}`} className="text-xs text-jul-text">
                {w}
              </p>
            ))}
          </div>
        )}
      </div>
    </Drawer>
  );
}
