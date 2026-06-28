import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  patchConfig,
  ConfigRejectedError,
  type AppProjection,
  type BackendProjection,
  type ConfigPatch,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { DiscoveryEditor, HealthCheckEditor } from "@/features/apps/AppSettingsEditor.tsx";

const STRATEGIES: ReadonlyArray<{ readonly value: string; readonly label: string }> = [
  { value: "round_robin", label: "Round robin" },
  { value: "weighted_round_robin", label: "Weighted round robin" },
  { value: "least_conn", label: "Least connections" },
];

function Row({ label, value }: { readonly label: string; readonly value: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[150px_1fr] gap-2 py-1.5">
      <span className="text-xs uppercase tracking-wider text-jul-muted">{label}</span>
      <span className="text-sm text-jul-text">{value}</span>
    </div>
  );
}

function BackendRow({
  b,
  canRemove,
  busy,
  onRemove,
}: {
  readonly b: BackendProjection;
  readonly canRemove: boolean;
  readonly busy: boolean;
  readonly onRemove: () => void;
}) {
  return (
    <tr className="border-b border-jul-border last:border-b-0">
      <td className="px-3 py-2">
        <div className="flex items-center gap-2">
          {b.healthy !== undefined ? (
            <span
              title={b.healthy ? "healthy" : "unhealthy"}
              className={`inline-block h-2 w-2 rounded-full ${b.healthy ? "bg-jul-success" : "bg-jul-danger"}`}
            />
          ) : (
            <span
              title="health unknown — no active checks"
              className="inline-block h-2 w-2 rounded-full bg-jul-muted/50"
            />
          )}
          <span className="font-mono text-sm text-jul-text">{b.address}</span>
        </div>
      </td>
      <td className="px-3 py-2 text-sm text-jul-muted">{b.weight}</td>
      <td className="px-3 py-2 text-sm text-jul-muted">{b.inflight ?? "—"}</td>
      <td className="px-3 py-2 text-right">
        <button
          type="button"
          disabled={busy || !canRemove}
          title={canRemove ? "Remove this backend" : "Cannot remove the last backend"}
          onClick={onRemove}
          className="rounded-md border border-jul-border px-2 py-0.5 text-xs text-jul-danger hover:bg-jul-danger/10 disabled:opacity-40"
        >
          Remove →
        </button>
      </td>
    </tr>
  );
}

export interface AppDetailProps {
  readonly app: AppProjection;
  readonly onClose: () => void;
}

/** App / upstream detail view (Milestone 2.4): shows backends, health,
 * strategy, discovery, and which routes depend on this app. Backend add/remove
 * are true in-place edits via the structured patch API (Wave B): each opens a
 * diff in the Config editor to review and apply, rather than appending a draft. */
