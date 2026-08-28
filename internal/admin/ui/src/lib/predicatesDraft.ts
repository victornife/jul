/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { HeaderPredicatePatch, LocationPredicatesPatch, QueryPredicatePatch } from "@/api/client.ts";

export type HeaderPredicateOp = "present" | "exact" | "regex";
export type QueryPredicateOp = "present" | "exact";

export interface HeaderPredicateRow {
  name: string;
  op: HeaderPredicateOp;
  value: string;
}

export interface QueryPredicateRow {
  name: string;
  op: QueryPredicateOp;
  value: string;
}

export interface PredicatesDraft {
  /** Comma/space-separated method list, the same convention AuthEditor uses for allow/deny. */
  methods: string;
  headers: HeaderPredicateRow[];
  query: QueryPredicateRow[];
}

export function emptyPredicatesDraft(): PredicatesDraft {
  return { methods: "", headers: [], query: [] };
}

export function emptyHeaderRow(): HeaderPredicateRow {
  return { name: "", op: "present", value: "" };
}

export function emptyQueryRow(): QueryPredicateRow {
  return { name: "", op: "present", value: "" };
}

/** seedPredicatesDraft builds the editor's draft from the route projection's summary fields. */
export function seedPredicatesDraft(
  methods: string[] | undefined,
  headers: readonly { name: string; op: string; value?: string }[] | undefined,
  query: readonly { name: string; op: string; value?: string }[] | undefined,
): PredicatesDraft {
  return {
    methods: (methods ?? []).join(", "),
    headers: (headers ?? []).map((h) => ({
      name: h.name,
      op: h.op === "exact" || h.op === "regex" ? h.op : "present",
      value: h.value ?? "",
    })),
    query: (query ?? []).map((q) => ({
      name: q.name,
      op: q.op === "exact" ? "exact" : "present",
      value: q.value ?? "",
    })),
  };
}

function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

/**
 * predicatesWarnings mirrors the backend's own grammar (applyLocationPredicates)
 * so a save the API would reject is caught before the diff is even requested.
 */
export function predicatesWarnings(d: PredicatesDraft): string[] {
  const warnings: string[] = [];
  if (splitList(d.methods).length === 0 && d.headers.length === 0 && d.query.length === 0) {
    warnings.push(
      "At least one predicate (method, header, or query) is required — use Clear predicates to remove them all.",
    );
  }
  d.headers.forEach((h, i) => {
    if (h.name.trim() === "") warnings.push(`Header row ${String(i + 1)} needs a name.`);
    else if (h.op !== "present" && h.value.trim() === "")
      warnings.push(`Header "${h.name}" needs a value for "${h.op}".`);
  });
  d.query.forEach((q, i) => {
    if (q.name.trim() === "") warnings.push(`Query row ${String(i + 1)} needs a name.`);
    else if (q.op !== "present" && q.value.trim() === "")
      warnings.push(`Query parameter "${q.name}" needs a value for "${q.op}".`);
  });
  return warnings;
}

/** predicatesDraftToPatch always names all three facets — this editor shows and replaces the complete predicate state, the same "editor replaces what it displays" convention AuthEditor/LocationWAFEditor use. */
export function predicatesDraftToPatch(d: PredicatesDraft): LocationPredicatesPatch {
  const headers: HeaderPredicatePatch[] = d.headers.map((h) => ({
    name: h.name.trim(),
    op: h.op,
    ...(h.op === "present" ? {} : { value: h.value }),
  }));
  const query: QueryPredicatePatch[] = d.query.map((q) => ({
    name: q.name.trim(),
    op: q.op,
    ...(q.op === "present" ? {} : { value: q.value }),
  }));
  return { methods: splitList(d.methods), headers, query };
}
