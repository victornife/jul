/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchSecurity,
  fetchListeners,
  type SecurityProjection,
  type LocationWAF,
  type RBACPosture,
  type EgressProjection,
  type ListenerClientAddress,
} from "@/api/client.ts";
import { ClientAddressEditor } from "@/features/security/ClientAddressEditor.tsx";
import { WAFEditor } from "@/features/security/WAFEditor.tsx";
import { LocationWAFEditor } from "@/features/security/LocationWAFEditor.tsx";
import { SecretHelper } from "@/features/security/SecretHelper.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading, MaturityBadge } from "@/components/ui.tsx";

// wafIsMixed reports whether protected locations run a mix of block and detect
// modes, in which case a single mode badge would mislead.
function wafIsMixed(d: SecurityProjection): boolean {
  return d.waf_block_locs > 0 && d.waf_detect_locs > 0;
}

// locationWafLabel renders a per-location override compactly: which route it
// targets and the effective mode/CRS, so the operator sees that the route runs
// its own policy rather than the global one.
function locationWafLabel(w: LocationWAF): string {
  const where = `${w.listen}${w.path ? ` ${w.path}` : ""}`;
  const bits = [w.enabled ? (w.mode ?? "block") : "disabled"];
  if (w.crs_enabled) bits.push("CRS");
  return `${where} — ${bits.join(", ")}`;
}

// wafCoverageSummary describes how many locations the WAF protects and, when
// the modes differ or the CRS is partially applied, the exact split — so the
// panel reports the real posture rather than implying one uniform policy.
function wafCoverageSummary(d: SecurityProjection): string {
  const locs = `${String(d.waf_locations)} location${d.waf_locations === 1 ? "" : "s"}`;
  const parts: string[] = [];
  if (wafIsMixed(d)) {
    parts.push(`${String(d.waf_block_locs)} block, ${String(d.waf_detect_locs)} detect`);
  }
  const crs = d.waf_crs_locs;
  if (crs > 0 && crs < d.waf_locations) {
    parts.push(`CRS on ${String(crs)}`);
  } else if (crs > 0) {
    parts.push("CRS");
  }
  return parts.length > 0 ? `${locs} (${parts.join("; ")})` : locs;
}

function Row({ label, children }: { readonly label: string; readonly children: React.ReactNode }) {
  return (
    <div className="flex items-start gap-4 border-b border-jul-border px-4 py-3 last:border-b-0">
      <span className="w-40 shrink-0 text-xs text-jul-muted">{label}</span>
      <span className="text-sm text-jul-text">{children}</span>
    </div>
  );
}

