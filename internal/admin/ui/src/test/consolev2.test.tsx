/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Vitest component & unit tests for Console v2 (Phase 3).
 *
 * Coverage:
 *  - api/client.ts: Zod schema parse / reject
 *  - features/overview: groupBy helper + rendering
 *  - features/routes: action badge logic
 *  - features/apps: health aggregation
 *  - features/tls: days-left colour thresholds
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

// ── test wrapper ──────────────────────────────────────────────────────────────

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

// ── api/client schemas ────────────────────────────────────────────────────────

import {
  OverviewSchema,
  RouteProjectionSchema,
  AppProjectionSchema,
  CertProjectionSchema,
  SecurityProjectionSchema,
  TrafficControlsSchema,
  HistoryEntrySchema,
  ConfigDiffSchema,
} from "@/api/client.ts";

describe("OverviewSchema", () => {
  it("parses a valid overview payload", () => {
    const raw = {
      product: "Jul.IA",
      version: "1.0.0",
      status: [{ group: "Traffic", name: "TLS", active: true, detail: "2 servers" }],
    };
    const result = OverviewSchema.parse(raw);
    expect(result.product).toBe("Jul.IA");
    expect(result.status).toHaveLength(1);
    expect(result.status[0]?.active).toBe(true);
  });

  it("rejects a payload missing required fields", () => {
    expect(() => OverviewSchema.parse({ product: "x" })).toThrow();
  });

  it("parses an optional stream_status reload outcome", () => {
    const result = OverviewSchema.parse({
      product: "Jul.IA",
      version: "1.0.0",
      status: [],
      stream_status: "failed: stream: listen tcp :5353: address already in use",
    });
    expect(result.stream_status).toMatch(/^failed:/);
  });

  it("parses a degraded audit_sink health report", () => {
    const result = OverviewSchema.parse({
      product: "Jul.IA",
      version: "1.0.0",
      status: [],
      audit_sink: {
        configured: true,
        path: "/var/log/jul/audit.jsonl",
        healthy: false,
        error: "open /var/log/jul/audit.jsonl: permission denied",
        write_failures: 3,
      },
    });
    expect(result.audit_sink?.configured).toBe(true);
    expect(result.audit_sink?.healthy).toBe(false);
    expect(result.audit_sink?.write_failures).toBe(3);
  });
});

describe("RouteProjectionSchema", () => {
  it("parses a route with TLS", () => {
    const raw = {
      listen: ":443",
      server_names: ["example.com"],
      tls: { enabled: true, acme: false },
      http3: false,
      h2c: false,
      locations: [
        { match: "/", type: "prefix", action: "proxy", target: "http://backend", auth: false, cache: false, secure: true },
      ],
    };
    const r = RouteProjectionSchema.parse(raw);
    expect(r.listen).toBe(":443");
    expect(r.tls?.enabled).toBe(true);
    expect(r.locations[0]?.action).toBe("proxy");
  });

  it("parses a per-location WAF override and defaults crs_enabled", () => {
    const raw = {
      listen: ":8080",
      http3: false,
      h2c: false,
      locations: [
        {
          match: "/admin",
          type: "prefix",
          action: "proxy",
          auth: false,
          cache: false,
          secure: false,
          waf: { enabled: true, mode: "detect" },
        },
      ],
    };
    const r = RouteProjectionSchema.parse(raw);
    expect(r.locations[0]?.waf?.enabled).toBe(true);
    expect(r.locations[0]?.waf?.mode).toBe("detect");
    expect(r.locations[0]?.waf?.crs_enabled).toBe(false);
  });
});

describe("AppProjectionSchema", () => {
  it("parses an app with discovery", () => {
    const raw = {
      name: "api",
      strategy: "round_robin",
      backends: [{ address: "10.0.0.1:80", weight: 1, healthy: true, inflight: 0 }],
      health_check: true,
      discovery: "consul",
    };
    const a = AppProjectionSchema.parse(raw);
    expect(a.discovery).toBe("consul");
    expect(a.backends[0]?.healthy).toBe(true);
  });
});

describe("CertProjectionSchema", () => {
  it("accepts optional fields", () => {
    const c = CertProjectionSchema.parse({
      server_names: ["example.com"],
      source: "file",
    });
    expect(c.days_left).toBeUndefined();
  });

  it("parses days_left as number", () => {
    const c = CertProjectionSchema.parse({
      server_names: ["x.com"],
      source: "acme",
      days_left: 14,
      not_after: "2026-07-07T00:00:00Z",
    });
    expect(c.days_left).toBe(14);
  });
});

