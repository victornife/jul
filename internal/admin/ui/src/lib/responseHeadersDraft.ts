/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { ResponseHeaderOpPatch } from "@/api/client.ts";

export type ResponseHeaderRowOp = "add" | "set" | "remove";

export interface ResponseHeaderRow {
  op: ResponseHeaderRowOp;
  name: string;
  value: string;
}

export function emptyResponseHeaderRow(): ResponseHeaderRow {
  return { op: "set", name: "", value: "" };
}

/** seedResponseHeaderRows preserves declaration order — order is semantically meaningful (ADR 0018 §8: a "set" followed by two "add"s is the canonical multi-value form). */
export function seedResponseHeaderRows(
  ops: readonly { op: string; name: string; value?: string }[] | undefined,
): ResponseHeaderRow[] {
  return (ops ?? []).map((o) => ({
    op: o.op === "add" || o.op === "remove" ? o.op : "set",
    name: o.name,
    value: o.value ?? "",
  }));
}

/** responseHeaderWarnings mirrors buildResponseHeaderOps's own grammar. */
export function responseHeaderWarnings(rows: readonly ResponseHeaderRow[]): string[] {
  const warnings: string[] = [];
  if (rows.length === 0) {
    warnings.push("At least one operation is required — use Clear to remove them all.");
  }
  rows.forEach((r, i) => {
    if (r.name.trim() === "") warnings.push(`Row ${String(i + 1)} needs a header name.`);
    else if (r.op !== "remove" && r.value.trim() === "")
      warnings.push(`"${r.op} ${r.name}" needs a value.`);
  });
  return warnings;
}

export function responseHeaderRowsToPatch(rows: readonly ResponseHeaderRow[]): ResponseHeaderOpPatch[] {
  return rows.map((r) => ({
    op: r.op,
    name: r.name.trim(),
    ...(r.op === "remove" ? {} : { value: r.value }),
  }));
}
