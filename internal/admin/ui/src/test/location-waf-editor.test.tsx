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
import { WAFEditor } from "@/features/security/WAFEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { ConfigPatch, LocationWAF, SecurityProjection } from "@/api/client.ts";

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
    expect(input).toBe("/api/config/patch/preview");
    seenBody = typeof init?.body === "string" ? init.body : "";
    return Promise.resolve(
      json({
        ok: true,
        summary: "route :8080 /api WAF override set",
        operation_summaries: [
          { op_index: 0, op: "location_waf_set", summary: "WAF override set" },
        ],
        diff: { summary: "1 change" },
        base_version: "deadbeef",
        valid: true,
        validation_errors: [],
        lifecycle: {
          changes: [],
          can_apply_hot: true,
          can_stage_restart: true,
          hot_paths: ["servers.locations.waf"],
          restart_required_paths: [],
          new_listener_only_paths: [],
          ignored_deprecated_paths: [],
          validation_rejected_paths: [],
          pending_subsystems: [],
        },
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

    fireEvent.click(screen.getByText("Review lifecycle and diff →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
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
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
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
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_waf_clear",
      listen: ":8080",
      path: "/api",
    });
  });

  it("allows adding a narrow rule exception and validates target syntax", async () => {
    render(
      <Wrapper>
        <LocationWAFEditor
          target={target({ enabled: true, mode: "block", crs_enabled: true })}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    fireEvent.click(screen.getByText("+ Add exception"));
    fireEvent.change(screen.getByLabelText("Exception 1 target"), {
      target: { value: "ARGS:profile_bio" },
    });
    fireEvent.change(screen.getByLabelText("Exception 1 rationale"), {
      target: { value: "Allow bio markdown" },
    });
    fireEvent.click(screen.getByText("Review lifecycle and diff →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    const patch = (JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0];
    expect(patch).toMatchObject({
      op: "location_waf_set",
      waf: {
        exceptions: [
          {
            rule_id: 942100,
            scope: "narrow_target",
            target: "ARGS:profile_bio",
            rationale: "Allow bio markdown",
          },
        ],
      },
    });
  });
});

describe("WAFEditor", () => {
  it("updates blocking_paranoia on modern policies when CRS paranoia level changes and seeds initialException", () => {
    const proj: SecurityProjection = {
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 1,
      waf_block_locs: 1,
      waf_detect_locs: 0,
      waf_crs_locs: 1,
      waf_global_enabled: true,
      waf_global_mode: "block",
      waf_crs_enabled: true,
      waf_paranoia: 1,
      waf_blocking_paranoia: 1,
      waf_detection_paranoia: 3,
      waf_response_body_check: false,
      secret_refs: 0,
    };

    render(
      <Wrapper>
        <WAFEditor
          current={proj}
          initialException={{
            id: "tune-942100",
            rule_id: 942100,
            scope: "narrow_target",
            target: "ARGS:profile_bio",
            rationale: "Tuned from event",
          }}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    const paranoiaSelect = screen.getByLabelText("CRS paranoia level");
    expect((paranoiaSelect as HTMLSelectElement).value).toBe("1");
    fireEvent.change(paranoiaSelect, { target: { value: "2" } });

    expect(screen.getByText(/blocking_paranoia = 2/)).toBeInTheDocument();
    expect(screen.getByText(/detection_paranoia = 3/)).toBeInTheDocument();
    expect(screen.getByText(/\[\[waf\.exceptions\]\]/)).toBeInTheDocument();
    expect(screen.getByDisplayValue("ARGS:profile_bio")).toBeInTheDocument();
  });

  it("freezes base_version at open time and refuses to overwrite intervening server changes", async () => {
    const projV1: SecurityProjection = {
      base_version: "ver-initial-1",
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 1,
      waf_block_locs: 1,
      waf_detect_locs: 0,
      waf_crs_locs: 1,
      waf_global_enabled: true,
      waf_global_mode: "block",
      waf_crs_enabled: true,
      waf_paranoia: 1,
      waf_response_body_check: false,
      secret_refs: 0,
    };

    const { rerender } = render(
      <Wrapper>
        <WAFEditor current={projV1} onClose={() => undefined} />
      </Wrapper>,
    );

    // Simulate a 15-second background query refresh delivering base_version = "ver-polled-2".
    rerender(
      <Wrapper>
        <WAFEditor
          current={{ ...projV1, base_version: "ver-polled-2", waf_global_mode: "detect" }}
          onClose={() => undefined}
        />
      </Wrapper>,
    );

    // Server now reports base_version = "ver-polled-2" on /api/config/raw; save must reject
    // rather than silently overlaying the old ver-initial-1 draft onto ver-polled-2!
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        json({
          raw: '[waf]\nenabled = true\nmode = "detect"\n',
          base_version: "ver-polled-2",
        }),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: /Review in editor/i }));
    expect(
      await screen.findByText(/Configuration changed on the server since this editor was opened/i),
    ).toBeInTheDocument();
    expect(takePendingDraft()).toBeNull();
  });

  it("round-trips a dotted-key root WAF document through WAFEditor Review in editor without duplicate waf declarations (Item 7)", async () => {
    takePendingDraft();
    const projDotted = {
      base_version: "ver-dotted-1",
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 1,
      waf_block_locs: 1,
      waf_detect_locs: 0,
      waf_crs_locs: 0,
      waf_global_enabled: true,
      waf_global_mode: "block",
      waf_crs_enabled: false,
      waf_response_body_check: false,
      secret_refs: 0,
    };
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        json({
          raw: 'waf.enabled = true\nwaf.mode = "block"\nwaf.crs_enabled = false\n\n[[servers]]\nlisten = ":8080"\n',
          base_version: "ver-dotted-1",
        }),
      ),
    );

    render(
      <MemoryRouter>
        <WAFEditor current={projDotted} onClose={() => undefined} />
      </MemoryRouter>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Review in editor/i }));
    await waitFor(() => {
      const pending = takePendingDraft();
      expect(pending).not.toBeNull();
      if (pending && "raw" in pending) {
        expect(pending.raw).not.toContain("waf.enabled");
        expect(pending.raw).toContain("[waf]\nenabled = true");
        expect(pending.raw).toContain('[[servers]]\nlisten = ":8080"');
      }
    });
  });
});
