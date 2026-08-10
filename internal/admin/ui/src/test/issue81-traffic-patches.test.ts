/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { GlobalSettingsProjection, ServerLimitsProjection, TrafficControls } from "@/api/client.ts";
import {
  buildCompressionPatch,
  buildGlobalPatch,
  buildGlobalRateLimitPatch,
  buildServerLimitsPatch,
  seedCache,
  seedCompression,
  seedGlobalSettings,
  seedRateLimit,
  seedServerLimits,
} from "@/lib/trafficPatchBuilders.ts";
import { generateCacheToml } from "@/lib/trafficToml.ts";

const globalProjection: GlobalSettingsProjection = {
  worker_threads: "8",
  log_level: "debug",
  log_format: "text",
  shutdown_timeout: "31s",
  reload_timeout: "11s",
  redact_min_secret_length: 9,
  lifecycle: {},
};

const server: ServerLimitsProjection = {
  listen: "127.0.0.1:8080",
  client_max_body_size: "17m",
  read_timeout: "21s",
  write_timeout: "22s",
  idle_timeout: "91s",
};

const traffic: TrafficControls = {
  global: globalProjection,
  compression: {
    enabled: false,
    encoders: ["zstd", "gzip"],
    level: 7,
    min_size: "3k",
    types: ["application/json", "text/plain"],
    precompressed: true,
  },
  rate_limit: {
    enabled: false,
    key: "header:X-Tenant",
    rate: 31,
    burst: 17,
    max_conns: 23,
  },
  cache: {
    enabled: false,
    memory_max_size: "73m",
    memory_max: "73m",
    disk_path: "/var/cache/jul",
    disk_max_size: "5g",
    default_ttl: "71s",
    stale_while_revalidate: "13s",
    stale_if_error: "29s",
  },
  servers: [server],
};

describe("issue #81 sparse patch builders", () => {
  it("round-trips every dormant compression field and emits no no-op", () => {
    const initial = seedCompression(traffic);
    expect(initial).toEqual({
      enabled: false,
      encoders: ["zstd", "gzip"],
      level: 7,
      minSize: "3k",
      types: ["application/json", "text/plain"],
      precompressed: true,
    });
    expect(buildCompressionPatch(initial, { ...initial })).toBeNull();
    expect(buildCompressionPatch(initial, { ...initial, enabled: true })).toEqual({
      op: "compression_set",
      compression: { enabled: true },
    });
  });

  it("preserves false, zero, empty string, and empty arrays as intentional properties", () => {
    const initial = seedCompression(traffic);
    expect(
      buildCompressionPatch(initial, {
        ...initial,
        enabled: true,
        encoders: [],
        level: 0,
        minSize: "",
        types: [],
        precompressed: false,
      }),
    ).toEqual({
      op: "compression_set",
      compression: {
        enabled: true,
        encoders: [],
        level: 0,
        min_size: "",
        types: [],
        precompressed: false,
      },
    });
  });

  it("round-trips dormant global max_conns and emits exactly one changed property", () => {
    const initial = seedRateLimit(traffic);
    expect(initial.maxConns).toBe(23);
    expect(buildGlobalRateLimitPatch(initial, { ...initial })).toBeNull();
    expect(buildGlobalRateLimitPatch(initial, { ...initial, burst: 0 })).toEqual({
      op: "rate_limit_global_set",
      rate_limit: { burst: 0 },
    });
    expect(buildGlobalRateLimitPatch(initial, { ...initial, enabled: true })).toEqual({
      op: "rate_limit_global_set",
      rate_limit: { enabled: true },
    });
  });

  it("builds sparse global_set and retains worker_threads as a wire string", () => {
    const initial = seedGlobalSettings(globalProjection);
    expect(buildGlobalPatch(initial, { ...initial })).toBeNull();
    expect(buildGlobalPatch(initial, { ...initial, workerThreads: "12" })).toEqual({
      op: "global_set",
      global: { worker_threads: "12" },
    });
    expect(buildGlobalPatch(initial, { ...initial, redactMinSecretLength: 0 })).toEqual({
      op: "global_set",
      global: { redact_min_secret_length: 0 },
    });
  });

  it("seeds server limits from projection and never overwrites a no-op", () => {
    const initial = seedServerLimits(server);
    expect(initial).toEqual({
      bodyLimit: "17m",
      readTimeout: "21s",
      writeTimeout: "22s",
      idleTimeout: "91s",
    });
    expect(buildServerLimitsPatch(server, initial, { ...initial })).toBeNull();
    expect(buildServerLimitsPatch(server, initial, { ...initial, idleTimeout: "0s" })).toEqual({
      op: "server_set_limits",
      listen: server.listen,
      server_names: [],
      limits: { idle_timeout: "0s" },
    });
  });
});

describe("issue #81 complete cache table", () => {
  it("seeds all seven fields and preserves them while disabled", () => {
    const draft = seedCache(traffic);
    expect(draft).toEqual({
      enabled: false,
      memoryMaxSize: "73m",
      diskPath: "/var/cache/jul",
      diskMaxSize: "5g",
      defaultTTL: "71s",
      staleWhileRevalidate: "13s",
      staleIfError: "29s",
    });
    const toml = generateCacheToml(draft);
    expect(toml).toContain("enabled = false");
    expect(toml).toContain('memory_max_size = "73m"');
    expect(toml).toContain('disk_path = "/var/cache/jul"');
    expect(toml).toContain('disk_max_size = "5g"');
    expect(toml).toContain('default_ttl = "71s"');
    expect(toml).toContain('stale_while_revalidate = "13s"');
    expect(toml).toContain('stale_if_error = "29s"');
  });

  it("preserves every untouched cache field during a one-field edit", () => {
    const initial = seedCache(traffic);
    const toml = generateCacheToml({ ...initial, defaultTTL: "2m" });
    expect(toml).toContain('memory_max_size = "73m"');
    expect(toml).toContain('disk_path = "/var/cache/jul"');
    expect(toml).toContain('disk_max_size = "5g"');
    expect(toml).toContain('default_ttl = "2m"');
    expect(toml).toContain('stale_while_revalidate = "13s"');
    expect(toml).toContain('stale_if_error = "29s"');
  });
});
