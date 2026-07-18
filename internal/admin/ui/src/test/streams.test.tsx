/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Vitest tests for the Console v2 Streams panel (Phase 4i): the schema, the
 * draft → patch lib helpers, and the panel/editor rendering + interactions.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
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

import { StreamsProjectionSchema, type StreamProjection } from "@/api/client.ts";
import {
  emptyStreamDraft,
  seedStreamDraft,
  parseSNIRoutes,
  formatSNIRoutes,
  streamDraftToPatch,
  streamDraftWarnings,
  streamSummary,
} from "@/lib/streams.ts";
import { StreamsPanel } from "@/features/streams/StreamsPanel.tsx";

// ── schema ────────────────────────────────────────────────────────────────────

describe("StreamsProjectionSchema", () => {
  it("parses a stream set with SNI routes", () => {
    const parsed = StreamsProjectionSchema.parse({
      compiled: true,
      streams: [
        {
          listen: "0.0.0.0:443",
          protocol: "tcp",
          sni_routes: { "app.example.com": "app-backend" },
          tls_passthrough: true,
          proxy_protocol: "in",
        },
      ],
    });
    expect(parsed.compiled).toBe(true);
    expect(parsed.streams[0]?.sni_routes?.["app.example.com"]).toBe("app-backend");
  });

  it("rejects a stream missing the required listen", () => {
    expect(() =>
      StreamsProjectionSchema.parse({
        compiled: false,
        streams: [{ protocol: "tcp", tls_passthrough: false }],
      }),
    ).toThrow();
  });
});

// ── lib helpers ───────────────────────────────────────────────────────────────

describe("streams lib", () => {
  it("parseSNIRoutes parses host = target lines and ignores blanks", () => {
    expect(parseSNIRoutes("a.example = back1\n\n b.example = 10.0.0.5:443 \nbroken")).toEqual({
      "a.example": "back1",
      "b.example": "10.0.0.5:443",
    });
  });

  it("formatSNIRoutes renders sorted host = target lines", () => {
    expect(formatSNIRoutes({ "b.example": "y", "a.example": "x" })).toBe(
      "a.example = x\nb.example = y",
    );
  });

  it("streamDraftToPatch omits empty optional fields", () => {
    const patch = streamDraftToPatch({ ...emptyStreamDraft(), listen: "0.0.0.0:5432" });
    expect(patch).toEqual({ listen: "0.0.0.0:5432", protocol: "tcp" });
  });

  it("streamDraftToPatch includes target, routes, caps and timeouts", () => {
    const draft = {
      ...emptyStreamDraft(),
      listen: "0.0.0.0:443",
      proxyPass: "db",
      sniRoutes: "app.example = app-backend",
      tlsPassthrough: true,
      proxyProtocol: "both" as const,
      connectTimeout: "10s",
      idleTimeout: "5m",
    };
    const patch = streamDraftToPatch(draft);
    expect(patch.proxy_pass).toBe("db");
    expect(patch.sni_routes).toEqual({ "app.example": "app-backend" });
    expect(patch.tls_passthrough).toBe(true);
    expect(patch.proxy_protocol).toBe("both");
    expect(patch.connect_timeout).toBe("10s");
    expect(patch.idle_timeout).toBe("5m");
  });

  it("seedStreamDraft fills the form from a projected stream", () => {
    const s: StreamProjection = {
      listen: "0.0.0.0:443",
      protocol: "tcp",
      proxy_pass: "db",
      sni_routes: { "app.example": "back" },
      tls_passthrough: true,
      proxy_protocol: "in",
      connect_timeout: "3s",
      idle_timeout: "",
    };
    const draft = seedStreamDraft(s);
    expect(draft.listen).toBe("0.0.0.0:443");
    expect(draft.proxyPass).toBe("db");
    expect(draft.sniRoutes).toBe("app.example = back");
    expect(draft.tlsPassthrough).toBe(true);
    expect(draft.proxyProtocol).toBe("in");
    expect(draft.connectTimeout).toBe("3s");
  });

  it("streamDraftWarnings flags a missing listen and missing target", () => {
    const w = streamDraftWarnings(emptyStreamDraft());
    expect(w.some((m) => /listen address is required/i.test(m))).toBe(true);
    expect(w.some((m) => /default backend.*or.*SNI route/i.test(m))).toBe(true);
  });

  it("streamDraftWarnings rejects TCP-only fields on a UDP stream", () => {
    const w = streamDraftWarnings({
      ...emptyStreamDraft(),
      listen: "0.0.0.0:53",
      protocol: "udp",
      proxyPass: "dns",
      sniRoutes: "a.example = back",
      tlsPassthrough: true,
      proxyProtocol: "in",
    });
    expect(w.some((m) => /SNI routes are only supported for TCP/i.test(m))).toBe(true);
    expect(w.some((m) => /TLS passthrough is only supported for TCP/i.test(m))).toBe(true);
    expect(w.some((m) => /PROXY protocol is only supported for TCP/i.test(m))).toBe(true);
  });

  it("streamSummary describes the target and SNI route count", () => {
    expect(
      streamSummary({
        listen: "0.0.0.0:443",
        protocol: "tcp",
        proxy_pass: "db",
        sni_routes: { a: "x", b: "y" },
        tls_passthrough: false,
        proxy_protocol: "",
        connect_timeout: "",
        idle_timeout: "",
      }),
    ).toBe("→ db · 2 SNI routes");
  });
});

// ── panel ─────────────────────────────────────────────────────────────────────

const projection = {
  compiled: true,
  streams: [
    {
      listen: "0.0.0.0:5432",
      protocol: "tcp",
      proxy_pass: "db",
      tls_passthrough: false,
      proxy_protocol: "",
      connect_timeout: "",
      idle_timeout: "",
    },
    {
      listen: "0.0.0.0:443",
      protocol: "tcp",
      sni_routes: { "app.example.com": "app-backend" },
      tls_passthrough: true,
      proxy_protocol: "",
      connect_timeout: "",
      idle_timeout: "",
    },
  ],
};

describe("StreamsPanel", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({ ok: true, json: () => Promise.resolve(projection) }),
    );
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("lists declared streams with protocol badges", async () => {
    render(<StreamsPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("0.0.0.0:5432")).toBeInTheDocument();
    expect(screen.getByText("0.0.0.0:443")).toBeInTheDocument();
    expect(screen.getByText("TLS passthrough")).toBeInTheDocument();
  });

  it("marks the panel GA per the maturity model", async () => {
    render(<StreamsPanel />, { wrapper: Wrapper });
    await screen.findByText("L4 streams");
    expect(screen.getByText("GA")).toBeInTheDocument();
  });

  it("opens the editor drawer from New stream", async () => {
    render(<StreamsPanel />, { wrapper: Wrapper });
    await screen.findByText("0.0.0.0:5432");
    fireEvent.click(screen.getByRole("button", { name: "New stream" }));
    await waitFor(() => {
      expect(screen.getByText(/A stream is an L4/i)).toBeInTheDocument();
    });
  });

  it("warns when the build lacks the stream proxy", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue({
        ok: true,
        json: () => Promise.resolve({ ...projection, compiled: false }),
      }),
    );
    render(<StreamsPanel />, { wrapper: Wrapper });
    expect(
      await screen.findByText(/does not include the L4 stream proxy/i),
    ).toBeInTheDocument();
  });
});
