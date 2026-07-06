/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect } from "vitest";
import type { AppProjection } from "@/api/client.ts";
import {
  seedHealthCheck,
  healthCheckToPatch,
  healthCheckWarnings,
  seedDiscovery,
  discoveryToPatch,
  discoveryTokenNote,
  discoveryWarnings,
} from "@/lib/appSettings.ts";

function app(over: Partial<AppProjection> = {}): AppProjection {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [],
    health_check: false,
    ...over,
  };
}

describe("seedHealthCheck", () => {
  it("hydrates the draft from projection detail", () => {
    const d = seedHealthCheck(
      app({
        health_check: true,
        health_check_type: "http",
        health_check_path: "/healthz",
        health_check_interval: "5s",
        health_check_timeout: "2s",
        health_check_healthy_threshold: 2,
        health_check_unhealthy_threshold: 3,
        health_check_expect_status: [200, 204],
        health_check_expect_body: "ok",
      }),
    );
    expect(d).toMatchObject({
      enabled: true,
      type: "http",
      path: "/healthz",
      interval: "5s",
      timeout: "2s",
      healthyThreshold: "2",
      unhealthyThreshold: "3",
      expectStatus: "200, 204",
      expectBody: "ok",
    });
  });

  it("defaults the type to http when unset", () => {
    expect(seedHealthCheck(app()).type).toBe("http");
  });
});

describe("healthCheckToPatch", () => {
  it("collapses to a disabled patch when off", () => {
    expect(healthCheckToPatch(seedHealthCheck(app()))).toEqual({ enabled: false });
  });

  it("emits only the populated fields", () => {
    const patch = healthCheckToPatch({
      enabled: true,
      type: "http",
      path: "/ready",
      interval: "5s",
      timeout: "",
      healthyThreshold: "2",
      unhealthyThreshold: "",
      expectStatus: "200, 204",
      expectBody: "OK",
    });
    expect(patch).toEqual({
      enabled: true,
      type: "http",
      path: "/ready",
      interval: "5s",
      healthy_threshold: 2,
      expect_status: [200, 204],
      expect_body: "OK",
    });
    expect(patch).not.toHaveProperty("timeout");
    expect(patch).not.toHaveProperty("unhealthy_threshold");
  });

  it("drops expect_body for tcp probes", () => {
    const patch = healthCheckToPatch({
      enabled: true,
      type: "tcp",
      path: "",
      interval: "",
      timeout: "",
      healthyThreshold: "",
      unhealthyThreshold: "",
      expectStatus: "",
      expectBody: "ignored",
    });
    expect(patch).toEqual({ enabled: true, type: "tcp" });
  });
});

describe("healthCheckWarnings", () => {
  it("is silent when disabled", () => {
    expect(healthCheckWarnings(seedHealthCheck(app()))).toEqual([]);
  });

  it("warns when an http probe has no path", () => {
    const w = healthCheckWarnings({
      enabled: true,
      type: "http",
      path: "  ",
      interval: "",
      timeout: "",
      healthyThreshold: "",
      unhealthyThreshold: "",
      expectStatus: "",
      expectBody: "",
    });
    expect(w.some((m) => m.includes("request path"))).toBe(true);
  });

  it("warns on out-of-range expected status codes", () => {
    const w = healthCheckWarnings({
      enabled: true,
      type: "http",
      path: "/h",
      interval: "",
      timeout: "",
      healthyThreshold: "",
      unhealthyThreshold: "",
      expectStatus: "200, 999",
      expectBody: "",
    });
    expect(w.some((m) => m.includes("999"))).toBe(true);
  });
});

describe("seedDiscovery", () => {
  it("hydrates a consul provider including has_token (display only)", () => {
    const d = seedDiscovery(
      app({
        discovery: "consul",
        discovery_refresh: "30s",
        discovery_consul: {
          address: "http://consul:8500",
          service: "web",
          tag: "v2",
          datacenter: "dc1",
          passing_only: true,
          has_token: true,
        },
      }),
    );
    expect(d).toMatchObject({
      type: "consul",
      refresh: "30s",
      consulAddress: "http://consul:8500",
      consulService: "web",
      consulTag: "v2",
      consulDatacenter: "dc1",
      consulPassingOnly: true,
      hasToken: true,
    });
  });

  it("falls back to static for an unknown provider", () => {
    expect(seedDiscovery(app({ discovery: "etcd" })).type).toBe("static");
  });
});

describe("discoveryToPatch", () => {
  it("collapses to static when disabled", () => {
    expect(discoveryToPatch(seedDiscovery(app()))).toEqual({ type: "static" });
  });

  it("emits a dns patch with the target", () => {
    const d = seedDiscovery(app({ discovery: "dns", discovery_target: "svc:8080" }));
    expect(discoveryToPatch(d)).toEqual({ type: "dns", target: "svc:8080" });
  });

  it("never carries a token in the consul patch", () => {
    const d = seedDiscovery(
      app({
        discovery: "consul",
        discovery_consul: { service: "web", has_token: true },
      }),
    );
    const patch = discoveryToPatch(d);
    expect(patch.consul).toEqual({ service: "web", passing_only: true });
    expect(JSON.stringify(patch)).not.toContain("token");
  });
});

describe("discoveryWarnings", () => {
  it("warns when a dns target is missing", () => {
    const w = discoveryWarnings(seedDiscovery(app({ discovery: "dns" })));
    expect(w.some((m) => m.includes("host:port"))).toBe(true);
  });

  it("warns when a consul service is missing", () => {
    const w = discoveryWarnings(seedDiscovery(app({ discovery: "consul" })));
    expect(w.some((m) => m.includes("service name"))).toBe(true);
  });

  it("does not treat a configured token as a blocking warning", () => {
    const d = seedDiscovery(
      app({ discovery: "consul", discovery_consul: { service: "web", has_token: true } }),
    );
    // Service is set, so there are no blocking warnings even with a token.
    expect(discoveryWarnings(d)).toEqual([]);
  });

  it("surfaces the preserved-token note separately as informational", () => {
    const note = discoveryTokenNote(
      seedDiscovery(
        app({ discovery: "consul", discovery_consul: { service: "web", has_token: true } }),
      ),
    );
    expect(note).toContain("preserved unchanged");
  });

  it("has no token note when no token is configured", () => {
    expect(discoveryTokenNote(seedDiscovery(app({ discovery: "consul" })))).toBeNull();
  });
});