describe("SecurityProjectionSchema", () => {
  it("parses auth + cert count", () => {
    const s = SecurityProjectionSchema.parse({
      auth_enabled: true,
      client_auth: "require",
      require_cert_count: 3,
      waf_enabled: true,
      waf_mode: "block",
      waf_locations: 2,
      secret_refs: 1,
    });
    expect(s.auth_enabled).toBe(true);
    expect(s.require_cert_count).toBe(3);
    expect(s.waf_enabled).toBe(true);
    expect(s.secret_refs).toBe(1);
  });

  it("parses per-location WAF overrides with a CRS default", () => {
    const s = SecurityProjectionSchema.parse({
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_locations: 1,
      secret_refs: 0,
      location_wafs: [
        { listen: ":8080", path: "/admin", enabled: true, mode: "block", crs_enabled: true },
        { listen: ":8080", path: "/public", enabled: false },
      ],
    });
    expect(s.location_wafs).toHaveLength(2);
    expect(s.location_wafs?.[0]?.mode).toBe("block");
    expect(s.location_wafs?.[0]?.crs_enabled).toBe(true);
    // crs_enabled defaults to false when the server omits it.
    expect(s.location_wafs?.[1]?.crs_enabled).toBe(false);
  });
});

describe("TrafficControlsSchema", () => {
  it("accepts all optional sections missing", () => {
    const t = TrafficControlsSchema.parse({});
    expect(t.compression).toBeUndefined();
    expect(t.rate_limit).toBeUndefined();
  });

  it("parses full payload", () => {
    const raw = {
      compression: { enabled: true, encoders: ["gzip", "br"] },
      rate_limit: { enabled: true, rate: 100, burst: 200, key: "ip" },
      cache: { enabled: true, default_ttl: "5m", memory_max: "128MiB" },
    };
    const t = TrafficControlsSchema.parse(raw);
    expect(t.compression?.encoders).toContain("gzip");
    expect(t.rate_limit?.rate).toBe(100);
  });
});

describe("HistoryEntrySchema", () => {
  it("parses a history entry", () => {
    const e = HistoryEntrySchema.parse({ id: "20260623T120000.000Z", time: "2026-06-23T12:00:00Z", size: 1024 });
    expect(e.id).toContain("2026");
  });
});

describe("ConfigDiffSchema", () => {
  it("parses a diff with modifications", () => {
    const raw = {
      summary: "1 server changed",
      modifications: [{ kind: "server", name: ":443", before: "8 locations", after: "9 locations" }],
    };
    const d = ConfigDiffSchema.parse(raw);
    expect(d.modifications).toHaveLength(1);
  });
});

// ── OverviewPanel rendering ───────────────────────────────────────────────────

import { OverviewPanel } from "@/features/overview/OverviewPanel.tsx";

describe("OverviewPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            product: "Jul.IA",
            version: "2.0.0",
            status: [
              { group: "Traffic", name: "TLS", active: true, detail: "2 servers" },
              { group: "Traffic", name: "HTTP/3", active: false },
              { group: "Security", name: "Auth", active: true },
            ],
          }),
      }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it("renders product name and grouped status rows", async () => {
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/Jul\.IA/)).toBeInTheDocument();
    expect(await screen.findByText("TLS")).toBeInTheDocument();
    expect(await screen.findByText("HTTP/3")).toBeInTheDocument();
    // Group header
    expect(await screen.findByText("Traffic")).toBeInTheDocument();
    expect(await screen.findByText("Security")).toBeInTheDocument();
  });

  it("shows active badges for active features", async () => {
    render(<OverviewPanel />, { wrapper: Wrapper });
    const badges = await screen.findAllByText("active");
    expect(badges.length).toBeGreaterThanOrEqual(2);
  });

  it("shows inactive badge for inactive features", async () => {
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("inactive")).toBeInTheDocument();
  });

  it("renders an admin_health banner when the admin subsystem is unhealthy", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            product: "Jul.IA",
            version: "2.0.0",
            status: [],
            admin_health: { healthy: false, reason: "admin_reload", detail: "reload timed out" },
          }),
      }),
    );
    render(<OverviewPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("Admin subsystem degraded.")).toBeInTheDocument();
    expect(await screen.findByText(/reload timed out/)).toBeInTheDocument();
  });
});

