import { Drawer } from "@/components/Drawer.tsx";
import type { AppProjection, BackendProjection } from "@/api/client.ts";

function Row({ label, value }: { readonly label: string; readonly value: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[150px_1fr] gap-2 py-1.5">
      <span className="text-xs uppercase tracking-wider text-jul-muted">{label}</span>
      <span className="text-sm text-jul-text">{value}</span>
    </div>
  );
}

function BackendRow({ b }: { readonly b: BackendProjection }) {
  return (
    <tr className="border-b border-jul-border last:border-b-0">
      <td className="px-3 py-2">
        <div className="flex items-center gap-2">
          {b.healthy !== undefined && (
            <span
              title={b.healthy ? "healthy" : "unhealthy"}
              className={`inline-block h-2 w-2 rounded-full ${b.healthy ? "bg-jul-success" : "bg-jul-danger"}`}
            />
          )}
          <span className="font-mono text-sm text-jul-text">{b.address}</span>
        </div>
      </td>
      <td className="px-3 py-2 text-sm text-jul-muted">{b.weight}</td>
      <td className="px-3 py-2 text-sm text-jul-muted">{b.inflight ?? "—"}</td>
    </tr>
  );
}

export interface AppDetailProps {
  readonly app: AppProjection;
  readonly onClose: () => void;
}

/** App / upstream detail view (Milestone 2.4): shows backends, health,
 * strategy, discovery, and which routes depend on this app. */
export function AppDetail({ app, onClose }: AppDetailProps) {
  const healthy = app.backends.filter((b) => b.healthy !== false).length;

  return (
    <Drawer title={app.name} subtitle={`${app.strategy} · ${String(app.backends.length)} backend(s)`} onClose={onClose}>
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          An app is a named pool of backend instances. Routes proxy to it by name, and
          Jul balances traffic across healthy backends.
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
          <Row label="Backends" value={`${String(healthy)}/${String(app.backends.length)} healthy`} />
          <Row label="Health checks" value={app.health_check ? `on${app.health_check_path ? " · " + app.health_check_path : ""}` : "off"} />
          {app.health_check_interval && <Row label="Probe interval" value={app.health_check_interval} />}
          {app.max_fails ? <Row label="Max fails" value={String(app.max_fails)} /> : null}
          {app.fail_timeout && <Row label="Fail timeout" value={app.fail_timeout} />}
          <Row label="Discovery" value={app.discovery ? `${app.discovery}${app.discovery_target ? " · " + app.discovery_target : ""}` : "static"} />
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
                </tr>
              </thead>
              <tbody>
                {app.backends.map((b) => (
                  <BackendRow key={b.address} b={b} />
                ))}
              </tbody>
            </table>
          )}
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
    </Drawer>
  );
}