/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Unit tests for the pure per-location WAF override mapping (Phase 4e). They
 * pin the draft <-> patch round-trip and the blocking-warning logic that mirrors
 * the server's validateWAF, so the guided editor refuses saves the backend would
 * reject and never clobbers rules it seeded.
 */
import { describe, it, expect } from "vitest";

import {
  seedWAFOverride,
  wafOverrideToPatch,
  wafOverrideWarnings,
  type WAFOverrideDraft,
} from "@/lib/wafOverride.ts";
import type { LocationWAF } from "@/api/client.ts";

function target(over: Partial<LocationWAF> = {}): LocationWAF {
  return {
    listen: ":8080",
    server_names: [],
    match_type: "prefix",
    path: "/api",
    enabled: true,
    crs_enabled: false,
    ...over,
  };
}

function draft(over: Partial<WAFOverrideDraft> = {}): WAFOverrideDraft {
  return {
    enabled: true,
    mode: "detect",
    crsEnabled: false,
    blockStatus: "",
    paranoia: "",
    requestBodyLimit: "",
    responseBodyCheck: false,
    directivesFiles: "",
    inlineRules: "",
    ...over,
  };
}

describe("seedWAFOverride", () => {
  it("seeds every advanced field from the projection", () => {
    const d = seedWAFOverride(
      target({
        enabled: true,
        mode: "block",
        crs_enabled: true,
        block_status: 429,
        paranoia: 3,
        request_body_limit: "256k",
        response_body_check: true,
        directives_files: ["/a.conf", "/b.conf"],
        inline_rules: "SecRule ARGS \"@rx x\" \"id:1,deny\"",
      }),
    );
    expect(d).toMatchObject({
      enabled: true,
      mode: "block",
      crsEnabled: true,
      blockStatus: "429",
      paranoia: "3",
      requestBodyLimit: "256k",
      responseBodyCheck: true,
      inlineRules: "SecRule ARGS \"@rx x\" \"id:1,deny\"",
    });
    // Rule files join one per line for the textarea.
    expect(d.directivesFiles).toBe("/a.conf\n/b.conf");
  });

  it("leaves numeric fields blank when the override omits them", () => {
    const d = seedWAFOverride(target({ enabled: true }));
    expect(d.blockStatus).toBe("");
    expect(d.paranoia).toBe("");
    expect(d.mode).toBe("detect");
    expect(d.directivesFiles).toBe("");
  });
});

describe("wafOverrideToPatch", () => {
  it("emits the full override when every field is populated", () => {
    const p = wafOverrideToPatch(
      draft({
        enabled: true,
        mode: "block",
        crsEnabled: true,
        blockStatus: "429",
        paranoia: "3",
        requestBodyLimit: " 256k ",
        responseBodyCheck: true,
        directivesFiles: "/a.conf\n  \n/b.conf",
        inlineRules: "SecRule ARGS x",
      }),
    );
    expect(p).toEqual({
      enabled: true,
      mode: "block",
      crs_enabled: true,
      response_body_check: true,
      block_status: 429,
      paranoia: 3,
      request_body_limit: "256k",
      directives_files: ["/a.conf", "/b.conf"],
      inline_rules: "SecRule ARGS x",
    });
  });

  it("omits unset optional fields so the override stays minimal", () => {
    const p = wafOverrideToPatch(draft({ enabled: true, mode: "detect" }));
    expect(p).toEqual({
      enabled: true,
      mode: "detect",
      crs_enabled: false,
      response_body_check: false,
    });
    expect(p).not.toHaveProperty("block_status");
    expect(p).not.toHaveProperty("paranoia");
    expect(p).not.toHaveProperty("request_body_limit");
    expect(p).not.toHaveProperty("directives_files");
    expect(p).not.toHaveProperty("inline_rules");
  });

  it("round-trips a seeded override back to an equivalent patch", () => {
    const t = target({
      enabled: true,
      mode: "block",
      crs_enabled: true,
      block_status: 406,
      paranoia: 2,
      request_body_limit: "128k",
      response_body_check: true,
      directives_files: ["/x.conf"],
      inline_rules: "SecRule A B",
    });
    const p = wafOverrideToPatch(seedWAFOverride(t));
    expect(p).toMatchObject({
      enabled: true,
      mode: "block",
      crs_enabled: true,
      block_status: 406,
      paranoia: 2,
      request_body_limit: "128k",
      response_body_check: true,
      directives_files: ["/x.conf"],
      inline_rules: "SecRule A B",
    });
  });
});

describe("wafOverrideWarnings", () => {
  it("is silent for a disabled override", () => {
    expect(wafOverrideWarnings(draft({ enabled: false }))).toEqual([]);
  });

  it("flags an enabled override with no rules", () => {
    const w = wafOverrideWarnings(draft({ enabled: true, crsEnabled: false }));
    expect(w.some((m) => m.includes("no rules"))).toBe(true);
  });

  it("accepts CRS, a rule file, or inline rules as rules", () => {
    expect(wafOverrideWarnings(draft({ enabled: true, crsEnabled: true }))).toEqual([]);
    expect(
      wafOverrideWarnings(draft({ enabled: true, directivesFiles: "/a.conf" })),
    ).toEqual([]);
    expect(wafOverrideWarnings(draft({ enabled: true, inlineRules: "SecRule A B" }))).toEqual([]);
  });

  it("rejects an out-of-range block status", () => {
    const w = wafOverrideWarnings(draft({ enabled: true, crsEnabled: true, blockStatus: "999" }));
    expect(w.some((m) => m.includes("Block status"))).toBe(true);
  });

  it("rejects an out-of-range paranoia level", () => {
    const w = wafOverrideWarnings(draft({ enabled: true, crsEnabled: true, paranoia: "7" }));
    expect(w.some((m) => m.includes("Paranoia level must be between 1 and 4"))).toBe(true);
  });

  it("warns paranoia applies only with the CRS", () => {
    const w = wafOverrideWarnings(
      draft({ enabled: true, crsEnabled: false, inlineRules: "x", paranoia: "2" }),
    );
    expect(w.some((m) => m.includes("only when the CRS is enabled"))).toBe(true);
  });
});
