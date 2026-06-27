/**
 * Vitest tests for the Console v2 guided mutual-TLS editor (Phase 4j): the
 * projection schema, the draft → patch lib helpers, the TLS panel's Mutual TLS
 * section (server posture + per-location require_client_cert toggle, bind-time
 * banner), and the RouteDetail require_client_cert quick edit.
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

import {
  MTLSProjectionSchema,
  type MTLSServerProjection,
  type LocationProjection,
  type RouteProjection,
} from "@/api/client.ts";
import {
  emptyMTLSDraft,
  seedMTLSDraft,
  parseSANList,
  formatSANList,
  mtlsDraftToPatch,
  mtlsDraftWarnings,
  mtlsServerSummary,
} from "@/lib/mtls.ts";
import { TLSPanel } from "@/features/tls/TLSPanel.tsx";
import { RouteDetail } from "@/features/routes/RouteDetail.tsx";

// ── schema ────────────────────────────────────────────────────────────────────

describe("MTLSProjectionSchema", () => {
  it("parses a server set with mode, CA/CRL, SAN and locations", () => {
    const parsed = MTLSProjectionSchema.parse({
      servers: [
        {
          listen: ":443",
          server_names: ["app.example"],
          mode: "require",
          ca_file: "/etc/ca.pem",
          crl_file: "/etc/crl.pem",
          verify_san: ["svc.internal"],
          locations: [{ match: "/admin", type: "prefix", require_client_cert: true }],
        },
      ],
    });
    expect(parsed.servers[0]?.mode).toBe("require");
    expect(parsed.servers[0]?.verify_san?.[0]).toBe("svc.internal");
    expect(parsed.servers[0]?.locations[0]?.require_client_cert).toBe(true);
  });

  it("rejects a server missing the required mode", () => {
    expect(() =>
      MTLSProjectionSchema.parse({
        servers: [{ listen: ":443", locations: [] }],
      }),
    ).toThrow();
  });
});

// ── lib helpers ───────────────────────────────────────────────────────────────

describe("mtls lib", () => {
  it("parseSANList trims entries and drops blanks", () => {
    expect(parseSANList(" a.internal \n\n b.internal \nc.internal")).toEqual([
      "a.internal",
      "b.internal",
      "c.internal",
    ]);
  });

  it("formatSANList renders one entry per line", () => {
    expect(formatSANList(["a", "b"])).toBe("a\nb");
  });

  it("mtlsDraftToPatch disables with mode none", () => {
    expect(mtlsDraftToPatch({ ...emptyMTLSDraft(), caFile: "/x", verifySAN: "a" })).toEqual({
      mode: "none",
    });
  });

  it("mtlsDraftToPatch omits empty optional fields", () => {
    expect(mtlsDraftToPatch({ mode: "require", caFile: "/etc/ca.pem", crlFile: "", verifySAN: "" })).toEqual(
      { mode: "require", ca_file: "/etc/ca.pem" },
    );
  });

  it("mtlsDraftToPatch includes CRL and SAN when set", () => {
    const patch = mtlsDraftToPatch({
      mode: "request",
      caFile: "/etc/ca.pem",
      crlFile: "/etc/crl.pem",
      verifySAN: "svc-a.internal\nsvc-b.internal",
    });
    expect(patch.crl_file).toBe("/etc/crl.pem");
    expect(patch.verify_san).toEqual(["svc-a.internal", "svc-b.internal"]);
  });

  it("seedMTLSDraft fills the form from a projected server", () => {
    const s: MTLSServerProjection = {
      listen: ":443",
      mode: "require",
      ca_file: "/etc/ca.pem",
      crl_file: "/etc/crl.pem",
      verify_san: ["svc.internal"],
      locations: [],
    };
    const draft = seedMTLSDraft(s);
    expect(draft.mode).toBe("require");
    expect(draft.caFile).toBe("/etc/ca.pem");
    expect(draft.verifySAN).toBe("svc.internal");
  });

  it("mtlsDraftWarnings requires a CA bundle when enabled", () => {
    const w = mtlsDraftWarnings({ mode: "require", caFile: "", crlFile: "", verifySAN: "" });
    expect(w.some((m) => /CA bundle .*is required/i.test(m))).toBe(true);
  });

  it("mtlsDraftWarnings flags a SAN list with mutual TLS off", () => {
    const w = mtlsDraftWarnings({ mode: "none", caFile: "", crlFile: "", verifySAN: "svc.internal" });
    expect(w.some((m) => /SAN allow-list only applies/i.test(m))).toBe(true);
  });

  it("mtlsServerSummary describes the mode, SANs and requiring routes", () => {
    expect(
      mtlsServerSummary({
        listen: ":443",
        mode: "require",
        verify_san: ["a", "b"],
        locations: [
          { match: "/", type: "prefix", require_client_cert: false },
          { match: "/admin", type: "prefix", require_client_cert: true },
        ],
      }),
    ).toBe("require · 2 SANs · 1 route require a cert");
    expect(
      mtlsServerSummary({ listen: ":443", mode: "none", locations: [] }),
    ).toBe("mutual TLS off");
  });
});

// ── TLS panel Mutual TLS section ───────────────────────────────────────────────

const mtlsProjection = {
  servers: [
    {
      listen: ":443",
      server_names: ["app.example"],
      mode: "require",
      ca_file: "/etc/ca.pem",
      locations: [
        { match: "/", type: "prefix", require_client_cert: false },
        { match: "/admin", type: "prefix", require_client_cert: true },
      ],
    },
  ],
};

function stubTLSFetch(): void {
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string) => {
      const body = url.includes("/mtls") ? mtlsProjection : [];
      return Promise.resolve({ ok: true, json: () => Promise.resolve(body) });
    }),
  );
}

describe("TLSPanel Mutual TLS section", () => {
  beforeEach(() => {
    stubTLSFetch();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("lists TLS-enabled servers with a posture badge", async () => {
    render(<TLSPanel />, { wrapper: Wrapper });
    expect(await screen.findByText("Mutual TLS")).toBeInTheDocument();
    expect(screen.getByText(":443")).toBeInTheDocument();
    expect(screen.getByText("require")).toBeInTheDocument();
  });

  it("opens the editor with the bind-time restart banner", async () => {
    render(<TLSPanel />, { wrapper: Wrapper });
    await screen.findByText("Mutual TLS");
    fireEvent.click(screen.getByRole("button", { name: "Edit mTLS" }));
    await waitFor(() => {
      expect(screen.getByText(/Takes effect on restart/i)).toBeInTheDocument();
    });
  });

  it("renders the per-location require-cert toggle", async () => {
    render(<TLSPanel />, { wrapper: Wrapper });
    await screen.findByText("Mutual TLS");
    // The /admin location already requires a cert; the / one offers to add it.
    expect(screen.getByRole("button", { name: /Required/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Require cert/ })).toBeInTheDocument();
  });
});

// ── RouteDetail require_client_cert quick edit ─────────────────────────────────

function detailLoc(over: Partial<LocationProjection> = {}): LocationProjection {
  return {
    index: 0,
    match: "/admin",
    type: "prefix",
    action: "proxy",
    auth: false,
    cache: false,
    compression: false,
    rate_limit: false,
    secure: true,
    require_client_cert: false,
    ...over,
  };
}

describe("RouteDetail require client cert quick edit", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  function stubPatch(onPatch: (body: string) => void): void {
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
              candidate: 'listen = ":443"\n',
              diff: { summary: "1 change" },
            }),
        });
      }),
    );
  }

  it("toggles require_client_cert when the server has mutual TLS", async () => {
    let body = "";
    stubPatch((b) => {
      body = b;
    });
    const route: RouteProjection = {
      listen: ":443",
      server_names: ["app.example"],
      tls: { enabled: true, acme: false, client_auth: "require" },
      http3: false,
      h2c: false,
      locations: [],
    };
    render(<RouteDetail route={route} loc={detailLoc()} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });

    fireEvent.click(screen.getByRole("button", { name: /Require client cert/ }));
    await waitFor(() => {
      expect(body).not.toBe("");
    });
    expect(JSON.parse(body)).toMatchObject({
      op: "location_toggle_require_client_cert",
      listen: ":443",
      match_type: "prefix",
      path: "/admin",
      enabled: true,
    });
  });

  it("disables the toggle when the server lacks mutual TLS", () => {
    const route: RouteProjection = {
      listen: ":443",
      server_names: ["app.example"],
      tls: { enabled: true, acme: false },
      http3: false,
      h2c: false,
      locations: [],
    };
    render(<RouteDetail route={route} loc={detailLoc()} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });
    expect(screen.getByRole("button", { name: /Require client cert/ })).toBeDisabled();
  });
});
