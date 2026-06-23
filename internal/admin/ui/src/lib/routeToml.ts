// Client-side generators for the guided Route and App editors (Milestones 2.2
// and 2.5). They emit a TOML fragment (a complete [[servers]] or [[upstreams]]
// block) that is appended to the running configuration and then routed through
// the authoritative Validate → Diff → Apply → Rollback pipeline in the Config
// editor. The editors never write directly: invalid drafts are caught by the
// validated apply path, so the running config is never replaced by a bad edit.

export type RouteAction = "static" | "proxy" | "redirect" | "deny" | "return";

export interface RouteDraft {
  listen: string;
  serverNames: string;
  path: string;
  matchType: "prefix" | "exact" | "regex";
  action: RouteAction;
  target: string;
  auth: boolean;
  cache: boolean;
  compression: boolean;
  rateLimit: boolean;
}

function tomlString(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlStringArray(items: string[]): string {
  return `[${items.map((i) => tomlString(i)).join(", ")}]`;
}

/** Generates a complete [[servers]] block for a new route. */
export function generateRouteToml(d: RouteDraft): string {
  const lines: string[] = [];
  lines.push("[[servers]]");
  lines.push(`listen = ${tomlString(d.listen.trim() || ":8080")}`);
  const names = d.serverNames
    .split(",")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);
  if (names.length > 0) {
    lines.push(`server_names = ${tomlStringArray(names)}`);
  }
  lines.push("");
  lines.push("  [[servers.locations]]");
  lines.push(`  match = { type = ${tomlString(d.matchType)}, path = ${tomlString(d.path.trim() || "/")} }`);
  switch (d.action) {
    case "static":
      lines.push(`  root = ${tomlString(d.target.trim())}`);
      break;
    case "proxy":
      lines.push(`  proxy_pass = ${tomlString(d.target.trim())}`);
      break;
    case "redirect":
      lines.push(`  redirect = ${tomlString(d.target.trim())}`);
      break;
    case "deny":
      lines.push(`  deny = true`);
      break;
    case "return":
      lines.push(`  return = ${String(Number(d.target.trim()) || 200)}`);
      break;
  }
  if (d.cache) lines.push(`  cache = true`);
  if (d.rateLimit) lines.push(`  rate_limit = { enabled = true }`);
  if (d.auth) lines.push(`  auth = { }`);
  return lines.join("\n");
}

export interface BackendDraft {
  address: string;
  weight: number;
}

export interface AppDraft {
  name: string;
  strategy: "round_robin" | "weighted_round_robin" | "least_conn";
  backends: BackendDraft[];
  healthCheck: boolean;
  healthCheckPath: string;
  healthCheckInterval: string;
}

/** Generates a complete [[upstreams]] block for a new app/upstream pool. */
export function generateAppToml(d: AppDraft): string {
  const lines: string[] = [];
  lines.push("[[upstreams]]");
  lines.push(`name = ${tomlString(d.name.trim())}`);
  lines.push(`strategy = ${tomlString(d.strategy)}`);
  const servers = d.backends
    .filter((b) => b.address.trim().length > 0)
    .map((b) => (b.weight > 1 ? `${b.address.trim()} weight=${String(b.weight)}` : b.address.trim()));
  if (servers.length > 0) {
    lines.push(`servers = ${tomlStringArray(servers)}`);
  }
  if (d.healthCheck) {
    lines.push("");
    lines.push("  [upstreams.health_check]");
    lines.push("  enabled = true");
    lines.push(`  type = "http"`);
    if (d.healthCheckPath.trim()) {
      lines.push(`  path = ${tomlString(d.healthCheckPath.trim())}`);
    }
    if (d.healthCheckInterval.trim()) {
      lines.push(`  interval = ${tomlString(d.healthCheckInterval.trim())}`);
    }
  }
  return lines.join("\n");
}

/** Appends a generated TOML fragment to the running raw config. */
export function appendFragment(raw: string, fragment: string): string {
  const base = raw.trimEnd();
  return base.length > 0 ? `${base}\n\n${fragment}\n` : `${fragment}\n`;
}