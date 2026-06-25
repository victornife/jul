import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetchSecurity } from "@/api/client.ts";
import { WAFEditor } from "@/features/security/WAFEditor.tsx";
import { SecretHelper } from "@/features/security/SecretHelper.tsx";

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
  const [externalizing, setExternalizing] = useState(false);
  const { data, isLoading, isError } = useQuery({
    queryKey: ["security"],
    queryFn: fetchSecurity,
  });

  if (isLoading) return <div className="text-jul-muted">Loading security…</div>;
  if (isError || !data) return <div className="text-jul-danger">Failed to load security info.</div>;

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold">Security</h1>
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
                    data.waf_mode === "detect"
                      ? "bg-jul-warning/15 text-jul-warning"
                      : "bg-jul-success/15 text-jul-success"
                  }`}
                >
                  {data.waf_mode ?? "block"}
                </span>
                <span className="text-jul-muted text-xs">
                  {data.waf_locations} location{data.waf_locations > 1 ? "s" : ""}
                </span>
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
              {data.waf_enabled ? "Edit" : "Configure"}
            </button>
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
      </div>

      {editingWAF && (
        <WAFEditor
          current={data}
          onClose={() => {
            setEditingWAF(false);
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
