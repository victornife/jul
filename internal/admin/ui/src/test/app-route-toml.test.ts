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
});
