/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import { type MTLSServerProjection } from "@/api/client.ts";
import {
  seedMTLSDraft,
  mtlsDraftToPatch,
  mtlsDraftWarnings,
  type MTLSDraft,
  type MTLSMode,
} from "@/lib/mtls.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";

function TextField({
  label,
  value,
  placeholder,
  hint,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly hint?: string;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function TextArea({
  label,
  value,
  placeholder,
  hint,
  rows,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly hint?: string;
  readonly rows?: number;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <textarea
        value={value}
        placeholder={placeholder}
        rows={rows ?? 3}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function Warnings({ items }: { readonly items: string[] }) {
  if (items.length === 0) return null;
  return (
    <div className="space-y-1 rounded-md border border-jul-danger/40 bg-jul-danger/10 p-3">
      {items.map((wn, i) => (
        <p key={`w-${String(i)}`} className="text-xs text-jul-text">
          {wn}
        </p>
      ))}
    </div>
  );
}

// MTLSEditor edits one TLS-enabled server block's mutual-TLS (client certificate)
// settings: the verification mode, the CA bundle and optional CRL the presented
// certificates are checked against, and an optional SAN allow-list. These are
// read when the listener binds, so the editor surfaces a persistent restart
// caveat; the edit still routes through Validate → Diff → Apply like every other
// guided editor.
export function MTLSEditor({
  server,
  onClose,
}: {
  readonly server: MTLSServerProjection;
  readonly onClose: () => void;
}) {
  const { error, busy, run } = useRunPatch();
  const [draft, setDraft] = useState<MTLSDraft>(() => seedMTLSDraft(server));
  const warnings = mtlsDraftWarnings(draft);
  const enabled = draft.mode !== "none";
  const subtitle =
    server.server_names && server.server_names.length > 0
      ? server.server_names.join(", ")
      : server.listen;

  function set<K extends keyof MTLSDraft>(key: K, val: MTLSDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    run({
      op: "server_set_client_auth",
      listen: server.listen,
      server_names: server.server_names ?? [],
      client_auth: mtlsDraftToPatch(draft),
    });
  }

  return (
    <Drawer
      title="Edit mutual TLS"
      subtitle={subtitle}
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            disabled={busy || warnings.length > 0}
            onClick={save}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-4">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          Mutual TLS asks each client to present a certificate, verified against a CA bundle you
          trust. Mode <span className="font-mono">request</span> verifies a certificate when offered
          but still admits clients without one; <span className="font-mono">require</span> rejects
          connections that do not present a valid certificate.
        </p>

        <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          <span className="font-medium">Takes effect on restart.</span> Server-level mutual TLS is
          read when the listener binds. Saving reloads HTTP routing immediately, but the new
          client-certificate settings apply only after you restart Jul (or change the listen
          address). Per-route “require client certificate” toggles, by contrast, take effect
          immediately.
        </div>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Mode</span>
          <select
            value={draft.mode}
            onChange={(e) => {
              const v = e.target.value;
              set("mode", v === "request" || v === "require" ? (v as MTLSMode) : "none");
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="none">Off — no client certificate</option>
            <option value="request">Request — verify if offered</option>
            <option value="require">Require — reject without a valid cert</option>
          </select>
        </label>

        {enabled && (
          <>
            <TextField
              label="CA bundle (ca_file)"
              value={draft.caFile}
              placeholder="/etc/jul/clients-ca.pem"
              hint="PEM file of the certificate authorities that may issue client certificates. Required."
              onChange={(v) => {
                set("caFile", v);
              }}
            />
            <TextField
              label="Revocation list (crl_file)"
              value={draft.crlFile}
              placeholder="/etc/jul/clients.crl  (optional)"
              hint="Optional PEM/DER CRL. Certificates listed here are rejected."
              onChange={(v) => {
                set("crlFile", v);
              }}
            />
            <TextArea
              label="SAN allow-list (verify_san)"
              value={draft.verifySAN}
              placeholder={"svc-a.internal\nsvc-b.internal"}
              rows={3}
              hint="One SAN per line. When set, only certificates whose Subject Alternative Names match an entry are accepted. Leave empty to accept any cert the CA signed."
              onChange={(v) => {
                set("verifySAN", v);
              }}
            />
          </>
        )}

        <Warnings items={warnings} />
      </div>
    </Drawer>
  );
}
