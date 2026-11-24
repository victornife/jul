/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  captureAdminIdentity,
  fetchPendingRestart,
  fetchRawConfig,
  previewRawConfig,
} from "@/api/client.ts";
import {
  recommendPatchAction,
  setPendingDraft,
  snapshotPendingRestart,
} from "@/lib/configDraftHandoff.ts";
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
  const [reviewing, setReviewing] = useState(false);

  const fragment = generateTLSToml(draft);
  const warnings = tlsWarnings(draft);

  function set<K extends keyof TLSDraft>(key: K, value: TLSDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    const assertIdentity = captureAdminIdentity();
    const dns01 = draft.mode === "acme" && draft.acmeChallenge === "dns-01";
    setError(null);
    setReviewing(true);
    try {
      const raw = await fetchRawConfig();
      const candidate = appendFragment(raw.raw ?? "", fragment);
      if (dns01) {
        if (raw.raw === undefined || !raw.base_version?.trim()) {
          throw new Error(
            "The raw configuration and its base version are required for DNS-01 review.",
          );
        }
        const pending = snapshotPendingRestart(await fetchPendingRestart());
        const preview = await previewRawConfig(candidate, raw.base_version);
        if (preview.base_version !== raw.base_version) {
          throw new Error("The DNS-01 preview did not match the source base version.");
        }
        if (!preview.valid || preview.validation_errors.length > 0) {
          const details = preview.validation_errors
            .map((issue) => `${issue.path ? `${issue.path}: ` : ""}${issue.summary}`)
            .join("; ");
          throw new Error(details || "The DNS-01 candidate is invalid.");
        }
        const action = recommendPatchAction(preview.lifecycle, pending);
        if (action === "none") {
          throw new Error(
            "The server did not offer a safe apply action for this DNS-01 candidate.",
          );
        }
        assertIdentity();
        setPendingDraft({
          kind: "toml",
          toml: candidate,
          baseVersion: raw.base_version,
          previewDiff: preview.diff,
          lifecycle: preview.lifecycle,
          recommendedAction: action,
          pendingRestart: pending,
          candidateState: "memory_only",
        });
      } else {
        assertIdentity();
        setPendingDraft({ kind: "toml", toml: candidate });
      }
      void navigate("/config");
    } catch (failure) {
      setError(dns01 && failure instanceof Error
        ? failure.message
        : "Could not load the current configuration to merge this TLS server.");
    } finally {
      setReviewing(false);
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
            disabled={reviewing}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            {reviewing ? "Reviewing…" : "Review in editor →"}
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
                <option value="dns-01">dns-01 (RFC 2136; wildcard capable)</option>
              </select>
            </label>
            <TextField
              label="ACME domains (optional)"
              hint="Comma-separated. Defaults to host names. A wildcard covers one label, not the apex or deeper names."
              value={draft.acmeDomains}
              placeholder="example.com, www.example.com"
              onChange={(v) => {
                set("acmeDomains", v);
              }}
            />
            {draft.acmeChallenge === "dns-01" && (
              <div className="space-y-3 rounded-md border border-jul-border p-3">
                <p className="text-xs text-jul-muted">
                  Provider: RFC 2136. Configure an authoritative DNS server to permit TSIG-signed
                  updates and queries for the challenge zone. Every CA-facing observer must see
                  the challenge before submission. No inbound ACME listener is needed.
                  ACME, provider, name and credential changes require a restart. The server must use
                  an ACME-enabled build.
                </p>
                <p className="text-xs text-jul-muted">
                  If propagation fails, check observer reachability and replication. Cleanup warnings
                  are separate from certificate status; retain records while the CA may validate.
                </p>
                <p className="text-xs text-jul-muted">
                  Jul automatically selects ECDSA P-256 or RSA-2048 for each client. Each variant
                  needs its own issuance and renewal order.
                </p>
                <TextField
                  label="DNS server and port"
                  value={draft.dnsServer}
                  placeholder="dns.example.com:53"
                  onChange={(v) => {
                    set("dnsServer", v);
                  }}
                />
                <TextField
                  label="CA-facing DNS observers"
                  hint="Comma-separated authoritative host:port endpoints. For CNAME delegation, configure the explicit mapping in the raw editor."
                  value={draft.dnsObservers}
                  placeholder="ns1.example.com:53, ns2.example.com:53"
                  onChange={(v) => {
                    set("dnsObservers", v);
                  }}
                />
                <TextField
                  label="DNS update zone"
                  value={draft.dnsZone}
                  placeholder="example.com"
                  onChange={(v) => {
                    set("dnsZone", v);
                  }}
                />
                <TextField
                  label="TSIG key name"
                  value={draft.dnsTSIGKeyName}
                  placeholder="jul.example.com"
                  onChange={(v) => {
                    set("dnsTSIGKeyName", v);
                  }}
                />
                <TextField
                  label="TSIG secret reference"
                  value={draft.dnsTSIGSecretRef}
                  placeholder="${env:JUL_DNS_TSIG}"
                  hint="Use ${env:NAME} or ${file:/path}; do not enter the secret itself."
                  onChange={(v) => {
                    set("dnsTSIGSecretRef", v);
                  }}
                />
              </div>
            )}
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
          <div
            role="status"
            className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2"
          >
            {warnings.map((wn, i) => (
              <p key={`tw-${String(i)}`} className="text-xs text-jul-warning">
                {wn}
              </p>
            ))}
          </div>
        )}

        <div className="space-y-1">
          <p className="text-xs text-jul-muted">
            Draft block only: TLS floor {draft.minVersion || "1.2 (default)"}; this block does not
            enable HTTP/3; this block
            {draft.clientAuthMode === "require"
              ? " requires client certificates."
              : draft.clientAuthMode === "request"
                ? " requests and verifies optional client certificates."
                : " does not request client certificates."}
            {" "}On a shared listener the strongest TLS floor and client-auth mode apply to all
            names, client trust is combined, and HTTP/3 may already be enabled. Review the merged
            listener policy before applying.
          </p>
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