// ── RoutesPanel rendering ─────────────────────────────────────────────────────

import { RoutesPanel } from "@/features/routes/RoutesPanel.tsx";

describe("RoutesPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            {
              listen: ":443",
              server_names: ["example.com"],
              tls: { enabled: true, acme: true },
              http3: true,
              h2c: false,
              locations: [
                { match: "/api", type: "prefix", action: "grpc_transcode", target: "backend:50051", auth: true, cache: false, secure: true },
                { match: "/", type: "prefix", action: "proxy", target: "http://web", auth: false, cache: true, secure: true },
              ],
            },
          ]),
      }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it("renders listen address and TLS/HTTP3 tags", async () => {
    render(<RoutesPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(":443")).toBeInTheDocument();
    expect(await screen.findByText("TLS")).toBeInTheDocument();
    expect(await screen.findByText("ACME")).toBeInTheDocument();
    expect(await screen.findByText("HTTP/3")).toBeInTheDocument();
  });

  it("renders action badges for each location", async () => {
    render(<RoutesPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("grpc_transcode")).toBeInTheDocument();
    expect(await screen.findByText("proxy")).toBeInTheDocument();
  });

  it("renders auth badge where configured", async () => {
    render(<RoutesPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("auth")).toBeInTheDocument();
  });

  it("filters by protocol-adapter actions (grpc_transcode)", async () => {
    render(<RoutesPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("grpc_transcode")).toBeInTheDocument();
    expect(screen.getByText("proxy")).toBeInTheDocument();
    fireEvent.change(screen.getByRole("combobox", { name: /Action/ }), {
      target: { value: "grpc_transcode" },
    });
    expect(screen.getByText("grpc_transcode")).toBeInTheDocument();
    expect(screen.queryByText("proxy")).not.toBeInTheDocument();
  });
});

// ── AppsPanel health aggregation ──────────────────────────────────────────────

import { AppsPanel } from "@/features/apps/AppsPanel.tsx";

describe("AppsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            {
              name: "api",
              strategy: "round_robin",
              backends: [
                { address: "10.0.0.1:80", weight: 1, healthy: true, inflight: 2 },
                { address: "10.0.0.2:80", weight: 1, healthy: false, inflight: 0 },
              ],
              health_check: true,
              discovery: "consul",
            },
          ]),
      }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it("renders pool name and strategy", async () => {
    render(<AppsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("api")).toBeInTheDocument();
    expect(await screen.findByText("round_robin")).toBeInTheDocument();
  });

  it("shows correct healthy/total count", async () => {
    render(<AppsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("1/2 healthy · 1 down")).toBeInTheDocument();
  });

  it("shows discovery badge", async () => {
    render(<AppsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("discovery:consul")).toBeInTheDocument();
  });

  it("marks pool health unknown when no live status is present", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            {
              name: "api",
              strategy: "round_robin",
              backends: [
                { address: "10.0.0.1:80", weight: 1 },
                { address: "10.0.0.2:80", weight: 1 },
              ],
              health_check: false,
            },
          ]),
      }),
    );
    render(<AppsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("2 backends · health unknown")).toBeInTheDocument();
  });
});

// ── TLSPanel expiry colour logic ──────────────────────────────────────────────

import { TLSPanel } from "@/features/tls/TLSPanel.tsx";

describe("TLSPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve([
            { server_names: ["example.com"], source: "acme", days_left: 5, not_after: "2026-06-28T00:00:00Z" },
            { server_names: ["other.com"], source: "file", days_left: 90 },
          ]),
      }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it("shows expiring soon warning when a cert has <=30 days left", async () => {
    render(<TLSPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/expiring within 30 days/i)).toBeInTheDocument();
  });

  it("renders both certs", async () => {
    render(<TLSPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("example.com")).toBeInTheDocument();
    expect(await screen.findByText("other.com")).toBeInTheDocument();
  });

  it("shows acme badge", async () => {
    render(<TLSPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("acme")).toBeInTheDocument();
  });
});

// ── SecurityPanel ─────────────────────────────────────────────────────────────

import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";

