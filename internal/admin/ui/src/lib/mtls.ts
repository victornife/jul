/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { MTLSServerProjection, ClientAuthPatch } from "@/api/client.ts";

export type MTLSMode = "none" | "request" | "require";

// MTLSDraft is the editable form state for a server block's mutual-TLS (client
// certificate) settings. It mirrors ClientAuthPatch but keeps the SAN allow-list
// as the raw multi-line text the editor binds to, parsing it on save.
export interface MTLSDraft {
  mode: MTLSMode;
  caFile: string;
  crlFile: string;
  verifySAN: string; // one SAN per line
}

// emptyMTLSDraft seeds the editor for a server with mutual TLS disabled.
export function emptyMTLSDraft(): MTLSDraft {
  return { mode: "none", caFile: "", crlFile: "", verifySAN: "" };
}

// seedMTLSDraft fills the edit form from a projected server's mTLS posture.
export function seedMTLSDraft(s: MTLSServerProjection): MTLSDraft {
  return {
    mode: normMode(s.mode),
    caFile: s.ca_file ?? "",
    crlFile: s.crl_file ?? "",
    verifySAN: formatSANList(s.verify_san ?? []),
  };
}

function normMode(v: string): MTLSMode {
  return v === "request" || v === "require" ? v : "none";
}

// formatSANList renders a SAN allow-list as one entry per line.
export function formatSANList(list: string[]): string {
  return list.join("\n");
}

// parseSANList parses the multi-line SAN textarea into a trimmed list, ignoring
// blank lines.
export function parseSANList(raw: string): string[] {
  const out: string[] = [];
  for (const line of raw.split("\n")) {
    const t = line.trim();
    if (t !== "") out.push(t);
  }
  return out;
}

// mtlsDraftToPatch builds the server_set_client_auth payload from the draft.
// A "none" mode disables mutual TLS (the backend clears the block); otherwise it
// omits empty optional fields so the request stays minimal
// (exactOptionalPropertyTypes: absent rather than undefined). The validated
// apply re-parse enforces that ca_file/crl_file are readable and that
// request/require carry a ca_file.
export function mtlsDraftToPatch(draft: MTLSDraft): ClientAuthPatch {
  if (draft.mode === "none") {
    return { mode: "none" };
  }
  const patch: ClientAuthPatch = { mode: draft.mode };
  if (draft.caFile.trim() !== "") patch.ca_file = draft.caFile.trim();
  if (draft.crlFile.trim() !== "") patch.crl_file = draft.crlFile.trim();
  const san = parseSANList(draft.verifySAN);
  if (san.length > 0) patch.verify_san = san;
  return patch;
}

// mtlsDraftWarnings returns blocking validation messages shown before save,
// mirroring the backend's near-side checks so the operator gets feedback without
// a round-trip.
export function mtlsDraftWarnings(draft: MTLSDraft): string[] {
  const out: string[] = [];
  if (draft.mode !== "none" && draft.caFile.trim() === "") {
    out.push("A CA bundle (ca_file) is required to verify client certificates.");
  }
  if (draft.mode === "none" && parseSANList(draft.verifySAN).length > 0) {
    out.push(
      "The SAN allow-list only applies when mutual TLS is enabled (mode request or require).",
    );
  }
  return out;
}

// mtlsServerSummary renders a one-line description of a server's mTLS posture
// for the list card.
export function mtlsServerSummary(s: MTLSServerProjection): string {
  const mode = normMode(s.mode);
  if (mode === "none") return "mutual TLS off";
  const parts: string[] = [mode];
  const sanCount = (s.verify_san ?? []).length;
  if (sanCount > 0) parts.push(`${String(sanCount)} SAN${sanCount === 1 ? "" : "s"}`);
  const requiring = s.locations.filter((l) => l.require_client_cert).length;
  if (requiring > 0) {
    parts.push(`${String(requiring)} route${requiring === 1 ? "" : "s"} require a cert`);
  }
  return parts.join(" · ");
}
