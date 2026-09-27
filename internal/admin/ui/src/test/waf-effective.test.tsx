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
  type SecurityProjection,
  type WAFEffectivePolicy,
  type WAFPolicySummary,
} from "@/api/client.ts";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { WAFEffectiveCard } from "@/features/security/WAFEffectiveCard.tsx";
import { plural, ruleSources, servingDiffers } from "@/lib/wafEffective.ts";

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
  external_digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
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
    const digest = screen.getByText("sha256:0123456789ab…");
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
      },
    });
    expect(screen.getByText("OWASP CRS (version not reported) · paranoia 3")).toBeInTheDocument();
    expect(screen.getByText("limit not reported")).toBeInTheDocument();
  });
});

describe("wafEffective helpers", () => {
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
