/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import type { ServerLimitsProjection } from "@/api/client.ts";
import {
  buildServerLimitsPatch,
  seedServerLimits,
  serverTargetKey,
} from "@/lib/trafficPatchBuilders.ts";

const base: ServerLimitsProjection = {
  listen: ":8443",
  server_names: ["a.example"],
  client_max_body_size: "1m",
  read_timeout: "10s",
  write_timeout: "10s",
  idle_timeout: "30s",
};
const vhostB: ServerLimitsProjection = { ...base, server_names: ["b.example"] };
const catchAll: ServerLimitsProjection = { ...base, server_names: [] };

describe("issue #82 exact server coordinates", () => {
  it("serializes the selected shared-listener vhost coordinates", () => {
    const initial = seedServerLimits(vhostB);
    expect(buildServerLimitsPatch(vhostB, initial, { ...initial, idleTimeout: "45s" })).toEqual({
      op: "server_set_limits",
      listen: ":8443",
      server_names: ["b.example"],
      limits: { idle_timeout: "45s" },
    });
    expect(serverTargetKey(base)).not.toBe(serverTargetKey(vhostB));
  });

  it("serializes an explicit empty server_names array for the catch-all", () => {
    const initial = seedServerLimits(catchAll);
    expect(buildServerLimitsPatch(catchAll, initial, { ...initial, readTimeout: "20s" })).toMatchObject({
      listen: ":8443",
      server_names: [],
    });
  });

  it("uses deterministic set identity independent of server-name order", () => {
    expect(serverTargetKey({ listen: ":8443", server_names: ["b", "a"] })).toBe(
      serverTargetKey({ listen: ":8443", server_names: ["a", "b"] }),
    );
  });
});
