/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

// Compatibility TOML generator for explicit raw/import Route handoffs.
// Structured Route and App creation use deterministic typed patch batches in
// routePatch.ts and appPatch.ts; no App TOML generator remains.

export type RouteAction = "static" | "proxy" | "grpc" | "redirect" | "deny" | "return";

// AuthMethod selects the concrete access-control mechanism for a route. "none"
// emits no auth block at all. Every other value produces a policy that actually
// enforces something — the guided editor never emits a credential-less
// "auth = {}", which the server treats as "allow all" while the Console would
// report the route as protected (the inert-auth trap rejected by validateAuth).
export type AuthMethod = "none" | "cidr" | "basic" | "jwt" | "forward";

export interface AuthDraft {
  method: AuthMethod;
  // cidr method
  allow: string; // comma/space/newline-separated CIDRs
  deny: string; // comma/space/newline-separated CIDRs
  // basic method
  basicFile: string; // htpasswd path
  basicRealm: string; // optional realm
  // jwt method
  jwtJwksUrl: string; // https JWKS endpoint
  jwtIssuer: string; // optional iss
  jwtAudience: string; // optional aud
  // forward method
  forwardUrl: string; // http(s) decision endpoint
}

export function emptyAuthDraft(): AuthDraft {
  return {
    method: "none",
    allow: "",
    deny: "",
    basicFile: "",
    basicRealm: "",
    jwtJwksUrl: "",
    jwtIssuer: "",
    jwtAudience: "",
    forwardUrl: "",
  };
}

// AUTH_METHODS lists the selectable access-control mechanisms, shared by the
// route-creation form and the per-location AuthEditor (which drops "none" in
// favour of its explicit Clear action). Kept in this component-free module so it
// can be reused without tripping the react-refresh only-export-components rule.
export const AUTH_METHODS: { value: AuthMethod; label: string; hint: string }[] = [
  { value: "none", label: "No authentication", hint: "Anyone who matches the route is allowed." },
  {
    value: "cidr",
    label: "IP allow / deny (CIDR)",
    hint: "Permit or reject clients by IP range. Deny wins over allow.",
  },
  {
    value: "basic",
    label: "HTTP Basic (htpasswd)",
    hint: "Username/password checked against a bcrypt htpasswd file.",
  },
  {
    value: "jwt",
    label: "JWT bearer (JWKS)",
    hint: "Validate bearer tokens against an issuer's JWKS endpoint.",
  },
  {
    value: "forward",
    label: "Forward-auth (external)",
    hint: "Delegate the decision to an external HTTP endpoint.",
  },
];

export interface RouteDraft {
  listen: string;
  serverNames: string;
  path: string;
  matchType: "prefix" | "exact" | "regex";
  action: RouteAction;
  target: string;
  auth: AuthDraft;
  cache: boolean;
  rateLimit: boolean;
}

function tomlString(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function tomlStringArray(items: string[]): string {
  return `[${items.map((i) => tomlString(i)).join(", ")}]`;
}

// splitList parses an operator-friendly list (comma, whitespace, or newline
// separated) into trimmed, non-empty entries.
function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

// authLines renders the per-location auth fields for a draft. It returns an
// empty array for the "none" method (no auth block at all), and otherwise emits
// only fields that enforce access control. It never emits a bare "auth = {}".
function authLines(a: AuthDraft): string[] {
  const out: string[] = [];
  switch (a.method) {
    case "none":
      return out;
    case "cidr": {
      const allow = splitList(a.allow);
      const deny = splitList(a.deny);
      if (allow.length > 0) out.push(`  auth.allow = ${tomlStringArray(allow)}`);
      if (deny.length > 0) out.push(`  auth.deny = ${tomlStringArray(deny)}`);
      return out;
    }
    case "basic": {
      out.push(
        `  auth.basic = { file = ${tomlString(a.basicFile.trim())}${
          a.basicRealm.trim() ? `, realm = ${tomlString(a.basicRealm.trim())}` : ""
        } }`,
      );
      return out;
    }
    case "jwt": {
      const parts = [`jwks_url = ${tomlString(a.jwtJwksUrl.trim())}`];
      if (a.jwtIssuer.trim()) parts.push(`issuer = ${tomlString(a.jwtIssuer.trim())}`);
      if (a.jwtAudience.trim()) parts.push(`audience = ${tomlString(a.jwtAudience.trim())}`);
      out.push(`  auth.jwt = { ${parts.join(", ")} }`);
      return out;
    }
    case "forward": {
      out.push(`  auth.forward_auth = { url = ${tomlString(a.forwardUrl.trim())} }`);
      return out;
    }
  }
}

// authWarnings reports human-readable problems with an auth draft so the editor
// can warn before the operator opens the diff (the server validates too, but a
// near-side hint avoids a wasted round-trip). An empty array means "looks ok".
export function authWarnings(a: AuthDraft): string[] {
  const warn: string[] = [];
  switch (a.method) {
    case "none":
      break;
    case "cidr":
      if (splitList(a.allow).length === 0 && splitList(a.deny).length === 0) {
        warn.push("Add at least one allow or deny CIDR, or the policy permits every client.");
      }
      break;
    case "basic":
      if (!a.basicFile.trim()) warn.push("Basic auth needs an htpasswd file path.");
      break;
    case "jwt": {
      const u = a.jwtJwksUrl.trim();
      if (!u) warn.push("JWT auth needs a JWKS URL.");
      else if (!u.startsWith("https://")) warn.push("The JWKS URL must be an https:// URL.");
      break;
    }
    case "forward": {
      const u = a.forwardUrl.trim();
      if (!u) warn.push("Forward-auth needs a decision endpoint URL.");
      else if (!/^https?:\/\//.test(u))
        warn.push("The forward-auth URL must be an http(s):// URL.");
      break;
    }
  }
  return warn;
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
  lines.push(
    `  match = { type = ${tomlString(d.matchType)}, path = ${tomlString(d.path.trim() || "/")} }`,
  );
  switch (d.action) {
    case "static":
      lines.push(`  root = ${tomlString(d.target.trim())}`);
      break;
    case "proxy":
      lines.push(`  proxy_pass = ${tomlString(d.target.trim())}`);
      break;
    case "grpc":
      lines.push(`  proxy_pass = ${tomlString(d.target.trim())}`);
      lines.push(`  grpc = true`);
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
  if (d.rateLimit)
    lines.push(`  rate_limit = { enabled = true, rate = 100, burst = 100, key = "ip" }`);
  for (const line of authLines(d.auth)) lines.push(line);
  return lines.join("\n");
}

/**
 * Raw/import compatibility helper. Structured Route and App workflows do not
 * call this path; they preview typed ConfigPatch operations instead.
 */
/** Appends a generated TOML fragment to the running raw config. */
export function appendFragment(raw: string, fragment: string): string {
  const base = raw.trimEnd();
  return base.length > 0 ? `${base}\n\n${fragment}\n` : `${fragment}\n`;
}
