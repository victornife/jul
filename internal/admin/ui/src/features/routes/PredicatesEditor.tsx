/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { type RouteTarget } from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  emptyHeaderRow,
  emptyQueryRow,
  predicatesDraftToPatch,
  predicatesWarnings,
  seedPredicatesDraft,
  type HeaderPredicateOp,
  type HeaderPredicateRow,
  type PredicatesDraft,
  type QueryPredicateOp,
  type QueryPredicateRow,
} from "@/lib/predicatesDraft.ts";

export interface PredicatesEditorProps {
  /** The location whose match predicates are being edited. */
  readonly target: RouteTarget;
  readonly seedMethods?: readonly string[] | undefined;
  readonly seedHeaders?: readonly { name: string; op: string; value?: string }[] | undefined;
  readonly seedQuery?: readonly { name: string; op: string; value?: string }[] | undefined;
  /** Whether the location already has predicates. */
  readonly existing?: boolean;
  readonly onClose: () => void;
}

const HEADER_OPS: HeaderPredicateOp[] = ["present", "exact", "regex"];
const QUERY_OPS: QueryPredicateOp[] = ["present", "exact"];

/**
 * Guided editor for a single location's method/header/query match predicates
 * (ADR 0018 §1–§7, #147). A list inside one field is an OR-set; the three
 * facets are ANDed. It routes the change through location_set_predicates /
 * location_clear_predicates, replacing the location's whole predicate set
 * wholesale — the same convention every other per-location editor here uses —
 * so it always names all three facets rather than sending partial updates,
 * even though the API itself supports touching one facet at a time.
 */
