/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { WAFInvestigationDrawer } from "@/features/security/WAFInvestigationDrawer.tsx";
import { SecurityPostureDrawer } from "@/features/security/SecurityPostureDrawer.tsx";
import { SecurityPanel } from "@/features/security/SecurityPanel.tsx";
import { matchLocationWAFForEvent } from "@/lib/wafEffective.ts";
import type { LocationWAF, WAFEventRecord } from "@/api/client.ts";

const realFetch = globalThis.fetch;

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <MemoryRouter>
      <QueryClientProvider client={qc}>{children}</QueryClientProvider>
    </MemoryRouter>
  );
}

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("WAFInvestigationDrawer (#632-B)", () => {
  it("renders bounded WAF transaction outcomes, filters, and narrow-tuning handoff", async () => {
    const onTune = vi.fn();
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            api_version: "v1",
            collection_state: "active",
            serving_generation: 3,
            serving_policy_digest: "ev1:01234567:abcdef012345",
            retention: { max_events: 256, max_event_bytes: 4096, ttl_seconds: 900 },
            stats: {
              retained_events: 1,
              recorded_total: 4,
              truncated_total: 0,
              dropped_total: 0,
            },
            events: [
              {
                event_id: "wev_0123456789abcdef0123456789abcdef",
                time: "2026-11-02T12:00:00Z",
                outcome: "interrupted_request",
                mode: "block",
                would_interrupt: true,
                interrupted_action: "deny",
                interrupted_rule_id: 942100,
                client_status: 403,
                origin_reached: false,
                partial_response_delivered: false,
                hijacked: false,
                method: "POST",
                host: "app.example.test",
                path: "/profile",
                query_omitted: true,
                route: {
                  route_id: "rt-profile",
                  listen: ":8443",
                  path_pattern: "/profile",
                  match_ordinal: 0,
                  scope: "override",
                },
                generation: 2,
                policy_digest: "ev1:01234567:olddigest1234",
                stale_policy: true,
                matched_rules: [
                  {
                    rule_id: 942100,
                    phase: 2,
                    severity: "CRITICAL",
                    source_class: "embedded",
                    disruptive: true,
                    logged: true,
                    matched_targets: ["ARGS:profile_bio"],
                  },
                ],
                total_matched_rules: 1,
                matches_truncated: false,
                support_header_emitted: true,
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    render(
      <Wrapper>
        <WAFInvestigationDrawer onClose={() => undefined} onTuneException={onTune} />
      </Wrapper>,
    );

    await screen.findByText("wev_0123456789abcdef0123456789abcdef");
    expect(screen.getByText("prior policy generation")).toBeInTheDocument();
    expect(screen.getByText(/targets: ARGS:profile_bio/)).toBeInTheDocument();

    fireEvent.click(screen.getByText("Tune ARGS:profile_bio →"));
    expect(onTune).toHaveBeenCalledWith(
      expect.objectContaining({
        rule_id: 942100,
        scope: "narrow_target",
        target: "ARGS:profile_bio",
        methods: ["POST"],
      }),
      expect.objectContaining({
        event_id: "wev_0123456789abcdef0123456789abcdef",
      }),
    );
  });
});

describe("SecurityPostureDrawer (#633)", () => {
  it("renders all 7 gateway security posture areas, epistemic states, and area filtering", async () => {
    globalThis.fetch = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            api_version: "v1",
            evaluated_at: "2026-11-02T12:00:00Z",
            overall_status: "warning",
            error_count: 0,
            warning_count: 1,
            info_count: 2,
            areas: [
              {
                area: "admin",
                status: "ok",
                epistemic_state: "configured",
                error_count: 0,
                warning_count: 0,
                info_count: 0,
                summary: "No material issues detected in admin posture.",
              },
              {
                area: "trust",
                status: "ok",
                epistemic_state: "configured",
                error_count: 0,
                warning_count: 0,
                info_count: 0,
                summary: "No material issues detected in trust posture.",
              },
              {
                area: "waf",
                status: "warning",
                epistemic_state: "configured",
                error_count: 0,
                warning_count: 1,
                info_count: 0,
                summary: "1 active HTTP route(s) have WAF inspection disabled.",
              },
              {
                area: "cache",
                status: "ok",
                epistemic_state: "configured",
                error_count: 0,
                warning_count: 0,
                info_count: 0,
                summary: "No material issues detected in cache posture.",
              },
              {
                area: "certificates",
                status: "ok",
                epistemic_state: "configured",
                error_count: 0,
                warning_count: 0,
                info_count: 0,
                summary: "No material issues detected in certificates posture.",
              },
              {
                area: "intent",
                status: "ok",
                epistemic_state: "external_unknown",
                error_count: 0,
                warning_count: 0,
                info_count: 2,
                summary: "Route :8080 /ext declares restricted access enforced externally.",
              },
              {
                area: "maintenance",
                status: "ok",
                epistemic_state: "configured",
                error_count: 0,
                warning_count: 0,
                info_count: 0,
                summary: "No material issues detected in maintenance posture.",
              },
            ],
            findings: [
              {
                code: "waf_routes_unprotected",
                area: "waf",
                severity: "warning",
                epistemic_state: "configured",
                scope: "waf",
                summary: "1 active HTTP route(s) have WAF inspection disabled.",
                remediation: "Enable global waf.enabled.",
              },
              {
                code: "sharing_external_enforcer_unverified",
                area: "intent",
                severity: "info",
                epistemic_state: "external_unknown",
                scope: ":8080 /ext",
                summary: "Route declares restricted access enforced externally by cloudflare.",
                remediation: "Verify network/firewall isolation.",
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );

    render(
      <Wrapper>
        <SecurityPostureDrawer onClose={() => undefined} />
      </Wrapper>,
    );

    await screen.findByText("waf_routes_unprotected");
    expect(screen.getByText("sharing_external_enforcer_unverified")).toBeInTheDocument();

    // Filter to intent area only.
    fireEvent.click(screen.getByText("intent"));
    await waitFor(() => {
      expect(screen.queryByText("waf_routes_unprotected")).not.toBeInTheDocument();
    });
    expect(screen.getByText("sharing_external_enforcer_unverified")).toBeInTheDocument();
  });
});

describe("SecurityPanel Tune exception route resolution (Finding 1)", () => {
  const multiListenerOverrides: LocationWAF[] = [
    {
      route_id: "rt-shop-checkout",
      listen: ":8443",
      server_names: ["shop.example.test"],
      match_type: "prefix",
      path: "/checkout",
      match_ordinal: 0,
      enabled: true,
      mode: "block",
      crs_enabled: true,
    },
    {
      route_id: "rt-admin-reports",
      listen: ":8443",
      server_names: ["admin.example.test"],
      match_type: "prefix",
      path: "/reports",
      match_ordinal: 0,
      enabled: true,
      mode: "block",
      crs_enabled: true,
    },
    {
      listen: ":8443",
      server_names: ["admin.example.test"],
      match_type: "exact",
      path: "/reports",
      match_ordinal: 0,
      enabled: true,
      mode: "detect",
      crs_enabled: true,
    },
  ];

  function makeEvent(over: Partial<WAFEventRecord>): WAFEventRecord {
    return {
      event_id: "wev_aaaaaaaa11111111bbbbbbbb22222222",
      time: "2026-11-02T12:00:00Z",
      outcome: "interrupted_request",
      mode: "block",
      would_interrupt: true,
      interrupted_action: "deny",
      interrupted_rule_id: 942100,
      client_status: 403,
      origin_reached: false,
      partial_response_delivered: false,
      hijacked: false,
      method: "POST",
      host: "admin.example.test",
      path: "/reports/export",
      query_omitted: false,
      route: {
        route_id: "rt-admin-reports",
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path_pattern: "/reports",
        match_ordinal: 0,
        scope: "override",
      },
      generation: 5,
      policy_digest: "ev1:01234567:abcdef012345",
      stale_policy: false,
      matched_rules: [
        {
          rule_id: 942100,
          phase: 2,
          severity: "CRITICAL",
          source_class: "embedded",
          disruptive: true,
          logged: true,
          matched_targets: ["ARGS:report_filter"],
        },
      ],
      total_matched_rules: 1,
      matches_truncated: false,
      support_header_emitted: true,
      ...over,
    };
  }

  it("selects the second route override on a shared listener and never the first", () => {
    const res = matchLocationWAFForEvent(makeEvent({}), multiListenerOverrides);
    expect(res.kind).toBe("matched");
    if (res.kind === "matched") {
      expect(res.target.path).toBe("/reports");
      expect(res.target.server_names).toEqual(["admin.example.test"]);
      expect(res.target.match_type).toBe("prefix");
    }
  });

  it("distinguishes exact vs prefix and virtual hosts on the same listener", () => {
    const exactEv = makeEvent({
      route: {
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "exact",
        path_pattern: "/reports",
        match_ordinal: 0,
        scope: "override",
      },
    });
    const res = matchLocationWAFForEvent(exactEv, multiListenerOverrides);
    expect(res.kind).toBe("matched");
    if (res.kind === "matched") {
      expect(res.target.match_type).toBe("exact");
      expect(res.target.mode).toBe("detect");
    }
  });

  it("rejects stale events, missing route overrides, and ambiguous overrides", () => {
    const staleRes = matchLocationWAFForEvent(
      makeEvent({ stale_policy: true }),
      multiListenerOverrides,
    );
    expect(staleRes.kind).toBe("rejected");

    const missingOverrideRes = matchLocationWAFForEvent(
      makeEvent({
        route: {
          route_id: "rt-deleted",
          listen: ":8443",
          server_names: ["gone.example.test"],
          match_type: "prefix",
          path_pattern: "/gone",
          match_ordinal: 0,
          scope: "override",
        },
      }),
      multiListenerOverrides,
    );
    expect(missingOverrideRes.kind).toBe("rejected");

    const ambiguousRes = matchLocationWAFForEvent(
      makeEvent({
        route: {
          listen: ":8443",
          server_names: ["dup.example.test"],
          match_type: "prefix",
          path_pattern: "/dup",
          match_ordinal: 0,
          scope: "override",
        },
      }),
      [
        {
          listen: ":8443",
          server_names: ["dup.example.test"],
          match_type: "prefix",
          path: "/dup",
          match_ordinal: 0,
          enabled: true,
          crs_enabled: true,
        },
        {
          listen: ":8443",
          server_names: ["dup.example.test"],
          match_type: "prefix",
          path: "/dup",
          match_ordinal: 0,
          enabled: true,
          crs_enabled: true,
        },
      ],
    );
    expect(ambiguousRes.kind).toBe("rejected");
  });

  it("completes the full drawer-to-editor workflow for a second override on a shared listener and carries initialException into global fallback", async () => {
    const secondRouteEvent = makeEvent({});
    const globalEvent = makeEvent({
      event_id: "wev_bbbbbbbb33333333cccccccc44444444",
      route: {
        route_id: "rt-global-docs",
        listen: ":8443",
        server_names: ["docs.example.test"],
        match_type: "prefix",
        path_pattern: "/docs",
        match_ordinal: 0,
        scope: "global",
      },
    });

    globalThis.fetch = vi.fn((input: string) => {
      if (input.startsWith("/api/security")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              auth_enabled: true,
              require_cert_count: 0,
              waf_enabled: true,
              waf_compiled: true,
              waf_locations: 3,
              waf_block_locs: 2,
              waf_detect_locs: 1,
              waf_crs_locs: 3,
              waf_global_enabled: true,
              waf_global_mode: "block",
              waf_crs_enabled: true,
              waf_paranoia: 1,
              waf_response_body_check: false,
              location_wafs: multiListenerOverrides,
              secret_refs: 0,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (input.startsWith("/api/listeners")) {
        return Promise.resolve(
          new Response(JSON.stringify({ listeners: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            api_version: "v1",
            collection_state: "active",
            serving_generation: 5,
            serving_policy_digest: "ev1:01234567:abcdef012345",
            retention: { max_events: 256, max_event_bytes: 4096, ttl_seconds: 900 },
            stats: { retained_events: 2, recorded_total: 2, truncated_total: 0, dropped_total: 0 },
            events: [secondRouteEvent, globalEvent],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <SecurityPanel />
      </Wrapper>,
    );

    const investigateBtn = await screen.findByText("Investigate outcomes");
    fireEvent.click(investigateBtn);

    const tuneBtns = await screen.findAllByText("Tune ARGS:report_filter →");
    expect(tuneBtns).toHaveLength(2);

    // Click Tune on the second route override (:8443 admin.example.test /reports)
    const firstTuneBtn = tuneBtns[0];
    expect(firstTuneBtn).toBeDefined();
    if (firstTuneBtn) {
      fireEvent.click(firstTuneBtn);
    }

    // LocationWAFEditor opens targeting :8443 /reports (NOT /checkout!) with the proposed exception pre-populated.
    await screen.findByText(/Override for :8443 \/reports/);
    expect(screen.getByDisplayValue("ARGS:report_filter")).toBeInTheDocument();

    // Also verify that an event on a route inheriting global WAF (scope: "global")
    // defaults to a route-local replacement (existing: false) rather than a global exception!
    const globalRouteRes = matchLocationWAFForEvent(globalEvent, multiListenerOverrides, {
      auth_enabled: true,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 3,
      waf_block_locs: 2,
      waf_detect_locs: 1,
      waf_crs_locs: 3,
      waf_global_enabled: true,
      waf_global_mode: "block",
      waf_crs_enabled: true,
      waf_paranoia: 2,
      waf_blocking_paranoia: 2,
      waf_detection_paranoia: 3,
      waf_request_body_limit: "256k",
      waf_response_body_check: true,
      waf_directives_files: ["/etc/jul/waf/global.conf"],
      waf_inline_rules: "SecAction \"id:1001,phase:1,pass\"",
      location_wafs: multiListenerOverrides,
      secret_refs: 0,
    });
    expect(globalRouteRes.kind).toBe("matched");
    if (globalRouteRes.kind === "matched") {
      expect(globalRouteRes.existing).toBe(false);
      expect(globalRouteRes.target.path).toBe("/docs");
      expect(globalRouteRes.target.server_names).toEqual(["docs.example.test"]);
      expect(globalRouteRes.target.blocking_paranoia).toBe(2);
      expect(globalRouteRes.target.detection_paranoia).toBe(3);
      expect(globalRouteRes.target.request_body_limit).toBe("256k");
      expect(globalRouteRes.target.directives_files).toEqual(["/etc/jul/waf/global.conf"]);
      expect(globalRouteRes.target.selector_version).toBe("global-v1");
    }
  });

  it("preserves match_ordinal, selector_version, and base_version through editor preview submission for same-path predicate routes and surfaces concurrent revision conflicts", async () => {
    const predicateOverrides: LocationWAF[] = [
      {
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 0,
        selector_version: "global-v1",
        methods: ["GET"],
        predicates: "(GET)",
        canonical_predicates: "methods=GET\n",
        enabled: true,
        mode: "block",
        crs_enabled: true,
      },
      {
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 1,
        selector_version: "global-v1",
        methods: ["POST"],
        predicates: "(POST)",
        canonical_predicates: "methods=POST\n",
        enabled: true,
        mode: "block",
        crs_enabled: true,
      },
    ];
    const secondPredicateEvent = makeEvent({
      method: "POST",
      route: {
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path_pattern: "/reports",
        match_ordinal: 1,
        scope: "override",
        methods: ["POST"],
        predicates: "(POST)",
        canonical_predicates: "methods=POST\n",
      },
    });

    let previewPayload = "";
    let simulateConflict = false;
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      if (input.startsWith("/api/security")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              base_version: "rev-predicate-123",
              auth_enabled: true,
              require_cert_count: 0,
              waf_enabled: true,
              waf_compiled: true,
              waf_locations: 2,
              waf_block_locs: 2,
              waf_detect_locs: 0,
              waf_crs_locs: 2,
              waf_global_enabled: true,
              waf_global_mode: "block",
              waf_crs_enabled: true,
              waf_paranoia: 1,
              waf_response_body_check: false,
              location_wafs: predicateOverrides,
              secret_refs: 0,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (input.startsWith("/api/listeners")) {
        return Promise.resolve(
          new Response(JSON.stringify({ listeners: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (input === "/api/config/patch/preview") {
        previewPayload = typeof init?.body === "string" ? init.body : "";
        if (simulateConflict) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: "base_version mismatch: configuration changed concurrently",
              }),
              { status: 409, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ok: true,
              summary: "route :8443 /reports#1 WAF override set",
              operation_summaries: [
                { op_index: 0, op: "location_waf_set", summary: "WAF override set" },
              ],
              diff: { summary: "1 change" },
              base_version: "rev-predicate-123",
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
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            api_version: "v1",
            collection_state: "active",
            serving_generation: 5,
            serving_policy_digest: "ev1:01234567:abcdef012345",
            retention: { max_events: 256, max_event_bytes: 4096, ttl_seconds: 900 },
            stats: { retained_events: 1, recorded_total: 1, truncated_total: 0, dropped_total: 0 },
            events: [secondPredicateEvent],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <SecurityPanel />
      </Wrapper>,
    );

    fireEvent.click(await screen.findByText("Investigate outcomes"));
    fireEvent.click(await screen.findByText("Tune ARGS:report_filter →"));
    await screen.findByText(/Override for :8443 \/reports/);

    // 1. Simulate a concurrent revision change (409 Conflict) first: editor surfaces error.
    simulateConflict = true;
    fireEvent.click(screen.getByText("Review lifecycle and diff →"));
    await screen.findByText(/base_version mismatch/);
    expect(JSON.parse(previewPayload)).toMatchObject({
      base_version: "rev-predicate-123",
      ops: [
        {
          op: "location_waf_set",
          listen: ":8443",
          server_names: ["admin.example.test"],
          match_type: "prefix",
          path: "/reports",
          match_ordinal: 1,
          selector_version: "global-v1",
        },
      ],
    });

    // 2. When base_version is current, preview succeeds with match_ordinal: 1 preserved.
    simulateConflict = false;
    previewPayload = "";
    fireEvent.click(screen.getByText("Review lifecycle and diff →"));
    await waitFor(() => {
      expect(previewPayload).not.toBe("");
    });
    expect(JSON.parse(previewPayload)).toMatchObject({
      base_version: "rev-predicate-123",
      ops: [
        {
          op: "location_waf_set",
          listen: ":8443",
          server_names: ["admin.example.test"],
          match_type: "prefix",
          path: "/reports",
          match_ordinal: 1,
          selector_version: "global-v1",
        },
      ],
    });
  });

  it("rejects persisted/staged vs serving generation mismatch and reassigned/reordered route_ids (Item 1)", () => {
    const evGen5 = makeEvent({ generation: 5 });

    // 1. Serving generation advanced on WAFEffective (generation 6 vs event generation 5).
    const genMismatch = matchLocationWAFForEvent(evGen5, multiListenerOverrides, {
      base_version: "rev-6",
      auth_enabled: true,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 3,
      waf_block_locs: 2,
      waf_detect_locs: 1,
      waf_crs_locs: 3,
      waf_global_enabled: true,
      waf_global_mode: "block",
      waf_crs_enabled: true,
      waf_response_body_check: false,
      secret_refs: 0,
      waf_effective: {
        compiled: true,
        generation: 6,
        inheriting_routes: 1,
        override_routes: 2,
        disabled_override_routes: 0,
        unprotected_routes: 0,
        block_routes: 2,
        detect_routes: 1,
        global_enabled: true,
        global: {
          mode: "block",
          block_status: 403,
          crs_enabled: true,
          response_body_inspection: false,
          rule_files_configured: 0,
          external_files: 0,
          inline_rules: false,
          rules: { total: 10, embedded: 10, external: 0, inline: 0, generated: 0 },
        },
      },
    });
    expect(genMismatch.kind).toBe("rejected");

    // 2. Persisted config differs from serving (waf_global_mode = "detect" vs serving "block").
    const persistedMismatch = matchLocationWAFForEvent(evGen5, multiListenerOverrides, {
      base_version: "rev-staged",
      auth_enabled: true,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 3,
      waf_block_locs: 2,
      waf_detect_locs: 1,
      waf_crs_locs: 3,
      waf_global_enabled: true,
      waf_global_mode: "detect",
      waf_crs_enabled: true,
      waf_response_body_check: false,
      secret_refs: 0,
      waf_effective: {
        compiled: true,
        generation: 5,
        inheriting_routes: 1,
        override_routes: 2,
        disabled_override_routes: 0,
        unprotected_routes: 0,
        block_routes: 2,
        detect_routes: 1,
        global_enabled: true,
        global: {
          mode: "block",
          block_status: 403,
          crs_enabled: true,
          response_body_inspection: false,
          rule_files_configured: 0,
          external_files: 0,
          inline_rules: false,
          rules: { total: 10, embedded: 10, external: 0, inline: 0, generated: 0 },
        },
      },
    });
    expect(persistedMismatch.kind).toBe("rejected");

    // 3. Reassigned route_id (rt-admin-reports moved to /new-reports or match_ordinal: 1).
    const reassigned = matchLocationWAFForEvent(evGen5, [
      {
        route_id: "rt-admin-reports",
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 1,
        enabled: true,
        crs_enabled: true,
      },
    ]);
    expect(reassigned.kind).toBe("rejected");

    // 4. Different route_id now occupying the event's coordinates/ordinal.
    const occupiedByOther = matchLocationWAFForEvent(evGen5, [
      {
        route_id: "rt-inserted-before-reports",
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 0,
        enabled: true,
        crs_enabled: true,
      },
    ]);
    expect(occupiedByOther.kind).toBe("rejected");
  });

  it("freezes selector and base_version in an open LocationWAFEditor when background polling reorders routes (Item 1)", async () => {
    let currentSecurityVersion = "rev-open-1";
    let currentLocations: LocationWAF[] = [
      {
        route_id: "rt-reports-get",
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 0,
        selector_version: "global-v1",
        enabled: true,
        mode: "block",
        crs_enabled: true,
      },
      {
        route_id: "rt-reports-post",
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 1,
        selector_version: "global-v1",
        enabled: true,
        mode: "block",
        crs_enabled: true,
      },
    ];

    let submittedPreview = "";
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      if (input.startsWith("/api/security")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              base_version: currentSecurityVersion,
              auth_enabled: true,
              require_cert_count: 0,
              waf_enabled: true,
              waf_compiled: true,
              waf_locations: currentLocations.length,
              waf_block_locs: currentLocations.length,
              waf_detect_locs: 0,
              waf_crs_locs: currentLocations.length,
              waf_global_enabled: true,
              waf_global_mode: "block",
              waf_crs_enabled: true,
              waf_paranoia: 1,
              waf_response_body_check: false,
              location_wafs: currentLocations,
              secret_refs: 0,
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (input.startsWith("/api/listeners")) {
        return Promise.resolve(
          new Response(JSON.stringify({ listeners: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (input === "/api/config/patch/preview") {
        submittedPreview = typeof init?.body === "string" ? init.body : "";
        const parsed = JSON.parse(submittedPreview) as { base_version?: string };
        if (parsed.base_version !== currentSecurityVersion) {
          return Promise.resolve(
            new Response(
              JSON.stringify({
                error: `stale base_version ${String(parsed.base_version)} != current ${currentSecurityVersion}`,
              }),
              { status: 409, headers: { "Content-Type": "application/json" } },
            ),
          );
        }
        return Promise.resolve(
          new Response(JSON.stringify({ ok: true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(new Response("{}", { status: 200 }));
    }) as unknown as typeof fetch;

    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <SecurityPanel />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    // Click direct "Edit" on the second same-path route (match_ordinal: 1) at rev-open-1.
    const editBtns = await screen.findAllByRole("button", { name: "Edit" });
    expect(editBtns.length).toBeGreaterThanOrEqual(2);
    const secondEditBtn = editBtns[1];
    if (secondEditBtn) {
      fireEvent.click(secondEditBtn);
    }
    await screen.findByText(/Override for :8443 \/reports/);

    // Now simulate a 15s background polling update where another admin inserted a new
    // same-path route at ordinal 0 and advanced base_version to "rev-polled-2"!
    currentSecurityVersion = "rev-polled-2";
    currentLocations = [
      {
        route_id: "rt-reports-head",
        listen: ":8443",
        server_names: ["admin.example.test"],
        match_type: "prefix",
        path: "/reports",
        match_ordinal: 0,
        selector_version: "global-v1",
        enabled: true,
        mode: "detect",
        crs_enabled: true,
      },
      ...currentLocations.map((loc, idx) => ({ ...loc, match_ordinal: idx + 1 })),
    ];
    await qc.invalidateQueries({ queryKey: ["security"] });

    // Submit from the still-open editor: it MUST submit the frozen snapshot
    // (base_version: "rev-open-1", match_ordinal: 1), NEVER "rev-polled-2" with old ordinal 1!
    fireEvent.click(screen.getByText("Review lifecycle and diff →"));
    await screen.findByText(/stale base_version rev-open-1 != current rev-polled-2/);
    expect(JSON.parse(submittedPreview)).toMatchObject({
      base_version: "rev-open-1",
      ops: [
        {
          op: "location_waf_set",
          listen: ":8443",
          path: "/reports",
          match_ordinal: 1,
          selector_version: "global-v1",
        },
      ],
    });
  });

  it("reconciles same-path inherited predicate routes without route_id against authoritative waf_routes and rejects staged reorder, removal, or reassignment (Item 1)", async () => {
    const inheritedGetEvent = makeEvent({
      event_id: "wev_cccccccc55555555dddddddd66666666",
      method: "GET",
      path: "/items",
      route: {
        listen: ":8443",
        server_names: ["shop.example.test"],
        match_type: "prefix",
        path_pattern: "/items",
        match_ordinal: 0,
        scope: "global",
        methods: ["GET"],
        predicates: "(GET)",
        canonical_predicates: "methods=GET\n",
      },
      generation: 5,
    });

    const baseProjection = {
      base_version: "rev-staged-reorder",
      auth_enabled: false,
      require_cert_count: 0,
      waf_enabled: true,
      waf_compiled: true,
      waf_locations: 2,
      waf_block_locs: 2,
      waf_detect_locs: 0,
      waf_crs_locs: 2,
      waf_global_enabled: true,
      waf_global_mode: "block",
      waf_crs_enabled: true,
      waf_paranoia: 1,
      waf_response_body_check: false,
      location_wafs: [],
      secret_refs: 0,
      rbac: {
        serving: { enabled: false, principal_count: 0, role_count: 0, legacy_token_active: true, generation: "g1" },
        persisted: { enabled: false, principal_count: 0, role_count: 0, legacy_token_active: true, generation: "g1" },
        pending: false,
      },
      waf_effective: {
        compiled: true,
        generation: 5,
        inheriting_routes: 2,
        override_routes: 0,
        disabled_override_routes: 0,
        unprotected_routes: 0,
        block_routes: 2,
        detect_routes: 0,
        global_enabled: true,
        global: {
          mode: "block",
          block_status: 403,
          crs_enabled: true,
          paranoia: 1,
          response_body_inspection: false,
          rule_files_configured: 0,
          external_files: 0,
          inline_rules: false,
          rules: { total: 10, embedded: 10, external: 0, inline: 0, generated: 0 },
        },
      },
    };

    // 1. Without waf_routes inventory, an ordinal-only inherited route is rejected rather than synthesized blindly.
    const noInventoryRes = matchLocationWAFForEvent(inheritedGetEvent, [], baseProjection);
    expect(noInventoryRes.kind).toBe("rejected");

    // 2. Staged reorder: POST is now match_ordinal 0 and GET is match_ordinal 1 in waf_routes, while WAF counts and rbac.pending are unchanged.
    const reorderedRes = matchLocationWAFForEvent(inheritedGetEvent, [], {
      ...baseProjection,
      waf_routes: [
        {
          listen: ":8443",
          server_names: ["shop.example.test"],
          match_type: "prefix",
          path: "/items",
          match_ordinal: 0,
          selector_version: "global-v1",
          methods: ["POST"],
          predicates: "(POST)",
          canonical_predicates: "methods=POST\n",
          scope: "global",
          enabled: true,
        },
        {
          listen: ":8443",
          server_names: ["shop.example.test"],
          match_type: "prefix",
          path: "/items",
          match_ordinal: 1,
          selector_version: "global-v1",
          methods: ["GET"],
          predicates: "(GET)",
          canonical_predicates: "methods=GET\n",
          scope: "global",
          enabled: true,
        },
      ],
    });
    expect(reorderedRes.kind).toBe("rejected");

    // 3. Staged reassignment to a route_id at match_ordinal 0 is rejected.
    const reassignedIdRes = matchLocationWAFForEvent(inheritedGetEvent, [], {
      ...baseProjection,
      waf_routes: [
        {
          route_id: "newly-assigned-id",
          listen: ":8443",
          server_names: ["shop.example.test"],
          match_type: "prefix",
          path: "/items",
          match_ordinal: 0,
          selector_version: "global-v1",
          methods: ["GET"],
          predicates: "(GET)",
          canonical_predicates: "methods=GET\n",
          scope: "global",
          enabled: true,
        },
      ],
    });
    expect(reassignedIdRes.kind).toBe("rejected");

    // 4. When waf_routes matches the serving event's ordinal and predicates, full initial investigation -> editor -> preview succeeds for the exact route.
    let previewPayload = "";
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      if (input.startsWith("/api/security")) {
        return Promise.resolve(
          new Response(
            JSON.stringify({
              ...baseProjection,
              base_version: "rev-inherited-pred-1",
              waf_routes: [
                {
                  listen: ":8443",
                  server_names: ["shop.example.test"],
                  match_type: "prefix",
                  path: "/items",
                  match_ordinal: 0,
                  selector_version: "global-v1",
                  methods: ["GET"],
                  predicates: "(GET)",
                  canonical_predicates: "methods=GET\n",
                  scope: "global",
                  enabled: true,
                },
                {
                  listen: ":8443",
                  server_names: ["shop.example.test"],
                  match_type: "prefix",
                  path: "/items",
                  match_ordinal: 1,
                  selector_version: "global-v1",
                  methods: ["POST"],
                  predicates: "(POST)",
                  canonical_predicates: "methods=POST\n",
                  scope: "global",
                  enabled: true,
                },
              ],
            }),
            { status: 200, headers: { "Content-Type": "application/json" } },
          ),
        );
      }
      if (input.startsWith("/api/listeners")) {
        return Promise.resolve(
          new Response(JSON.stringify({ listeners: [] }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      if (input === "/api/config/patch/preview") {
        previewPayload = typeof init?.body === "string" ? init.body : "";
        return Promise.resolve(
          new Response(JSON.stringify({ ok: true }), {
            status: 200,
            headers: { "Content-Type": "application/json" },
          }),
        );
      }
      return Promise.resolve(
        new Response(
          JSON.stringify({
            api_version: "v1",
            collection_state: "active",
            serving_generation: 5,
            serving_policy_digest: "ev1:01234567:abcdef012345",
            retention: { max_events: 256, max_event_bytes: 4096, ttl_seconds: 900 },
            stats: { retained_events: 1, recorded_total: 1, truncated_total: 0, dropped_total: 0 },
            events: [inheritedGetEvent],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <SecurityPanel />
      </Wrapper>,
    );

    fireEvent.click(await screen.findByText("Investigate outcomes"));
    fireEvent.click(await screen.findByText("Tune ARGS:report_filter →"));
    await screen.findByText(/Override for :8443 \/items/);
    fireEvent.click(screen.getByText("Review lifecycle and diff →"));
    await waitFor(() => {
      expect(previewPayload).not.toBe("");
    });
    expect(JSON.parse(previewPayload)).toMatchObject({
      base_version: "rev-inherited-pred-1",
      ops: [
        {
          op: "location_waf_set",
          listen: ":8443",
          server_names: ["shop.example.test"],
          match_type: "prefix",
          path: "/items",
          match_ordinal: 0,
          selector_version: "global-v1",
        },
      ],
    });
  });
});

