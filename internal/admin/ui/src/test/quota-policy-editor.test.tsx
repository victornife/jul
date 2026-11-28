/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  fetchRoutes: vi.fn(),
  fetchStats: vi.fn(),
  fetchTrafficControls: vi.fn(),
  run: vi.fn(),
  clearError: vi.fn(),
}));

vi.mock("@/api/client.ts", async () => {
  const actual = await vi.importActual<typeof import("@/api/client.ts")>("@/api/client.ts");
  return {
    ...actual,
    fetchRoutes: mocks.fetchRoutes,
    fetchStats: mocks.fetchStats,
    fetchTrafficControls: mocks.fetchTrafficControls,
  };
});

vi.mock("@/lib/useRunPatchBatch.ts", async () => {
  const actual = await vi.importActual<typeof import("@/lib/useRunPatchBatch.ts")>(
    "@/lib/useRunPatchBatch.ts",
  );
  return {
    ...actual,
    useRunPatchBatch: () => ({
      busy: false,
      error: null,
      clearError: mocks.clearError,
      run: mocks.run,
      preview: vi.fn(),
      handoff: vi.fn(),
    }),
  };
});

import { QuotaPolicyEditor } from "@/features/traffic-controls/QuotaPolicyEditor.tsx";
import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import type { RouteProjection, TrafficControls } from "@/api/client.ts";

const policy: NonNullable<TrafficControls["quota_policies"]>[number] = {
  name: "api",
  mode: "enforce",
  rate: 100,
  burst: 120,
  max_callers: 500,
  classes: [
    {
      name: "account",
      shared: { rate: 60, burst: 70 },
      per_caller: { rate: 20, burst: 25 },
      principal_count: 1,
    },
    {
      name: "public",
      shared: { rate: 40, burst: 50 },
      per_caller: { rate: 10, burst: 12 },
      principal_count: 0,
    },
  ],
  pools: [{ name: "office", class: "network", cidrs: ["192.0.2.0/24"] }],
  operations: [
    {
      name: "export",
      shared: { rate: 5, burst: 8 },
      per_caller: { rate: 2, burst: 3 },
    },
  ],
};

function renderEditor(current: TrafficControls) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <QuotaPolicyEditor current={current} onClose={() => undefined} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("application quota editor", () => {
  beforeEach(() => {
    mocks.fetchRoutes.mockReset();
    mocks.fetchStats.mockReset();
    mocks.fetchTrafficControls.mockReset();
    mocks.run.mockReset().mockResolvedValue(undefined);
    mocks.clearError.mockReset();
    mocks.fetchStats.mockResolvedValue({
      quotaDecisions: { api: { "enforce:admitted": 8, "enforce:application": 3, "observe:class": 2 } },
    });
  });

  it("edits fixed allocations, attaches routes, and omits principal values", async () => {
    mocks.fetchRoutes.mockResolvedValue([
      {
        base_version: "base-1",
        listen: ":8080",
        server_names: ["api.example"],
        http3: false,
        h2c: false,
        locations: [
          {
            index: 0,
            match: "/api",
            type: "prefix",
            action: "proxy",
            auth: true,
            cache: false,
            compression: false,
            rate_limit: false,
            quota_policy: "api",
            quota_operation: "export",
            secure: false,
            require_client_cert: false,
            withdrawn: false,
            response_headers: false,
            match_ordinal: 0,
            selector_version: "route-selector-v1",
          },
        ],
      },
    ]);

    renderEditor({ base_version: "base-1", quota_policies: [policy] });

    expect(await screen.findByText(/1 verified identity mapping/)).toBeInTheDocument();
    expect(screen.getByText(/office → network \(192\.0\.2\.0\/24\)/)).toBeInTheDocument();
    expect(screen.queryByText(/private-account-id/)).not.toBeInTheDocument();
    expect(await screen.findByText(/enforce:admitted 8, enforce:application 3, observe:class 2/)).toBeInTheDocument();

    const sharedRate = screen.getAllByLabelText("Shared rate").at(0);
    if (sharedRate === undefined) throw new Error("account shared rate field was not rendered");
    fireEvent.change(sharedRate, { target: { value: "55" } });
    const routeSelect = await screen.findByLabelText("Route");
    fireEvent.change(routeSelect, {
      target: { value: JSON.stringify([":8080", ["api.example"], "prefix", "/api", 0]) },
    });
    fireEvent.change(screen.getByLabelText("Application policy"), { target: { value: "" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));

    expect(mocks.run).toHaveBeenCalledTimes(1);
    const [operations, baseVersion] = mocks.run.mock.calls[0] as [Array<Record<string, unknown>>, string];
    expect(baseVersion).toBe("base-1");
    expect(operations).toHaveLength(2);
    expect(operations[0]).toMatchObject({
      op: "quota_policy_set",
      quota_policy: {
        name: "api",
        classes: { account: { shared: { rate: 55, burst: 70 } } },
      },
    });
    expect(operations[1]).toMatchObject({
      op: "route_set_quota_policy",
      listen: ":8080",
      server_names: ["api.example"],
      match_type: "prefix",
      path: "/api",
      match_ordinal: 0,
      selector_version: "route-selector-v1",
      route_quota: { policy: "" },
    });
  });

  it("shows effective class selection and CIDR pools on an attached route", async () => {
    mocks.fetchTrafficControls.mockResolvedValue({ quota_policies: [policy] });
    const location: RouteProjection["locations"][number] = {
      index: 0,
      match: "/api",
      type: "prefix",
      action: "proxy",
      auth: true,
      cache: false,
      compression: false,
      rate_limit: false,
      quota_policy: "api",
      quota_operation: "export",
      secure: false,
      require_client_cert: false,
    };
    const route: RouteProjection = {
      listen: ":8080",
      server_names: ["api.example"],
      http3: false,
      h2c: false,
      locations: [location],
    };
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, refetchInterval: false } },
    });

    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <RouteDetail route={route} loc={location} onClose={() => undefined} onEdit={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText(/Class selection: exact verified principal mapping/)).toBeInTheDocument();
    expect(screen.getByText(/office → network \(192\.0\.2\.0\/24\)/)).toBeInTheDocument();
    expect(screen.getByText("export", { exact: true })).toBeInTheDocument();
    expect(screen.queryByText(/private-account-id/)).not.toBeInTheDocument();
  });
});
