import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import {
  type AppProjection,
} from "@/api/client.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  discoveryToPatch,
  discoveryTokenNote,
  discoveryWarnings,
  healthCheckToPatch,
  healthCheckWarnings,
  seedDiscovery,
  seedHealthCheck,
  type DiscoveryDraft,
  type DiscoveryKind,
  type HealthCheckDraft,
  type HealthCheckType,
} from "@/lib/appSettings.ts";

function TextField({
  label,
  value,
  placeholder,
  hint,
  mono,
  onChange,
}: {
  readonly label: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly hint?: string;
  readonly mono?: boolean;
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
        className={`w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent ${mono ? "font-mono" : ""}`}
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function Toggle({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface text-jul-accent focus:ring-jul-accent"
      />
      <span className="text-sm text-jul-text">{label}</span>
    </label>
  );
}

function Warnings({ items }: { readonly items: string[] }) {
  if (items.length === 0) return null;
  return (
    <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
      {items.map((wn, i) => (
        <p key={`w-${String(i)}`} className="text-xs text-jul-text">
          {wn}
        </p>
      ))}
    </div>
  );
}

export interface AppEditorDrawerProps {
  readonly app: AppProjection;
  readonly onClose: () => void;
}

/**
 * Guided editor for an upstream pool's active health checks (Phase 4b). It seeds
 * from the Apps projection, edits the [health_check] block, and routes the
 * change through the upstream_set_health_check patch op as a reviewed diff.
 */
export function HealthCheckEditor({ app, onClose }: AppEditorDrawerProps) {
  const { error, busy, run } = useRunPatch();
  const [draft, setDraft] = useState<HealthCheckDraft>(() => seedHealthCheck(app));
  const warnings = healthCheckWarnings(draft);

  function set<K extends keyof HealthCheckDraft>(key: K, val: HealthCheckDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    run({ op: "upstream_set_health_check", upstream: app.name, health_check: healthCheckToPatch(draft) });
  }

  return (
    <Drawer
      title="Active health checks"
      subtitle={app.name}
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
          Active probes mark a backend unhealthy after repeated failures (and healthy again after
          recoveries) without waiting for live traffic. Disabling leaves only passive health checks.
        </p>

        <Toggle
          label="Enable active health checks"
          checked={draft.enabled}
          onChange={(v) => {
            set("enabled", v);
          }}
        />

        {draft.enabled && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Probe type</span>
              <select
                value={draft.type}
                onChange={(e) => {
                  set("type", e.target.value as HealthCheckType);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="http">HTTP</option>
                <option value="tcp">TCP connect</option>
              </select>
            </label>

            {draft.type === "http" && (
              <TextField
                label="Path"
                value={draft.path}
                placeholder="/healthz"
                mono
                onChange={(v) => {
                  set("path", v);
                }}
              />
            )}

            <div className="grid grid-cols-2 gap-3">
              <TextField
                label="Interval"
                value={draft.interval}
                placeholder="5s"
                hint="default 5s"
                onChange={(v) => {
                  set("interval", v);
                }}
              />
              <TextField
                label="Timeout"
                value={draft.timeout}
                placeholder="2s"
                hint="must be < interval"
                onChange={(v) => {
                  set("timeout", v);
                }}
              />
              <TextField
                label="Healthy threshold"
                value={draft.healthyThreshold}
                placeholder="2"
                onChange={(v) => {
                  set("healthyThreshold", v);
                }}
              />
              <TextField
                label="Unhealthy threshold"
                value={draft.unhealthyThreshold}
                placeholder="3"
                onChange={(v) => {
                  set("unhealthyThreshold", v);
                }}
              />
            </div>

            {draft.type === "http" && (
              <>
                <TextField
                  label="Expected status codes"
                  value={draft.expectStatus}
                  placeholder="200"
                  hint="comma-separated; default 200"
                  onChange={(v) => {
                    set("expectStatus", v);
                  }}
                />
                <TextField
                  label="Expected body contains"
                  value={draft.expectBody}
                  placeholder="(optional)"
                  onChange={(v) => {
                    set("expectBody", v);
                  }}
                />
              </>
            )}
          </>
        )}

        <Warnings items={warnings} />
      </div>
    </Drawer>
  );
}

const DISCOVERY_OPTIONS: ReadonlyArray<{ readonly value: DiscoveryKind; readonly label: string }> = [
  { value: "static", label: "Static (no discovery)" },
  { value: "dns", label: "DNS (A/AAAA)" },
  { value: "dns_srv", label: "DNS SRV" },
  { value: "consul", label: "Consul" },
  { value: "kubernetes", label: "Kubernetes" },
];

/**
 * Guided editor for an upstream pool's dynamic discovery (Phase 4b). It seeds
 * from the Apps projection, edits the [discovery] block (static / dns / dns_srv
 * / consul / kubernetes), and routes the change through upstream_set_discovery
 * as a reviewed diff. Secret tokens are never shown or sent — the backend keeps
 * the existing token when the provider type is unchanged.
 */
export function DiscoveryEditor({ app, onClose }: AppEditorDrawerProps) {
  const { error, busy, run } = useRunPatch();
  const [draft, setDraft] = useState<DiscoveryDraft>(() => seedDiscovery(app));
  const warnings = discoveryWarnings(draft);
  const tokenNote = discoveryTokenNote(draft);

  function set<K extends keyof DiscoveryDraft>(key: K, val: DiscoveryDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    run({ op: "upstream_set_discovery", upstream: app.name, discovery: discoveryToPatch(draft) });
  }

  return (
    <Drawer
      title="Service discovery"
      subtitle={app.name}
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
          Discovery resolves this pool&apos;s backends from an external source and refreshes them
          live without a reload. Choose Static to manage the backend list yourself.
        </p>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Provider</span>
          <select
            value={draft.type}
            onChange={(e) => {
              set("type", e.target.value as DiscoveryKind);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {DISCOVERY_OPTIONS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
        </label>

        {(draft.type === "dns" || draft.type === "dns_srv") && (
          <TextField
            label="Target"
            value={draft.target}
            placeholder={draft.type === "dns" ? "svc.internal:8080" : "_grpc._tcp.svc.example.com"}
            hint={
              draft.type === "dns"
                ? "host:port — the port is applied to every resolved address"
                : "the SRV name (carries port and weight)"
            }
            mono
            onChange={(v) => {
              set("target", v);
            }}
          />
        )}

        {draft.type === "consul" && (
          <div className="space-y-3">
            <TextField
              label="Service"
              value={draft.consulService}
              placeholder="web"
              onChange={(v) => {
                set("consulService", v);
              }}
            />
            <TextField
              label="Address"
              value={draft.consulAddress}
              placeholder="http://127.0.0.1:8500"
              mono
              onChange={(v) => {
                set("consulAddress", v);
              }}
            />
            <div className="grid grid-cols-2 gap-3">
              <TextField
                label="Tag"
                value={draft.consulTag}
                placeholder="(optional)"
                onChange={(v) => {
                  set("consulTag", v);
                }}
              />
              <TextField
                label="Datacenter"
                value={draft.consulDatacenter}
                placeholder="(optional)"
                onChange={(v) => {
                  set("consulDatacenter", v);
                }}
              />
            </div>
            <Toggle
              label="Only passing instances"
              checked={draft.consulPassingOnly}
              onChange={(v) => {
                set("consulPassingOnly", v);
              }}
            />
          </div>
        )}

        {draft.type === "kubernetes" && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <TextField
                label="Namespace"
                value={draft.k8sNamespace}
                placeholder="default"
                onChange={(v) => {
                  set("k8sNamespace", v);
                }}
              />
              <TextField
                label="Service"
                value={draft.k8sService}
                placeholder="web"
                onChange={(v) => {
                  set("k8sService", v);
                }}
              />
            </div>
            <TextField
              label="Port"
              value={draft.k8sPort}
              placeholder="(name or number, optional)"
              onChange={(v) => {
                set("k8sPort", v);
              }}
            />
            <TextField
              label="API server"
              value={draft.k8sApiServer}
              placeholder="(in-cluster by default)"
              mono
              onChange={(v) => {
                set("k8sApiServer", v);
              }}
            />
            <TextField
              label="CA file"
              value={draft.k8sCaFile}
              placeholder="(mounted CA by default)"
              mono
              onChange={(v) => {
                set("k8sCaFile", v);
              }}
            />
            <Toggle
              label="Skip API server TLS verification (testing only)"
              checked={draft.k8sInsecure}
              onChange={(v) => {
                set("k8sInsecure", v);
              }}
            />
          </div>
        )}

        {draft.type !== "static" && (
          <TextField
            label="Refresh interval"
            value={draft.refresh}
            placeholder="30s"
            hint="default 30s"
            onChange={(v) => {
              set("refresh", v);
            }}
          />
        )}

        {tokenNote && (
          <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
            {tokenNote}
          </p>
        )}

        <Warnings items={warnings} />
      </div>
    </Drawer>
  );
}
