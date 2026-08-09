/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { ConfigPatch } from "@/api/client.ts";

describe("issue #80 patch wire contract", () => {
  it("retains intentional false, zero, empty string, and empty arrays", () => {
    const compression = {
      op: "compression_set",
      compression: {
        enabled: false,
        encoders: [],
        level: 0,
        min_size: "",
        types: [],
        precompressed: false,
      },
    } satisfies ConfigPatch;
    const rateLimit = {
      op: "rate_limit_global_set",
      rate_limit: { enabled: false, key: "", rate: 0, burst: 0, max_conns: 0 },
    } satisfies ConfigPatch;

    expect(JSON.parse(JSON.stringify(compression))).toEqual(compression);
    expect(JSON.parse(JSON.stringify(rateLimit))).toEqual(rateLimit);
  });

  it("omits properties that the caller does not supply", () => {
    const global = {
      op: "global_set",
      global: { log_level: "debug" },
    } satisfies ConfigPatch;
    const encoded = JSON.parse(JSON.stringify(global)) as Record<string, unknown>;

    expect(encoded).toEqual({ op: "global_set", global: { log_level: "debug" } });
    expect(JSON.stringify(encoded)).not.toContain("worker_threads");
    expect(JSON.stringify(encoded)).not.toContain("log_format");
  });

  it("discriminates route and global operations behind the same rate_limit key", () => {
    const route = {
      op: "route_set_rate_limit",
      listen: ":8080",
      server_names: [],
      match_type: "prefix",
      path: "/api",
      rate_limit: { enabled: true, rate: 100, burst: 200, key: "ip" },
    } satisfies ConfigPatch;
    const global = {
      op: "rate_limit_global_set",
      rate_limit: { max_conns: 0 },
    } satisfies ConfigPatch;

    expect(route.op).toBe("route_set_rate_limit");
    expect(global.op).toBe("rate_limit_global_set");
    expect(Object.keys(route).filter((key) => key === "rate_limit")).toHaveLength(1);
    expect(Object.keys(global).filter((key) => key === "rate_limit")).toHaveLength(1);
  });
});
