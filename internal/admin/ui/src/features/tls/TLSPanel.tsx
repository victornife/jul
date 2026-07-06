/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  fetchTLS,
  fetchMTLS,
  type CertProjection,
  type MTLSServerProjection,
} from "@/api/client.ts";
import { PageHeader, Button, Loading } from "@/components/ui.tsx";
import { PanelError } from "@/components/PanelError.tsx";
import { TLSEditor } from "@/features/tls/TLSEditor.tsx";
import { MTLSEditor } from "@/features/tls/MTLSEditor.tsx";
import { mtlsServerSummary } from "@/lib/mtls.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";

function daysLeftColor(days: number | undefined): string {
  if (days === undefined) return "text-jul-muted";
  if (days <= 7) return "text-jul-danger";
  if (days <= 30) return "text-jul-warning";
  return "text-jul-success";
}

function CertCard({ cert }: { readonly cert: CertProjection }) {
  const daysLeft = cert.days_left;

  return (
    <div className="rounded-lg border border-jul-border bg-jul-surface p-4 space-y-3">
      {/* server names */}
      <div className="flex flex-wrap items-center gap-2">
        {cert.server_names.map((sn) => (
          <span key={sn} className="font-mono text-sm font-semibold text-jul-text">
            {sn}
          </span>
        ))}
        <span
          className={`ml-auto rounded-full px-2 py-0.5 text-xs font-medium ${
            cert.source === "acme"
              ? "bg-jul-accent/15 text-jul-accent"
              : "bg-jul-border text-jul-muted"
          }`}
        >
          {cert.source}
        </span>
      </div>

      {/* metadata */}
      <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-xs sm:grid-cols-3">
        {cert.issuer && (
          <>
            <dt className="text-jul-muted">Issuer</dt>
            <dd className="col-span-1 sm:col-span-2 font-mono text-jul-text">{cert.issuer}</dd>
          </>
        )}
        {cert.not_after && (
          <>
            <dt className="text-jul-muted">Expires</dt>
            <dd className="col-span-1 sm:col-span-2 font-mono text-jul-text">{cert.not_after}</dd>
          </>
        )}
        {daysLeft !== undefined && (
          <>
            <dt className="text-jul-muted">Days left</dt>
            <dd className={`col-span-1 sm:col-span-2 font-semibold ${daysLeftColor(daysLeft)}`}>
              {daysLeft}
            </dd>
          </>
        )}
      </dl>
    </div>
  );
}

function MTLSCard({
  server,
  onEdit,
}: {
  readonly server: MTLSServerProjection;
  readonly onEdit: () => void;
}) {
  const { run } = useRunPatch();
  const active = server.mode !== "none";

  function toggleRequire(match: string, type: string, current: boolean): void {
    run({
      op: "location_toggle_require_client_cert",
      listen: server.listen,
      server_names: server.server_names ?? [],
      match_type: type,
      path: match,
      enabled: !current,
    });
  }

  return (
    <div className="space-y-3 rounded-lg border border-jul-border bg-jul-surface p-4">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-semibold text-jul-text">{server.listen}</span>
            {(server.server_names ?? []).map((sn) => (
              <span key={sn} className="font-mono text-xs text-jul-muted">
                {sn}
              </span>
            ))}
            <span
              className={`rounded-full px-2 py-0.5 text-xs font-medium ${
                active ? "bg-jul-accent/15 text-jul-accent" : "bg-jul-border text-jul-muted"
              }`}
            >
              {active ? server.mode : "off"}
            </span>
          </div>
          <p className="mt-1 truncate text-xs text-jul-muted">{mtlsServerSummary(server)}</p>
        </div>
        <button
          type="button"
          onClick={onEdit}
          className="shrink-0 rounded-md border border-jul-border px-2 py-1 text-xs text-jul-text hover:border-jul-accent"
        >
          Edit mTLS
        </button>
      </div>

      {server.locations.length > 0 && (
        <div className="space-y-1.5 border-t border-jul-border pt-3">
          <p className="text-xs text-jul-muted">
            Per-route client-certificate requirement (takes effect immediately):
          </p>
          {server.locations.map((loc) => (
            <div
              key={`${loc.type}:${loc.match}`}
              className="flex items-center justify-between gap-3"
            >
              <span className="truncate font-mono text-xs text-jul-text">
                {loc.match}
                <span className="ml-1 text-jul-muted">({loc.type})</span>
              </span>
              <button
                type="button"
                disabled={!active && !loc.require_client_cert}
                onClick={() => {
                  toggleRequire(loc.match, loc.type, loc.require_client_cert);
                }}
                className={`shrink-0 rounded-full px-2 py-0.5 text-xs font-medium disabled:opacity-40 ${
                  loc.require_client_cert
                    ? "bg-jul-success/15 text-jul-success"
                    : "border border-jul-border text-jul-muted hover:border-jul-accent"
                }`}
              >
                {loc.require_client_cert ? "Required ✓" : "Require cert"}
              </button>
            </div>
          ))}
          {!active && (
            <p className="text-xs text-jul-muted">
              Enable mutual TLS (mode request or require) above to require a certificate per route.
            </p>
          )}
        </div>
      )}
    </div>
  );
}

