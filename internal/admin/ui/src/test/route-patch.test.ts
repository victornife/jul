/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";
import {
  buildExistingServerRouteBatch,
  buildNewServerRouteBatch,
  buildRouteRemovalBatch,
  buildServerRemovalBatch,
  exactServerExists,
  parseServerNamesInput,
  restoreRouteSelection,
  routeIdentityKey,
  sameServerIdentity,
  serverIdentityFromRoute,
  serverIdentityKey,
  storeRouteIdentity,
  storeRouteSelection,
  validateStructuredRouteSpec,
} from "@/lib/routePatch.ts";

function location(
  index: number,
  match: string,
  type = "prefix",
  action = "proxy",
): LocationProjection {
  return {
    index,
    match,
    type,
    action,
    target: "http://app",
    auth: false,
    cache: false,
    compression: false,
    rate_limit: false,
    secure: false,
    require_client_cert: false,
  };
}

function route(
  listen: string,
  serverNames: string[],
  locations: LocationProjection[] = [location(0, "/")],
): RouteProjection {
  return {
    listen,
    server_names: serverNames,
    http3: false,
    h2c: false,
    locations,
  };
}

const existing = route(":8080", ["b.example", "a.example"]);
const inventory = [
  existing,
  route(":8080", ["other.example"], [location(0, "/other")]),
  route(":9090", ["a.example"], [location(0, "/admin")]),
];

const baseSpec = {
  server: { listen: ":8080", serverNames: ["a.example", "b.example"] },
  matchType: "prefix" as const,
  path: "/api/",
  action: { kind: "proxy" as const, target: "http://app" },
};

describe("route patch exact server identity", () => {
  it("treats name order as irrelevant while preserving listen and case", () => {
    expect(
      sameServerIdentity(
        { listen: ":8080", serverNames: ["a.example", "b.example"] },
        { listen: ":8080", serverNames: ["b.example", "a.example"] },
      ),
    ).toBe(true);
    expect(
      sameServerIdentity(
        { listen: ":8080", serverNames: ["a.example"] },
        { listen: ":8080", serverNames: ["A.example"] },
      ),
    ).toBe(false);
    expect(
      sameServerIdentity(
        { listen: ":8080", serverNames: ["a.example"] },
        { listen: ":9090", serverNames: ["a.example"] },
      ),
    ).toBe(false);
  });

  it("rejects blank and duplicate server-name input instead of deduplicating it", () => {
    expect(parseServerNamesInput("a.example,,b.example").errors).toContain(
      "Server names cannot contain blank entries.",
    );
    expect(parseServerNamesInput("a.example, a.example").errors[0]).toContain(
      "Duplicate server name",
    );
    expect(parseServerNamesInput("b.example, a.example")).toEqual({
      names: ["a.example", "b.example"],
      errors: [],
    });
    expect(parseServerNamesInput("   ")).toEqual({ names: [], errors: [] });
  });

  it("uses collision-safe canonical keys", () => {
    const a = { listen: ":8080", serverNames: ["b.example", "a.example"] };
    const b = { listen: ":8080", serverNames: ["a.example", "b.example"] };
    expect(serverIdentityKey(a)).toBe(serverIdentityKey(b));
    expect(routeIdentityKey(a, { matchType: "prefix", path: "/" })).not.toBe(
      routeIdentityKey(a, { matchType: "exact", path: "/" }),
    );
  });

  it("prefers a durable route_id over the match fingerprint when present", () => {
    const a = { listen: ":8080", serverNames: ["a.example"] };
    const b = { listen: ":9090", serverNames: ["b.example"] };
    // Same route_id correlates even across different servers/matches (the
    // Console never re-derives its own correlation logic; it consumes
    // whatever the server sent, per ADR 0019 §4/§7).
    expect(
      routeIdentityKey(a, { matchType: "prefix", path: "/old", routeId: "r-same" }),
    ).toBe(routeIdentityKey(b, { matchType: "exact", path: "/new", routeId: "r-same" }));
    // Different route_ids never collide, even with identical server+match.
    expect(
      routeIdentityKey(a, { matchType: "prefix", path: "/", routeId: "r-one" }),
    ).not.toBe(routeIdentityKey(a, { matchType: "prefix", path: "/", routeId: "r-two" }));
    // No route_id falls back to the pre-route_id fingerprint behavior.
    expect(routeIdentityKey(a, { matchType: "prefix", path: "/" })).toBe(
      routeIdentityKey(a, { matchType: "prefix", path: "/" }),
    );
  });

  it("finds an exact identity rather than any server sharing the listen", () => {
    expect(exactServerExists(inventory, baseSpec.server)).toBe(true);
    expect(
      exactServerExists(inventory, { listen: ":8080", serverNames: ["missing.example"] }),
    ).toBe(false);
  });
});

