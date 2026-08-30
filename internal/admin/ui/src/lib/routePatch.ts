/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type {
  ConfigPatch,
  LocationActionPatch,
  LocationAuthPatch,
  LocationProjection,
  RateLimitPatch,
  RouteProjection,
  RouteTarget,
} from "@/api/client.ts";

/** A server block's exact structured-patch identity. */
export interface ServerIdentity {
  readonly listen: string;
  readonly serverNames: readonly string[];
}

/** A location's exact identity inside one exact server block. */
export interface LocationIdentity {
  readonly matchType: string;
  readonly path: string;
  // routeId is the route's durable identity (ADR 0019 §4), when the server
  // sent one. The Console never derives its own correlation logic from it —
  // it is simply preferred over the revision-relative matchType+path key
  // when present, since it is stable across edits that change the match.
  readonly routeId?: string;
}

export interface RouteSelection {
  readonly route: RouteProjection;
  readonly loc: LocationProjection;
}

export interface ParsedServerNames {
  readonly names: string[];
  readonly errors: string[];
}

export interface ParsedPluginNames {
  readonly names: string[];
  readonly errors: string[];
}

export type StructuredRouteAction = LocationActionPatch;

export interface RouteCreateSpec {
  readonly server: ServerIdentity;
  readonly matchType: "prefix" | "exact" | "regex";
  readonly path: string;
  readonly action: StructuredRouteAction;
  readonly auth?: LocationAuthPatch | null | undefined;
  readonly cache?: boolean | undefined;
  readonly rateLimit?: RateLimitPatch | null | undefined;
  /** Middleware plugins, in the exact declaration order requested by the operator. */
  readonly plugins?: readonly string[] | undefined;
}

export interface StoredRouteSelectionV2 {
  readonly version: 2;
  readonly server: {
    readonly listen: string;
    readonly server_names: string[];
  };
  readonly location: {
    readonly match_type: string;
    readonly path: string;
  };
}

export class RoutePatchValidationError extends Error {
  constructor(public readonly issues: readonly string[]) {
    super(issues.join(" "));
    this.name = "RoutePatchValidationError";
  }
}

function compareCodeUnits(a: string, b: string): number {
  return a < b ? -1 : a > b ? 1 : 0;
}

/**
 * Returns a deterministic display/key order without changing case sensitivity.
 * Duplicate values are retained: callers validating operator input must reject
 * them rather than silently deduplicating an ambiguous identity.
 */
export function canonicalServerNames(names: readonly string[]): string[] {
  return [...names].sort(compareCodeUnits);
}

/** Parse the comma-separated server-name field and reject blank/duplicate names. */
export function parseServerNamesInput(raw: string): ParsedServerNames {
  if (raw.trim() === "") return { names: [], errors: [] };

  const pieces = raw.split(",").map((name) => name.trim());
  const errors: string[] = [];
  if (pieces.some((name) => name === "")) {
    errors.push("Server names cannot contain blank entries.");
  }
  if (pieces.some((name) => /\s/.test(name))) {
    errors.push("Each server name must be one host pattern without whitespace.");
  }

  const seen = new Set<string>();
  const duplicates = new Set<string>();
  for (const name of pieces) {
    if (name === "") continue;
    if (seen.has(name)) duplicates.add(name);
    seen.add(name);
  }
  if (duplicates.size > 0) {
    errors.push(
      `Duplicate server ${duplicates.size === 1 ? "name" : "names"}: ${canonicalServerNames([...duplicates]).join(", ")}.`,
    );
  }

  return errors.length === 0
    ? { names: canonicalServerNames(pieces), errors }
    : { names: [], errors };
}

/** Parse comma/newline-separated middleware plugin names without reordering them. */
export function parsePluginNamesInput(raw: string): ParsedPluginNames {
  if (raw.trim() === "") return { names: [], errors: [] };

  const pieces = raw.split(/[\n,]/).map((name) => name.trim());
  const errors: string[] = [];
  if (pieces.some((name) => name === "")) {
    errors.push("Plugin names cannot contain blank entries.");
  }

  const seen = new Set<string>();
  const duplicates = new Set<string>();
  for (const name of pieces) {
    if (name === "") continue;
    if (seen.has(name)) duplicates.add(name);
    seen.add(name);
  }
  if (duplicates.size > 0) {
    errors.push(
      `Duplicate plugin ${duplicates.size === 1 ? "name" : "names"}: ${[...duplicates].sort(compareCodeUnits).join(", ")}.`,
    );
  }

  return errors.length === 0 ? { names: pieces, errors } : { names: [], errors };
}

