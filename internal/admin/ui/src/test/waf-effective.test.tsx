/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import {
  SecurityProjectionSchema,
  WAFEffectivePolicySchema,
  type SecurityProjection,
  type WAFEffectivePolicy,
  type WAFPolicySummary,
} from "@/api/client.ts";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { WAFEffectiveCard } from "@/features/security/WAFEffectiveCard.tsx";
import {
  evaluationStateLabel,
  plural,
  ruleSources,
  servingDiffers,
  shortDigest,
} from "@/lib/wafEffective.ts";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

const config = SecurityProjectionSchema.parse({
  auth_enabled: false,
  require_cert_count: 0,
  waf_enabled: true,
  waf_locations: 3,
  waf_global_enabled: true,
  waf_global_mode: "block",
  waf_crs_enabled: true,
  secret_refs: 0,
});

const crsGlobal: WAFPolicySummary = {
  mode: "block",
  block_status: 403,
  crs_enabled: true,
  crs_version: "4.25.0",
  paranoia: 1,
  paranoia_default: true,
  request_body_limit_bytes: 131072,
  response_body_inspection: false,
  rule_files_configured: 0,
  external_files: 0,
  inline_rules: false,
  rules: { total: 612, embedded: 612, external: 0, inline: 0, generated: 0 },
};

const detectOverride: WAFPolicySummary = {
  mode: "detect",
  block_status: 403,
  crs_enabled: false,
  request_body_limit_bytes: 65536,
  response_body_inspection: true,
  rule_files_configured: 1,
  external_files: 2,
  external_digest:
    "ev1:0123456789abcdef0123456789abcdef:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
  inline_rules: true,
  rules: { total: 3, embedded: 0, external: 2, inline: 1, generated: 0 },
};

const serving: WAFEffectivePolicy = {
  compiled: true,
  embedded_crs_version: "4.25.0",
  engine_version: "v3.7.0",
  generation: 4,
  compiled_at: "2026-09-27T10:00:00Z",
  inheriting_routes: 2,
  override_routes: 1,
  disabled_override_routes: 1,
  unprotected_routes: 1,
  global_enabled: true,
  global: crsGlobal,
  overrides: [
    {
      listen: ":8443",
      server_names: ["shop.example"],
      match_type: "prefix",
      path: "/api",
      enabled: true,
      policy: detectOverride,
    },
    { listen: ":8443", match_type: "exact", path: "/health", enabled: false },
  ],
};

function renderCard(effective: WAFEffectivePolicy | undefined, data: SecurityProjection = config) {
  return render(<WAFEffectiveCard data={data} effective={effective} />);
}