describe("route creation batches", () => {
  it("existing-server mode starts with location_add and never emits server_add", () => {
    const ops = buildExistingServerRouteBatch(baseSpec, inventory);
    expect(ops.map((op) => op.op)).toEqual(["location_add"]);
    expect(ops.some((op) => op.op === "server_add")).toBe(false);
    expect(ops[0]).toMatchObject({
      op: "location_add",
      listen: ":8080",
      server_names: ["a.example", "b.example"],
    });
  });

  it("new-server mode emits server_add then location_add", () => {
    const ops = buildNewServerRouteBatch(
      { ...baseSpec, server: { listen: ":8181", serverNames: ["new.example"] } },
      inventory,
    );
    expect(ops.map((op) => op.op)).toEqual(["server_add", "location_add"]);
  });

  it("preserves deterministic modifier and plugin order", () => {
    const ops = buildExistingServerRouteBatch(
      {
        ...baseSpec,
        auth: { method: "cidr", allow: ["10.0.0.0/8"], deny: [] },
        cache: true,
        rateLimit: { enabled: true, rate: 25, burst: 50, key: "ip" },
        plugins: ["authz", "headers"],
      },
      inventory,
    );
    expect(ops.map((op) => op.op)).toEqual([
      "location_add",
      "location_set_auth",
      "route_toggle_cache",
      "route_set_rate_limit",
      "location_attach_plugin",
      "location_attach_plugin",
    ]);
    expect(ops.slice(-2)).toEqual([
      expect.objectContaining({ plugin_name: "authz" }),
      expect.objectContaining({ plugin_name: "headers" }),
    ]);
  });

  it("requires an exact selected identity in existing mode", () => {
    expect(() =>
      buildExistingServerRouteBatch(
        { ...baseSpec, server: { listen: ":8080", serverNames: ["missing.example"] } },
        inventory,
      ),
    ).toThrow(/no longer exists/i);
  });

  it("rejects a duplicate location on the exact selected server", () => {
    expect(() =>
      buildExistingServerRouteBatch(
        { ...baseSpec, path: "/", server: serverIdentityFromRoute(existing) },
        inventory,
      ),
    ).toThrow(/already has a prefix route/i);
  });

  it("rejects a new server whose exact identity already exists", () => {
    expect(() => buildNewServerRouteBatch(baseSpec, inventory)).toThrow(/already exists/i);
  });

  it("rejects an invalid structured combination rather than changing semantics", () => {
    expect(() =>
      buildExistingServerRouteBatch(
        {
          ...baseSpec,
          action: { kind: "static", target: "/srv/www" },
          cache: true,
        },
        inventory,
      ),
    ).toThrow(/cache cannot be enabled for a static/i);
  });

  it("delegates regex grammar to the Go/RE2 server preview", () => {
      const spec = {
        ...baseSpec,
        matchType: "regex" as const,
        path: "(?P<tenant>[a-z]+)",
      };

      expect(validateStructuredRouteSpec(spec)).toEqual([]);
      expect(buildExistingServerRouteBatch(spec, inventory)).toEqual([
        expect.objectContaining({
          op: "location_add",
          match_set: { type: "regex", path: "(?P<tenant>[a-z]+)" },
        }),
      ]);
    });

    it("rejects unsupported protocol actions instead of degrading to HTTP proxy", () => {
    const issues = validateStructuredRouteSpec({
      ...baseSpec,
      action: { kind: "grpc_proxy", target: "grpc://service" } as never,
    });
    expect(issues.join(" ")).toMatch(/Native gRPC is not represented/i);
  });

  it("rejects inert authentication near-side", () => {
    expect(() =>
      buildExistingServerRouteBatch(
        { ...baseSpec, auth: { method: "cidr", allow: [], deny: [] } },
        inventory,
      ),
    ).toThrow(/at least one allow or deny/i);
  });

  it("rejects duplicate plugin attachment names near-side", () => {
    expect(() =>
      buildExistingServerRouteBatch({ ...baseSpec, plugins: ["headers", "headers"] }, inventory),
    ).toThrow(/Duplicate plugin name/i);
  });
});

describe("route and server removal batches", () => {
  it("targets the exact location without dependent-resource operations", () => {
    const ops = buildRouteRemovalBatch(serverIdentityFromRoute(existing), {
      matchType: "prefix",
      path: "/api/",
    });
    expect(ops).toEqual([
      {
        op: "location_remove",
        listen: ":8080",
        server_names: ["a.example", "b.example"],
        match_type: "prefix",
        path: "/api/",
      },
    ]);
  });

  it("targets the exact server and emits no cascade", () => {
    const ops = buildServerRemovalBatch(serverIdentityFromRoute(existing));
    expect(ops).toEqual([
      {
        op: "server_remove",
        listen: ":8080",
        server_names: ["a.example", "b.example"],
      },
    ]);
    expect(ops).toHaveLength(1);
    expect(ops[0]).not.toMatchObject({ server_names: ["other.example"] });
  });
});

