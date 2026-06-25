import { describe, it, expect } from "vitest";
import { emptyWAFDraft, generateWafToml, wafWarnings, type WAFDraft } from "@/lib/wafToml.ts";

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

  it("collapses to a single disabled line when disabled", () => {
    expect(generateWafToml(draft({ enabled: false }))).toBe("[waf]\nenabled = false");
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