/** Exact identity is listen plus an order-independent, case-sensitive name set. */
export function sameServerIdentity(a: ServerIdentity, b: ServerIdentity): boolean {
  if (a.listen !== b.listen) return false;
  const left = canonicalServerNames(a.serverNames);
  const right = canonicalServerNames(b.serverNames);
  return left.length === right.length && left.every((name, index) => name === right[index]);
}

export function serverIdentityFromRoute(route: RouteProjection): ServerIdentity {
  return {
    listen: route.listen,
    serverNames: canonicalServerNames(route.server_names ?? []),
  };
}

/** Collision-safe key used for React identity and browser-session storage. */
export function serverIdentityKey(identity: ServerIdentity): string {
  return JSON.stringify([identity.listen, canonicalServerNames(identity.serverNames)]);
}

export function routeIdentityKey(server: ServerIdentity, location: LocationIdentity): string {
  if (location.routeId) {
    // A durable route_id is stable across a match/predicate change, unlike
    // the fingerprint below; it also does not depend on the server
    // identity, since a route_id is unique across the whole configuration
    // (ADR 0019 §4), but keeping the same JSON.stringify shape as the
    // fallback keeps this a plain opaque string key either way.
    return JSON.stringify(["route_id", location.routeId]);
  }
  return JSON.stringify([
    server.listen,
    canonicalServerNames(server.serverNames),
    location.matchType,
    location.path,
  ]);
}

export function formatServerIdentity(identity: ServerIdentity): string {
  const names = canonicalServerNames(identity.serverNames);
  return `${identity.listen} · ${names.length > 0 ? names.join(", ") : "any host"}`;
}

/** Return the one exact server identity, or null for missing/ambiguous data. */
export function findExactServer(
  routes: readonly RouteProjection[],
  identity: ServerIdentity,
): RouteProjection | null {
  const matches = routes.filter((route) =>
    sameServerIdentity(serverIdentityFromRoute(route), identity),
  );
  return matches.length === 1 ? (matches[0] ?? null) : null;
}

export function exactServerExists(
  routes: readonly RouteProjection[],
  identity: ServerIdentity,
): boolean {
  return routes.some((route) => sameServerIdentity(serverIdentityFromRoute(route), identity));
}

function normalizedServerIdentity(identity: ServerIdentity): ServerIdentity {
  if (identity.listen.trim() === "") {
    throw new RoutePatchValidationError(["A listener address is required."]);
  }

  const names = [...identity.serverNames];
  const issues: string[] = [];
  if (names.some((name) => name === "")) {
    issues.push("Server names cannot contain blank entries.");
  }
  if (names.some((name) => /\s/.test(name))) {
    issues.push("Each server name must be one host pattern without whitespace.");
  }
  const seen = new Set<string>();
  const duplicates = new Set<string>();
  for (const name of names) {
    if (seen.has(name)) duplicates.add(name);
    seen.add(name);
  }
  if (duplicates.size > 0) {
    issues.push(
      `Duplicate server ${duplicates.size === 1 ? "name" : "names"}: ${canonicalServerNames([...duplicates]).join(", ")}.`,
    );
  }
  if (issues.length > 0) throw new RoutePatchValidationError(issues);
  return { listen: identity.listen, serverNames: canonicalServerNames(names) };
}

function normalizedPlugins(plugins: readonly string[] | undefined): string[] {
  if (!plugins || plugins.length === 0) return [];
  const parsed = parsePluginNamesInput(plugins.join(","));
  if (parsed.errors.length > 0) throw new RoutePatchValidationError(parsed.errors);
  return parsed.names;
}

function validateAuthPatch(auth: LocationAuthPatch | null | undefined): string[] {
  if (!auth) return [];
  switch (auth.method) {
    case "cidr":
      return [...(auth.allow ?? []), ...(auth.deny ?? [])].some((entry) => entry.trim() !== "")
        ? []
        : ["CIDR authentication needs at least one allow or deny entry."];
    case "basic":
      return auth.basic_file?.trim() ? [] : ["Basic authentication needs an htpasswd file path."];
    case "jwt": {
      const url = auth.jwt_jwks_url?.trim() ?? "";
      if (url === "") return ["JWT authentication needs a JWKS URL."];
      return url.startsWith("https://") ? [] : ["The JWT JWKS URL must use https://."];
    }
    case "forward": {
      const url = auth.forward_url?.trim() ?? "";
      if (url === "") return ["Forward authentication needs a decision endpoint URL."];
      return /^https?:\/\//.test(url) ? [] : ["The forward-auth URL must use http:// or https://."];
    }
  }
}

