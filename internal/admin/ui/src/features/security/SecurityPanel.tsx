/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchSecurity, type SecurityProjection, type LocationWAF } from "@/api/client.ts";
import { WAFEditor } from "@/features/security/WAFEditor.tsx";
import { LocationWAFEditor } from "@/features/security/LocationWAFEditor.tsx";
import { SecretHelper } from "@/features/security/SecretHelper.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { Loading } from "@/components/ui.tsx";

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

export function SecurityPanel() {
  const [editingWAF, setEditingWAF] = useState(false);
  const [editingLocationWAF, setEditingLocationWAF] = useState<LocationWAF | null>(null);
  const [externalizing, setExternalizing] = useState(false);
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["security"],
    queryFn: fetchSecurity,
  });

  if (isLoading) return <Loading label="Loading security…" />;
  if (isError || !data)
    return <PanelError error={error} resource="security info" onRetry={() => void refetch()} />;

  const locationWafs = data.location_wafs ?? [];

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">Security</h1>
        <p className="max-w-3xl text-sm text-jul-muted">
          Authentication, mutual TLS, and WAF posture across all listeners and locations.
          Verify which routes are protected and how requests are inspected before they reach backends.
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