describe("SecurityPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () =>
          Promise.resolve({
            auth_enabled: true,
            client_auth: "require",
            require_cert_count: 2,
            waf_enabled: true,
            waf_mode: "detect",
            waf_locations: 3,
            secret_refs: 2,
          }),
      }),
    );
  });
  afterEach(() => vi.restoreAllMocks());

  it("renders auth enabled", async () => {
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("enabled")).toBeInTheDocument();
  });

  it("renders mTLS mode", async () => {
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("require")).toBeInTheDocument();
  });

  it("renders require cert count", async () => {
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/2 locations require cert/i)).toBeInTheDocument();
  });

  it("renders WAF mode and location count", async () => {
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("detect")).toBeInTheDocument();
    expect(await screen.findByText(/3 locations/i)).toBeInTheDocument();
  });

  it("renders secret reference count", async () => {
    render(<SecurityPanel />, { wrapper: Wrapper });
    expect(await screen.findByText(/2 references/i)).toBeInTheDocument();
  });
});

describe("SecurityPanel per-location WAF disclosure", () => {
  afterEach(() => vi.restoreAllMocks());

  function stubSecurity(payload: Record<string, unknown>) {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve(payload),
      }),
    );
  }

  it("lists per-location overrides and labels the global edit truthfully", async () => {
    stubSecurity({
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_mode: "block",
      waf_locations: 2,
      waf_block_locs: 2,
      secret_refs: 0,
      location_wafs: [
        { listen: ":8080", path: "/admin", enabled: true, mode: "block", crs_enabled: true },
        { listen: ":8080", path: "/public", enabled: false },
      ],
    });
    render(<SecurityPanel />, { wrapper: Wrapper });
    // The override rows are shown so the operator sees routes run their own policy.
    expect(await screen.findByText(/:8080 \/admin — block, CRS/)).toBeInTheDocument();
    expect(await screen.findByText(/:8080 \/public — disabled/)).toBeInTheDocument();
    // The disclosure makes clear editing changes only the global policy, and the
    // button is relabelled accordingly so it is not mistaken for a per-route edit.
    expect(await screen.findByText(/2 locations override the global policy/i)).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Edit global" })).toBeInTheDocument();
  });

  it("does not show the disclosure when no per-location overrides exist", async () => {
    stubSecurity({
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_mode: "block",
      waf_locations: 1,
      waf_block_locs: 1,
      secret_refs: 0,
    });
    render(<SecurityPanel />, { wrapper: Wrapper });
    // The plain "Edit" label confirms the global-only path; no override notice.
    expect(await screen.findByRole("button", { name: "Edit" })).toBeInTheDocument();
    expect(screen.queryByText(/override the global policy/i)).not.toBeInTheDocument();
  });
});

describe("SecurityPanel per-location WAF editor", () => {
  afterEach(() => vi.restoreAllMocks());

  const override = {
    listen: ":8080",
    path: "/admin",
    match_type: "prefix",
    server_names: [],
    enabled: true,
    mode: "block",
    crs_enabled: true,
  };

  // stubSecurityAndPatch routes the security GET to a projection carrying one
  // per-location override, and captures the body of any /api/config/patch POST
  // so a test can assert the structured op the editor dispatched.
  function stubSecurityAndPatch(onPatch: (body: string) => void) {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string, init?: RequestInit) => {
        if (typeof url === "string" && url.includes("/api/config/patch")) {
          onPatch(typeof init?.body === "string" ? init.body : "");
          return Promise.resolve({
            ok: true,
            json: () =>
              Promise.resolve({
                ok: true,
                summary: "ok",
                candidate: 'listen = ":8080"\n',
                diff: { summary: "1 change" },
              }),
          });
        }
        return Promise.resolve({
          ok: true,
          json: () =>
            Promise.resolve({
              auth_enabled: false,
              require_cert_count: 0,
              waf_enabled: true,
              waf_mode: "block",
              waf_locations: 1,
              waf_block_locs: 1,
              secret_refs: 0,
              location_wafs: [override],
            }),
        });
      }),
    );
  }

  it("opens the editor seeded from the override and saves a location_waf_set", async () => {
    let body = "";
    stubSecurityAndPatch((b) => {
      body = b;
    });
    render(<SecurityPanel />, { wrapper: Wrapper });

    // The per-location row carries its own Edit button (the global one is
    // relabelled "Edit global" while overrides exist), so "Edit" resolves to the
    // single override row.
    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));

    // The drawer identifies the exact route it targets.
    expect(await screen.findByText(/Override for :8080 \/admin/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Review in editor/ }));

    await waitFor(() => {
      expect(body).not.toBe("");
    });
    // The seeded values round-trip into a structured set op on the right target.
    expect(JSON.parse(body)).toMatchObject({
      op: "location_waf_set",
      listen: ":8080",
      path: "/admin",
      match_type: "prefix",
      waf: { enabled: true, mode: "block", crs_enabled: true },
    });
  });

  it("clears the override with a location_waf_clear op", async () => {
    let body = "";
    stubSecurityAndPatch((b) => {
      body = b;
    });
    render(<SecurityPanel />, { wrapper: Wrapper });

    fireEvent.click(await screen.findByRole("button", { name: "Edit" }));
    fireEvent.click(await screen.findByRole("button", { name: "Clear override" }));

    await waitFor(() => {
      expect(body).not.toBe("");
    });
    expect(JSON.parse(body)).toMatchObject({
      op: "location_waf_clear",
      listen: ":8080",
      path: "/admin",
    });
  });
});