function MTLSSection() {
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["mtls"],
    queryFn: fetchMTLS,
  });
  const [editing, setEditing] = useState<MTLSServerProjection | null>(null);

  if (isLoading) return <Loading label="Loading mutual TLS…" />;
  if (isError || !data)
    return <PanelError error={error} resource="mutual TLS" onRetry={() => void refetch()} />;

  return (
    <section className="space-y-4">
      <div>
        <h2 className="text-lg font-semibold text-jul-text">Mutual TLS</h2>
        <p className="text-sm text-jul-muted">
          Verify client certificates on a TLS listener. Server-level settings (mode, CA bundle, CRL,
          SAN allow-list) are read when the listener binds and take effect on restart; the per-route
          “require client certificate” toggle takes effect immediately.
        </p>
      </div>

      {data.servers.length === 0 ? (
        <p className="text-sm text-jul-muted">
          No TLS-enabled server blocks. Add a TLS server above before configuring mutual TLS.
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {data.servers.map((server, i) => (
            <MTLSCard
              key={`${server.listen}-${String(i)}`}
              server={server}
              onEdit={() => {
                setEditing(server);
              }}
            />
          ))}
        </div>
      )}

      {editing && (
        <MTLSEditor
          server={editing}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
    </section>
  );
}

export function TLSPanel() {
  const [creating, setCreating] = useState(false);
  const { data, isLoading, isError, error, refetch } = useQuery({
    queryKey: ["tls"],
    queryFn: fetchTLS,
  });

  if (isLoading) return <Loading label="Loading TLS certificates…" />;
  if (isError || !data)
    return <PanelError error={error} resource="TLS info" onRetry={() => void refetch()} />;
  const expiringSoon = data.filter((c) => c.days_left !== undefined && c.days_left <= 30);

  return (
    <div className="space-y-6">
      <PageHeader
        title="TLS & Certificates"
        description="Secure a listener with a certificate — automatically via ACME / Let's Encrypt, or with your own cert and key. The guided editor builds a TLS server block and routes it through the validated apply pipeline."
        actions={
          <Button
            variant="primary"
            onClick={() => {
              setCreating(true);
            }}
          >
            New TLS server
          </Button>
        }
      />

      {expiringSoon.length > 0 && (
        <div className="rounded-lg border border-jul-warning/40 bg-jul-warning/10 px-4 py-3 text-sm text-jul-warning">
          ⚠ {expiringSoon.length} certificate{expiringSoon.length > 1 ? "s" : ""} expiring within 30
          days.
        </div>
      )}

      {data.length === 0 ? (
        <p className="text-jul-muted text-sm">
          No TLS-enabled server blocks. Use “New TLS server” to add one.
        </p>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {data.map((cert, i) => (
            <CertCard key={`${cert.server_names.join(",")}-${String(i)}`} cert={cert} />
          ))}
        </div>
      )}

      <MTLSSection />

      {creating && (
        <TLSEditor
          onClose={() => {
            setCreating(false);
          }}
        />
      )}
    </div>
  );
}
