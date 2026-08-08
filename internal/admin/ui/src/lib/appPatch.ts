/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { AppProjection, ConfigPatch, RouteProjection } from "@/api/client.ts";
import {
  discoveryToPatch,
  discoveryWarnings,
  healthCheckToPatch,
  healthCheckWarnings,
  type DiscoveryDraft,
  type HealthCheckDraft,
} from "@/lib/appSettings.ts";
import {
  canonicalServerNames,
  exactServerExists,
  findExactServer,
  formatServerIdentity,
  parseServerNamesInput,
  type ServerIdentity,
} from "@/lib/routePatch.ts";

export type AppStrategy = "round_robin" | "weighted_round_robin" | "least_conn";
export type AppProtocol = "http" | "grpc";
export type AppRouteMatchType = "prefix" | "exact";

export interface AppBackendDraft {
  readonly address: string;
  readonly weight: number;
}

interface MountedAppRoute {
  readonly protocol: AppProtocol;
  readonly matchType: AppRouteMatchType;
  readonly path: string;
}

export type AppMountDraft =
  | { readonly mode: "none" }
  | (MountedAppRoute & {
      readonly mode: "existing";
      readonly server: ServerIdentity;
    })
  | (MountedAppRoute & {
      readonly mode: "new";
      readonly server: ServerIdentity;
    });

export interface AppDiscoveryDraft {
  readonly settings: DiscoveryDraft;
  /**
   * Creation cannot send a new Consul/Kubernetes token through typed patch
   * DTOs. When authentication is required, fail closed and direct the operator
   * to the separately authorized raw editor instead of silently omitting it.
   */
  readonly requiresNewToken: boolean;
}

export interface AppCreateDraft {
  readonly name: string;
  readonly strategy: AppStrategy;
  readonly backends: readonly AppBackendDraft[];
  readonly healthCheck?: HealthCheckDraft | undefined;
  readonly discovery?: AppDiscoveryDraft | undefined;
  readonly mount: AppMountDraft;
}

export interface AppPatchInventory {
  readonly apps: readonly AppProjection[];
  readonly routes: readonly RouteProjection[];
}

export class AppPatchValidationError extends Error {
  constructor(
    public readonly issues: readonly string[],
    public readonly rawEditorRequired = false,
  ) {
    super(issues.join(" "));
    this.name = "AppPatchValidationError";
  }
}

function normalizedWeight(weight: number): number {
  return Number.isInteger(weight) && weight > 0 ? weight : 1;
}

function normalizedServer(identity: ServerIdentity): ServerIdentity {
  const parsed = parseServerNamesInput(identity.serverNames.join(","));
  const issues = [...parsed.errors];
  const listen = identity.listen.trim();
  if (listen === "") issues.unshift("A listener address is required.");
  if (issues.length > 0) throw new AppPatchValidationError(issues);
  return { listen, serverNames: parsed.names };
}

function normalizedRoutePath(matchType: AppRouteMatchType, raw: string): string {
  const path = raw.trim();
  if (path === "") throw new AppPatchValidationError(["A route match path is required."]);
  if (!path.startsWith("/")) {
    throw new AppPatchValidationError([`An App ${matchType} route path must start with '/'.`]);
  }
  return path;
}

function routeCollision(
  route: RouteProjection,
  matchType: AppRouteMatchType,
  path: string,
): boolean {
  return route.locations.some((location) => location.type === matchType && location.match === path);
}

function locationOperation(
  name: string,
  server: ServerIdentity,
  protocol: AppProtocol,
  matchType: AppRouteMatchType,
  path: string,
): ConfigPatch {
  return {
    op: "location_add",
    listen: server.listen,
    server_names: canonicalServerNames(server.serverNames),
    match_set: { type: matchType, path },
    action:
      protocol === "grpc"
        ? { kind: "grpc_proxy", target: `http://${name}` }
        : { kind: "proxy", target: `http://${name}` },
  };
}

function appendHealthCheck(ops: ConfigPatch[], name: string, draft?: HealthCheckDraft): void {
  if (!draft?.enabled) return;
  const issues = healthCheckWarnings(draft);
  if (issues.length > 0) throw new AppPatchValidationError(issues);
  ops.push({
    op: "upstream_set_health_check",
    upstream: name,
    health_check: healthCheckToPatch(draft),
  });
}

function appendDiscovery(ops: ConfigPatch[], name: string, draft?: AppDiscoveryDraft): void {
  if (!draft || draft.settings.type === "static") return;
  const issues = discoveryWarnings(draft.settings);
  if (issues.length > 0) throw new AppPatchValidationError(issues);
  if (
    draft.requiresNewToken &&
    (draft.settings.type === "consul" || draft.settings.type === "kubernetes")
  ) {
    throw new AppPatchValidationError(
      [
        `Creating authenticated ${draft.settings.type} discovery requires a new secret token. Typed App patches never carry secret values; use the raw configuration editor with config:raw permission.`,
      ],
      true,
    );
  }
  ops.push({
    op: "upstream_set_discovery",
    upstream: name,
    discovery: discoveryToPatch(draft.settings),
  });
}

