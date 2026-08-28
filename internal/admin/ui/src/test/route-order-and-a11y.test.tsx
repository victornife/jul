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
import { PredicatesEditor } from "@/features/routes/PredicatesEditor.tsx";
import { ResponseHeadersEditor } from "@/features/routes/ResponseHeadersEditor.tsx";
import type { LocationProjection, RouteProjection, RouteTarget } from "@/api/client.ts";

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