export function PredicatesEditor({
  target,
  seedMethods,
  seedHeaders,
  seedQuery,
  existing = Boolean(
    (seedMethods && seedMethods.length > 0) ||
      (seedHeaders && seedHeaders.length > 0) ||
      (seedQuery && seedQuery.length > 0),
  ),
  onClose,
}: PredicatesEditorProps) {
  const [draft, setDraft] = useState<PredicatesDraft>(() =>
    seedPredicatesDraft(seedMethods as string[] | undefined, seedHeaders, seedQuery),
  );
  const { run: runPatch, error, busy } = useRunPatch();
  const { has } = usePermission();
  const canWrite = has("config:write");

  const headersContainerRef = useRef<HTMLDivElement>(null);
  const addHeaderButtonRef = useRef<HTMLButtonElement>(null);
  const focusNewHeaderRef = useRef(false);
  const queryContainerRef = useRef<HTMLDivElement>(null);
  const addQueryButtonRef = useRef<HTMLButtonElement>(null);
  const focusNewQueryRef = useRef(false);

  // Focus management (#147 §9): a new row's name field gets focus once it
  // mounts, and removing a row returns focus to its list's "+ Add" button
  // rather than letting it silently fall back to the document body.
  useEffect(() => {
    if (!focusNewHeaderRef.current) return;
    focusNewHeaderRef.current = false;
    const inputs = headersContainerRef.current?.querySelectorAll<HTMLInputElement>(
      'input[aria-label$="name"]',
    );
    inputs?.[inputs.length - 1]?.focus();
  }, [draft.headers.length]);

  useEffect(() => {
    if (!focusNewQueryRef.current) return;
    focusNewQueryRef.current = false;
    const inputs = queryContainerRef.current?.querySelectorAll<HTMLInputElement>(
      'input[aria-label$="name"]',
    );
    inputs?.[inputs.length - 1]?.focus();
  }, [draft.query.length]);

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;
  const warnings = predicatesWarnings(draft);

  function updateHeader(i: number, patch: Partial<HeaderPredicateRow>): void {
    setDraft((d) => ({ ...d, headers: d.headers.map((h, j) => (j === i ? { ...h, ...patch } : h)) }));
  }
  function updateQuery(i: number, patch: Partial<QueryPredicateRow>): void {
    setDraft((d) => ({ ...d, query: d.query.map((q, j) => (j === i ? { ...q, ...patch } : q)) }));
  }

  function save(): void {
    runPatch({ op: "location_set_predicates", ...target, predicates: predicatesDraftToPatch(draft) });
  }

  function clearPredicates(): void {
    runPatch({ op: "location_clear_predicates", ...target });
  }

  return (
    <Drawer
      title={existing ? "Edit match predicates" : "Add match predicates"}
      subtitle={`Predicates for ${where}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && (
              <span role="alert" className="text-xs text-jul-danger">
                {error}
              </span>
            )}
            {existing && (
              <button
                type="button"
                disabled={busy || !canWrite}
                onClick={clearPredicates}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
              >
                Clear predicates
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
          This <strong>replaces</strong> the whole predicate set for <span className="font-mono">{where}</span>{" "}
          wholesale. A list within one facet is an OR-set; the three facets below are ANDed together.
          Path specificity always outranks predicates.
        </p>

        {existing && (
          <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
            Existing header/query predicate names, operators and values are not read back from the
            API (they may be operator-sensitive) — this route&apos;s current ones are not shown
            below. Saving replaces the whole predicate set, so re-add any header or query predicate
            you want to keep before continuing.
          </p>
        )}

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Methods</span>
          <input
            type="text"
            value={draft.methods}
            placeholder="GET, POST"
            onChange={(e) => {
              setDraft((d) => ({ ...d, methods: e.target.value }));
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
          />
          <span className="text-xs text-jul-muted">
            Comma/space separated. A route listing GET also matches HEAD. Blank means no method
            constraint.
          </span>
        </label>

        <div className="space-y-2">
          <span className="text-sm font-medium text-jul-text">Header predicates</span>
          <div ref={headersContainerRef} className="space-y-2">
            {draft.headers.map((row, i) => (
              <div
                key={`hp-${String(i)}`}
                className="flex items-start gap-2 rounded-md border border-jul-border bg-jul-surface p-2"
              >
                <input
                  type="text"
                  aria-label={`Header row ${String(i + 1)} name`}
                  placeholder="X-Api-Version"
                  value={row.name}
                  onChange={(e) => {
                    updateHeader(i, { name: e.target.value });
                  }}
                  className="flex-1 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
                />
                <select
                  aria-label={`Header row ${String(i + 1)} operator`}
                  value={row.op}
                  onChange={(e) => {
                    updateHeader(i, { op: e.target.value as HeaderPredicateOp });
                  }}
                  className="rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                >
                  {HEADER_OPS.map((op) => (
                    <option key={op} value={op}>
                      {op}
                    </option>
                  ))}
                </select>
                {row.op !== "present" && (
                  <input
                    type="text"
                    aria-label={`Header row ${String(i + 1)} value`}
                    placeholder={row.op === "regex" ? "^v[0-9]+$" : "2"}
                    value={row.value}
                    onChange={(e) => {
                      updateHeader(i, { value: e.target.value });
                    }}
                    className="flex-1 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  />
                )}
                <button
                  type="button"
                  aria-label={`Remove header row ${String(i + 1)}`}
                  onClick={() => {
                    setDraft((d) => ({ ...d, headers: d.headers.filter((_, j) => j !== i) }));
                    addHeaderButtonRef.current?.focus();
                  }}
                  className="rounded-md border border-jul-border px-2 py-1.5 text-xs text-jul-danger hover:bg-jul-bg"
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
          <button
            type="button"
            ref={addHeaderButtonRef}
            onClick={() => {
              focusNewHeaderRef.current = true;
              setDraft((d) => ({ ...d, headers: [...d.headers, emptyHeaderRow()] }));
            }}
            className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg"
          >
            + Add header predicate
          </button>
        </div>

        <div className="space-y-2">
          <span className="text-sm font-medium text-jul-text">Query predicates</span>
          <div ref={queryContainerRef} className="space-y-2">
            {draft.query.map((row, i) => (
              <div
                key={`qp-${String(i)}`}
                className="flex items-start gap-2 rounded-md border border-jul-border bg-jul-surface p-2"
              >
                <input
                  type="text"
                  aria-label={`Query row ${String(i + 1)} name`}
                  placeholder="debug"
                  value={row.name}
                  onChange={(e) => {
                    updateQuery(i, { name: e.target.value });
                  }}
                  className="flex-1 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
                />
                <select
                  aria-label={`Query row ${String(i + 1)} operator`}
                  value={row.op}
                  onChange={(e) => {
                    updateQuery(i, { op: e.target.value as QueryPredicateOp });
                  }}
                  className="rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                >
                  {QUERY_OPS.map((op) => (
                    <option key={op} value={op}>
                      {op}
                    </option>
                  ))}
                </select>
                {row.op !== "present" && (
                  <input
                    type="text"
                    aria-label={`Query row ${String(i + 1)} value`}
                    placeholder="true"
                    value={row.value}
                    onChange={(e) => {
                      updateQuery(i, { value: e.target.value });
                    }}
                    className="flex-1 rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  />
                )}
                <button
                  type="button"
                  aria-label={`Remove query row ${String(i + 1)}`}
                  onClick={() => {
                    setDraft((d) => ({ ...d, query: d.query.filter((_, j) => j !== i) }));
                    addQueryButtonRef.current?.focus();
                  }}
                  className="rounded-md border border-jul-border px-2 py-1.5 text-xs text-jul-danger hover:bg-jul-bg"
                >
                  Remove
                </button>
              </div>
            ))}
          </div>
          <button
            type="button"
            ref={addQueryButtonRef}
            onClick={() => {
              focusNewQueryRef.current = true;
              setDraft((d) => ({ ...d, query: [...d.query, emptyQueryRow()] }));
            }}
            className="rounded-md border border-jul-border px-3 py-1.5 text-xs text-jul-text hover:bg-jul-bg"
          >
            + Add query predicate
          </button>
        </div>

        {warnings.length > 0 && (
          <div
            role="alert"
            className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3"
          >
            {warnings.map((w, i) => (
              <p key={`pw-${String(i)}`} className="text-xs text-jul-text">
                {w}
              </p>
            ))}
          </div>
        )}
      </div>
    </Drawer>
  );
}