// ── RouteDetail per-location WAF ───────────────────────────────────────────────

import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import type { RouteProjection, LocationProjection } from "@/api/client.ts";

describe("RouteDetail per-location WAF", () => {
  afterEach(() => vi.restoreAllMocks());

  const route: RouteProjection = {
    listen: ":8080",
    server_names: [],
    http3: false,
    h2c: false,
    locations: [],
  };

  function loc(over?: LocationProjection["waf"]): LocationProjection {
    return {
      index: 0,
      match: "/admin",
      type: "prefix",
      action: "deny",
      auth: false,
      cache: false,
      compression: false,
      rate_limit: false,
      secure: false,
      require_client_cert: false,
      waf: over,
    };
  }

  // stubPatch captures the body of the /api/config/patch POST the editor fires.
  function stubPatch(onPatch: (body: string) => void) {
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) => {
        onPatch(typeof init?.body === "string" ? init.body : "");
        return Promise.resolve({
          ok: true,
          json: () =>
            Promise.resolve({
              ok: true,
              summary: "ok",
              candidate: 'listen = ":8080"\n',
              diff: { summary: "1 change" },
            }),
        });
      }),
    );
  }

  it("offers to add an override on an inheriting route and seeds detect defaults", async () => {
    let body = "";
    stubPatch((b) => {
      body = b;
    });
    render(<RouteDetail route={route} loc={loc()} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });

    // An inheriting location offers "Add"; opening it shows the create copy and
    // hides the clear action (nothing to clear yet).
    fireEvent.click(screen.getByRole("button", { name: /Add WAF override/ }));
    expect(await screen.findByText("Add per-location WAF")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Clear override" })).not.toBeInTheDocument();

    // The detect-first default seeds an enabled override with no rules, which is
    // invalid (it would inspect nothing), so the save stays blocked until rules
    // are supplied — enabling the CRS unblocks it.
    expect(screen.getByRole("button", { name: /Review in editor/ })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox", { name: /Core Rule Set/ }));

    fireEvent.click(screen.getByRole("button", { name: /Review in editor/ }));
    await waitFor(() => {
      expect(body).not.toBe("");
    });
    expect(JSON.parse(body)).toMatchObject({
      op: "location_waf_set",
      listen: ":8080",
      path: "/admin",
      match_type: "prefix",
      waf: { enabled: true, mode: "detect", crs_enabled: true },
    });
  });

  it("offers to edit an existing override seeded from its state", async () => {
    stubPatch(() => {
      /* not exercised here */
    });
    render(
      <RouteDetail
        route={route}
        loc={loc({ enabled: true, mode: "block", crs_enabled: true })}
        onClose={vi.fn()}
        onEdit={vi.fn()}
      />,
      { wrapper: Wrapper },
    );

    // An override present ⇒ the button says "Edit" and the editor exposes Clear.
    fireEvent.click(screen.getByRole("button", { name: /Edit WAF override/ }));
    expect(await screen.findByText("Edit per-location WAF")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Clear override" })).toBeInTheDocument();
  });
});

// ── RouteDetail server-scope protocol toggles ──────────────────────────────────

