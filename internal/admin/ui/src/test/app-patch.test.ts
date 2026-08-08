/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { AppProjection, RouteProjection } from "@/api/client.ts";
import {
  AppPatchValidationError,
  buildAppCreationBatch,
  buildAppRemovalBatch,
  summarizeAppPatchBatch,
  type AppCreateDraft,
} from "@/lib/appPatch.ts";
import type { DiscoveryDraft, HealthCheckDraft } from "@/lib/appSettings.ts";

function app(name: string): AppProjection {
  return { name, strategy: "round_robin", backends: [], health_check: false };
}

function route(
  listen: string,
  serverNames: string[],
  options: Partial<RouteProjection> = {},
): RouteProjection {
  return {
    listen,
    server_names: serverNames,
    http3: false,
    h2c: false,
    locations: [],
    ...options,
  };
}

const routes = [
  route(":443", ["api.example"], { tls: { enabled: true, acme: false } }),
  route(":8080", ["grpc.example"], { h2c: true }),
  route(":8081", ["plain.example"]),
  route(":9000", ["sibling.example"]),
];

function healthCheck(over: Partial<HealthCheckDraft> = {}): HealthCheckDraft {
  return {
    enabled: true,
    type: "http",
    path: "/ready",
    interval: "5s",
    timeout: "2s",
    healthyThreshold: "2",
    unhealthyThreshold: "3",
    expectStatus: "200, 204",
    expectBody: "secret-looking expected body",
    ...over,
  };
}

function discovery(over: Partial<DiscoveryDraft> = {}): DiscoveryDraft {
  return {
    type: "static",
    target: "",
    refresh: "",
    consulAddress: "",
    consulService: "",
    consulTag: "",
    consulDatacenter: "",
    consulPassingOnly: true,
    k8sNamespace: "",
    k8sService: "",
    k8sPort: "",
    k8sApiServer: "",
    k8sCaFile: "",
    k8sInsecure: false,
    hasToken: false,
    ...over,
  };
}

function draft(over: Partial<AppCreateDraft> = {}): AppCreateDraft {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [
      { address: "", weight: 5 },
      { address: "127.0.0.1:3000", weight: 2 },
      { address: "127.0.0.1:3001", weight: 3 },
    ],
    mount: { mode: "none" },
    ...over,
  };
}

const inventory = { apps: [] as AppProjection[], routes };