/** Near-side validation for structured action/modifier combinations. */
export function validateStructuredRouteSpec(spec: RouteCreateSpec): string[] {
  const errors: string[] = [];
  if (spec.server.listen.trim() === "") errors.push("A listener address is required.");

  const serverNames = parseServerNamesInput(spec.server.serverNames.join(","));
  errors.push(...serverNames.errors);

  const path = spec.path.trim();
  if (path === "") {
    errors.push("A route match path is required.");
  } else if (spec.matchType !== "regex" && !path.startsWith("/")) {
    errors.push("A prefix or exact match path must start with '/'.");
  }

  // Jul validates and executes route regexes with Go's regexp/RE2 grammar.
  // Browser-side JavaScript compilation would reject valid RE2 constructs and
  // accept JavaScript-only syntax, so the shared server preview remains the
  // grammar authority for regex matches.

  // Keep the runtime boundary closed because session data and JavaScript callers
  // are not trusted to obey the compile-time discriminated union.
  const action = spec.action;
  switch (action.kind) {
    case "proxy":
      if (action.target.trim() === "") errors.push("The HTTP proxy action needs a target.");
      break;
    case "static":
      if (action.target.trim() === "") errors.push("The static action needs a root directory.");
      if (spec.cache) errors.push("Route cache cannot be enabled for a static-file action.");
      break;
    case "redirect":
      if (action.target.trim() === "") errors.push("The redirect action needs a target URL.");
      if (
        action.status !== undefined &&
        (!Number.isInteger(action.status) || action.status < 300 || action.status > 399)
      ) {
        errors.push("A redirect status must be in the 3xx range.");
      }
      break;
    case "return":
      if (!Number.isInteger(action.status) || action.status < 100 || action.status > 599) {
        errors.push("The fixed response status must be a valid HTTP status (100–599).");
      }
      break;
    case "deny":
      break;
    case "grpc_proxy":
      errors.push(
        "Native gRPC is not represented by the generic structured Route creator. Use the protocol-specific gRPC workflow or raw configuration so the required server protocol settings are preserved.",
      );
      break;
    default:
      errors.push("This route action is not supported by the structured Route creator.");
  }

  errors.push(...validateAuthPatch(spec.auth));
  if (spec.rateLimit?.enabled) {
    if ((spec.rateLimit.rate ?? 0) <= 0) errors.push("Rate limit must be greater than zero.");
    if ((spec.rateLimit.burst ?? 0) <= 0)
      errors.push("Rate-limit burst must be greater than zero.");
  }
  errors.push(...parsePluginNamesInput((spec.plugins ?? []).join(",")).errors);
  return errors;
}

function routeTarget(spec: RouteCreateSpec, server: ServerIdentity): RouteTarget {
  return {
    listen: server.listen,
    server_names: [...server.serverNames],
    match_type: spec.matchType,
    path: spec.path.trim(),
  };
}

function buildLocationAndModifiers(spec: RouteCreateSpec, server: ServerIdentity): ConfigPatch[] {
  const issues = validateStructuredRouteSpec({ ...spec, server });
  if (issues.length > 0) throw new RoutePatchValidationError(issues);

  const target = routeTarget(spec, server);
  const ops: ConfigPatch[] = [
    {
      op: "location_add",
      listen: target.listen,
      server_names: target.server_names,
      match_set: { type: spec.matchType, path: target.path },
      action: spec.action,
    },
  ];

  // Modifier order is a public, tested contract. Plugin attachment order is the
  // operator's requested middleware-chain order and is preserved exactly.
  if (spec.auth) ops.push({ op: "location_set_auth", ...target, auth: spec.auth });
  if (spec.cache) ops.push({ op: "route_toggle_cache", ...target, enabled: true });
  if (spec.rateLimit) {
    ops.push({ op: "route_set_rate_limit", ...target, rate_limit: spec.rateLimit });
  }
  for (const pluginName of normalizedPlugins(spec.plugins)) {
    ops.push({ op: "location_attach_plugin", ...target, plugin_name: pluginName });
  }
  return ops;
}

/** Existing-server mode: location_add is always the first operation. */
export function buildExistingServerRouteBatch(
  spec: RouteCreateSpec,
  inventory: readonly RouteProjection[],
): ConfigPatch[] {
  const server = normalizedServerIdentity(spec.server);
  const existing = findExactServer(inventory, server);
  if (existing === null) {
    throw new RoutePatchValidationError([
      `The selected server no longer exists: ${formatServerIdentity(server)}. Refresh and choose it again.`,
    ]);
  }
  const path = spec.path.trim();
  if (
    existing.locations.some(
      (location) => location.type === spec.matchType && location.match === path,
    )
  ) {
    throw new RoutePatchValidationError([
      `This exact server already has a ${spec.matchType} route for ${path}. Choose a different match.`,
    ]);
  }
  return buildLocationAndModifiers(spec, server);
}