describe("RouteDetail server toggles", () => {
  afterEach(() => vi.restoreAllMocks());

  function loc(): LocationProjection {
    return {
      index: 0,
      match: "/",
      type: "prefix",
      action: "proxy",
      auth: false,
      cache: false,
      compression: false,
      rate_limit: false,
      secure: false,
      require_client_cert: false,
    };
  }

  function stubPatch(onPatch: (body: string) => void) {
    vi.stubGlobal(
      "fetch",
      vi.fn((_url: string, init?: RequestInit) => {
        onPatch(typeof init?.body === "string" ? init.body : "");
        return Promise.resolve({
          ok: true,
          json: () =>
            Promise.resolve({
              ok: true,
              summary: "ok",
              candidate: 'listen = ":8080"\n',
              diff: { summary: "1 change" },
            }),
        });
      }),
    );
  }

  it("enables h2c on a plaintext listener and disables the HTTP/3 toggle", async () => {
    let body = "";
    stubPatch((b) => {
      body = b;
    });
    const route: RouteProjection = {
      listen: ":8080",
      server_names: [],
      http3: false,
      h2c: false,
      locations: [],
    };
    render(<RouteDetail route={route} loc={loc()} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });

    // Plaintext listener ⇒ h2c is offered, HTTP/3 is gated behind TLS.
    expect(screen.getByRole("button", { name: /Enable HTTP\/3/ })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /Enable h2c/ }));
    await waitFor(() => {
      expect(body).not.toBe("");
    });
    expect(JSON.parse(body)).toMatchObject({ op: "server_toggle_h2c", listen: ":8080", enabled: true });
  });

  it("enables HTTP/3 on a TLS listener and disables the h2c toggle", async () => {
    let body = "";
    stubPatch((b) => {
      body = b;
    });
    const route: RouteProjection = {
      listen: ":443",
      server_names: [],
      tls: { enabled: true, acme: false },
      http3: false,
      h2c: false,
      locations: [],
    };
    render(<RouteDetail route={route} loc={loc()} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });

    // TLS listener ⇒ HTTP/3 is offered, h2c does not apply.
    expect(screen.getByRole("button", { name: /Enable h2c/ })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /Enable HTTP\/3/ }));
    await waitFor(() => {
      expect(body).not.toBe("");
    });
    expect(JSON.parse(body)).toMatchObject({ op: "server_toggle_http3", listen: ":443", enabled: true });
  });
});

// ── RouteDetail generated TOML: rate_limit projection ──────────────────────────

describe("RouteDetail rate_limit generated TOML", () => {
  afterEach(() => vi.restoreAllMocks());

  const route: RouteProjection = {
    listen: ":8080",
    server_names: [],
    http3: false,
    h2c: false,
    locations: [],
  };

  function rlLoc(detail: LocationProjection["rate_limit_detail"]): LocationProjection {
    return {
      index: 0,
      match: "/api",
      type: "prefix",
      action: "proxy",
      target: "http://app",
      auth: false,
      cache: false,
      compression: false,
      rate_limit: true,
      rate_limit_detail: detail,
      secure: false,
      require_client_cert: false,
    };
  }

  it("emits only the fields the detail carries and never literal undefined", () => {
    render(
      <RouteDetail route={route} loc={rlLoc({ enabled: true, rate: 10 })} onClose={vi.fn()} onEdit={vi.fn()} />,
      { wrapper: Wrapper },
    );
    const toml = screen.getByText(/\[\[servers\]\]/);
    expect(toml).toHaveTextContent("rate_limit = { enabled = true, rate = 10 }");
    expect(toml).not.toHaveTextContent("undefined");
    expect(toml).not.toHaveTextContent("burst");
    expect(toml).not.toHaveTextContent("key");
  });

  it("emits every field when all are present", () => {
    render(
      <RouteDetail
        route={route}
        loc={rlLoc({ enabled: true, rate: 100, burst: 200, key: "ip" })}
        onClose={vi.fn()}
        onEdit={vi.fn()}
      />,
      { wrapper: Wrapper },
    );
    const toml = screen.getByText(/\[\[servers\]\]/);
    expect(toml).toHaveTextContent('rate_limit = { enabled = true, rate = 100, burst = 200, key = "ip" }');
  });

  it("falls back to enabled-only when no detail is present", () => {
    render(
      <RouteDetail route={route} loc={rlLoc(undefined)} onClose={vi.fn()} onEdit={vi.fn()} />,
      { wrapper: Wrapper },
    );
    const toml = screen.getByText(/\[\[servers\]\]/);
    expect(toml).toHaveTextContent("rate_limit = { enabled = true }");
    expect(toml).not.toHaveTextContent("undefined");
  });
});