function appendMount(
  ops: ConfigPatch[],
  name: string,
  mount: AppMountDraft,
  routes: readonly RouteProjection[],
): void {
  if (mount.mode === "none") return;
  const server = normalizedServer(mount.server);
  const path = normalizedRoutePath(mount.matchType, mount.path);

  if (mount.mode === "existing") {
    const existing = findExactServer(routes, server);
    if (existing === null) {
      throw new AppPatchValidationError([
        `The selected server no longer exists: ${formatServerIdentity(server)}. Refresh and choose the exact identity again.`,
      ]);
    }
    if (routeCollision(existing, mount.matchType, path)) {
      throw new AppPatchValidationError([
        `The selected server already has a ${mount.matchType} route for ${path}.`,
      ]);
    }
    if (mount.protocol === "grpc" && !existing.tls?.enabled && !existing.h2c) {
      throw new AppPatchValidationError([
        `Native gRPC requires HTTP/2. The selected plaintext server ${formatServerIdentity(server)} does not already have h2c enabled; this workflow will not change an existing listener or its sibling virtual hosts.`,
      ]);
    }
    ops.push(locationOperation(name, server, mount.protocol, mount.matchType, path));
    return;
  }

  if (exactServerExists(routes, server)) {
    throw new AppPatchValidationError([
      `A server with the exact identity ${formatServerIdentity(server)} already exists. Select existing-server mode instead.`,
    ]);
  }

  const sameListen = routes.filter((route) => route.listen === server.listen);
  if (mount.protocol === "grpc" && sameListen.length > 0) {
    throw new AppPatchValidationError([
      `Native gRPC new-server mode cannot reuse listener ${server.listen}: enabling h2c is listener-address scoped and could change ${String(sameListen.length)} sibling virtual host${sameListen.length === 1 ? "" : "s"}. Select an exact existing HTTP/2-capable server or choose an unused plaintext listener.`,
    ]);
  }

  ops.push({
    op: "server_add",
    listen: server.listen,
    server_names: canonicalServerNames(server.serverNames),
  });
  if (mount.protocol === "grpc") {
    // The listener is new and dedicated to this App, so the address-scoped h2c
    // mutation cannot alter a pre-existing sibling virtual host.
    ops.push({ op: "server_toggle_h2c", listen: server.listen, enabled: true });
  }
  ops.push(locationOperation(name, server, mount.protocol, mount.matchType, path));
}

/**
 * Build the complete deterministic App/upstream creation batch. This helper is
 * pure: it never previews, applies, navigates, or mutates the supplied inventory.
 */
export function buildAppCreationBatch(
  draft: AppCreateDraft,
  inventory: AppPatchInventory,
): ConfigPatch[] {
  const name = draft.name.trim();
  if (name === "") throw new AppPatchValidationError(["Give the App/upstream a name."]);
  if (inventory.apps.some((app) => app.name === name)) {
    throw new AppPatchValidationError([`An App/upstream named ${name} already exists.`]);
  }

  const strategies: readonly AppStrategy[] = ["round_robin", "weighted_round_robin", "least_conn"];
  if (!strategies.includes(draft.strategy)) {
    throw new AppPatchValidationError([`Unsupported load-balancing strategy: ${draft.strategy}.`]);
  }

  // Blank placeholder rows are omitted before choosing the first backend. This
  // prevents a later non-empty row from being emitted twice.
  const backends = draft.backends
    .filter((backend) => backend.address.trim() !== "")
    .map((backend) => ({
      address: backend.address.trim(),
      weight: normalizedWeight(backend.weight),
    }));
  const first = backends[0];
  if (first === undefined) {
    throw new AppPatchValidationError(["Add at least one backend address."]);
  }

  const ops: ConfigPatch[] = [
    {
      op: "upstream_add",
      upstream: name,
      address: first.address,
      weight: first.weight,
      strategy: draft.strategy,
    },
  ];
  for (const backend of backends.slice(1)) {
    ops.push({
      op: "upstream_add_backend",
      upstream: name,
      address: backend.address,
      weight: backend.weight,
    });
  }
  appendHealthCheck(ops, name, draft.healthCheck);
  appendDiscovery(ops, name, draft.discovery);
  appendMount(ops, name, draft.mount, inventory.routes);
  return ops;
}

/** Build the one-op no-cascade deletion batch after reference-aware gating. */
export function buildAppRemovalBatch(name: string, routesUsing: readonly string[]): ConfigPatch[] {
  const upstream = name.trim();
  if (upstream === "") throw new AppPatchValidationError(["The App/upstream name is required."]);
  if (routesUsing.length > 0) {
    throw new AppPatchValidationError([
      `This App/upstream is still referenced by ${String(routesUsing.length)} route${routesUsing.length === 1 ? "" : "s"}. Repoint or remove those routes first; deletion never cascades.`,
    ]);
  }
  return [{ op: "upstream_remove", upstream }];
}

/**
 * Stable display summaries for locally built operations. They deliberately omit
 * backend addresses, health expected-body text, discovery details, and every
 * secret-bearing field. Authoritative preview summaries still come from Jul.
 */
export function summarizeAppPatchBatch(ops: readonly ConfigPatch[]): string[] {
  let backendNumber = 1;
  return ops.map((operation) => {
    switch (operation.op) {
      case "upstream_add":
        return "Create App/upstream with backend 1";
      case "upstream_add_backend":
        backendNumber += 1;
        return `Add backend ${String(backendNumber)}`;
      case "upstream_set_health_check":
        return "Configure active health checks";
      case "upstream_set_discovery":
        return "Configure service discovery";
      case "server_add":
        return "Create exact server identity";
      case "server_toggle_h2c":
        return "Enable h2c on the new dedicated listener";
      case "location_add":
        return operation.action.kind === "grpc_proxy"
          ? "Mount native gRPC route"
          : "Mount HTTP reverse-proxy route";
      case "upstream_remove":
        return "Remove App/upstream without cascade";
      default:
        return operation.op;
    }
  });
}
