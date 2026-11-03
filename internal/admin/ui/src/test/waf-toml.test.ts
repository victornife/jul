/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect } from "vitest";
import { emptyWAFDraft, generateWafToml, wafWarnings, type WAFDraft } from "@/lib/wafToml.ts";
import { upsertTopLevelTable } from "@/lib/trafficToml.ts";

function draft(over: Partial<WAFDraft>): WAFDraft {
  return { ...emptyWAFDraft(), ...over };
}

describe("generateWafToml", () => {
  it("defaults to detect mode with the CRS enabled", () => {
    const toml = generateWafToml(emptyWAFDraft());
    expect(toml).toContain("[waf]");
    expect(toml).toContain("enabled = true");
    expect(toml).toContain('mode = "detect"');
    expect(toml).toContain("crs_enabled = true");
    expect(toml).toContain("paranoia = 1");
  });

  it("preserves dormant policy fields and exceptions when disabled", () => {
    const disabled = generateWafToml(
      draft({
        enabled: false,
        mode: "block",
        crsEnabled: true,
        blockingParanoia: 2,
        detectionParanoia: 3,
        directivesFiles: "/etc/jul/waf/custom.conf",
        inlineRules: 'SecRule ARGS:id "@rx ^\\d+$" "id:1001,phase:2,deny"',
        exceptions: [{ id: "exc-1", rule_id: 942100, scope: "narrow_target", target: "ARGS:bio" }],
      }),
    );
    expect(disabled).toContain("enabled = false");
    expect(disabled).toContain('mode = "block"');
    expect(disabled).toContain("blocking_paranoia = 2");
    expect(disabled).toContain("detection_paranoia = 3");
    expect(disabled).toContain('directives_files = ["/etc/jul/waf/custom.conf"]');
    expect(disabled).toContain('SecRule ARGS:id "@rx ^\\\\d+$" "id:1001,phase:2,deny"');
    expect(disabled).toContain("[[waf.exceptions]]");
    expect(disabled).toContain("rule_id = 942100");
  });

  it("escapes backslashes and triple/quadruple quotes in multiline inline_rules", () => {
    const toml = generateWafToml(
      draft({
        crsEnabled: false,
        inlineRules: 'SecRule ARGS:code "@rx ^\\d{4}$" "id:1002,phase:2,deny,msg:\'bad """" quote\'"',
      }),
    );
    expect(toml).toContain('"@rx ^\\\\d{4}$"');
    expect(toml).toContain('\\"\\"\\"\\"');
  });

  it("replaces dispersed [[waf.exceptions]] after [[servers]] without leaving duplicates and preserves [servers.locations.waf]", () => {
    const rawWithDispersed = [
      "[waf]",
      "enabled = true",
      'mode = "block"',
      "crs_enabled = true",
      "",
      "[[servers]]",
      'listen = ":8080"',
      "  [[servers.locations]]",
      '  match = { type = "prefix", path = "/api" }',
      "  [servers.locations.waf]",
      "  enabled = true",
      '  mode = "detect"',
      "",
      "[[waf.exceptions]]",
      'id = "stale-after-servers"',
      "rule_id = 942100",
      'scope = "narrow_target"',
      'target = "ARGS:old_field"',
      "",
    ].join("\n");

    const updatedFragment = generateWafToml(
      draft({
        enabled: true,
        mode: "block",
        crsEnabled: true,
        exceptions: [
          {
            id: "fresh-exc",
            rule_id: 941100,
            scope: "narrow_target",
            target: "ARGS:new_field",
          },
        ],
      }),
    );
    const merged = upsertTopLevelTable(rawWithDispersed, "waf", updatedFragment);
    expect(merged).not.toContain("stale-after-servers");
    expect(merged).not.toContain("ARGS:old_field");
    expect(merged.match(/\[\[waf\.exceptions\]\]/g)).toHaveLength(1);
    expect(merged).toContain("fresh-exc");
    expect(merged).toContain("[servers.locations.waf]");
    expect(merged).toContain('match = { type = "prefix", path = "/api" }');

    // Removing all exceptions must leave zero [[waf.exceptions]] blocks.
    const noExcFragment = generateWafToml(draft({ enabled: true, mode: "block", crsEnabled: true }));
    const mergedNoExc = upsertTopLevelTable(merged, "waf", noExcFragment);
    expect(mergedNoExc).not.toContain("[[waf.exceptions]]");
    expect(mergedNoExc).toContain("[servers.locations.waf]");

    // Multiline literal strings ('''), single-quoted strings ('...'), escaped basic strings, and comments (#)
    // containing bracketed text must not be mistaken for top-level headers.
    const literalDoc = [
      "# top comment [waf]",
      "[waf]",
      'note = "escaped \\" [servers] quote"',
      "literal_single = '[servers]'",
      "raw_rules = '''",
      "[servers]",
      "[[waf.exceptions]]",
      "'''",
      'ml_basic = """',
      'escaped \\""" [servers]',
      '"""',
      "",
      "[[servers]] # server comment",
      'listen = ":8443"',
    ].join("\n");
    const replacedLiteral = upsertTopLevelTable(literalDoc, "waf", "[waf]\nenabled = false");
    expect(replacedLiteral).toContain("enabled = false");
    expect(replacedLiteral).not.toContain("raw_rules");
    expect(replacedLiteral).toContain("[[servers]] # server comment");

    // Item 7 / P2: Root dotted-key and inline-table WAF assignments preceding root admin.*
    // and global.* assignments must keep all unrelated root assignments BEFORE the emitted
    // [waf] table so they never become waf.admin.* or waf.global.* subkeys.
    const dottedDoc = [
      "# global header comment",
      "waf.enabled = true",
      'waf.mode = "block"',
      "waf.crs_enabled = false",
      "waf.directives_files = [",
      '  "/etc/jul/waf/a.conf",',
      "]",
      "waf.exceptions = [",
      '  { id = "old-exc", rule_id = 942100, scope = "narrow_target", target = "ARGS:q" },',
      "]",
      'admin.enabled = true',
      'admin.listen = "127.0.0.1:9090"',
      'global.log_level = "info"',
      "",
      "[[servers]]",
      'listen = ":8080"',
    ].join("\n");
    const replacedDotted = upsertTopLevelTable(
      dottedDoc,
      "waf",
      generateWafToml(draft({ enabled: true, mode: "detect", crsEnabled: true })),
    );
    expect(replacedDotted).toContain('admin.listen = "127.0.0.1:9090"');
    expect(replacedDotted).toContain('global.log_level = "info"');
    expect(replacedDotted).not.toContain("waf.enabled");
    expect(replacedDotted).not.toContain("waf.mode");
    expect(replacedDotted).not.toContain("waf.crs_enabled");
    expect(replacedDotted).not.toContain("old-exc");
    expect(replacedDotted).toContain("[waf]\nenabled = true\nmode = \"detect\"");
    expect(replacedDotted).toContain("[[servers]]\nlisten = \":8080\"");
    // Verify unrelated root assignments appear BEFORE [waf] so they stay in root scope.
    expect(replacedDotted.indexOf('admin.listen = "127.0.0.1:9090"')).toBeLessThan(
      replacedDotted.indexOf("[waf]"),
    );
    expect(replacedDotted.indexOf('global.log_level = "info"')).toBeLessThan(
      replacedDotted.indexOf("[waf]"),
    );

    const inlineRootDoc = [
      'waf = { enabled = true, mode = "block", crs_enabled = false }',
      "",
      "[[servers]]",
      'listen = ":8080"',
    ].join("\n");
    const replacedInline = upsertTopLevelTable(
      inlineRootDoc,
      "waf",
      generateWafToml(draft({ enabled: false, mode: "detect", crsEnabled: true })),
    );
    expect(replacedInline).not.toContain("waf = {");
    expect(replacedInline).toContain("[waf]\nenabled = false");
    expect(replacedInline).toContain("[[servers]]\nlisten = \":8080\"");

    // Quoted top-level table names containing a dot (e.g. ["waf.audit"]) are distinct keys from [waf]
    // and must be preserved, while [waf."audit.log"] belongs to waf and is replaced.
    const quotedKeyDoc = [
      '[waf."audit.log"]',
      "enabled = true",
      "",
      '["waf.audit"]',
      'sink = "preserve-me"',
      "",
      "[['waf.audit']]",
      'item = "preserve-array"',
    ].join("\n");
    const replacedQuoted = upsertTopLevelTable(quotedKeyDoc, "waf", "[waf]\nenabled = false");
    expect(replacedQuoted).toContain("[waf]\nenabled = false");
    expect(replacedQuoted).not.toContain('[waf."audit.log"]');
    expect(replacedQuoted).toContain('["waf.audit"]');
    expect(replacedQuoted).toContain('sink = "preserve-me"');
    expect(replacedQuoted).toContain("[['waf.audit']]");
  });

  it("omits paranoia when the CRS is disabled", () => {
    const toml = generateWafToml(draft({ crsEnabled: false, paranoia: 3 }));
    expect(toml).toContain("crs_enabled = false");
    expect(toml).not.toContain("paranoia");
  });

  it("emits block_status only when set", () => {
    expect(generateWafToml(draft({ blockStatus: 0 }))).not.toContain("block_status");
    expect(generateWafToml(draft({ blockStatus: 406 }))).toContain("block_status = 406");
  });

  it("emits directive files and a request body limit", () => {
    const toml = generateWafToml(
      draft({
        crsEnabled: false,
        directivesFiles: "/etc/jul/waf/a.conf\n/etc/jul/waf/b.conf",
        requestBodyLimit: "256k",
      }),
    );
    expect(toml).toContain('directives_files = ["/etc/jul/waf/a.conf", "/etc/jul/waf/b.conf"]');
    expect(toml).toContain('request_body_limit = "256k"');
  });

  it("emits inline rules as a multi-line string", () => {
    const toml = generateWafToml(
      draft({ crsEnabled: false, inlineRules: 'SecRule ARGS "@rx evil" "id:1,deny"' }),
    );
    expect(toml).toContain('inline_rules = """');
    expect(toml).toContain('SecRule ARGS "@rx evil" "id:1,deny"');
  });

  it("emits response_body_check only when enabled", () => {
    expect(generateWafToml(draft({ responseBodyCheck: false }))).not.toContain(
      "response_body_check",
    );
    expect(generateWafToml(draft({ responseBodyCheck: true }))).toContain(
      "response_body_check = true",
    );
  });

  it("emits separate blocking_paranoia, detection_paranoia, and [[waf.exceptions]]", () => {
    const toml = generateWafToml(
      draft({
        blockingParanoia: 1,
        detectionParanoia: 3,
        exceptions: [
          {
            id: "allow-bio",
            rule_id: 942100,
            scope: "narrow_target",
            target: "ARGS:profile_bio",
            methods: ["POST"],
            rationale: "Allow SQL keywords in bio",
            owner: "secops",
            reference: "SEC-631",
            created_at: "2026-11-01",
            review_at: "2027-02-01",
            fixtures: ["benign-bio"],
          },
        ],
      }),
    );
    expect(toml).toContain("blocking_paranoia = 1");
    expect(toml).toContain("detection_paranoia = 3");
    expect(toml).toContain("[[waf.exceptions]]");
    expect(toml).toContain("rule_id = 942100");
    expect(toml).toContain('target = "ARGS:profile_bio"');
    expect(toml).toContain('review_at = "2027-02-01"');
  });

  it("updates blocking_paranoia when a modern policy changes paranoia", () => {
    const toml = generateWafToml(
      draft({
        paranoia: 2,
        blockingParanoia: 2,
        detectionParanoia: 3,
      }),
    );
    expect(toml).toContain("blocking_paranoia = 2");
    expect(toml).toContain("detection_paranoia = 3");
    expect(toml).not.toContain("\nparanoia =");
  });
});

describe("wafWarnings", () => {
  it("has no warnings for the safe default (detect + CRS)", () => {
    expect(wafWarnings(emptyWAFDraft())).toHaveLength(0);
  });

  it("warns when enabled with no rules", () => {
    expect(wafWarnings(draft({ crsEnabled: false })).some((w) => w.includes("no rules"))).toBe(
      true,
    );
  });

  it("warns about block mode with the CRS (detect-first rollout)", () => {
    expect(
      wafWarnings(draft({ mode: "block" })).some((w) =>
        w.toLowerCase().includes("detect mode first"),
      ),
    ).toBe(true);
  });

  it("warns about paranoia without the CRS", () => {
    expect(
      wafWarnings(draft({ crsEnabled: false, inlineRules: "SecRule ...", paranoia: 2 })).some((w) =>
        w.includes("Paranoia level applies only"),
      ),
    ).toBe(true);
  });

  it("warns about high paranoia in block mode", () => {
    expect(
      wafWarnings(draft({ mode: "block", paranoia: 3 })).some((w) => w.includes("paranoia ≥ 3")),
    ).toBe(true);
  });

  it("returns nothing when disabled", () => {
    expect(wafWarnings(draft({ enabled: false }))).toHaveLength(0);
  });
});