describe("WAFEffectiveCard (#440)", () => {
  it("renders nothing for a server that predates the projection", () => {
    const { container } = renderCard(undefined);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the serving generation, authoritative CRS version and coverage", () => {
    renderCard(serving);
    const card = screen.getByRole("region", { name: "Serving WAF policy" });
    expect(within(card).getByText(/generation 4 · compiled/)).toBeInTheDocument();
    expect(within(card).getByText(/Coraza v3.7.0 · embedded OWASP CRS 4.25.0/)).toBeInTheDocument();
    expect(within(card).getByText(/never updated online/)).toBeInTheDocument();
    expect(
      within(card).getByText(
        /2 inheriting the global policy · 1 with their own policy · 1 not inspected \(1 turned off by an override\)/,
      ),
    ).toBeInTheDocument();
    expect(
      within(card).getByText("OWASP CRS 4.25.0 · paranoia 1 (CRS default)"),
    ).toBeInTheDocument();
    expect(within(card).getByText("612 rules (612 embedded CRS)")).toBeInTheDocument();
    expect(
      within(card).getByText(/for non-CRS rules; CRS anomaly blocks use 403/),
    ).toBeInTheDocument();
    expect(within(card).queryByText(/differs from the serving policy/)).not.toBeInTheDocument();
  });

  it("shows overrides with detect mode, source classes and a content digest — never a path", () => {
    renderCard(serving);
    expect(screen.getByText(":8443 shop.example prefix /api")).toBeInTheDocument();
    expect(screen.getByText("detect only — not blocking")).toBeInTheDocument();
    expect(screen.getByText("3 rules (2 from rule files, 1 inline)")).toBeInTheDocument();
    expect(screen.getByText(/2 files read at compile/)).toBeInTheDocument();
    const digest = screen.getByText("ev1:01234567:fedcba987654…");
    expect(digest).toHaveAttribute("title", detectOverride.external_digest);
    expect(screen.getByText(/inspected \(limit set by the engine/)).toBeInTheDocument();
    expect(screen.getByText("WAF turned off for this route by its override.")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/\.conf|\.data|\/etc\//);
  });

  it("states when the build has no engine", () => {
    renderCard({
      ...serving,
      compiled: false,
      embedded_crs_version: undefined,
      generation: 3,
      global: undefined,
      overrides: [],
      global_enabled: false,
    });
    expect(screen.getByText(/This build has no WAF engine/)).toBeInTheDocument();
    expect(screen.getByText("Global policy disabled.")).toBeInTheDocument();
  });

  it("handles no serving generation and an unreported CRS version", () => {
    renderCard({
      ...serving,
      generation: 0,
      compiled_at: undefined,
      embedded_crs_version: undefined,
      engine_version: undefined,
      global: undefined,
      overrides: undefined,
    });
    expect(screen.getByText("no serving generation yet")).toBeInTheDocument();
    expect(screen.getByText(/CRS version not reported/)).toBeInTheDocument();
    expect(screen.queryByText(/Routes:/)).not.toBeInTheDocument();
  });

  it("flags a saved configuration that is not what is serving", () => {
    renderCard(serving, { ...config, waf_global_mode: "detect" });
    expect(screen.getByRole("status")).toHaveTextContent(/differs from the serving policy/);
  });

  it("says when every route overrides an enabled global policy", () => {
    renderCard({ ...serving, global: undefined });
    expect(
      screen.getByText("Global policy enabled, but every route overrides it."),
    ).toBeInTheDocument();
  });

  it("covers unreported limits and CRS version fallbacks", () => {
    renderCard({
      ...serving,
      overrides: [],
      global: {
        ...crsGlobal,
        crs_version: "",
        paranoia: 3,
        paranoia_default: false,
        request_body_limit_bytes: undefined,
        external_files: 1,
        external_digest: detectOverride.external_digest,
        policy_digest: detectOverride.external_digest,
      },
      evaluation: {
        state: "current",
        source: "server_observed",
        pack_id: "jul-waf-default-v1",
        total_cases: 10,
        passed_cases: 10,
        failed_cases: 0,
        legitimate_total: 4,
        legitimate_blocked: 0,
        malicious_total: 4,
        malicious_allowed: 0,
        changed_from_baseline: 0,
      },
    });
    expect(screen.getByText("OWASP CRS (version not reported) · paranoia 3")).toBeInTheDocument();
    expect(screen.getByText("limit not reported")).toBeInTheDocument();
    expect(screen.getByText("Verified (10/10 fixtures)")).toBeInTheDocument();
    expect(
      screen.getByText(/External rule files are not restored by configuration rollback alone/),
    ).toBeInTheDocument();
  });

  it("renders stale evaluation warning badges", () => {
    renderCard({
      ...serving,
      overrides: [],
      evaluation: {
        state: "stale_asset_changed",
        source: "supplied_report",
        total_cases: 10,
        passed_cases: 9,
        failed_cases: 1,
        legitimate_total: 4,
        legitimate_blocked: 1,
        malicious_total: 4,
        malicious_allowed: 0,
        changed_from_baseline: 1,
      },
    });
    expect(screen.getByText("Stale: external asset changed")).toBeInTheDocument();
  });

  it("keeps verified and untested sibling policies visibly separate", () => {
    const current = {
      state: "current",
      scope: "app-a",
      source: "server_observed",
      total_cases: 10,
      passed_cases: 10,
      failed_cases: 0,
      legitimate_total: 4,
      legitimate_blocked: 0,
      malicious_total: 4,
      malicious_allowed: 0,
      changed_from_baseline: 0,
    };
    renderCard(
      WAFEffectivePolicySchema.parse({
        ...serving,
        global: undefined,
        global_enabled: false,
        evaluation: current,
        overrides: [
          {
            listen: ":8443",
            path: "/a",
            route_id: "app-a",
            enabled: true,
            policy: crsGlobal,
            evaluation: current,
          },
          { listen: ":8443", path: "/b", route_id: "app-b", enabled: true, policy: detectOverride },
          { listen: ":8443", path: "/off", route_id: "off", enabled: false },
        ],
      }),
    );
    expect(screen.getByText(/1 of 2 active policies/)).toHaveClass("text-jul-warning");
    const a = screen.getByRole("group", { name: "Fixture evaluation: app-a" });
    const b = screen.getByRole("group", { name: "Fixture evaluation: app-b" });
    expect(within(a).getByText("Verified (10/10 fixtures)")).toBeInTheDocument();
    expect(within(a).getByText("Report scope: app-a")).toBeInTheDocument();
    expect(within(b).getByText("Not yet evaluated")).toBeInTheDocument();
    expect(within(b).queryByText(/Verified/)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("group", { name: "Fixture evaluation: off" }),
    ).not.toBeInTheDocument();
  });
});

describe("wafEffective helpers", () => {
  it("describes evaluation states (#630)", () => {
    expect(evaluationStateLabel(undefined).badge).toBe("Not yet evaluated");
    expect(
      evaluationStateLabel({
        state: "current",
        source: "supplied_report",
        pack_id: "custom",
        total_cases: 5,
        passed_cases: 4,
        failed_cases: 1,
        legitimate_total: 3,
        legitimate_blocked: 1,
        malicious_total: 2,
        malicious_allowed: 0,
        changed_from_baseline: 0,
      }).badge,
    ).toBe("Evaluated (1 failed)");
    expect(
      evaluationStateLabel({
        state: "failed",
        source: "server_observed",
        pack_id: "jul-waf-default-v1",
        total_cases: 10,
        passed_cases: 8,
        failed_cases: 2,
        legitimate_total: 5,
        legitimate_blocked: 1,
        malicious_total: 5,
        malicious_allowed: 1,
        changed_from_baseline: 2,
      }),
    ).toMatchObject({
      badge: "Evaluation failed (2 failed)",
      tone: "warning",
    });
    expect(
      evaluationStateLabel({
        state: "stale_policy_changed",
        total_cases: 5,
        passed_cases: 5,
        failed_cases: 0,
        legitimate_total: 3,
        legitimate_blocked: 0,
        malicious_total: 2,
        malicious_allowed: 0,
        changed_from_baseline: 0,
      }).badge,
    ).toBe("Stale: policy changed");
    expect(
      evaluationStateLabel({
        state: "stale_engine_or_crs_changed",
        total_cases: 5,
        passed_cases: 5,
        failed_cases: 0,
        legitimate_total: 3,
        legitimate_blocked: 0,
        malicious_total: 2,
        malicious_allowed: 0,
        changed_from_baseline: 0,
      }).badge,
    ).toBe("Stale: engine or CRS version changed");
    expect(
      evaluationStateLabel({
        state: "unavailable",
        total_cases: 0,
        passed_cases: 0,
        failed_cases: 0,
        legitimate_total: 0,
        legitimate_blocked: 0,
        malicious_total: 0,
        malicious_allowed: 0,
        changed_from_baseline: 0,
      }).badge,
    ).toBe("Evaluation unavailable");
  });

  it("pluralizes and describes rule sources", () => {
    expect(plural(1, "rule")).toBe("1 rule");
    expect(plural(2, "rule")).toBe("2 rules");
    expect(
      ruleSources({
        ...crsGlobal,
        rules: { total: 0, embedded: 0, external: 0, inline: 0, generated: 0 },
      }),
    ).toBe("0 rules");
    expect(
      ruleSources({
        ...crsGlobal,
        rules: { total: 5, embedded: 3, external: 0, inline: 1, generated: 1 },
      }),
    ).toBe("5 rules (3 embedded CRS, 1 inline, 1 Jul-generated)");
  });

  it("compares saved configuration with serving policy", () => {
    expect(servingDiffers(config, serving)).toBe(false);
    expect(servingDiffers({ ...config, waf_global_enabled: false }, serving)).toBe(true);
    expect(servingDiffers({ ...config, waf_crs_enabled: false }, serving)).toBe(true);
    expect(servingDiffers(config, { ...serving, global: undefined })).toBe(false);
    expect(servingDiffers({ ...config, waf_global_mode: undefined }, serving)).toBe(false);
    expect(shortDigest(detectOverride.external_digest)).toBe("ev1:01234567:fedcba987654…");
    expect(shortDigest("sha256:0123456789abcdef0123456789abcdef")).toBe("sha256:0123456789ab…");
    expect(shortDigest(undefined)).toBe("…");
  });
});

describe("SecurityPanel serving WAF policy", () => {
  afterEach(() => vi.restoreAllMocks());

  it("renders the serving policy from /api/security", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ...config, waf_effective: serving }),
      }),
    );
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByRole("heading", { name: "Serving WAF policy" })).toBeInTheDocument();
  });
});
