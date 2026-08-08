/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import type { AppProjection, RouteProjection } from "@/api/client.ts";
import { usePermission } from "@/auth/usePermission.ts";
import { Drawer } from "@/components/Drawer.tsx";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import {
  AppPatchValidationError,
  buildAppCreationBatch,
  summarizeAppPatchBatch,
  type AppBackendDraft,
  type AppCreateDraft,
  type AppMountDraft,
  type AppProtocol,
  type AppRouteMatchType,
  type AppStrategy,
} from "@/lib/appPatch.ts";
import {
  type DiscoveryDraft,
  type DiscoveryKind,
  type HealthCheckDraft,
  type HealthCheckType,
} from "@/lib/appSettings.ts";
import {
  formatServerIdentity,
  serverIdentityFromRoute,
  serverIdentityKey,
} from "@/lib/routePatch.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";

interface Preset {
  readonly id: string;
  readonly label: string;
  readonly placeholder: string;
  readonly hint: string;
  readonly protocol: AppProtocol;
}

const PRESETS: readonly Preset[] = [
  {
    id: "node",
    label: "Express / Node.js",
    placeholder: "127.0.0.1:3000",
    hint: "Typical Node.js apps listen on :3000.",
    protocol: "http",
  },
  {
    id: "apollo",
    label: "Apollo GraphQL",
    placeholder: "127.0.0.1:4000",
    hint: "Apollo Server defaults to :4000.",
    protocol: "http",
  },
  {
    id: "fastapi",
    label: "FastAPI",
    placeholder: "127.0.0.1:8000",
    hint: "Uvicorn/FastAPI defaults to :8000.",
    protocol: "http",
  },
  {
    id: "django",
    label: "Django / Flask",
    placeholder: "127.0.0.1:8000",
    hint: "Django/Flask development servers often use :8000 or :5000.",
    protocol: "http",
  },
  {
    id: "go",
    label: "Go HTTP app",
    placeholder: "127.0.0.1:8080",
    hint: "Go services commonly listen on :8080.",
    protocol: "http",
  },
  {
    id: "generic",
    label: "Generic HTTP app",
    placeholder: "127.0.0.1:8080",
    hint: "Any HTTP backend.",
    protocol: "http",
  },
  {
    id: "grpc",
    label: "gRPC backend",
    placeholder: "127.0.0.1:50051",
    hint: "Native gRPC uses Jul's grpc_proxy action and requires HTTP/2.",
    protocol: "grpc",
  },
];

const DEFAULT_PRESET: Preset = {
  id: "generic",
  label: "Generic HTTP app",
  placeholder: "127.0.0.1:8080",
  hint: "Any HTTP backend.",
  protocol: "http",
};

const STRATEGIES: ReadonlyArray<{ readonly value: AppStrategy; readonly label: string }> = [
  { value: "round_robin", label: "Round robin" },
  { value: "weighted_round_robin", label: "Weighted round robin" },
  { value: "least_conn", label: "Least connections" },
];

const DISCOVERY_OPTIONS: ReadonlyArray<{ readonly value: DiscoveryKind; readonly label: string }> =
  [
    { value: "static", label: "Static (manual backends)" },
    { value: "dns", label: "DNS (A/AAAA)" },
    { value: "dns_srv", label: "DNS SRV" },
    { value: "consul", label: "Consul" },
    { value: "kubernetes", label: "Kubernetes" },
  ];

function emptyHealthCheck(): HealthCheckDraft {
  return {
    enabled: false,
    type: "http",
    path: "/healthz",
    interval: "5s",
    timeout: "2s",
    healthyThreshold: "2",
    unhealthyThreshold: "3",
    expectStatus: "200",
    expectBody: "",
  };
}

function emptyDiscovery(): DiscoveryDraft {
  return {
    type: "static",
    target: "",
    refresh: "30s",
    consulAddress: "",
    consulService: "",
    consulTag: "",
    consulDatacenter: "",
    consulPassingOnly: true,
    k8sNamespace: "default",
    k8sService: "",
    k8sPort: "",
    k8sApiServer: "",
    k8sCaFile: "",
    k8sInsecure: false,
    hasToken: false,
  };
}