describe("route selection restoration", () => {
  it("restores the correct virtual host sharing a listener", () => {
    const chosen = inventory[1];
    if (chosen === undefined) throw new Error("expected second virtual-host fixture");
    const location = chosen.locations[0];
    if (location === undefined) throw new Error("expected a location fixture");
    const stored = storeRouteSelection({ route: chosen, loc: location }, "v1");
    const restored = restoreRouteSelection(inventory, stored);
    expect(restored?.route.server_names).toEqual(["other.example"]);
    expect(restored?.loc.match).toBe("/other");
  });

  it("stores the exact post-mutation target identity", () => {
    expect(
      storeRouteIdentity(
        { listen: ":8080", serverNames: ["b.example", "a.example"] },
        { matchType: "exact", path: " /created " },
        "v1",
      ),
    ).toEqual({
      version: 2,
      server: { listen: ":8080", server_names: ["a.example", "b.example"] },
      location: { match_type: "exact", path: "/created" },
      base_version: "v1",
    });
  });

  it("migrates an unambiguous legacy listen-only value", () => {
    const only = [route(":7070", ["only.example"], [location(0, "/only")])];
    const restored = restoreRouteSelection(only, {
      route: { listen: ":7070" },
      loc: { index: 0 },
    });
    expect(restored?.route.server_names).toEqual(["only.example"]);
  });

  it("clears an ambiguous legacy listen-only value", () => {
    const restored = restoreRouteSelection(inventory, {
      route: { listen: ":8080" },
      loc: { index: 0 },
    });
    expect(restored).toBeNull();
  });

  it("stores a route with a durable route_id in the v3 durable form, not v2 coordinates", () => {
    const chosen = inventory[1];
    if (chosen === undefined) throw new Error("expected second virtual-host fixture");
    const withID: LocationProjection = { ...location(0, "/other"), route_id: "r-durable" };
    const stored = storeRouteSelection({ route: chosen, loc: withID }, "v1");
    expect(stored).toEqual({ version: 3, route_id: "r-durable" });
  });

  it("resolves the v3 durable form across any server/match, unlike v2", () => {
    const withID: LocationProjection = { ...location(0, "/other"), route_id: "r-durable" };
    const routes = [route(":8080", ["other.example"], [withID])];
    const restored = restoreRouteSelection(routes, { version: 3, route_id: "r-durable" });
    expect(restored?.loc.match).toBe("/other");

    // The durable form still resolves after the route's own coordinates
    // change, which a v2 (coordinates-based) selection could never do.
    const moved = [
      route(":9090", ["renamed.example"], [{ ...location(0, "/moved"), route_id: "r-durable" }]),
    ];
    const restoredAfterMove = restoreRouteSelection(moved, { version: 3, route_id: "r-durable" });
    expect(restoredAfterMove?.loc.match).toBe("/moved");
    expect(restoredAfterMove?.route.listen).toBe(":9090");
  });

  it("fails closed when a durable route_id no longer exists, never guessing a different route", () => {
    const restored = restoreRouteSelection(inventory, { version: 3, route_id: "r-deleted" });
    expect(restored).toBeNull();
  });

  it("falls back to the v2 revision-bound form for a route with no route_id", () => {
    expect(
      storeRouteIdentity(
        { listen: ":8080", serverNames: ["b.example", "a.example"] },
        { matchType: "exact", path: "/created" },
        "v1",
      ),
    ).toEqual({
      version: 2,
      server: { listen: ":8080", server_names: ["a.example", "b.example"] },
      location: { match_type: "exact", path: "/created" },
      base_version: "v1",
    });
  });

  it("resolves a v2 selection when the current revision matches the reviewed one", () => {
    const stored = storeRouteIdentity(
      { listen: ":8080", serverNames: ["a.example", "b.example"] },
      { matchType: "prefix", path: "/" },
      "v1",
    );
    const restored = restoreRouteSelection(inventory, stored, "v1");
    expect(restored?.route.listen).toBe(":8080");
  });

  it("fails closed on a v2 selection when the revision has moved (ADR 0019 §8)", () => {
    const stored = storeRouteIdentity(
      { listen: ":8080", serverNames: ["a.example", "b.example"] },
      { matchType: "prefix", path: "/" },
      "v1",
    );
    const restored = restoreRouteSelection(inventory, stored, "v2");
    expect(restored).toBeNull();
  });

  it("resolves a v2 selection by coordinates alone when no current version is supplied", () => {
    const stored = storeRouteIdentity(
      { listen: ":8080", serverNames: ["a.example", "b.example"] },
      { matchType: "prefix", path: "/" },
      "v1",
    );
    const restored = restoreRouteSelection(inventory, stored);
    expect(restored?.route.listen).toBe(":8080");
  });

  it("fails closed on a v2 selection missing base_version entirely", () => {
    const restored = restoreRouteSelection(
      inventory,
      {
        version: 2,
        server: { listen: ":8080", server_names: ["a.example", "b.example"] },
        location: { match_type: "prefix", path: "/" },
      },
      "v1",
    );
    expect(restored).toBeNull();
  });
});
