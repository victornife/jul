/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ClientAddressPolicyInput, ListenerClientAddress } from "@/api/client.ts";
import { PermissionContext } from "@/auth/usePermission.ts";
import { ClientAddressEditor } from "@/features/security/ClientAddressEditor.tsx";

function Scope({
  children,
  permissions,
}: {
  readonly children: ReactNode;
  readonly permissions: readonly string[];
}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return (
    <QueryClientProvider client={client}>
      <PermissionContext.Provider
        value={{
          identity: {
            principal: "trust-admin",
            role: "custom",
            token_id: "",
            permissions: [...permissions],
            legacy: false,
          },
          isLoading: false,
          ready: true,
          has: (permission) => permissions.includes(permission),
        }}
      >
        <MemoryRouter>{children}</MemoryRouter>
      </PermissionContext.Provider>
    </QueryClientProvider>
  );
}

function listener(over: Partial<ListenerClientAddress> = {}): ListenerClientAddress {
  return {
    listen: "127.0.0.1:8443",
    server_blocks: 2,
    configured: true,
    trusted_proxies: ["10.0.0.0/8"],
    forwarded_headers: ["forwarded", "x-forwarded-for"],
    max_hops: 16,
    headers_disabled: false,
    trusts_every_client: false,
    ...over,
  };
}

// bodyOf reads a stubbed request body as the JSON the component sent. The body
// is always a string here because the client serialises with JSON.stringify.
function bodyOf(init: RequestInit): { client_address: ClientAddressPolicyInput | null } {
  return JSON.parse(init.body as string) as {
    client_address: ClientAddressPolicyInput | null;
  };
}

function stubFetch(): ReturnType<typeof vi.fn> {
  const fetchMock = vi.fn().mockResolvedValue({
    ok: true,
    status: 200,
    statusText: "OK",
    json: () => Promise.resolve({ ok: true, mode: "hot", message: "applied" }),
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("ClientAddressEditor", () => {
  it("states that the policy covers every server block on the listener", () => {
    render(
      <Scope permissions={["config:trust"]}>
        <ClientAddressEditor listener={listener()} onClose={() => undefined} />
      </Scope>,
    );
    expect(screen.getByText(/security boundary/i)).toBeInTheDocument();
    expect(screen.getByText(/2 server blocks/)).toBeInTheDocument();
  });

  it("requires config:trust to apply, not config:write", () => {
    render(
      <Scope permissions={["config:write", "config:apply"]}>
        <ClientAddressEditor listener={listener()} onClose={() => undefined} />
      </Scope>,
    );
    expect(screen.getByRole("button", { name: /Apply to this listener/ })).toBeDisabled();
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:trust")),
    ).toBe(true);
  });

  it("sends the edited policy to the listener endpoint", async () => {
    const fetchMock = stubFetch();
    render(
      <Scope permissions={["config:trust"]}>
        <ClientAddressEditor listener={listener()} onClose={() => undefined} />
      </Scope>,
    );

    fireEvent.change(screen.getByRole("textbox"), {
      target: { value: "10.0.0.0/8\n192.0.2.10" },
    });
    fireEvent.click(screen.getByRole("button", { name: /Apply to this listener/ }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalled();
    });
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/listeners/127.0.0.1%3A8443/client_address");
    expect(init.method).toBe("PATCH");
    expect(bodyOf(init)).toEqual({
      client_address: {
        trusted_proxies: ["10.0.0.0/8", "192.0.2.10"],
        forwarded_headers: ["forwarded", "x-forwarded-for"],
        max_hops: 16,
      },
    });
  });

  it("sends an explicitly empty header list when headers are turned off", async () => {
    const fetchMock = stubFetch();
    render(
      <Scope permissions={["config:trust"]}>
        <ClientAddressEditor listener={listener()} onClose={() => undefined} />
      </Scope>,
    );

    fireEvent.change(screen.getByRole("combobox"), { target: { value: "none" } });
    fireEvent.click(screen.getByRole("button", { name: /Apply to this listener/ }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalled();
    });
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    // An empty list is a real setting (read no forwarding header), distinct
    // from omitting the field, which keeps the default preference.
    expect(bodyOf(init).client_address?.forwarded_headers).toEqual([]);
  });

  it("clears the policy with a null payload", async () => {
    const fetchMock = stubFetch();
    render(
      <Scope permissions={["config:trust"]}>
        <ClientAddressEditor listener={listener()} onClose={() => undefined} />
      </Scope>,
    );

    fireEvent.click(screen.getByRole("button", { name: /Trust no proxy/ }));

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalled();
    });
    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit];
    expect(bodyOf(init)).toEqual({ client_address: null });
  });

  it("surfaces the server's validation error rather than validating locally", async () => {
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      statusText: "Bad Request",
      headers: new Headers(),
      json: () =>
        Promise.resolve({
          error: "servers[0].client_address.trusted_proxies[0]: trusted proxy has host bits set",
        }),
    });
    vi.stubGlobal("fetch", fetchMock);

    render(
      <Scope permissions={["config:trust"]}>
        <ClientAddressEditor listener={listener()} onClose={() => undefined} />
      </Scope>,
    );

    fireEvent.change(screen.getByRole("textbox"), { target: { value: "10.1.2.3/8" } });
    fireEvent.click(screen.getByRole("button", { name: /Apply to this listener/ }));

    await waitFor(() => {
      expect(screen.getByText(/host bits set/)).toBeInTheDocument();
    });
  });

  it("offers Configure rather than Edit for an untrusting listener", () => {
    render(
      <Scope permissions={["config:trust"]}>
        <ClientAddressEditor
          listener={listener({ configured: false, trusted_proxies: [] })}
          onClose={() => undefined}
        />
      </Scope>,
    );
    // Nothing to clear when no policy exists.
    expect(screen.queryByRole("button", { name: /Trust no proxy/ })).toBeNull();
  });
});