export function AppDetail({ app, onClose }: AppDetailProps) {
  const navigate = useNavigate();
  const total = app.backends.length;
  const healthy = app.backends.filter((b) => b.healthy === true).length;
  const unhealthy = app.backends.filter((b) => b.healthy === false).length;
  const backendsValue =
    total === 0
      ? "none"
      : healthy + unhealthy === 0
        ? `${String(total)} backends · health unknown`
        : `${String(healthy)}/${String(total)} healthy${unhealthy > 0 ? ` · ${String(unhealthy)} down` : ""}`;
  const [newAddr, setNewAddr] = useState("");
  const [newWeight, setNewWeight] = useState(1);
  const [strategy, setStrategy] = useState(app.strategy || "round_robin");
  const [editing, setEditing] = useState<null | "health" | "discovery">(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Discovery-backed pools manage their own backend set; manual add/remove only
  // applies to a static pool, so the controls are hidden when discovery is on.
  const isStatic = !app.discovery || app.discovery === "static";

  async function runPatch(patch: ConfigPatch): Promise<void> {
    setError(null);
    setBusy(true);
    try {
      const res = await patchConfig(patch);
      setPendingDraft({
        kind: "patch",
        ops: [patch],
        baseVersion: res.base_version,
        previewDiff: res.diff,
        candidate: res.candidate,
      });
      void navigate("/config");
    } catch (err) {
      setError(err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={app.name}
      subtitle={`${app.strategy} · ${String(app.backends.length)} backend(s)`}
      onClose={onClose}
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          An app is a named pool of backend instances. Routes proxy to it by name, and Jul balances
          traffic across healthy backends.
        </p>

        {app.warnings && app.warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-warning">
              Warnings
            </span>
            {app.warnings.map((wn, i) => (
              <p key={`aw-${String(i)}`} className="text-xs text-jul-text">
                {wn}
              </p>
            ))}
          </div>
        )}

        <div className="rounded-md border border-jul-border bg-jul-surface px-4 py-2">
          <Row label="Strategy" value={app.strategy} />
          <Row
            label="Backends"
            value={backendsValue}
          />
          <Row
            label="Health checks"
            value={
              app.health_check
                ? `on${app.health_check_path ? " · " + app.health_check_path : ""}`
                : "off"
            }
          />
          {app.health_check_interval && (
            <Row label="Probe interval" value={app.health_check_interval} />
          )}
          {app.max_fails ? <Row label="Max fails" value={String(app.max_fails)} /> : null}
          {app.fail_timeout && <Row label="Fail timeout" value={app.fail_timeout} />}
          <Row
            label="Discovery"
            value={
              app.discovery
                ? `${app.discovery}${app.discovery_target ? " · " + app.discovery_target : ""}`
                : "static"
            }
          />
        </div>

        <div className="space-y-3 rounded-md border border-jul-border bg-jul-surface p-4">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Pool settings (in place)
          </span>
          <div className="flex flex-wrap items-end gap-2">
            <label className="flex-1 space-y-1">
              <span className="text-xs text-jul-muted">Load-balancing strategy</span>
              <select
                value={strategy}
                onChange={(e) => {
                  setStrategy(e.target.value);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                {STRATEGIES.map((s) => (
                  <option key={s.value} value={s.value}>
                    {s.label}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              disabled={busy || strategy === (app.strategy || "round_robin")}
              onClick={() => {
                void runPatch({
                  op: "upstream_set_strategy",
                  upstream: app.name,
                  strategy,
                });
              }}
              className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              Apply →
            </button>
          </div>
          <div className="flex flex-wrap gap-2">
            <button
              type="button"
              onClick={() => {
                setEditing("health");
              }}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text hover:bg-jul-bg"
            >
              Edit health checks →
            </button>
            <button
              type="button"
              onClick={() => {
                setEditing("discovery");
              }}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text hover:bg-jul-bg"
            >
              Edit discovery →
            </button>
          </div>
          <span className="text-xs text-jul-muted">each opens a diff to review &amp; apply</span>
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Backends
          </span>
          {app.backends.length === 0 ? (
            <p className="text-xs text-jul-muted">No backends configured.</p>
          ) : (
            <table className="w-full overflow-hidden rounded-md border border-jul-border bg-jul-surface text-left">
              <thead>
                <tr className="border-b border-jul-border text-xs text-jul-muted">
                  <th className="px-3 py-2">Address</th>
                  <th className="px-3 py-2">Weight</th>
                  <th className="px-3 py-2">In-flight</th>
                  {isStatic && <th className="px-3 py-2" />}
                </tr>
              </thead>
              <tbody>
                {app.backends.map((b) => (
                  <BackendRow
                    key={b.address}
                    b={b}
                    canRemove={isStatic && app.backends.length > 1}
                    busy={busy}
                    onRemove={() => {
                      void runPatch({
                        op: "upstream_remove_backend",
                        upstream: app.name,
                        address: b.address,
                      });
                    }}
                  />
                ))}
              </tbody>
            </table>
          )}

          {isStatic && (
            <div className="space-y-1 rounded-md border border-jul-border bg-jul-surface p-3">
              <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
                Add backend (in place)
              </span>
              <div className="flex flex-wrap items-end gap-2">
                <label className="flex-1 space-y-1">
                  <span className="text-xs text-jul-muted">Address</span>
                  <input
                    type="text"
                    value={newAddr}
                    placeholder="10.0.0.2:8080"
                    onChange={(e) => {
                      setNewAddr(e.target.value);
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  />
                </label>
                <label className="w-24 space-y-1">
                  <span className="text-xs text-jul-muted">Weight</span>
                  <input
                    type="number"
                    min={1}
                    value={newWeight}
                    onChange={(e) => {
                      setNewWeight(Math.max(1, Number(e.target.value) || 1));
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  />
                </label>
                <button
                  type="button"
                  disabled={busy || newAddr.trim() === ""}
                  onClick={() => {
                    void runPatch({
                      op: "upstream_add_backend",
                      upstream: app.name,
                      address: newAddr.trim(),
                      weight: newWeight,
                    });
                  }}
                  className="rounded-md bg-jul-accent px-3 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
                >
                  Add →
                </button>
              </div>
              <span className="text-xs text-jul-muted">opens a diff to review &amp; apply</span>
            </div>
          )}

          {error && <p className="text-xs text-jul-danger">{error}</p>}
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Routes using this app
          </span>
          {app.routes_using && app.routes_using.length > 0 ? (
            <ul className="space-y-1">
              {app.routes_using.map((r, i) => (
                <li key={`ru-${String(i)}`} className="font-mono text-xs text-jul-text">
                  {r}
                </li>
              ))}
            </ul>
          ) : (
            <p className="text-xs text-jul-muted">No routes reference this app yet.</p>
          )}
        </div>
      </div>

      {editing === "health" && (
        <HealthCheckEditor
          app={app}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
      {editing === "discovery" && (
        <DiscoveryEditor
          app={app}
          onClose={() => {
            setEditing(null);
          }}
        />
      )}
    </Drawer>
  );
}