/** New-server mode: server_add, location_add, then deterministic modifiers. */
export function buildNewServerRouteBatch(
  spec: RouteCreateSpec,
  inventory: readonly RouteProjection[],
): ConfigPatch[] {
  const server = normalizedServerIdentity(spec.server);
  if (exactServerExists(inventory, server)) {
    throw new RoutePatchValidationError([
      `A server with this exact identity already exists: ${formatServerIdentity(server)}. Choose existing-server mode instead.`,
    ]);
  }
  return [
    { op: "server_add", listen: server.listen, server_names: [...server.serverNames] },
    ...buildLocationAndModifiers(spec, server),
  ];
}

export function buildRouteRemovalBatch(
  server: ServerIdentity,
  location: LocationIdentity,
): ConfigPatch[] {
  const exact = normalizedServerIdentity(server);
  const path = location.path;
  if (path.trim() === "") throw new RoutePatchValidationError(["The route path is required."]);
  return [
    {
      op: "location_remove",
      listen: exact.listen,
      server_names: [...exact.serverNames],
      match_type: location.matchType,
      path,
    },
  ];
}

export function buildServerRemovalBatch(server: ServerIdentity): ConfigPatch[] {
  const exact = normalizedServerIdentity(server);
  return [
    {
      op: "server_remove",
      listen: exact.listen,
      server_names: [...exact.serverNames],
    },
  ];
}

export function storeRouteSelection(selection: RouteSelection): StoredRouteSelectionV2 {
  const server = serverIdentityFromRoute(selection.route);
  return storeRouteIdentity(server, {
    matchType: selection.loc.type,
    path: selection.loc.match,
  });
}

/** Store an exact target identity for post-preview/post-mutation restoration. */
export function storeRouteIdentity(
  server: ServerIdentity,
  location: LocationIdentity,
): StoredRouteSelectionV2 {
  return {
    version: 2,
    server: {
      listen: server.listen,
      server_names: canonicalServerNames(server.serverNames),
    },
    location: { match_type: location.matchType, path: location.path.trim() },
  };
}

function stringArray(value: unknown): string[] | null {
  if (!Array.isArray(value) || !value.every((item) => typeof item === "string")) return null;
  return value;
}

function objectValue(value: unknown): Record<string, unknown> | null {
  return typeof value === "object" && value !== null ? (value as Record<string, unknown>) : null;
}

function findLocation(
  route: RouteProjection,
  matchType: string,
  path: string,
): LocationProjection | null {
  const matches = route.locations.filter((loc) => loc.type === matchType && loc.match === path);
  return matches.length === 1 ? (matches[0] ?? null) : null;
}

/**
 * Restore the current v2 shape and the previous Selection-shaped value. A
 * listen-only legacy value is migrated only when that listen is unambiguous;
 * otherwise it fails closed rather than selecting a sibling virtual host.
 */
export function restoreRouteSelection(
  routes: readonly RouteProjection[],
  stored: unknown,
): RouteSelection | null {
  const root = objectValue(stored);
  if (!root) return null;

  if (root.version === 2) {
    const server = objectValue(root.server);
    const location = objectValue(root.location);
    if (!server || !location) return null;
    if (typeof server.listen !== "string") return null;
    const names = stringArray(server.server_names);
    if (names === null) return null;
    if (typeof location.match_type !== "string" || typeof location.path !== "string") return null;

    const route = findExactServer(routes, { listen: server.listen, serverNames: names });
    if (!route) return null;
    const loc = findLocation(route, location.match_type, location.path);
    return loc ? { route, loc } : null;
  }

  // Legacy shape: { route: RouteProjection-ish, loc: LocationProjection-ish }.
  const legacyRoute = objectValue(root.route);
  const legacyLoc = objectValue(root.loc);
  if (!legacyRoute || !legacyLoc || typeof legacyRoute.listen !== "string") return null;

  let route: RouteProjection | null = null;
  if (Object.prototype.hasOwnProperty.call(legacyRoute, "server_names")) {
    const names = stringArray(legacyRoute.server_names);
    if (names === null) return null;
    route = findExactServer(routes, { listen: legacyRoute.listen, serverNames: names });
  } else {
    const matches = routes.filter((candidate) => candidate.listen === legacyRoute.listen);
    route = matches.length === 1 ? (matches[0] ?? null) : null;
  }
  if (!route) return null;

  if (typeof legacyLoc.type === "string" && typeof legacyLoc.match === "string") {
    const loc = findLocation(route, legacyLoc.type, legacyLoc.match);
    if (loc) return { route, loc };
  }
  // The pre-#78 session shape stored a location index. It is migrated only
  // after the parent listener has resolved to one unambiguous server block;
  // ambiguous same-listen virtual hosts already failed closed above.
  if (typeof legacyLoc.index === "number" && Number.isInteger(legacyLoc.index)) {
    const loc = route.locations[legacyLoc.index];
    if (loc) return { route, loc };
  }
  return null;
}
