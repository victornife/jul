/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppProjection } from "@/api/client.ts";
import { AppsPanel } from "@/features/apps/AppsPanel.tsx";

const REOPEN_KEY = "jul-apps-reopen-selection";
const realFetch = globalThis.fetch;

function app(name: string, strategy = "round_robin"): AppProjection {
  return {
    name,
    strategy,
    backends: [{ address: "127.0.0.1:8080", weight: 1 }],
    health_check: false,
    routes_using: [],
  };
}

function makeClient(
  apps: readonly AppProjection[],
  options: { readonly stale?: boolean } = {},
): QueryClient {
  const client = new QueryClient({
    defaultOptions: {
      queries: {
        retry: false,
        staleTime: options.stale ? 0 : Number.POSITIVE_INFINITY,
        refetchInterval: false,
      },
    },
  });
  client.setQueryData(["apps"], apps);
  client.setQueryData(["routes"], []);
  return client;
}

function json(body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { "Content-Type": "application/json" },
  });
}

function Wrapper({
  children,
  client,
}: {
  readonly children: ReactNode;
  readonly client: QueryClient;
}) {
  return (
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={["/apps"]}>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

afterEach(() => {
  sessionStorage.clear();
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("AppsPanel exact-name selection state", () => {
  it("reopens only an exact App name that exists in the refreshed projection", async () => {
    sessionStorage.setItem(REOPEN_KEY, "api");
    const client = makeClient([app("api"), app("worker")]);
    globalThis.fetch = vi.fn();

    render(
      <Wrapper client={client}>
        <AppsPanel />
      </Wrapper>,
    );

    expect(await screen.findByRole("dialog", { name: "api" })).toBeInTheDocument();
    expect(sessionStorage.getItem(REOPEN_KEY)).toBeNull();
  });

  it("keeps the exact reopen marker while cached pre-apply data is refreshing", async () => {
    let resolveApps: ((response: Response) => void) | undefined;
    const appsResponse = new Promise<Response>((resolve) => {
      resolveApps = resolve;
    });
    globalThis.fetch = vi.fn((input: string) => {
      if (input === "/api/apps") return appsResponse;
      if (input === "/api/routes") return Promise.resolve(json([]));
      return Promise.reject(new Error(`unexpected request: ${input}`));
    }) as typeof fetch;
    sessionStorage.setItem(REOPEN_KEY, "api");
    const client = makeClient([app("api-v2")], { stale: true });

    render(
      <Wrapper client={client}>
        <AppsPanel />
      </Wrapper>,
    );

    await waitFor(() => {
      expect(globalThis.fetch).toHaveBeenCalledWith("/api/apps", expect.anything());
    });
    expect(sessionStorage.getItem(REOPEN_KEY)).toBe("api");
    expect(screen.queryByRole("dialog")).toBeNull();

    act(() => {
      resolveApps?.(json([app("api-v2"), app("api")]));
    });

    expect(await screen.findByRole("dialog", { name: "api" })).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "api-v2" })).toBeNull();
    expect(sessionStorage.getItem(REOPEN_KEY)).toBeNull();
  });

  it("consumes a stale reopen request without selecting a different App", async () => {
    sessionStorage.setItem(REOPEN_KEY, "missing");
    const client = makeClient([app("api")]);
    globalThis.fetch = vi.fn();

    render(
      <Wrapper client={client}>
        <AppsPanel />
      </Wrapper>,
    );

    await waitFor(() => {
      expect(sessionStorage.getItem(REOPEN_KEY)).toBeNull();
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("derives an open detail from the latest exact-name projection and clears it on removal", async () => {
    const client = makeClient([app("api")]);
    globalThis.fetch = vi.fn();

    render(
      <Wrapper client={client}>
        <AppsPanel />
      </Wrapper>,
    );

    fireEvent.click(screen.getByRole("button", { name: "Open App api" }));
    expect(await screen.findByRole("dialog", { name: "api" })).toBeInTheDocument();
    expect(screen.getByText("round_robin · 1 backend(s)")).toBeInTheDocument();

    act(() => {
      client.setQueryData(["apps"], [app("api", "least_conn")]);
    });
    expect(await screen.findByText("least_conn · 1 backend(s)")).toBeInTheDocument();

    act(() => {
      client.setQueryData(["apps"], []);
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog", { name: "api" })).toBeNull();
    });
  });
});
