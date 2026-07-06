/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { StreamProjection, StreamDefPatch } from "@/api/client.ts";

// StreamDraft is the editable form state for one [[stream]] L4 listener. It
// mirrors StreamDefPatch but keeps the SNI route table as the raw multi-line
// "host = target" text the editor binds to, parsing it on save.
export interface StreamDraft {
  listen: string;
  protocol: "tcp" | "udp";
  proxyPass: string;
  sniRoutes: string; // "host = target" lines
  tlsPassthrough: boolean;
  proxyProtocol: "" | "in" | "out" | "both";
  connectTimeout: string;
  idleTimeout: string;
}

// emptyStreamDraft seeds the create form: a new tcp listener with no target yet.
export function emptyStreamDraft(): StreamDraft {
  return {
    listen: "",
    protocol: "tcp",
    proxyPass: "",
    sniRoutes: "",
    tlsPassthrough: false,
    proxyProtocol: "",
    connectTimeout: "",
    idleTimeout: "",
  };
}

// seedStreamDraft fills the edit form from a projected stream.
export function seedStreamDraft(s: StreamProjection): StreamDraft {
  return {
    listen: s.listen,
    protocol: s.protocol === "udp" ? "udp" : "tcp",
    proxyPass: s.proxy_pass ?? "",
    sniRoutes: formatSNIRoutes(s.sni_routes ?? {}),
    tlsPassthrough: s.tls_passthrough,
    proxyProtocol: normProxyProtocol(s.proxy_protocol),
    connectTimeout: s.connect_timeout ?? "",
    idleTimeout: s.idle_timeout ?? "",
  };
}

function normProxyProtocol(v: string | undefined): "" | "in" | "out" | "both" {
  return v === "in" || v === "out" || v === "both" ? v : "";
}

// formatSNIRoutes renders an SNI route map as sorted "host = target" lines.
export function formatSNIRoutes(map: Record<string, string>): string {
  return Object.keys(map)
    .sort()
    .map((host) => `${host} = ${map[host] ?? ""}`)
    .join("\n");
}

// parseSNIRoutes parses "host = target" lines into a record, ignoring blank
// lines and entries missing a host or target.
export function parseSNIRoutes(raw: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of raw.split("\n")) {
    const trimmed = line.trim();
    if (trimmed === "") continue;
    const eq = trimmed.indexOf("=");
    if (eq < 0) continue;
    const host = trimmed.slice(0, eq).trim();
    const target = trimmed.slice(eq + 1).trim();
    if (host !== "" && target !== "") out[host] = target;
  }
  return out;
}

// streamDraftToPatch builds the stream_add / stream_set payload from the draft,
// omitting empty optional fields so the request stays minimal
// (exactOptionalPropertyTypes: absent rather than undefined). The validated
// apply re-parse enforces the rest (target references, duplicate listeners, the
// TCP-only constraints).
export function streamDraftToPatch(draft: StreamDraft): StreamDefPatch {
  const patch: StreamDefPatch = { listen: draft.listen.trim(), protocol: draft.protocol };
  if (draft.proxyPass.trim() !== "") patch.proxy_pass = draft.proxyPass.trim();
  const sni = parseSNIRoutes(draft.sniRoutes);
  if (Object.keys(sni).length > 0) patch.sni_routes = sni;
  if (draft.tlsPassthrough) patch.tls_passthrough = true;
  if (draft.proxyProtocol !== "") patch.proxy_protocol = draft.proxyProtocol;
  if (draft.connectTimeout.trim() !== "") patch.connect_timeout = draft.connectTimeout.trim();
  if (draft.idleTimeout.trim() !== "") patch.idle_timeout = draft.idleTimeout.trim();
  return patch;
}

// streamDraftWarnings returns blocking validation messages shown before save,
// mirroring the backend's near-side checks so the operator gets feedback without
// a round-trip.
export function streamDraftWarnings(draft: StreamDraft): string[] {
  const out: string[] = [];
  if (draft.listen.trim() === "") {
    out.push("A listen address is required (e.g. 0.0.0.0:5432).");
  }
  const hasSNI = Object.keys(parseSNIRoutes(draft.sniRoutes)).length > 0;
  if (draft.proxyPass.trim() === "" && !hasSNI) {
    out.push("A default backend (proxy_pass) or at least one SNI route is required.");
  }
  if (draft.protocol === "udp") {
    if (hasSNI) out.push("SNI routes are only supported for TCP streams.");
    if (draft.tlsPassthrough) out.push("TLS passthrough is only supported for TCP streams.");
    if (draft.proxyProtocol !== "") {
      out.push("The PROXY protocol is only supported for TCP streams.");
    }
  }
  return out;
}

// streamSummary renders a one-line description of a projected stream for the
// list card.
export function streamSummary(s: StreamProjection): string {
  const target = s.proxy_pass?.trim();
  const sniCount = Object.keys(s.sni_routes ?? {}).length;
  const parts: string[] = [];
  if (target) parts.push(`→ ${target}`);
  if (sniCount > 0) parts.push(`${String(sniCount)} SNI route${sniCount === 1 ? "" : "s"}`);
  return parts.length > 0 ? parts.join(" · ") : "(no target)";
}
