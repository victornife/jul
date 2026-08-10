/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect } from "vitest";
import {
  upsertTopLevelTable,
  generateCacheToml,
  compressionWarnings,
  cacheWarnings,
  rateLimitWarnings,
} from "@/lib/trafficToml.ts";
import { resolveTheme } from "@/lib/theme.ts";

describe("upsertTopLevelTable", () => {
  it("appends the table when absent", () => {
    const out = upsertTopLevelTable('listen = ":80"\n', "cache", "[cache]\nenabled = true");
    expect(out).toContain('listen = ":80"');
    expect(out).toContain("[cache]\nenabled = true");
  });

  it("replaces an existing top-level table in place", () => {
    const raw = [
      "[cache]",
      "enabled = false",
      "",
      "[[servers]]",
      'listen = ":80"',
      "",
    ].join("\n");
    const out = upsertTopLevelTable(raw, "cache", "[cache]\nenabled = true");
    expect(out).toContain("enabled = true");
    expect(out).not.toContain("enabled = false");
    // The unrelated server block is preserved.
    expect(out).toContain("[[servers]]");
    expect(out).toContain('listen = ":80"');
  });

  it("replaces a table including its sub-tables", () => {
    const raw = [
      "[cache]",
      "enabled = true",
      "[cache.tiers]",
      "memory = true",
      "[rate_limit]",
      "enabled = true",
    ].join("\n");
    const out = upsertTopLevelTable(raw, "cache", "[cache]\nenabled = false");
    expect(out).not.toContain("[cache.tiers]");
    expect(out).toContain("[rate_limit]");
  });
});

describe("cache generator", () => {
  it("emits cache fields", () => {
    const toml = generateCacheToml({
      enabled: true,
      memoryMaxSize: "64m",
      diskPath: "/var/cache",
      diskMaxSize: "",
      defaultTTL: "60s",
      staleWhileRevalidate: "10s",
      staleIfError: "",
    });
    expect(toml).toContain('memory_max_size = "64m"');
    expect(toml).toContain('disk_path = "/var/cache"');
    expect(toml).toContain('stale_while_revalidate = "10s"');
  });
});

describe("warnings", () => {
  it("warns about compressing already-compressed assets", () => {
    expect(
      compressionWarnings({
        enabled: true,
        encoders: ["gzip"],
        level: 0,
        minSize: "1k",
        types: ["image/png"],
        precompressed: false,
      }).length,
    ).toBeGreaterThan(0);
  });

  it("warns about a missing cache cap", () => {
    expect(
      cacheWarnings({
        enabled: true,
        memoryMaxSize: "",
        diskPath: "",
        diskMaxSize: "",
        defaultTTL: "60s",
        staleWhileRevalidate: "",
        staleIfError: "",
      }).length,
    ).toBeGreaterThan(0);
  });

  it("warns about spoofable header keys and zero rate", () => {
    expect(
      rateLimitWarnings({ enabled: true, key: "header:X", rate: 0, burst: 0, maxConns: 0 }).length,
    ).toBe(2);
  });
});

describe("resolveTheme", () => {
  it("passes through explicit themes", () => {
    expect(resolveTheme("light")).toBe("light");
    expect(resolveTheme("dark")).toBe("dark");
  });
  it("resolves system to a concrete theme", () => {
    expect(["light", "dark"]).toContain(resolveTheme("system"));
  });
});