function OnOff({ on }: { readonly on: boolean }) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs font-medium ${
        on ? "bg-jul-success/15 text-jul-success" : "bg-jul-border text-jul-muted"
      }`}
    >
      {on ? "enabled" : "disabled"}
    </span>
  );
}

// RBACStatusCell renders the secret-free admin RBAC posture (P3-03 §35): whether
// named-principal RBAC is active, the principal/custom-role counts, and whether a
// legacy shared token is still accepted. It shows the SERVING (installed) state
// as active and warns when a staged persisted change is not yet live (N-03). It
// exposes no principal names, tokens, or hashes.
function RBACStatusCell({ posture }: { readonly posture: RBACPosture | undefined }) {
  if (!posture) {
    return <span className="text-jul-muted text-xs">unknown</span>;
  }
  const serving = posture.serving;
  return (
    <span className="flex w-full flex-col gap-1">
      {serving.enabled ? (
        <>
          <span className="flex items-center gap-2">
            <OnOff on={true} />
            <span className="text-jul-muted text-xs">
              {serving.principal_count} principal{serving.principal_count === 1 ? "" : "s"},{" "}
              {serving.role_count} custom role{serving.role_count === 1 ? "" : "s"}
            </span>
          </span>
          {serving.legacy_token_active && (
            <span className="text-jul-warning text-xs">
              A legacy shared admin token is still active alongside RBAC. Remove{" "}
              <span className="font-mono">admin.token</span> to fully migrate to named principals.
            </span>
          )}
          <span className="flex items-start gap-2 text-jul-muted text-xs">
            <MaturityBadge level="preview" />
            <span>
              Interactive token management — creating and revoking scoped tokens from the
              Console — is planned. Principals, roles, and tokens are defined in{" "}
              <span className="font-mono">[admin.rbac]</span> in the configuration today.
            </span>
          </span>
        </>
      ) : (
        <span className="flex flex-col gap-0.5">
          <OnOff on={false} />
          <span className="text-jul-muted text-xs">
            Using the legacy shared token or open (loopback) access; named principals are off.
          </span>
        </span>
      )}
      {posture.pending && (
        <span className="text-jul-warning text-xs">
          A staged configuration change is not yet serving. The values above are what the admin API
          enforces now; the persisted configuration (
          {posture.persisted.enabled
            ? `RBAC with ${String(posture.persisted.principal_count)} principal${posture.persisted.principal_count === 1 ? "" : "s"}, ${String(posture.persisted.role_count)} custom role${posture.persisted.role_count === 1 ? "" : "s"}`
            : "the legacy shared token or open access"}
          ) takes effect after the next restart.
        </span>
      )}
    </span>
  );
}

// EgressCell renders the outbound egress allow-list posture (P4-01): whether the
// allow-list is enabled, the number of allow rules, and a bounded breakdown of
// what it has recently refused by subsystem and reason. It never names a
// destination host or IP.
function EgressCell({ egress }: { readonly egress: EgressProjection | undefined }) {
  if (!egress) {
    return <span className="text-jul-muted text-xs">unknown</span>;
  }
  if (!egress.enabled) {
    return (
      <span className="flex flex-col gap-0.5">
        <OnOff on={false} />
        <span className="text-jul-muted text-xs">
          No outbound-destination restriction; the server&apos;s config-driven fetches may reach any
          host.
        </span>
        <EgressDocsLink />
      </span>
    );
  }
  const blocked = egress.recent_blocked ?? [];
  return (
    <span className="flex w-full flex-col gap-1">
      <span className="flex items-center gap-2">
        <OnOff on={true} />
        <span className="text-jul-muted text-xs">
          {egress.allow_rule_count} allow rule{egress.allow_rule_count === 1 ? "" : "s"}
        </span>
      </span>
      {blocked.length > 0 ? (
        <span className="flex flex-col gap-0.5">
          {blocked.map((b, i) => (
            <span key={`egblk-${String(i)}`} className="text-jul-muted text-xs">
              Blocked {b.count} in <span className="font-mono">{b.subsystem}</span> ({b.reason})
            </span>
          ))}
        </span>
      ) : (
        <span className="text-jul-muted text-xs">No blocked destinations recorded.</span>
      )}
      <EgressDocsLink />
    </span>
  );
}

// EgressDocsLink points the operator at the canonical egress documentation
// (trust semantics, redirects, DNS, proxy, and ACME/plugin behavior). It is the
// Console's documentation pointer for the startup-bound [egress] policy.
function EgressDocsLink() {
  return (
    <a
      href="https://github.com/victornife/jul/blob/main/docs/egress.md"
      target="_blank"
      rel="noreferrer"
      className="text-jul-accent text-xs hover:underline"
    >
      Egress documentation
    </a>
  );
}

// clientAddressSummary describes one listener's trusted-proxy posture in
// bounded terms: how many ranges it trusts and which header preference applies.
// The ranges themselves are configuration the operator wrote, so showing them is
// safe; nothing request-derived ever appears here.
function clientAddressSummary(l: ListenerClientAddress): string {
  if (!l.configured || l.trusted_proxies.length === 0) return "no proxy trusted";
  const ranges = `${String(l.trusted_proxies.length)} range${l.trusted_proxies.length === 1 ? "" : "s"}`;
  const headers = l.headers_disabled ? "headers off" : l.forwarded_headers.join(", ");
  return `${ranges} · ${headers} · max ${String(l.max_hops)} hops`;
}

export function SecurityPanel() {
  const [editingWAF, setEditingWAF] = useState(false);
  const [editingClientAddress, setEditingClientAddress] = useState<ListenerClientAddress | null>(
    null,
  );
  const [editingLocationWAF, setEditingLocationWAF] = useState<LocationWAF | null>(null);
  const [externalizing, setExternalizing] = useState(false);
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["security"],
    queryFn: fetchSecurity,
  });
  const listeners = useQuery({ queryKey: ["listeners"], queryFn: fetchListeners });

  if (isLoading) return <Loading label="Loading security…" />;
  if (isError || !data)
    return <PanelError error={error} resource="security info" onRetry={() => void refetch()} />;

  const locationWafs = data.location_wafs ?? [];

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Security</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Authentication, mutual TLS, WAF, and outbound egress posture across all listeners and
          locations. Verify which routes are protected, how requests are inspected before they reach
          backends, and what outbound destinations the server may reach.
        </p>
      </div>
      {!data.waf_compiled && (
        <div className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
          This build does not include the web application firewall (the <code>waf</code> tag). You
          can edit WAF policy here, but applying a config that enables the WAF will be rejected by
          the apply preflight until you run a WAF-enabled binary.
        </div>
      )}
      <div className="rounded-lg border border-jul-border bg-jul-surface">
        <Row label="Authentication">
          <OnOff on={data.auth_enabled} />
        </Row>
        <Row label="Access control (RBAC)">
          <RBACStatusCell posture={data.rbac} />
        </Row>
        <Row label="Mutual TLS">
          {data.client_auth ? (
            <span className="rounded-full bg-jul-accent/15 px-2 py-0.5 text-xs text-jul-accent">
              {data.client_auth}
            </span>
          ) : (
            <span className="text-jul-muted text-xs">none</span>
          )}
        </Row>
        <Row label="Require client cert">
          {data.require_cert_count > 0 ? (
            <span className="text-jul-warning text-sm">
              {data.require_cert_count} location{data.require_cert_count > 1 ? "s" : ""} require
              cert
            </span>
          ) : (
            <span className="text-jul-muted text-xs">no locations</span>
          )}
        </Row>
        <Row label="Web app firewall">
          <span className="flex w-full items-center gap-2">
            {data.waf_enabled ? (
              <>
                <span
                  className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                    wafIsMixed(data) || data.waf_mode === "detect"
                      ? "bg-jul-warning/15 text-jul-warning"
                      : "bg-jul-success/15 text-jul-success"
                  }`}
                >
                  {wafIsMixed(data) ? "mixed" : (data.waf_mode ?? "block")}
                </span>
                <span className="text-jul-muted text-xs">{wafCoverageSummary(data)}</span>
              </>
            ) : (
              <OnOff on={false} />
            )}
            <button
              type="button"
              onClick={() => {
                setEditingWAF(true);
              }}
              className="ml-auto rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
            >
              {locationWafs.length > 0
                ? "Edit global"
                : data.waf_enabled
                  ? "Edit"
                  : "Configure"}
            </button>
          </span>
        </Row>
        {locationWafs.length > 0 && (
          <Row label="WAF per-location">
            <span className="flex w-full flex-col gap-1">
              <span className="text-jul-muted text-xs">
                {locationWafs.length} location{locationWafs.length > 1 ? "s" : ""} override the
                global policy. Editing above changes only the global{" "}
                <span className="font-mono">[waf]</span>; edit each override below.
              </span>
              {locationWafs.map((w, i) => (
                <span
                  key={`locwaf-${String(i)}`}
                  className="flex items-center gap-2 font-mono text-xs text-jul-text"
                >
                  <span className="min-w-0 flex-1 truncate">{locationWafLabel(w)}</span>
                  <button
                    type="button"
                    onClick={() => {
                      setEditingLocationWAF(w);
                    }}
                    className="shrink-0 rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-text hover:bg-jul-bg"
                  >
                    Edit
                  </button>
                </span>
              ))}
            </span>
          </Row>
        )}
        <Row label="Trusted client address">
          <span className="flex w-full flex-col gap-1">
            {(listeners.data ?? []).length === 0 ? (
              <span className="text-jul-muted text-xs">no listeners</span>
            ) : (
              (listeners.data ?? []).map((l) => (
                <span key={l.listen} className="flex items-center gap-2 text-xs">
                  <span className="font-mono text-jul-text">{l.listen}</span>
                  <span className={l.configured ? "text-jul-text" : "text-jul-muted"}>
                    {clientAddressSummary(l)}
                  </span>
                  {l.trusts_every_client && (
                    <span className="rounded-full bg-jul-warning/15 px-2 py-0.5 text-jul-warning">
                      trusts every client
                    </span>
                  )}
                  <button
                    type="button"
                    onClick={() => {
                      setEditingClientAddress(l);
                    }}
                    className="ml-auto shrink-0 rounded-md border border-jul-border px-2 py-0.5 text-jul-text hover:bg-jul-bg"
                  >
                    {l.configured ? "Edit" : "Configure"}
                  </button>
                </span>
              ))
            )}
            <span className="text-jul-muted text-xs">
              Forwarding headers are ignored unless the immediate peer is one of these ranges. This
              is a security boundary: keep it as narrow as the deployment allows.
            </span>
          </span>
        </Row>
        <Row label="Secret references">
          <span className="flex w-full items-center gap-2">
            {data.secret_refs > 0 ? (
              <span className="text-sm">
                {data.secret_refs} reference{data.secret_refs > 1 ? "s" : ""}{" "}
                <span className="text-jul-muted text-xs">(masked in logs)</span>
              </span>
            ) : (
              <span className="text-jul-muted text-xs">none</span>
            )}
            <button
              type="button"
              onClick={() => {
                setExternalizing(true);
              }}
              className="ml-auto rounded-md border border-jul-border px-2.5 py-1 text-xs text-jul-text hover:bg-jul-bg"
            >
              Externalize a secret
            </button>
          </span>
        </Row>
        <Row label="Body limit">
          {data.body_limit ? (
            <span className="font-mono text-sm">{data.body_limit}</span>
          ) : (
            <span className="text-jul-muted text-xs">unlimited</span>
          )}
        </Row>
        <Row label="Outbound egress">
          <EgressCell egress={data.egress} />
        </Row>
      </div>

      {editingWAF && (
        <WAFEditor
          current={data}
          onClose={() => {
            setEditingWAF(false);
          }}
        />
      )}

      {editingLocationWAF && (
        <LocationWAFEditor
          target={editingLocationWAF}
          onClose={() => {
            setEditingLocationWAF(null);
          }}
        />
      )}

      {editingClientAddress && (
        <ClientAddressEditor
          listener={editingClientAddress}
          onClose={() => {
            setEditingClientAddress(null);
          }}
        />
      )}

      {externalizing && (
        <SecretHelper
          onClose={() => {
            setExternalizing(false);
          }}
        />
      )}
    </div>
  );
}