function TextField({
  label,
  value,
  placeholder,
  hint,
  mono = false,
  type = "text",
  min,
  onChange,
}: {
  readonly label: string;
  readonly value: string | number;
  readonly placeholder?: string;
  readonly hint?: string;
  readonly mono?: boolean;
  readonly type?: "text" | "number";
  readonly min?: number;
  readonly onChange: (value: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type={type}
        min={min}
        value={value}
        placeholder={placeholder}
        onChange={(event) => {
          onChange(event.target.value);
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
  readonly onChange: (checked: boolean) => void;
}) {
  return (
    <label className="flex items-start gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(event) => {
          onChange(event.target.checked);
        }}
        className="mt-0.5 h-4 w-4 rounded border-jul-border bg-jul-surface text-jul-accent focus:ring-jul-accent"
      />
      <span>{label}</span>
    </label>
  );
}

function HealthCheckFields({
  value,
  onChange,
}: {
  readonly value: HealthCheckDraft;
  readonly onChange: (value: HealthCheckDraft) => void;
}) {
  function set<K extends keyof HealthCheckDraft>(key: K, next: HealthCheckDraft[K]): void {
    onChange({ ...value, [key]: next });
  }

  return (
    <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface/50 p-3">
      <Toggle
        label="Enable active health checks"
        checked={value.enabled}
        onChange={(enabled) => {
          set("enabled", enabled);
        }}
      />
      {value.enabled && (
        <div className="space-y-3 border-t border-jul-border pt-3">
          <label className="block space-y-1">
            <span className="text-sm font-medium text-jul-text">Probe type</span>
            <select
              value={value.type}
              onChange={(event) => {
                set("type", event.target.value as HealthCheckType);
              }}
              className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
            >
              <option value="http">HTTP</option>
              <option value="tcp">TCP connect</option>
            </select>
          </label>
          {value.type === "http" && (
            <TextField
              label="Path"
              value={value.path}
              placeholder="/healthz"
              mono
              onChange={(next) => {
                set("path", next);
              }}
            />
          )}
          <div className="grid grid-cols-2 gap-3">
            <TextField
              label="Interval"
              value={value.interval}
              placeholder="5s"
              onChange={(next) => {
                set("interval", next);
              }}
            />
            <TextField
              label="Timeout"
              value={value.timeout}
              placeholder="2s"
              onChange={(next) => {
                set("timeout", next);
              }}
            />
            <TextField
              label="Healthy threshold"
              value={value.healthyThreshold}
              placeholder="2"
              onChange={(next) => {
                set("healthyThreshold", next);
              }}
            />
            <TextField
              label="Unhealthy threshold"
              value={value.unhealthyThreshold}
              placeholder="3"
              onChange={(next) => {
                set("unhealthyThreshold", next);
              }}
            />
          </div>
          {value.type === "http" && (
            <>
              <TextField
                label="Expected status codes"
                value={value.expectStatus}
                placeholder="200, 204"
                hint="Comma-separated."
                onChange={(next) => {
                  set("expectStatus", next);
                }}
              />
              <TextField
                label="Expected body contains"
                value={value.expectBody}
                placeholder="Optional"
                onChange={(next) => {
                  set("expectBody", next);
                }}
              />
            </>
          )}
        </div>
      )}
    </div>
  );
}

function DiscoveryFields({
  value,
  requiresNewToken,
  onChange,
  onRequiresNewToken,
}: {
  readonly value: DiscoveryDraft;
  readonly requiresNewToken: boolean;
  readonly onChange: (value: DiscoveryDraft) => void;
  readonly onRequiresNewToken: (value: boolean) => void;
}) {
  function set<K extends keyof DiscoveryDraft>(key: K, next: DiscoveryDraft[K]): void {
    onChange({ ...value, [key]: next });
  }

  return (
    <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface/50 p-3">
      <label className="block space-y-1">
        <span className="text-sm font-medium text-jul-text">Discovery provider</span>
        <select
          value={value.type}
          onChange={(event) => {
            const type = event.target.value as DiscoveryKind;
            onChange({ ...value, type });
            if (type !== "consul" && type !== "kubernetes") onRequiresNewToken(false);
          }}
          className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
        >
          {DISCOVERY_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>

      {(value.type === "dns" || value.type === "dns_srv") && (
        <TextField
          label="Target"
          value={value.target}
          placeholder={value.type === "dns" ? "svc.internal:8080" : "_grpc._tcp.svc.example.com"}
          mono
          onChange={(next) => {
            set("target", next);
          }}
        />
      )}

      {value.type === "consul" && (
        <div className="space-y-3">
          <TextField
            label="Service"
            value={value.consulService}
            placeholder="web"
            onChange={(next) => {
              set("consulService", next);
            }}
          />
          <TextField
            label="Address"
            value={value.consulAddress}
            placeholder="http://127.0.0.1:8500"
            mono
            onChange={(next) => {
              set("consulAddress", next);
            }}
          />
          <div className="grid grid-cols-2 gap-3">
            <TextField
              label="Tag"
              value={value.consulTag}
              placeholder="Optional"
              onChange={(next) => {
                set("consulTag", next);
              }}
            />
            <TextField
              label="Datacenter"
              value={value.consulDatacenter}
              placeholder="Optional"
              onChange={(next) => {
                set("consulDatacenter", next);
              }}
            />
          </div>
          <Toggle
            label="Only passing instances"
            checked={value.consulPassingOnly}
            onChange={(next) => {
              set("consulPassingOnly", next);
            }}
          />
        </div>
      )}

      {value.type === "kubernetes" && (
        <div className="space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <TextField
              label="Namespace"
              value={value.k8sNamespace}
              placeholder="default"
              onChange={(next) => {
                set("k8sNamespace", next);
              }}
            />
            <TextField
              label="Service"
              value={value.k8sService}
              placeholder="web"
              onChange={(next) => {
                set("k8sService", next);
              }}
            />
          </div>
          <TextField
            label="Port"
            value={value.k8sPort}
            placeholder="Name or number (optional)"
            onChange={(next) => {
              set("k8sPort", next);
            }}
          />
          <TextField
            label="API server"
            value={value.k8sApiServer}
            placeholder="In-cluster by default"
            mono
            onChange={(next) => {
              set("k8sApiServer", next);
            }}
          />
          <TextField
            label="CA file"
            value={value.k8sCaFile}
            placeholder="Mounted CA by default"
            mono
            onChange={(next) => {
              set("k8sCaFile", next);
            }}
          />
          <Toggle
            label="Skip API server TLS verification (testing only)"
            checked={value.k8sInsecure}
            onChange={(next) => {
              set("k8sInsecure", next);
            }}
          />
        </div>
      )}

      {value.type !== "static" && (
        <TextField
          label="Refresh interval"
          value={value.refresh}
          placeholder="30s"
          onChange={(next) => {
            set("refresh", next);
          }}
        />
      )}

      {(value.type === "consul" || value.type === "kubernetes") && (
        <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
          <Toggle
            label="This new provider requires an authentication token"
            checked={requiresNewToken}
            onChange={onRequiresNewToken}
          />
          <p className="text-xs text-jul-muted">
            Typed App patches never collect or drop a token. Token-required creation stops before
            preview and points to the separately authorized raw editor.
          </p>
        </div>
      )}
    </div>
  );
}

export interface AppEditorProps {
  readonly initial?: Partial<Pick<AppCreateDraft, "name" | "strategy" | "backends">>;
  readonly existingApps?: readonly AppProjection[];
  readonly existingRoutes?: readonly RouteProjection[];
  readonly routeInventoryReady?: boolean | undefined;
  readonly onReview?: ((appName: string) => void) | undefined;
  readonly onClose: () => void;
}

/** Guided App/upstream creation backed exclusively by one ordered typed patch batch. */
export function AppEditor({
  initial,
  existingApps = [],
  existingRoutes = [],
  routeInventoryReady = true,
  onReview,
  onClose,
}: AppEditorProps) {
  const permission = usePermission();
  const canWrite = permission.has("config:write");
  const canReadRaw = permission.has("config:raw");
  const batch = useRunPatchBatch();
  const [presetId, setPresetId] = useState("generic");
  const preset = PRESETS.find((candidate) => candidate.id === presetId) ?? DEFAULT_PRESET;
  const [name, setName] = useState(initial?.name ?? "");
  const [strategy, setStrategy] = useState<AppStrategy>(initial?.strategy ?? "round_robin");
  const [backends, setBackends] = useState<AppBackendDraft[]>(
    initial?.backends?.length ? [...initial.backends] : [{ address: "", weight: 1 }],
  );
  const [healthCheck, setHealthCheck] = useState<HealthCheckDraft>(emptyHealthCheck);
  const [discovery, setDiscovery] = useState<DiscoveryDraft>(emptyDiscovery);
  const [requiresNewToken, setRequiresNewToken] = useState(false);
  const [mountMode, setMountMode] = useState<AppMountDraft["mode"]>("none");
  const [existingServerKey, setExistingServerKey] = useState("");
  const [newListen, setNewListen] = useState(":8080");
  const [newServerNames, setNewServerNames] = useState("");
  const [protocol, setProtocol] = useState<AppProtocol>(preset.protocol);
  const [matchType, setMatchType] = useState<AppRouteMatchType>("prefix");
  const [path, setPath] = useState("/");
  const [localError, setLocalError] = useState<AppPatchValidationError | null>(null);
  const [plannedSummaries, setPlannedSummaries] = useState<string[]>([]);

  const exactServers = useMemo(() => {
    const seen = new Set<string>();
    return existingRoutes.flatMap((route) => {
      const identity = serverIdentityFromRoute(route);
      const key = serverIdentityKey(identity);
      if (seen.has(key)) return [];
      seen.add(key);
      return [{ key, identity }];
    });
  }, [existingRoutes]);

  const previewError = describePatchBatchError(batch.error);
  const shownError = localError?.message ?? previewError;

  function updateBackend(index: number, patch: Partial<AppBackendDraft>): void {
    setBackends((current) =>
      current.map((backend, candidate) =>
        candidate === index ? { ...backend, ...patch } : backend,
      ),
    );
  }

  function buildMount(): AppMountDraft {
    if (mountMode === "none") return { mode: "none" };
    if (!routeInventoryReady) {
      throw new AppPatchValidationError([
        "The complete server inventory is not available. Retry loading Routes or choose no route mount before previewing this App.",
      ]);
    }
    if (mountMode === "existing") {
      const selected = exactServers.find((server) => server.key === existingServerKey);
      if (selected === undefined) {
        throw new AppPatchValidationError([
          "Choose the exact existing server identity before previewing the App batch.",
        ]);
      }
      return {
        mode: "existing",
        server: selected.identity,
        protocol,
        matchType,
        path,
      };
    }
    return {
      mode: "new",
      server: {
        listen: newListen,
        serverNames: newServerNames.split(",").map((item) => item.trim()),
      },
      protocol,
      matchType,
      path,
    };
  }

  async function preview(): Promise<void> {
    if (!canWrite) return;
    batch.clearError();
    setLocalError(null);
    try {
      const ops = buildAppCreationBatch(
        {
          name,
          strategy,
          backends,
          healthCheck,
          discovery: { settings: discovery, requiresNewToken },
          mount: buildMount(),
        },
        { apps: existingApps, routes: existingRoutes },
      );
      setPlannedSummaries(summarizeAppPatchBatch(ops));
      const pending = await batch.preview(ops);
      if (pending !== null) {
        onReview?.(name.trim());
        batch.handoff(pending);
      }
    } catch (caught) {
      setPlannedSummaries([]);
      setLocalError(
        caught instanceof AppPatchValidationError
          ? caught
          : new AppPatchValidationError(["The App batch could not be built safely."]),
      );
    }
  }

  return (
    <Drawer
      title="New app / upstream"
      subtitle="Build one ordered, lifecycle-aware patch batch and review it in Configuration."
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-start justify-between gap-3">
            <div className="min-w-0">
              {shownError && <p className="text-xs text-jul-danger">{shownError}</p>}
              <ForbiddenAction permission="config:write" />
              <ForbiddenAction permission="config:apply" />
            </div>
            <button
              type="button"
              disabled={batch.busy || !canWrite}
              onClick={() => {
                void preview();
              }}
              className="ml-auto shrink-0 rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {batch.busy ? "Previewing ordered batch…" : "Review batch in editor →"}
            </button>
          </div>
          <p className="text-xs text-jul-muted">
            Preview requires <span className="font-mono">config:write</span>. Final apply is gated
            independently by <span className="font-mono">config:apply</span> in Configuration.
          </p>
        </div>
      }
    >
      <fieldset disabled={!canWrite || batch.busy} className="space-y-5 disabled:opacity-70">
        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Framework preset</span>
          <select
            value={presetId}
            onChange={(event) => {
              const next = PRESETS.find((candidate) => candidate.id === event.target.value);
              setPresetId(event.target.value);
              if (next !== undefined) setProtocol(next.protocol);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {PRESETS.map((candidate) => (
              <option key={candidate.id} value={candidate.id}>
                {candidate.label}
              </option>
            ))}
          </select>
          <span className="text-xs text-jul-muted">{preset.hint}</span>
        </label>

        <TextField
          label="App / upstream name"
          value={name}
          placeholder="api"
          mono
          onChange={setName}
        />

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Load-balancing strategy</span>
          <select
            value={strategy}
            onChange={(event) => {
              setStrategy(event.target.value as AppStrategy);
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {STRATEGIES.map((candidate) => (
              <option key={candidate.value} value={candidate.value}>
                {candidate.label}
              </option>
            ))}
          </select>
        </label>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <span className="text-sm font-medium text-jul-text">Backend instances</span>
            <button
              type="button"
              onClick={() => {
                setBackends((current) => [...current, { address: "", weight: 1 }]);
              }}
              className="rounded-md border border-jul-border px-2 py-1 text-xs text-jul-text hover:bg-jul-surface"
            >
              Add backend
            </button>
          </div>
          {backends.map((backend, index) => (
            <div
              key={`backend-${String(index)}`}
              className="grid grid-cols-[1fr_90px_auto] items-end gap-2 rounded-md border border-jul-border bg-jul-surface/50 p-3"
            >
              <TextField
                label={`Address ${String(index + 1)}`}
                value={backend.address}
                placeholder={preset.placeholder}
                mono
                onChange={(next) => {
                  updateBackend(index, { address: next });
                }}
              />
              <TextField
                label="Weight"
                value={backend.weight}
                type="number"
                min={1}
                onChange={(next) => {
                  updateBackend(index, { weight: Math.max(1, Number(next) || 1) });
                }}
              />
              <button
                type="button"
                disabled={backends.length === 1}
                onClick={() => {
                  setBackends((current) => current.filter((_, candidate) => candidate !== index));
                }}
                className="rounded-md border border-jul-border px-2 py-1.5 text-xs text-jul-danger hover:bg-jul-danger/10 disabled:opacity-40"
              >
                Remove
              </button>
            </div>
          ))}
        </div>

        <section className="space-y-2">
          <div>
            <h3 className="text-sm font-semibold text-jul-text">Active health checks</h3>
            <p className="text-xs text-jul-muted">
              Every supported health field is carried into the typed batch.
            </p>
          </div>
          <HealthCheckFields value={healthCheck} onChange={setHealthCheck} />
        </section>

        <section className="space-y-2">
          <div>
            <h3 className="text-sm font-semibold text-jul-text">Service discovery</h3>
            <p className="text-xs text-jul-muted">
              Static, DNS, DNS SRV, Consul, and Kubernetes non-secret fields are supported.
            </p>
          </div>
          <DiscoveryFields
            value={discovery}
            requiresNewToken={requiresNewToken}
            onChange={setDiscovery}
            onRequiresNewToken={setRequiresNewToken}
          />
        </section>

        <section className="space-y-3 rounded-md border border-jul-border bg-jul-surface/50 p-3">
          <div>
            <h3 className="text-sm font-semibold text-jul-text">Mount on a route</h3>
            <p className="text-xs text-jul-muted">
              Choose no mount, an exact existing server identity, or a new exact server identity.
              Listener-only or blank-host inference is never used.
            </p>
          </div>
          <div className="grid gap-2 sm:grid-cols-3">
            {(
              [
                ["none", "No route mount"],
                ["existing", "Existing exact server"],
                ["new", "New exact server"],
              ] as const
            ).map(([value, label]) => (
              <label
                key={value}
                className="flex items-center gap-2 rounded-md border border-jul-border p-2 text-sm text-jul-text"
              >
                <input
                  type="radio"
                  name="app-mount-mode"
                  value={value}
                  disabled={value !== "none" && !routeInventoryReady}
                  checked={mountMode === value}
                  onChange={() => {
                    setMountMode(value);
                  }}
                />
                {label}
              </label>
            ))}
          </div>

          {!routeInventoryReady && (
            <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
              The complete Routes/server inventory is unavailable. Upstream-only creation remains
              available, but existing/new server mounting is disabled so exact identity and
              collision checks cannot use stale or partial data.
            </p>
          )}

          {mountMode === "none" && (
            <p className="rounded-md border border-jul-border bg-jul-bg p-3 text-xs text-jul-muted">
              This batch creates only the App/upstream and its optional health/discovery settings.
              No data-plane server or route will point to it until a later reviewed route change.
            </p>
          )}

          {mountMode === "existing" && (
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Exact server identity</span>
              <select
                value={existingServerKey}
                onChange={(event) => {
                  setExistingServerKey(event.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="">Choose listen + complete host-name set…</option>
                {exactServers.map((server) => (
                  <option key={server.key} value={server.key}>
                    {formatServerIdentity(server.identity)}
                  </option>
                ))}
              </select>
            </label>
          )}

          {mountMode === "new" && (
            <div className="grid gap-3 sm:grid-cols-2">
              <TextField
                label="Listener"
                value={newListen}
                placeholder=":8080"
                mono
                onChange={setNewListen}
              />
              <TextField
                label="Server names"
                value={newServerNames}
                placeholder="api.example.com, www.api.example.com"
                hint="Comma-separated; leave empty only for the intentional catch-all identity."
                mono
                onChange={setNewServerNames}
              />
            </div>
          )}

          {mountMode !== "none" && (
            <div className="space-y-3 border-t border-jul-border pt-3">
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">Protocol</span>
                  <select
                    value={protocol}
                    onChange={(event) => {
                      setProtocol(event.target.value as AppProtocol);
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  >
                    <option value="http">HTTP reverse proxy</option>
                    <option value="grpc">Native gRPC proxy</option>
                  </select>
                </label>
                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">Path match</span>
                  <select
                    value={matchType}
                    onChange={(event) => {
                      setMatchType(event.target.value as AppRouteMatchType);
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  >
                    <option value="prefix">Prefix</option>
                    <option value="exact">Exact</option>
                  </select>
                </label>
              </div>
              <TextField label="Route path" value={path} placeholder="/" mono onChange={setPath} />
              {protocol === "grpc" && (
                <p className="rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3 text-xs text-jul-text">
                  Existing TLS servers already provide HTTP/2. Existing plaintext servers are
                  accepted only when h2c is already enabled. A new plaintext gRPC server may enable
                  h2c only on an unused, dedicated listener so sibling virtual hosts are never
                  changed silently.
                </p>
              )}
            </div>
          )}
        </section>
      </fieldset>

      {plannedSummaries.length > 0 && (
        <section className="mt-5 space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
          <h3 className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Secret-safe local operation plan
          </h3>
          <ol className="list-decimal space-y-1 pl-5 text-xs text-jul-text">
            {plannedSummaries.map((summary, index) => (
              <li key={`${String(index)}-${summary}`}>{summary}</li>
            ))}
          </ol>
        </section>
      )}

      {localError?.rawEditorRequired && (
        <section className="mt-5 space-y-2 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
          <p className="text-xs text-jul-text">
            This creation needs a new secret token. The typed workflow stopped without previewing or
            omitting it.
          </p>
          {canReadRaw ? (
            <Link
              to="/config"
              className="inline-block rounded-md border border-jul-border px-3 py-1.5 text-xs font-medium text-jul-text hover:bg-jul-bg"
            >
              Open raw configuration editor →
            </Link>
          ) : (
            <>
              <button
                type="button"
                disabled
                className="rounded-md border border-jul-border px-3 py-1.5 text-xs font-medium text-jul-text opacity-40"
              >
                Open raw configuration editor →
              </button>
              <ForbiddenAction permission="config:raw" />
            </>
          )}
        </section>
      )}
    </Drawer>
  );
}
