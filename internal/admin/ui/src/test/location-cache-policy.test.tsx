/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { LocationCachePolicyEditor } from "@/features/routes/LocationCachePolicyEditor.tsx";
import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import { PermissionContext } from "@/auth/usePermission.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import {
  buildCachePolicyPatchOps,
  CACHE_POLICY_STARTERS,
  parseBypassCookiesInput,
  parseDurationSecondsApprox,
  seedCachePolicyForm,
  validateCachePolicyForm,
} from "@/lib/cachePolicyDraft.ts";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";

const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
  takePendingDraft();
  vi.restoreAllMocks();
});

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <PermissionContext.Provider
        value={{
          identity: {
            principal: "admin",
            role: "admin",
            token_id: "t1",
            permissions: [
              "status:read",
              "config:read",
              "config:write",
              "config:apply",
              "config:raw",
            ],
            legacy: false,
          },
          isLoading: false,
          ready: true,
          has: () => true,
        }}
      >
        <MemoryRouter>{children}</MemoryRouter>
      </PermissionContext.Provider>
    </QueryClientProvider>
  );
}

const SAMPLE_ROUTE: RouteProjection = {
  base_version: "ver-123",
  listen: ":8080",
  server_names: ["example.com"],
  http3: false,
  h2c: false,
  locations: [],
};

const SAMPLE_LOC: LocationProjection = {
  index: 0,
  route_id: "route-api",
  match: "/api",
  type: "prefix",
  action: "proxy",
  target: "http://127.0.0.1:9000",
  auth: false,
  cache: true,
  compression: false,
  rate_limit: false,
  secure: false,
  require_client_cert: false,
  cache_policy: {
    default_ttl: "0s",
    max_ttl: "1m0s",
    stale_while_revalidate: "30s",
    stale_if_error: "5m0s",
    bypass_cookies: ["session_id"],
    bypass_any_cookie: false,
  },
  cache_effective: {
    global_enabled: true,
    default_ttl: "0s",
    default_ttl_source: "explicit_zero",
    max_ttl: "1m0s",
    max_ttl_source: "route_cap",
    stale_while_revalidate: "30s",
    swr_source: "route_override",
    stale_if_error: "5m0s",
    sie_source: "route_override",
    bypass_mode: "selected_cookies",
    bypass_cookies: ["session_id"],
    lock_enabled: true,
    range_enabled: true,
    memory_max_size: "128m",
    disk_enabled: true,
    max_entry_size: "16m",
  },
};

describe("cachePolicyDraft helpers", () => {
  it("parses durations and validates bypass cookies", () => {
    expect(parseDurationSecondsApprox("")).toBeNull();
    expect(parseDurationSecondsApprox("0")).toBe(0);
    expect(parseDurationSecondsApprox("0s")).toBe(0);
    expect(parseDurationSecondsApprox("1m30s")).toBe(90);
    expect(parseDurationSecondsApprox("2h")).toBe(7200);
    expect(parseDurationSecondsApprox("500ms")).toBe(0.5);
    expect(parseDurationSecondsApprox("100us")).toBeCloseTo(0.0001);
    expect(parseDurationSecondsApprox("100ns")).toBeCloseTo(0.0000001);
    expect(parseDurationSecondsApprox("-5s")).toBeNull();
    expect(parseDurationSecondsApprox("bogus")).toBeNull();

    const validCookies = parseBypassCookiesInput("Session_ID, __Host-auth");
    expect(validCookies.errors).toEqual([]);
    expect(validCookies.cookies).toEqual(["__host-auth", "session_id"]);

    const dupCookies = parseBypassCookiesInput("sid, SID, bad=val");
    expect(dupCookies.errors.length).toBe(2);

    const manyCookies = Array.from({ length: 33 }, (_, i) => `c${String(i)}`).join(", ");
    expect(parseBypassCookiesInput(manyCookies).errors.length).toBe(1);
    expect(parseBypassCookiesInput("a".repeat(130)).errors.length).toBe(1);
  });

  it("seeds form, validates warnings/errors, and builds patch ops", () => {
    const seeded = seedCachePolicyForm(SAMPLE_LOC);
    expect(seeded.cacheEnabled).toBe(true);
    expect(seeded.defaultTTL).toBe("0s");
    expect(seeded.bypassMode).toBe("named");
    expect(seeded.bypassCookiesInput).toBe("session_id");

    const seededAny = seedCachePolicyForm({
      ...SAMPLE_LOC,
      cache_policy: { bypass_any_cookie: true },
    });
    expect(seededAny.bypassMode).toBe("any");

    const warnCheck = validateCachePolicyForm({
      ...seeded,
      defaultTTL: "10m",
      maxTTL: "1m",
      cacheEnabled: false,
    });
    expect(warnCheck.errors).toEqual(["default_ttl (10m) must not exceed max_ttl (1m)."]);
    expect(warnCheck.warnings.length).toBe(1);

    const errCheck = validateCachePolicyForm({
      ...seeded,
      defaultTTL: "-1s",
      maxTTL: "invalid",
      bypassMode: "named",
      bypassCookiesInput: "",
    });
    expect(errCheck.errors.length).toBe(3);

    const target = {
      listen: ":8080",
      server_names: ["example.com"],
      match_type: "prefix",
      path: "/api",
    };
    const clearOps = buildCachePolicyPatchOps(
      target,
      SAMPLE_LOC,
      {
        cacheEnabled: false,
        defaultTTL: "",
        maxTTL: "",
        staleWhileRevalidate: "",
        staleIfError: "",
        bypassMode: "none",
        bypassCookiesInput: "",
      },
      null,
    );
    expect(clearOps).toEqual([
      { op: "route_toggle_cache", ...target, enabled: false },
      { op: "location_cache_policy_clear", ...target },
    ]);
  });
});

