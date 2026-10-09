/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Tests for #147 slice 4: the route-order/precedence context row in
 * RouteDetail, and the accessibility additions (focus management on add/
 * remove, accessible live regions for warnings/errors) in the three guided
 * predicate/response-header/CORS editors.
 */
import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { RouteDetail } from "@/features/routes/RouteDetail.tsx";
import { Sparkline } from "@/components/Sparkline.tsx";
import { PredicatesEditor } from "@/features/routes/PredicatesEditor.tsx";
import { ResponseHeadersEditor } from "@/features/routes/ResponseHeadersEditor.tsx";
import type { LocationProjection, RouteProjection, RouteTarget } from "@/api/client.ts";
import { routeMetricInterval } from "@/lib/useMetricsHistory";
import * as metricsHistory from "@/lib/useMetricsHistory";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

function baseLoc(over: Partial<LocationProjection> = {}): LocationProjection {
  return {
    index: 0,
    match: "/api/",
    type: "prefix",
    action: "deny",
    auth: false,
    cache: false,
    compression: false,
    rate_limit: false,
    secure: false,
    require_client_cert: false,
    ...over,
  };
}

afterEach(() => {
  vi.restoreAllMocks();
});

describe("RouteDetail route-order note", () => {
  it("is absent when this location's match coordinates are unique", () => {
    const loc = baseLoc({ match_ordinal: 0 });
    const route: RouteProjection = {
      listen: ":8080",
      server_names: [],
      http3: false,
      h2c: false,
      locations: [loc],
    };
    render(<RouteDetail route={route} loc={loc} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });
    expect(screen.queryByText("Route order")).not.toBeInTheDocument();
  });

  it("names this location's position among locations sharing its match", () => {
    const a = baseLoc({ match_ordinal: 0, predicates: "POST" });
    const b = baseLoc({ match_ordinal: 1, predicates: "GET" });
    const route: RouteProjection = {
      listen: ":8080",
      server_names: [],
      http3: false,
      h2c: false,
      locations: [a, b],
    };
    render(<RouteDetail route={route} loc={b} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });
    expect(screen.getByText("Route order")).toBeInTheDocument();
    expect(screen.getByText(/2 of 2 routes sharing this match/)).toBeInTheDocument();
  });
});