describe("buildAppCreationBatch", () => {
  it("builds an upstream-only batch in deterministic backend order", () => {
    const ops = buildAppCreationBatch(draft(), inventory);
    expect(ops).toEqual([
      {
        op: "upstream_add",
        upstream: "api",
        address: "127.0.0.1:3000",
        weight: 2,
        strategy: "round_robin",
      },
      {
        op: "upstream_add_backend",
        upstream: "api",
        address: "127.0.0.1:3001",
        weight: 3,
      },
    ]);
  });

  it("rejects an empty name, an empty backend list, and an unsupported strategy", () => {
    expect(() => buildAppCreationBatch(draft({ name: "   " }), inventory)).toThrow(
      /give the App\/upstream a name/i,
    );
    expect(() =>
      buildAppCreationBatch(draft({ backends: [{ address: " ", weight: 1 }] }), inventory),
    ).toThrow(/at least one backend/i);
    expect(() =>
      buildAppCreationBatch(draft({ strategy: "random" as AppCreateDraft["strategy"] }), inventory),
    ).toThrow(/unsupported load-balancing strategy/i);
  });

  it("defaults non-positive or fractional weights exactly as the backend does", () => {
    const ops = buildAppCreationBatch(
      draft({
        backends: [
          { address: "127.0.0.1:3000", weight: 0 },
          { address: "127.0.0.1:3001", weight: 1.5 },
          { address: "127.0.0.1:3002", weight: 4 },
        ],
      }),
      inventory,
    );
    expect(ops).toMatchObject([
      { op: "upstream_add", weight: 1 },
      { op: "upstream_add_backend", weight: 1 },
      { op: "upstream_add_backend", weight: 4 },
    ]);
  });

  it("keeps health before discovery and both before a mount", () => {
    const ops = buildAppCreationBatch(
      draft({
        healthCheck: healthCheck(),
        discovery: {
          settings: discovery({ type: "dns", target: "api.internal:8080" }),
          requiresNewToken: false,
        },
        mount: {
          mode: "new",
          server: { listen: ":7071", serverNames: ["api.example"] },
          protocol: "http",
          matchType: "prefix",
          path: "/api",
        },
      }),
      inventory,
    );
    expect(ops.map((operation) => operation.op)).toEqual([
      "upstream_add",
      "upstream_add_backend",
      "upstream_set_health_check",
      "upstream_set_discovery",
      "server_add",
      "location_add",
    ]);
  });

  it("mounts HTTP on an exact existing server without server mutations", () => {
    const ops = buildAppCreationBatch(
      draft({
        mount: {
          mode: "existing",
          server: { listen: ":443", serverNames: ["api.example"] },
          protocol: "http",
          matchType: "prefix",
          path: "/api",
        },
      }),
      inventory,
    );
    expect(ops.map((operation) => operation.op)).toEqual([
      "upstream_add",
      "upstream_add_backend",
      "location_add",
    ]);
    expect(ops.at(-1)).toMatchObject({
      op: "location_add",
      listen: ":443",
      server_names: ["api.example"],
      action: { kind: "proxy", target: "http://api" },
    });
  });

  it("creates a new HTTP server before its location", () => {
    const ops = buildAppCreationBatch(
      draft({
        mount: {
          mode: "new",
          server: { listen: ":7070", serverNames: ["b.example", "a.example"] },
          protocol: "http",
          matchType: "exact",
          path: "/",
        },
      }),
      inventory,
    );
    expect(ops.slice(-2)).toEqual([
      { op: "server_add", listen: ":7070", server_names: ["a.example", "b.example"] },
      {
        op: "location_add",
        listen: ":7070",
        server_names: ["a.example", "b.example"],
        match_set: { type: "exact", path: "/" },
        action: { kind: "proxy", target: "http://api" },
      },
    ]);
  });

  it("uses native gRPC on an existing TLS server without h2c", () => {
    const ops = buildAppCreationBatch(
      draft({
        mount: {
          mode: "existing",
          server: { listen: ":443", serverNames: ["api.example"] },
          protocol: "grpc",
          matchType: "prefix",
          path: "/",
        },
      }),
      inventory,
    );
    expect(ops.at(-1)).toMatchObject({
      op: "location_add",
      action: { kind: "grpc_proxy", target: "http://api" },
    });
    expect(ops.some((operation) => operation.op === "server_toggle_h2c")).toBe(false);
  });

  it("uses native gRPC on an existing plaintext server only when h2c is already enabled", () => {
    const ops = buildAppCreationBatch(
      draft({
        mount: {
          mode: "existing",
          server: { listen: ":8080", serverNames: ["grpc.example"] },
          protocol: "grpc",
          matchType: "prefix",
          path: "/rpc",
        },
      }),
      inventory,
    );
    expect(ops.at(-1)).toMatchObject({ action: { kind: "grpc_proxy" } });
    expect(ops.some((operation) => operation.op === "server_toggle_h2c")).toBe(false);
  });

  it("rejects native gRPC on an existing plaintext server without h2c", () => {
    expect(() =>
      buildAppCreationBatch(
        draft({
          mount: {
            mode: "existing",
            server: { listen: ":8081", serverNames: ["plain.example"] },
            protocol: "grpc",
            matchType: "prefix",
            path: "/",
          },
        }),
        inventory,
      ),
    ).toThrow(/does not already have h2c enabled/i);
  });

  it("creates a dedicated plaintext gRPC listener in server/toggle/location order", () => {
    const ops = buildAppCreationBatch(
      draft({
        mount: {
          mode: "new",
          server: { listen: ":5051", serverNames: [] },
          protocol: "grpc",
          matchType: "prefix",
          path: "/",
        },
      }),
      inventory,
    );
    expect(ops.slice(-3).map((operation) => operation.op)).toEqual([
      "server_add",
      "server_toggle_h2c",
      "location_add",
    ]);
    expect(ops.at(-1)).toMatchObject({ action: { kind: "grpc_proxy", target: "http://api" } });
  });

  it("rejects same-listen sibling mutation for new native gRPC", () => {
    expect(() =>
      buildAppCreationBatch(
        draft({
          mount: {
            mode: "new",
            server: { listen: ":9000", serverNames: ["new.example"] },
            protocol: "grpc",
            matchType: "prefix",
            path: "/",
          },
        }),
        inventory,
      ),
    ).toThrow(/sibling virtual host/i);
  });

  it("rejects App name and exact server identity collisions", () => {
    expect(() => buildAppCreationBatch(draft(), { apps: [app("api")], routes })).toThrow(
      /already exists/i,
    );
    expect(() =>
      buildAppCreationBatch(
        draft({
          mount: {
            mode: "new",
            server: { listen: ":443", serverNames: ["api.example"] },
            protocol: "http",
            matchType: "prefix",
            path: "/new",
          },
        }),
        inventory,
      ),
    ).toThrow(/exact identity/i);
  });

  it("requires the exact selected server identity to still exist", () => {
    expect(() =>
      buildAppCreationBatch(
        draft({
          mount: {
            mode: "existing",
            server: { listen: ":443", serverNames: ["API.example"] },
            protocol: "http",
            matchType: "prefix",
            path: "/",
          },
        }),
        inventory,
      ),
    ).toThrow(/no longer exists/i);
  });

  it("treats server names as an order-independent, case-sensitive set", () => {
    const exactRoutes = [
      route(":8443", ["b.example", "a.example"]),
      route(":8443", ["sibling.example"]),
    ];
    const exact = buildAppCreationBatch(
      draft({
        mount: {
          mode: "existing",
          server: { listen: ":8443", serverNames: ["a.example", "b.example"] },
          protocol: "http",
          matchType: "prefix",
          path: "/api",
        },
      }),
      { apps: [], routes: exactRoutes },
    );
    expect(exact.at(-1)).toMatchObject({
      listen: ":8443",
      server_names: ["a.example", "b.example"],
    });

    for (const server of [
      { listen: ":8443", serverNames: ["a.example"] },
      { listen: ":8444", serverNames: ["a.example", "b.example"] },
      { listen: ":8443", serverNames: ["A.example", "b.example"] },
    ]) {
      expect(() =>
        buildAppCreationBatch(
          draft({
            mount: {
              mode: "existing",
              server,
              protocol: "http",
              matchType: "prefix",
              path: "/api",
            },
          }),
          { apps: [], routes: exactRoutes },
        ),
      ).toThrow(/no longer exists/i);
    }
  });

  it("rejects blank or duplicate new-server names without falling back to a sibling", () => {
    for (const serverNames of [
      ["api.example", ""],
      ["api.example", "api.example"],
    ]) {
      expect(() =>
        buildAppCreationBatch(
          draft({
            mount: {
              mode: "new",
              server: { listen: ":7443", serverNames },
              protocol: "http",
              matchType: "prefix",
              path: "/",
            },
          }),
          inventory,
        ),
      ).toThrow(/blank entries|duplicate server name/i);
    }
  });

  it("rejects a duplicate route only on the exact selected virtual host", () => {
    const exactRoutes = [
      route(":9443", ["api.example"], {
        locations: [
          {
            index: 0,
            type: "prefix",
            match: "/api",
            action: "proxy",
            auth: false,
            cache: false,
            compression: false,
            rate_limit: false,
            secure: false,
            require_client_cert: false,
          },
        ],
      }),
      route(":9443", ["sibling.example"], {
        locations: [
          {
            index: 0,
            type: "prefix",
            match: "/other",
            action: "proxy",
            auth: false,
            cache: false,
            compression: false,
            rate_limit: false,
            secure: false,
            require_client_cert: false,
          },
        ],
      }),
    ];
    expect(() =>
      buildAppCreationBatch(
        draft({
          mount: {
            mode: "existing",
            server: { listen: ":9443", serverNames: ["api.example"] },
            protocol: "http",
            matchType: "prefix",
            path: "/api",
          },
        }),
        { apps: [], routes: exactRoutes },
      ),
    ).toThrow(/already has a prefix route/i);

    const siblingSafe = buildAppCreationBatch(
      draft({
        mount: {
          mode: "existing",
          server: { listen: ":9443", serverNames: ["sibling.example"] },
          protocol: "http",
          matchType: "prefix",
          path: "/api",
        },
      }),
      { apps: [], routes: exactRoutes },
    );
    expect(siblingSafe.at(-1)).toMatchObject({ server_names: ["sibling.example"] });
  });

  it("converts a TCP health check without carrying HTTP-only fields", () => {
    const ops = buildAppCreationBatch(
      draft({
        healthCheck: healthCheck({
          type: "tcp",
          path: "/ignored",
          expectStatus: "200",
          expectBody: "ignored",
        }),
      }),
      inventory,
    );
    expect(ops.at(-1)).toEqual({
      op: "upstream_set_health_check",
      upstream: "api",
      health_check: {
        enabled: true,
        type: "tcp",
        interval: "5s",
        timeout: "2s",
        healthy_threshold: 2,
        unhealthy_threshold: 3,
      },
    });
    expect(JSON.stringify(ops)).not.toContain("ignored");
    expect(ops.at(-1)).not.toHaveProperty("health_check.path");
    expect(ops.at(-1)).not.toHaveProperty("health_check.expect_status");
    expect(ops.at(-1)).not.toHaveProperty("health_check.expect_body");
  });

  it("preserves every supported health field without exposing expected body in summaries", () => {
    const ops = buildAppCreationBatch(draft({ healthCheck: healthCheck() }), inventory);
    expect(ops.at(-1)).toEqual({
      op: "upstream_set_health_check",
      upstream: "api",
      health_check: {
        enabled: true,
        type: "http",
        path: "/ready",
        interval: "5s",
        timeout: "2s",
        healthy_threshold: 2,
        unhealthy_threshold: 3,
        expect_status: [200, 204],
        expect_body: "secret-looking expected body",
      },
    });
    expect(summarizeAppPatchBatch(ops).join(" ")).not.toContain("secret-looking");
  });

  it("omits disabled health and static discovery operations", () => {
    const ops = buildAppCreationBatch(
      draft({
        healthCheck: healthCheck({ enabled: false }),
        discovery: { settings: discovery(), requiresNewToken: false },
      }),
      inventory,
    );
    expect(ops.some((operation) => operation.op === "upstream_set_health_check")).toBe(false);
    expect(ops.some((operation) => operation.op === "upstream_set_discovery")).toBe(false);
  });

  it("includes non-secret DNS, SRV, Consul, and Kubernetes discovery payloads", () => {
    const cases: Array<{ settings: DiscoveryDraft; match: Record<string, unknown> }> = [
      { settings: discovery({ type: "dns", target: "svc.internal:8080" }), match: { type: "dns" } },
      {
        settings: discovery({ type: "dns_srv", target: "_grpc._tcp.svc" }),
        match: { type: "dns_srv" },
      },
      {
        settings: discovery({
          type: "consul",
          consulAddress: "http://consul:8500",
          consulService: "web",
        }),
        match: { type: "consul", consul: { service: "web" } },
      },
      {
        settings: discovery({
          type: "kubernetes",
          k8sNamespace: "default",
          k8sService: "web",
        }),
        match: { type: "kubernetes", kubernetes: { namespace: "default", service: "web" } },
      },
    ];
    for (const example of cases) {
      const ops = buildAppCreationBatch(
        draft({ discovery: { settings: example.settings, requiresNewToken: false } }),
        inventory,
      );
      expect(ops.at(-1)).toMatchObject({
        op: "upstream_set_discovery",
        discovery: example.match,
      });
      expect(JSON.stringify(ops)).not.toContain("token");
    }
  });

  it("fails closed when creation requires a new discovery token", () => {
    try {
      buildAppCreationBatch(
        draft({
          discovery: {
            settings: discovery({ type: "consul", consulService: "web" }),
            requiresNewToken: true,
          },
        }),
        inventory,
      );
      throw new Error("expected token-required creation to fail");
    } catch (error) {
      expect(error).toBeInstanceOf(AppPatchValidationError);
      expect((error as AppPatchValidationError).rawEditorRequired).toBe(true);
      expect(error).toHaveProperty("message", expect.stringMatching(/raw configuration editor/i));
    }
  });
});

describe("buildAppRemovalBatch", () => {
  it("emits exactly one no-cascade upstream_remove operation", () => {
    expect(buildAppRemovalBatch("api", [])).toEqual([{ op: "upstream_remove", upstream: "api" }]);
  });

  it("blocks deletion while projected route references remain", () => {
    expect(() => buildAppRemovalBatch("api", [":8080 /", ":9090 /rpc"])).toThrow(
      /repoint or remove those routes first/i,
    );
  });
});
