/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect } from "vitest";
import { generateAppToml, generateAppWithRouteToml, type AppDraft } from "@/lib/routeToml.ts";

function appDraft(): AppDraft {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [{ address: "127.0.0.1:3000", weight: 1 }],
    healthCheck: false,
    healthCheckPath: "/healthz",
    healthCheckInterval: "5s",
  };
}

describe("generateAppWithRouteToml (P2-13 mount-on-route)", () => {
  it("returns only the upstream block when no mount is requested", () => {
    const out = generateAppWithRouteToml(appDraft());
    expect(out).toBe(generateAppToml(appDraft()));
    expect(out).not.toContain("[[servers]]");
  });

  it("appends a reverse-proxy server block targeting the app pool when mounted", () => {
    const out = generateAppWithRouteToml(appDraft(), { listen: ":8080", path: "/api" });
    expect(out).toContain("[[upstreams]]");
    expect(out).toContain('name = "api"');
    expect(out).toContain("[[servers]]");
    expect(out).toContain('listen = ":8080"');
    expect(out).toContain("[[servers.locations]]");
    expect(out).toContain('match = { type = "prefix", path = "/api" }');
    // The route proxies to the app by name, so the two blocks are linked.
    expect(out).toContain('proxy_pass = "http://api"');
  });

  it("defaults listen and path when blank", () => {
    const out = generateAppWithRouteToml(appDraft(), { listen: "", path: "" });
    expect(out).toContain('listen = ":8080"');
    expect(out).toContain('path = "/"');
  });

  it("keeps the upstream block first so the route can resolve the pool", () => {
    const out = generateAppWithRouteToml(appDraft(), { listen: ":80", path: "/" });
    expect(out.indexOf("[[upstreams]]")).toBeLessThan(out.indexOf("[[servers]]"));
  });

  it("emits grpc=true on the location and h2c=true on the listener for a gRPC mount", () => {
    const out = generateAppWithRouteToml(appDraft(), { listen: ":8080", path: "/", grpc: true });
    // The cleartext listener must enable h2c so gRPC clients can use HTTP/2.
    expect(out).toContain("h2c = true");
    // The location proxies the native gRPC stream unchanged.
    expect(out).toContain("grpc = true");
    expect(out).toContain('proxy_pass = "http://api"');
    // h2c belongs to the server block (before the location), grpc to the location.
    expect(out.indexOf("h2c = true")).toBeLessThan(out.indexOf("[[servers.locations]]"));
    expect(out.indexOf("[[servers.locations]]")).toBeLessThan(out.indexOf("grpc = true"));
  });

  it("omits grpc/h2c for a non-gRPC mount", () => {
    const out = generateAppWithRouteToml(appDraft(), { listen: ":8080", path: "/" });
    expect(out).not.toContain("h2c = true");
    expect(out).not.toContain("grpc = true");
  });
});
