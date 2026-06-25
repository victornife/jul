import { Drawer } from "@/components/Drawer.tsx";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";

function Row({ label, value }: { readonly label: string; readonly value: React.ReactNode }) {
  return (
    <div className="grid grid-cols-[140px_1fr] gap-2 py-1.5">
      <span className="text-xs uppercase tracking-wider text-jul-muted">{label}</span>
      <span className="text-sm text-jul-text">{value}</span>
    </div>
  );
}

function Flag({ on, label }: { readonly on: boolean; readonly label: string }) {
  return (
    <span
      className={`inline-block rounded-full px-2 py-0.5 text-xs ${
        on ? "bg-jul-success/15 text-jul-success" : "bg-jul-border/40 text-jul-muted"
      }`}
    >
      {label}: {on ? "on" : "off"}
    </span>
  );
}

function describe(action: string): string {
  switch (action) {
    case "proxy":
      return "This route proxies traffic to an app. Jul receives the request, applies edge rules, and forwards it to the selected upstream.";
    case "grpc":
      return "This route proxies native gRPC traffic end-to-end over HTTP/2.";
    case "grpc_transcode":
      return "This route transcodes REST/JSON requests to gRPC and returns the reply as JSON.";
    case "static":
      return "This route serves static files from a directory on disk.";
    case "redirect":
      return "This route redirects matching requests to another URL.";
    case "deny":
      return "This route rejects matching requests with 403 Forbidden.";
    case "return":
      return "This route returns a fixed HTTP status code.";
    case "fastcgi":
      return "This route forwards requests to a FastCGI/uWSGI application.";
    default:
      return "This route's action is not recognized.";
  }
}

// generatedFragment renders the effective TOML for one location so the operator
// sees the effective config, not just raw config (Milestone 2.1 criterion).
function generatedFragment(route: RouteProjection, loc: LocationProjection): string {
  const lines: string[] = ["[[servers]]", `listen = "${route.listen}"`];
  if (route.server_names && route.server_names.length > 0) {
    lines.push(`server_names = [${route.server_names.map((n) => `"${n}"`).join(", ")}]`);
  }
  lines.push("");
  lines.push("  [[servers.locations]]");
  lines.push(`  match = { type = "${loc.type}", path = "${loc.match}" }`);
  if (loc.target) {
    const key =
      loc.action === "static"
        ? "root"
        : loc.action === "redirect"
          ? "redirect"
          : loc.action === "return"
            ? "return"
            : "proxy_pass";
    const val = loc.action === "return" ? loc.target : `"${loc.target}"`;
    lines.push(`  ${key} = ${val}`);
  }
  if (loc.action === "deny") lines.push("  deny = true");
  if (loc.cache) lines.push("  cache = true");
  if (loc.rate_limit) lines.push("  rate_limit = { enabled = true }");
  // The route projection reports only that an auth policy is present, not its
  // method or parameters (which can carry secrets). Show a placeholder comment
  // rather than a literal "auth = {}", which would read as a valid—but inert,
  // allow-all—block and misrepresent the effective policy.
  if (loc.auth)
    lines.push("  # auth = { … }  (a policy is configured; see the raw config for details)");
  return lines.join("\n");
}

export interface RouteDetailProps {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
  readonly onClose: () => void;
  readonly onEdit: () => void;
}

/** Route detail drawer (Milestone 2.1): explains what a route does, shows its
 * effective config and any warnings, without the operator reading TOML. */
export function RouteDetail({ route, loc, onClose, onEdit }: RouteDetailProps) {
  return (
    <Drawer
      title={`${loc.type} ${loc.match}`}
      subtitle={`${route.listen}${route.server_names && route.server_names.length > 0 ? " · " + route.server_names.join(", ") : ""}`}
      onClose={onClose}
      footer={
        <button
          type="button"
          onClick={onEdit}
          className="ml-auto block rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-text hover:bg-jul-surface"
        >
          Edit as new route →
        </button>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          {describe(loc.action)}
        </p>

        {loc.warnings && loc.warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            <span className="text-xs font-semibold uppercase tracking-wider text-jul-warning">
              Warnings
            </span>
            {loc.warnings.map((wn, i) => (
              <p key={`w-${String(i)}`} className="text-xs text-jul-text">
                {wn}
              </p>
            ))}
          </div>
        )}

        <div className="rounded-md border border-jul-border bg-jul-surface px-4 py-2">
          <Row label="Listener" value={<span className="font-mono">{route.listen}</span>} />
          <Row
            label="Host names"
            value={
              route.server_names && route.server_names.length > 0
                ? route.server_names.join(", ")
                : "any host"
            }
          />
          <Row label="Path match" value={<span className="font-mono">{loc.match}</span>} />
          <Row label="Match type" value={loc.type} />
          <Row label="Action" value={loc.action} />
          {loc.target && (
            <Row label="Target" value={<span className="font-mono">{loc.target}</span>} />
          )}
          {loc.upstream && (
            <Row label="Upstream" value={<span className="font-mono">{loc.upstream}</span>} />
          )}
          <Row
            label="TLS"
            value={route.tls?.enabled ? `enabled${route.tls.acme ? " (ACME)" : ""}` : "off"}
          />
        </div>

        <div className="flex flex-wrap gap-2">
          <Flag on={loc.auth} label="auth" />
          <Flag on={loc.cache} label="cache" />
          <Flag on={loc.compression} label="compression" />
          <Flag on={loc.rate_limit} label="rate limit" />
          <Flag on={loc.secure} label="TLS" />
        </div>

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Generated TOML
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {generatedFragment(route, loc)}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}
