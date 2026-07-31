/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Vitest tests for the Console Security panel build-tag degradation (Phase 1.4):
 * the schema default and the WAF "not compiled" banner that warns the apply
 * preflight will reject an enabled WAF on a non-`waf` build.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

import { SecurityProjectionSchema } from "@/api/client.ts";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";

// A minimal security projection with the WAF enabled.
const projection = {
  auth_enabled: false,
  require_cert_count: 0,
  waf_enabled: true,
  waf_locations: 1,
  waf_block_locs: 1,
  waf_detect_locs: 0,
  waf_crs_locs: 0,
  waf_mode: "block",
  secret_refs: 0,
};

// ── schema ──────────────────────────────────────────────────────────────────

describe("SecurityProjectionSchema", () => {
  it("defaults waf_compiled to true when the field is omitted", () => {
    const parsed = SecurityProjectionSchema.parse(projection);
    expect(parsed.waf_compiled).toBe(true);
  });

  it("preserves an explicit waf_compiled=false", () => {
    const parsed = SecurityProjectionSchema.parse({ ...projection, waf_compiled: false });
    expect(parsed.waf_compiled).toBe(false);
  });
});

// ── panel ───────────────────────────────────────────────────────────────────

describe("SecurityPanel WAF build-tag degradation", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("warns when the build lacks the WAF engine", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ...projection, waf_compiled: false }),
      }),
    );
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(
      await screen.findByText(/does not include the web application firewall/i),
    ).toBeInTheDocument();
  });

  it("shows no WAF build-tag banner on a waf-enabled build", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ...projection, waf_compiled: true }),
      }),
    );
    render(<SecurityPanel />, { wrapper: Wrapper });
    // Wait for the panel to render, then assert the banner is absent.
    await screen.findByText("Security");
    await waitFor(() => {
      expect(
        screen.queryByText(/does not include the web application firewall/i),
      ).not.toBeInTheDocument();
    });
  });
});

// ── RBAC status projection (P3-03 §35) ───────────────────────────────────────

describe("SecurityPanel RBAC status", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("summarises an enabled RBAC posture and warns about a retained legacy token", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            ...projection,
            rbac: {
              serving: { enabled: true, principal_count: 3, role_count: 2, legacy_token_active: true },
              persisted: { enabled: true, principal_count: 3, role_count: 2, legacy_token_active: true },
              pending: false,
            },
          }),
      }),
    );
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/3 principals, 2 custom roles/)).toBeInTheDocument();
    expect(
      screen.getByText(/legacy shared admin token is still active/i),
    ).toBeInTheDocument();
  });

  it("reports the disabled state when RBAC is off", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            ...projection,
            rbac: {
              serving: { enabled: false, principal_count: 0, role_count: 0, legacy_token_active: false },
              persisted: { enabled: false, principal_count: 0, role_count: 0, legacy_token_active: false },
              pending: false,
            },
          }),
      }),
    );
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(
      await screen.findByText(/named principals are off/i),
    ).toBeInTheDocument();
  });

  it("shows the serving policy and warns when a staged change is not yet live (N-03)", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            ...projection,
            rbac: {
              serving: { enabled: true, principal_count: 1, role_count: 0, legacy_token_active: false },
              persisted: { enabled: true, principal_count: 2, role_count: 1, legacy_token_active: false },
              pending: true,
            },
          }),
      }),
    );
    render(<SecurityPanel />, { wrapper: Wrapper });
    // The staged-change warning discloses the persisted (not-yet-serving) policy
    // so the operator never mistakes it for the active one.
    expect(
      await screen.findByText(/staged configuration change is not yet serving/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/2 principals, 1 custom role/)).toBeInTheDocument();
  });
});
