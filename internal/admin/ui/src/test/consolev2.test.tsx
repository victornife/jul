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
import { render, screen } from "@testing-library/react";
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
    expect(await screen.findByText("Jul.IA")).toBeInTheDocument();
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
    expect(await screen.findByText("1/2 healthy")).toBeInTheDocument();
  });

  it("shows discovery badge", async () => {
    render(<AppsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("discovery:consul")).toBeInTheDocument();
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
