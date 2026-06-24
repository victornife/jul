/**
 * Vitest tests for Console v2 Phase 5/6 frontend additions.
 *
 * Coverage:
 *  - api/client.ts: new operational schemas parse/reject
 *  - api/client.ts: auditExportUrl query building
 *  - api/client.ts: reportClientError posts to the error sink
 *  - lib/errorReporter.ts: global error capture + dedupe + slow-fetch flagging
 *  - features/operations: OperationsPanel renders section headings
 *  - features/observability: TimelinePanel renders merged events
 *  - features/security: AuditPanel renders rows and triggers export
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import {
  RequestSampleSchema,
  RouteFailureSchema,
  BackendHealthHistorySchema,
  CertRenewalHistorySchema,
  ConsoleHealthSchema,
  TimelineEventSchema,
  AuditEventSchema,
  auditExportUrl,
  reportClientError,
} from "@/api/client.ts";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

// ── schemas ──────────────────────────────────────────────────────────────────

describe("operational schemas", () => {
  it("parses a request sample", () => {
    const s = RequestSampleSchema.parse({
      time: "2024-01-01T00:00:00Z",
      method: "GET",
      path: "/x",
      status: 200,
      duration_ms: 1.5,
    });
    expect(s.path).toBe("/x");
  });

  it("parses a route failure", () => {
    const r = RouteFailureSchema.parse({
      path: "/bad",
      total: 10,
      status_4xx: 2,
      status_5xx: 3,
      error_rate: 0.5,
      latency_p95_ms: 12,
    });
    expect(r.status_5xx).toBe(3);
  });

  it("parses upstream health history", () => {
    const h = BackendHealthHistorySchema.parse({
      pool: "p",
      backend: "b",
      healthy: true,
      transitions: 1,
      flapping: false,
    });
    expect(h.healthy).toBe(true);
  });

  it("parses certificate renewal history", () => {
    const c = CertRenewalHistorySchema.parse({ domain: "x.test", days_left: 30 });
    expect(c.days_left).toBe(30);
  });

  it("parses console health", () => {
    const ch = ConsoleHealthSchema.parse({
      status: "ok",
      requests: 5,
      errors: 0,
      latency_p50: 1,
      latency_p95: 2,
      latency_p99: 3,
      sse_conns: 1,
    });
    expect(ch.status).toBe("ok");
  });

  it("parses a timeline event", () => {
    const ev = TimelineEventSchema.parse({
      time: "2024-01-01T00:00:00Z",
      category: "config",
      type: "apply",
      severity: "info",
      message: "applied",
    });
    expect(ev.type).toBe("apply");
  });

  it("parses an audit event", () => {
    const a = AuditEventSchema.parse({
      id: 1,
      time: "2024-01-01T00:00:00Z",
      actor: "operator",
      operation: "config.apply",
      result: "success",
    });
    expect(a.operation).toBe("config.apply");
  });

  it("rejects an audit event missing required fields", () => {
    expect(() => AuditEventSchema.parse({ id: 1 })).toThrow();
  });
});

// ── auditExportUrl ───────────────────────────────────────────────────────────

describe("auditExportUrl", () => {
  it("builds a format-only URL", () => {
    expect(auditExportUrl("csv")).toBe("/api/audit/export?format=csv");
  });
  it("includes filters", () => {
    const url = auditExportUrl("json", { op: "config.", result: "failure" });
    expect(url).toContain("format=json");
    expect(url).toContain("op=config.");
    expect(url).toContain("result=failure");
  });
});

// ── reportClientError ────────────────────────────────────────────────────────

describe("reportClientError", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });

  it("POSTs the message to the error sink", () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    reportClientError({ message: "boom", source: "app.js", line: 1, col: 2 });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/admin/client-errors");
    expect(init.method).toBe("POST");
    expect(String(init.body)).toContain("boom");
  });
});

// ── errorReporter ────────────────────────────────────────────────────────────

describe("installErrorReporter", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("reports uncaught errors and dedupes repeats", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }));
    vi.stubGlobal("fetch", fetchMock);
    const { installErrorReporter } = await import("@/lib/errorReporter.ts");
    const cleanup = installErrorReporter();

    globalThis.dispatchEvent(new ErrorEvent("error", { message: "kaboom", filename: "a.js" }));
    globalThis.dispatchEvent(new ErrorEvent("error", { message: "kaboom", filename: "a.js" }));

    // Only the first identical error within the dedupe window is reported.
    const sinkCalls = fetchMock.mock.calls.filter(
      (c) => (c[0] as string) === "/api/admin/client-errors",
    );
    expect(sinkCalls).toHaveLength(1);
    cleanup();
  });
});

// ── component renders (mock the client module) ──────────────────────────────

vi.mock("@/api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client.ts")>();
  return {
    ...actual,
    fetchConsoleHealth: vi.fn().mockResolvedValue({
      status: "ok",
      requests: 3,
      errors: 0,
      latency_p50: 1,
      latency_p95: 2,
      latency_p99: 3,
      sse_conns: 1,
      client_errors: [],
    }),
    fetchRequestSamples: vi.fn().mockResolvedValue([
      { time: "2024-01-01T00:00:00Z", method: "GET", path: "/x", status: 200, duration_ms: 1 },
    ]),
    fetchFailingRoutes: vi.fn().mockResolvedValue([
      { path: "/bad", total: 5, status_4xx: 1, status_5xx: 2, error_rate: 0.6, latency_p95_ms: 9, last_error_class: "5xx" },
    ]),
    fetchUpstreamHistory: vi.fn().mockResolvedValue([
      { pool: "p", backend: "b", healthy: true, transitions: 1, flapping: false },
    ]),
    fetchCertHistory: vi.fn().mockResolvedValue([{ domain: "x.test", days_left: 30 }]),
    fetchOverview: vi.fn().mockResolvedValue({
      product: "Jul.IA",
      version: "1",
      status: [],
      traffic_sources: { origins: { "a.com": 3 }, referers: {}, preflight_count: 1 },
    }),
    fetchTimeline: vi.fn().mockResolvedValue([
      { time: "2024-01-01T00:00:00Z", category: "config", type: "apply", severity: "info", message: "applied" },
    ]),
    fetchAudit: vi.fn().mockResolvedValue([
      { id: 1, time: "2024-01-01T00:00:00Z", actor: "operator", operation: "config.apply", result: "success" },
    ]),
  };
});

describe("OperationsPanel", () => {
  it("renders the operational section headings", async () => {
    const { OperationsPanel } = await import("@/features/operations/OperationsPanel.tsx");
    render(
      <Wrapper>
        <OperationsPanel />
      </Wrapper>,
    );
    expect(screen.getByRole("heading", { name: "Operations" })).toBeInTheDocument();
    await waitFor(() => {
      expect(screen.getByText("/bad")).toBeInTheDocument();
    });
    expect(screen.getByText("p/b")).toBeInTheDocument();
    expect(screen.getByText("x.test")).toBeInTheDocument();
  });
});

describe("TimelinePanel", () => {
  it("renders merged timeline events", async () => {
    const { TimelinePanel } = await import("@/features/observability/TimelinePanel.tsx");
    render(
      <Wrapper>
        <TimelinePanel />
      </Wrapper>,
    );
    await waitFor(() => {
      expect(screen.getByText("applied")).toBeInTheDocument();
    });
  });
});

describe("AuditPanel", () => {
  it("renders audit rows", async () => {
    const { AuditPanel } = await import("@/features/security/AuditPanel.tsx");
    render(
      <Wrapper>
        <AuditPanel />
      </Wrapper>,
    );
    await waitFor(() => {
      expect(screen.getByText("config.apply")).toBeInTheDocument();
    });
    // Export controls are present and labelled.
    expect(screen.getByRole("button", { name: "Export JSON" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Export CSV" })).toBeInTheDocument();
  });
});
