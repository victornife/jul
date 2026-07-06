/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchRawConfig } from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { appendFragment } from "@/lib/routeToml.ts";
import {
  emptyTLSDraft,
  generateTLSToml,
  tlsWarnings,
  type ACMEChallenge,
  type ACMEEnvironment,
  type ClientAuthMode,
  type TLSDraft,
  type TLSMinVersion,
  type TLSMode,
  type TLSRouteAction,
} from "@/lib/tlsToml.ts";

function TextField({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
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

export interface TLSEditorProps {
  readonly onClose: () => void;
}

/**
 * Guided TLS/ACME editor (Wave A). It generates a complete TLS-enabled
 * [[servers]] block and hands it to the Config editor, where it flows through
 * Validate → Diff → Apply → Rollback. It never writes directly. The
 * static-vs-ACME choice and the ACME staging-vs-production environment are
 * explicit so the most dangerous TLS decisions are deliberate.
 */
export function TLSEditor({ onClose }: TLSEditorProps) {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<TLSDraft>(emptyTLSDraft());
  const [error, setError] = useState<string | null>(null);

  const fragment = generateTLSToml(draft);
  const warnings = tlsWarnings(draft);

  function set<K extends keyof TLSDraft>(key: K, value: TLSDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    try {
      const raw = await fetchRawConfig();
      setPendingDraft({ kind: "toml", toml: appendFragment(raw.raw ?? "", fragment) });
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this TLS server.");
    }
  }

  return (
    <Drawer
      title="New TLS server"
      subtitle="Generate a TLS-enabled server block, then review and apply it safely in the editor."
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          TLS secures a listener with a certificate. Choose automatic certificates (ACME /
          Let&apos;s Encrypt) or supply your own cert and key. Nothing is applied until you review
          the diff and confirm in the editor.
        </p>

        <TextField
          label="Listener"
          hint="The HTTPS address this server block binds to."
          value={draft.listen}
          placeholder=":443"
          onChange={(v) => {
            set("listen", v);
          }}
        />
        <TextField
          label="Host names"
          hint="Comma-separated. Required for ACME (the certificate is issued for these names)."
          value={draft.serverNames}
          placeholder="example.com, www.example.com"
          onChange={(v) => {
            set("serverNames", v);
          }}
        />

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Certificate source</span>
          <select
            value={draft.mode}
            onChange={(e) => {
              set("mode", e.target.value as TLSMode);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="acme">Automatic (ACME / Let&apos;s Encrypt)</option>
            <option value="static">Static certificate &amp; key files</option>
          </select>
        </label>

        {draft.mode === "acme" ? (
          <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
              ACME
            </span>
            <TextField
              label="Account email"
              hint="Let's Encrypt sends expiry and policy notices here."
              value={draft.acmeEmail}
              placeholder="ops@example.com"
              onChange={(v) => {
                set("acmeEmail", v);
              }}
            />
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Environment</span>
              <select
                value={draft.acmeEnvironment}
                onChange={(e) => {
                  set("acmeEnvironment", e.target.value as ACMEEnvironment);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="staging">Staging (recommended first — no rate limits)</option>
                <option value="production">Production (trusted certs, strict rate limits)</option>
              </select>
              <span className="text-xs text-jul-muted">
                Verify issuance against staging, then switch to production. Staging certs are not
                browser-trusted.
              </span>
            </label>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Challenge</span>
              <select
                value={draft.acmeChallenge}
                onChange={(e) => {
                  set("acmeChallenge", e.target.value as ACMEChallenge);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="http-01">http-01 (port 80 must be reachable)</option>
                <option value="tls-alpn-01">tls-alpn-01 (port 443 only)</option>
              </select>
            </label>
            <TextField
              label="ACME domains (optional)"
              hint="Comma-separated. Defaults to the host names above."
              value={draft.acmeDomains}
              placeholder="example.com, www.example.com"
              onChange={(v) => {
                set("acmeDomains", v);
              }}
            />
          </div>
        ) : (
          <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
              Static certificate
            </span>
            <TextField
              label="Certificate file"
              hint="PEM certificate chain path on the server."
              value={draft.certFile}
              placeholder="/etc/jul/tls/cert.pem"
              onChange={(v) => {
                set("certFile", v);
              }}
            />
            <TextField
              label="Private-key file"
              hint="PEM private-key path on the server."
              value={draft.keyFile}
              placeholder="/etc/jul/tls/key.pem"
              onChange={(v) => {
                set("keyFile", v);
              }}
            />
          </div>
        )}

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Minimum TLS version</span>
          <select
            value={draft.minVersion}
            onChange={(e) => {
              set("minVersion", e.target.value as TLSMinVersion);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            <option value="">Server default</option>
            <option value="1.2">TLS 1.2</option>
            <option value="1.3">TLS 1.3 (most secure)</option>
          </select>
        </label>

        <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Mutual TLS (client certificates)
          </span>
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Client-certificate mode</span>
            <select
              value={draft.clientAuthMode}
              onChange={(e) => {
                set("clientAuthMode", e.target.value as ClientAuthMode);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="none">Off (no client certificates)</option>
              <option value="request">Request (verify if presented, allow anonymous)</option>
              <option value="require">Require (reject without a verified client cert)</option>
            </select>
          </label>
          {draft.clientAuthMode !== "none" && (
            <>
              <TextField
                label="Client CA bundle"
                hint="PEM bundle of the CA(s) that sign accepted client certificates."
                value={draft.clientCAFile}
                placeholder="/etc/jul/clients-ca.pem"
                onChange={(v) => {
                  set("clientCAFile", v);
                }}
              />
              <TextField
                label="CRL file (optional)"
                hint="PEM/DER certificate revocation list to reject revoked client certs."
                value={draft.clientCRLFile}
                placeholder="/etc/jul/clients.crl"
                onChange={(v) => {
                  set("clientCRLFile", v);
                }}
              />
              <TextField
                label="Allowed client SANs (optional)"
                hint="Comma-separated. Restrict to client certs whose SAN matches one of these."
                value={draft.clientVerifySAN}
                placeholder="svc-a.internal, svc-b.internal"
                onChange={(v) => {
                  set("clientVerifySAN", v);
                }}
              />
            </>
          )}
        </div>

        <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-3">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            What this server serves
          </span>
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Action</span>
            <select
              value={draft.action}
              onChange={(e) => {
                set("action", e.target.value as TLSRouteAction);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="proxy">Reverse proxy</option>
              <option value="static">Serve static files</option>
            </select>
          </label>
          <TextField
            label="Path"
            value={draft.path}
            placeholder="/"
            onChange={(v) => {
              set("path", v);
            }}
          />
          <TextField
            label={draft.action === "static" ? "Root directory" : "Upstream target"}
            value={draft.target}
            placeholder={draft.action === "static" ? "/var/www/site" : "http://app"}
            onChange={(v) => {
              set("target", v);
            }}
          />
          <label className="flex items-center gap-2 text-sm text-jul-text">
            <input
              type="checkbox"
              checked={draft.requireClientCert}
              onChange={(e) => {
                set("requireClientCert", e.target.checked);
              }}
              className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
            />
            Require a verified client certificate on this location
          </label>
        </div>

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2">
            {warnings.map((wn, i) => (
              <p key={`tw-${String(i)}`} className="text-xs text-jul-warning">
                {wn}
              </p>
            ))}
          </div>
        )}

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Generated TOML
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {fragment}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}