describe("RouteDetail metrics", () => {
  it("renders separate latency segments without compressing gaps", () => {
    render(
      <Sparkline
        data={[50, 75, null, 100, 150]}
        width={240}
        height={64}
        ariaLabel="Sparse latency"
      />,
    );
    const chart = screen.getByRole("img", { name: "Sparse latency" });
    expect(
      Array.from(chart.querySelectorAll("polyline"), (line) => line.getAttribute("points")),
    ).toEqual(["2,62 61,47", "179,32 238,2"]);
  });

  it("does not manufacture values for all-missing or non-finite samples", () => {
    const view = render(<Sparkline data={[null, null, null]} ariaLabel="Missing latency" />);
    let chart = screen.getByRole("img", { name: "Missing latency" });
    expect(chart.querySelectorAll("circle, polyline")).toHaveLength(0);
    view.rerender(<Sparkline data={[50, Number.NaN, 100]} ariaLabel="Missing latency" />);
    chart = screen.getByRole("img", { name: "Missing latency" });
    expect(chart.querySelectorAll("polyline")).toHaveLength(0);
    expect(chart.querySelectorAll("circle")).toHaveLength(2);
    expect(chart.innerHTML).not.toMatch(/NaN|Infinity/);
  });
  it("keeps missing sparkline intervals at their original time indexes", () => {
    const hover = vi.fn();
    render(
      <Sparkline
        data={[50, null, 100]}
        width={240}
        height={64}
        ariaLabel="Latency gaps"
        onPointHover={hover}
      />,
    );
    const chart = screen.getByRole("img", { name: "Latency gaps" });
    expect(chart.querySelectorAll("polyline")).toHaveLength(0);
    expect(
      Array.from(chart.querySelectorAll("circle"), (point) => point.getAttribute("cx")),
    ).toEqual(["2", "238"]);
    fireEvent.keyDown(chart, { key: "ArrowRight" });
    fireEvent.keyDown(chart, { key: "ArrowRight" });
    expect(hover).toHaveBeenLastCalledWith(1, null);
    expect(chart.querySelectorAll("circle")).toHaveLength(2);
    expect(chart.innerHTML).not.toMatch(/NaN|Infinity/);
  });
  it("calculates interval RED and byte rates without cumulative averages", () => {
    const previous = {
      routeId: "route-one",
      requests: 100,
      errors: 10,
      responseBytes: 1000,
      durationCount: 100,
      durationSumSeconds: 5,
    };
    const current = {
      routeId: "route-one",
      requests: 120,
      errors: 12,
      responseBytes: 5000,
      durationCount: 120,
      durationSumSeconds: 6,
    };
    expect(routeMetricInterval(previous, current, 2000)).toEqual({
      requestsPerSec: 10,
      errorsPercent: 10,
      latencyMs: 50,
      bytesPerSec: 2000,
    });
    expect(routeMetricInterval(current, previous, 2000)).toBeNull();
    expect(routeMetricInterval(previous, { ...current, routeId: "other-route" }, 2000)).toBeNull();
    expect(routeMetricInterval(previous, previous, 2000)?.latencyMs).toBeNull();
  });

  it("does not present unidentified aggregate data as a specific route", () => {
    const loc = baseLoc();
    const route: RouteProjection = {
      listen: ":8080",
      server_names: [],
      http3: false,
      h2c: false,
      locations: [loc],
    };
    render(<RouteDetail route={route} loc={loc} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });
    expect(screen.getByText("No durable route ID")).toBeInTheDocument();
  });

  it("renders disabled route metrics after a successful stats read", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          uptimeSeconds: 1,
          requestsTotal: 0,
          requestsPerSec: 0,
          inFlight: 0,
          connections: 0,
          errorRate: 0,
          latencyAvgMs: 0,
          latencyP50Ms: 0,
          latencyP95Ms: 0,
          latencyP99Ms: 0,
          cacheHitRatio: 0,
          routeMetricsEnabled: false,
          routeMetrics: [],
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );
    const loc = baseLoc({ route_id: "route-one" });
    const route: RouteProjection = {
      listen: ":8080",
      server_names: [],
      http3: false,
      h2c: false,
      locations: [loc],
    };
    render(<RouteDetail route={route} loc={loc} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });
    expect(await screen.findByText("Route metrics disabled")).toBeInTheDocument();
  });

  it("renders four route charts with an initial sampling baseline", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            uptimeSeconds: 1,
            requestsTotal: 10,
            requestsPerSec: 0,
            inFlight: 0,
            connections: 0,
            errorRate: 0,
            latencyAvgMs: 0,
            latencyP50Ms: 0,
            latencyP95Ms: 0,
            latencyP99Ms: 0,
            cacheHitRatio: 0,
            routeMetricsEnabled: true,
            routeMetrics: [
              {
                routeId: "route-one",
                requests: 10,
                errors: 1,
                responseBytes: 1000,
                durationCount: 10,
                durationSumSeconds: 0.5,
              },
            ],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      ),
    );
    const loc = baseLoc({ route_id: "route-one" });
    const route: RouteProjection = {
      listen: ":8080",
      server_names: [],
      http3: false,
      h2c: false,
      locations: [loc],
    };
    const view = render(
      <RouteDetail route={route} loc={loc} onClose={vi.fn()} onEdit={vi.fn()} />,
      {
        wrapper: Wrapper,
      },
    );
    expect(
      await screen.findByRole("img", { name: "Request rate for route route-one" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: "Server errors for route route-one" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: "Mean latency for route route-one" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("img", { name: "Response rate for route route-one" }),
    ).toBeInTheDocument();
    vi.spyOn(metricsHistory, "useRouteMetricsHistory").mockReturnValue(
      [50, null, 100].map((latencyMs) => ({
        requestsPerSec: 1,
        errorsPercent: 0,
        latencyMs,
        bytesPerSec: 4,
      })),
    );
    view.unmount();
    render(<RouteDetail route={route} loc={loc} onClose={vi.fn()} onEdit={vi.fn()} />, {
      wrapper: Wrapper,
    });
    const latency = await screen.findByRole("img", { name: "Mean latency for route route-one" });
    expect(latency.querySelectorAll("polyline")).toHaveLength(0);
    expect(
      Array.from(latency.querySelectorAll("circle"), (point) => point.getAttribute("cx")),
    ).toEqual(["2", "238"]);
    expect(
      screen
        .getByRole("img", { name: "Request rate for route route-one" })
        .querySelector("polyline")
        ?.getAttribute("points")
        ?.split(" "),
    ).toHaveLength(3);
  });
});

function target(): RouteTarget {
  return { listen: ":8080", server_names: [], match_type: "prefix", path: "/api" };
}

describe("PredicatesEditor accessibility", () => {
  it("moves focus to the new row's name field after adding a header predicate", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add header predicate"));
    expect(screen.getByLabelText("Header row 1 name")).toHaveFocus();
  });

  it("returns focus to + Add header predicate after removing the only row", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add header predicate"));
    fireEvent.click(screen.getByLabelText("Remove header row 1"));
    expect(screen.getByText("+ Add header predicate")).toHaveFocus();
  });

  it("moves focus to the new row's name field after adding a query predicate", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add query predicate"));
    expect(screen.getByLabelText("Query row 1 name")).toHaveFocus();
  });

  it("announces predicate warnings via role=alert", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/at least one predicate/i);
  });
});

describe("ResponseHeadersEditor accessibility", () => {
  it("moves focus to the new row's name field after adding an operation", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add operation"));
    expect(screen.getByLabelText("Row 1 header name")).toHaveFocus();
  });

  it("returns focus to + Add operation after removing the only row", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add operation"));
    fireEvent.click(screen.getByLabelText("Remove row 1"));
    expect(screen.getByText("+ Add operation")).toHaveFocus();
  });

  it("announces the zero-rows warning via role=alert", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByRole("alert")).toHaveTextContent(/use clear to remove them all/i);
  });
});
