/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it } from "vitest";
import {
  accessLogWarnings,
  defaultAccessLogDraft,
  generateAccessLogToml,
} from "@/lib/accessLogToml.ts";

describe("access-log TOML", () => {
  it("emits an explicit default-on block", () => {
    const toml = generateAccessLogToml(defaultAccessLogDraft());
    expect(toml).toContain("[observability.access_log]");
    expect(toml).toContain("enabled = true");
    expect(toml).toContain('sinks = ["stdout"]');
  });

  it("retains dormant file settings when disabled", () => {
    const toml = generateAccessLogToml({
      ...defaultAccessLogDraft(),
      enabled: false,
      sinks: ["file"],
      file: "/var/log/jul/access.log",
      format: "json",
    });
    expect(toml).toContain("enabled = false");
    expect(toml).toContain('file = "/var/log/jul/access.log"');
    expect(toml).toContain('format = "json"');
  });

  it("warns when enabled without a sink", () => {
    expect(accessLogWarnings({ ...defaultAccessLogDraft(), sinks: [] }).join(" ")).toContain(
      "no sink is selected",
    );
  });
});
