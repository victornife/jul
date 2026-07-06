/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the guided per-location WAF override editor (Phase 4e).
 * They mount the drawer, seed it from the security projection, and assert that
 * saving posts the full location_waf_set patch and stages the resulting diff for
 * the Config editor — never writing directly — and that a save the backend would
 * reject is blocked in the UI.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { LocationWAFEditor } from "@/features/security/LocationWAFEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { LocationWAF } from "@/api/client.ts";

const realFetch = globalThis.fetch;
let seenBody = "";

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function target(over: Partial<LocationWAF> = {}): LocationWAF {
  return {
    listen: ":8080",
    server_names: [],
    match_type: "prefix",
    path: "/api",
    enabled: true,
    crs_enabled: false,
    ...over,
  };
}

beforeEach(() => {
  seenBody = "";
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch");
    seenBody = typeof init?.body === "string" ? init.body : "";
    return Promise.resolve(
      json({
        ok: true,
        summary: "route :8080 /api WAF override set",
        candidate: 'listen = ":8080"\n',
        diff: { summary: "1 change" },
        base_version: "deadbeef",
      }),
    );
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("LocationWAFEditor", () => {
  it("seeds the advanced fields and posts the full location_waf_set patch", async () => {
    render(
      <Wrapper>
        <LocationWAFEditor
          target={target({
            enabled: true,
            mode: "block",
            crs_enabled: true,
            block_status: 429,
            paranoia: 3,
            request_body_limit: "256k",
            response_body_check: true,
            directives_files: ["/etc/jul/waf/custom.conf"],
            inline_rules: "SecRule ARGS x",
          })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    // Seeded from the projection: advanced fields are prefilled.
    expect(screen.getByDisplayValue("429")).toBeTruthy();
    expect(screen.getByDisplayValue("256k")).toBeTruthy();
    expect(screen.getByDisplayValue("/etc/jul/waf/custom.conf")).toBeTruthy();

    fireEvent.click(screen.getByText("Review in editor →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_waf_set",
      listen: ":8080",
      match_type: "prefix",
      path: "/api",
      waf: {
        enabled: true,
        mode: "block",
        crs_enabled: true,
        block_status: 429,
        paranoia: 3,
        request_body_limit: "256k",
        response_body_check: true,
        directives_files: ["/etc/jul/waf/custom.conf"],
        inline_rules: "SecRule ARGS x",
      },
    });
    // The edit is staged for diff review, never applied directly.
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("blocks save while an enabled override defines no rules", () => {
    render(
      <Wrapper>
        <LocationWAFEditor
          target={target({ enabled: true, mode: "detect", crs_enabled: false })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    expect(screen.getByText(/defines no rules/)).toBeInTheDocument();
    expect(screen.getByText("Review in editor →")).toBeDisabled();
  });

  it("hides Clear override when adding a fresh override", () => {
    render(
      <Wrapper>
        <LocationWAFEditor
          target={target({ enabled: true, crs_enabled: true })}
          existing={false}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    expect(screen.getByText("Add per-location WAF")).toBeInTheDocument();
    expect(screen.queryByText("Clear override")).not.toBeInTheDocument();
  });

  it("posts a location_waf_clear patch when clearing an existing override", async () => {
    render(
      <Wrapper>
        <LocationWAFEditor
          target={target({ enabled: true, crs_enabled: true })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Clear override"));
    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_waf_clear",
      listen: ":8080",
      path: "/api",
    });
  });
});