describe("LocationCachePolicyEditor and RouteDetail integration", () => {
  it("renders effective cache posture, applies starters, and previews batch ops", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      headers: new Headers(),
      json: () =>
        Promise.resolve({
          ok: true,
          summary: "route :8080/api cache policy set",
          operation_summaries: [
            {
              op_index: 0,
              op: "location_cache_policy_set",
              summary: "route :8080/api cache policy set",
            },
          ],
          diff: {
            summary: "1 modification",
            modifications: [
              {
                kind: "cache",
                name: "example.com :8080 /api",
                detail: "Change route cache policy",
              },
            ],
          },
          base_version: "ver-123",
          valid: true,
          validation_errors: [],
          lifecycle: {
            changes: [],
            can_apply_hot: true,
            can_stage_restart: true,
            hot_paths: ["servers.*.locations.*.cache_policy.max_ttl"],
            restart_required_paths: [],
            new_listener_only_paths: [],
            ignored_deprecated_paths: [],
            validation_rejected_paths: [],
            pending_subsystems: ["cache"],
          },
        }),
    });
    globalThis.fetch = fetchMock as typeof fetch;

    const onClose = vi.fn();
    render(
      <Wrapper>
        <LocationCachePolicyEditor route={SAMPLE_ROUTE} loc={SAMPLE_LOC} onClose={onClose} />
      </Wrapper>,
    );

    expect(screen.getByLabelText("Effective route cache posture")).toBeInTheDocument();
    expect(screen.getByText(/explicit_zero/)).toBeInTheDocument();
    expect(screen.getByText(/disable fresh storage when/)).toBeInTheDocument();

    // Exercise form input and radio button handlers, warnings, and errors
    fireEvent.click(screen.getByLabelText(/Enable caching on this route/i));
    fireEvent.change(screen.getByPlaceholderText(/inherit global \(or 0s, 5m, 1h\)/i), {
      target: { value: "2m" },
    });
    fireEvent.change(screen.getByPlaceholderText(/no route cap/i), {
      target: { value: "1m" },
    });
    fireEvent.change(screen.getByPlaceholderText(/inherit global \(or 0s, 30s\)/i), {
      target: { value: "15s" },
    });
    fireEvent.change(screen.getByPlaceholderText(/inherit global \(or 0s, 5m\)/i), {
      target: { value: "3m" },
    });
    fireEvent.click(screen.getByLabelText(/None \(requests with cookies/i));
    fireEvent.click(screen.getByLabelText(/Bypass on any Cookie header/i));
    fireEvent.click(screen.getByLabelText(/Bypass on specific cookie names/i));
    fireEvent.change(screen.getByPlaceholderText(/session_id, __Host-auth/i), {
      target: { value: "sid, auth_token" },
    });
    expect(
      screen.getByText(/default_ttl \(2m\) must not exceed max_ttl \(1m\)/i),
    ).toBeInTheDocument();
    expect(screen.getByText(/Route cache is disabled/i)).toBeInTheDocument();

    // Click the Static / versioned assets starter
    const staticStarter = screen.getByText(CACHE_POLICY_STARTERS[2]?.label ?? "");
    fireEvent.click(staticStarter);

    const previewBtn = screen.getByRole("button", { name: /Preview change/i });
    fireEvent.click(previewBtn);

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(1);
    });
    const draft = takePendingDraft();
    expect(draft).not.toBeNull();
    expect(draft?.kind).toBe("patch");
    if (draft?.kind === "patch") {
      expect(draft.ops).toEqual([
        {
          op: "location_cache_policy_set",
          listen: ":8080",
          server_names: ["example.com"],
          match_type: "prefix",
          path: "/api",
          cache_policy: {
            default_ttl: "1h",
            max_ttl: "24h",
            stale_while_revalidate: "5m",
            stale_if_error: "1h",
          },
        },
      ]);
    }

    // Exercise Reset to global defaults button
    const resetBtn = screen.getByRole("button", { name: /Reset to global defaults/i });
    fireEvent.click(resetBtn);
    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });
    const resetDraft = takePendingDraft();
    expect(resetDraft?.kind).toBe("patch");
    if (resetDraft?.kind === "patch") {
      expect(resetDraft.ops).toEqual([
        {
          op: "location_cache_policy_clear",
          listen: ":8080",
          server_names: ["example.com"],
          match_type: "prefix",
          path: "/api",
        },
      ]);
    }
  });

  it("renders Cache policy row and generated TOML in RouteDetail and opens editor", async () => {
    render(
      <Wrapper>
        <RouteDetail
          route={{ ...SAMPLE_ROUTE, locations: [SAMPLE_LOC] }}
          loc={{
            ...SAMPLE_LOC,
            cache_policy: {
              ...SAMPLE_LOC.cache_policy,
              bypass_any_cookie: true,
            },
          }}
          onClose={() => {}}
          onEdit={() => {}}
        />
      </Wrapper>,
    );

    expect(screen.getByText("Cache policy")).toBeInTheDocument();
    const editBtn = screen.getByRole("button", { name: /Edit cache policy/i });
    expect(editBtn).toBeInTheDocument();
    expect(screen.getByText(/\[servers\.locations\.cache_policy\]/)).toBeInTheDocument();
    expect(screen.getByText(/bypass_any_cookie = true/)).toBeInTheDocument();

    fireEvent.click(editBtn);
    await waitFor(() => {
      expect(screen.getByText("Route cache policy")).toBeInTheDocument();
    });
  });

  it("handles failed preview responses and denied config:write permissions with accessible alerts", async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      statusText: "Bad Request",
      headers: new Headers(),
      json: () =>
        Promise.resolve({
          ok: false,
          message: "location_cache_policy_set rejected by server",
          errors: [
            {
              code: "invalid_cache_policy",
              path: "servers[0].locations[0].cache_policy",
              summary: "default_ttl exceeds max_ttl",
              severity: "error",
            },
          ],
          op_index: 0,
          op: "location_cache_policy_set",
        }),
    }) as typeof fetch;

    render(
      <Wrapper>
        <LocationCachePolicyEditor route={SAMPLE_ROUTE} loc={SAMPLE_LOC} onClose={() => {}} />
      </Wrapper>,
    );

    fireEvent.click(screen.getByText(CACHE_POLICY_STARTERS[1]?.label ?? ""));
    fireEvent.click(screen.getByRole("button", { name: /Preview change/i }));

    await waitFor(() => {
      expect(screen.getByRole("alert")).toHaveTextContent(/default_ttl exceeds max_ttl|rejected/i);
    });

    // Verify viewer (no config:write) has all starters and actions disabled
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <PermissionContext.Provider
          value={{
            identity: {
              principal: "viewer",
              role: "viewer",
              token_id: "t-view",
              permissions: ["status:read", "config:read"],
              legacy: false,
            },
            isLoading: false,
            ready: true,
            has: (p) => p === "status:read" || p === "config:read",
          }}
        >
          <MemoryRouter>
            <LocationCachePolicyEditor
              route={SAMPLE_ROUTE}
              loc={SAMPLE_LOC}
              onClose={() => {}}
            />
          </MemoryRouter>
        </PermissionContext.Provider>
      </QueryClientProvider>,
    );

    const previewButtons = screen.getAllByRole("button", { name: /Preview change/i });
    expect(previewButtons[previewButtons.length - 1]).toBeDisabled();
  });
});
