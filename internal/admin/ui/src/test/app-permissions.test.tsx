/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AppProjection } from "@/api/client.ts";
import { PermissionContext } from "@/auth/usePermission.ts";
import { AppDetail } from "@/features/apps/AppDetail.tsx";
import { AppEditor } from "@/features/apps/AppEditor.tsx";
import { DiscoveryEditor, HealthCheckEditor } from "@/features/apps/AppSettingsEditor.tsx";
import { AppsPanel } from "@/features/apps/AppsPanel.tsx";

function Scope({
  children,
  permissions,
  query = false,
}: {
  readonly children: ReactNode;
  readonly permissions: readonly string[];
  readonly query?: boolean;
}) {
  const content = (
    <PermissionContext.Provider
      value={{
        identity: {
          principal: "app-operator",
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
  );
  if (!query) return content;
  const client = new QueryClient({
    defaultOptions: {
      queries: { retry: false, refetchInterval: false, staleTime: Number.POSITIVE_INFINITY },
    },
  });
  client.setQueryData(["apps"], []);
  client.setQueryData(["routes"], []);
  return <QueryClientProvider client={client}>{content}</QueryClientProvider>;
}

function app(): AppProjection {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [
      { address: "10.0.0.1:8080", weight: 1 },
      { address: "10.0.0.2:8080", weight: 1 },
    ],
    health_check: false,
    routes_using: [],
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("App config:write/config:apply/config:raw boundaries", () => {
  it("allows preview with config:write alone while explaining final apply remains separate", () => {
    render(
      <Scope permissions={["config:write"]}>
        <AppEditor
          initial={{ name: "api", backends: [{ address: "10.0.0.1:8080", weight: 1 }] }}
          onClose={() => undefined}
        />
      </Scope>,
    );

    expect(screen.getByRole("button", { name: "Review batch in editor →" })).toBeEnabled();
    expect(screen.getByText(/Final apply is gated independently/i)).toBeInTheDocument();
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:apply")),
    ).toBe(true);
  });

  it.each([
    ["config:apply only", ["config:apply"]],
    ["config:raw only", ["config:raw"]],
    ["read-only", []],
  ] as const)("disables structured App creation for %s", (_label, permissions) => {
    render(
      <Scope permissions={permissions}>
        <AppEditor
          initial={{ name: "api", backends: [{ address: "10.0.0.1:8080", weight: 1 }] }}
          onClose={() => undefined}
        />
      </Scope>,
    );

    expect(screen.getByRole("button", { name: "Review batch in editor →" })).toBeDisabled();
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:write")),
    ).toBe(true);
  });

  it("keeps the token-required raw-editor handoff behind config:raw", async () => {
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    render(
      <Scope permissions={["config:write", "config:apply"]}>
        <AppEditor
          initial={{ name: "api", backends: [{ address: "10.0.0.1:8080", weight: 1 }] }}
          onClose={() => undefined}
        />
      </Scope>,
    );

    fireEvent.change(screen.getByRole("combobox", { name: "Discovery provider" }), {
      target: { value: "consul" },
    });
    fireEvent.change(screen.getByRole("textbox", { name: "Service" }), {
      target: { value: "web" },
    });
    fireEvent.click(
      screen.getByRole("checkbox", {
        name: "This new provider requires an authentication token",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Review batch in editor →" }));

    expect(await screen.findByText(/needs a new secret token/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open raw configuration editor →" })).toBeDisabled();
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:raw")),
    ).toBe(true);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([
    [
      "health-check",
      <HealthCheckEditor
        key="health"
        app={{
          ...app(),
          health_check: true,
          health_check_type: "http",
          health_check_path: "/healthz",
        }}
        onClose={() => undefined}
      />,
      "/healthz",
    ],
    [
      "discovery",
      <DiscoveryEditor
        key="discovery"
        app={{
          ...app(),
          discovery: "consul",
          discovery_consul: { service: "web" },
        }}
        onClose={() => undefined}
      />,
      "web",
    ],
  ] as const)("disables the direct %s editor without config:write", (_label, editor, value) => {
    render(<Scope permissions={[]}>{editor}</Scope>);

    expect(screen.getByDisplayValue(value)).toBeDisabled();
    expect(screen.getByRole("button", { name: "Review in editor →" })).toBeDisabled();
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:write")),
    ).toBe(true);
  });

  it("disables every AppDetail mutation and deletion control without config:write", () => {
    render(
      <Scope permissions={["config:apply", "config:raw"]}>
        <AppDetail app={app()} onClose={() => undefined} />
      </Scope>,
    );

    expect(screen.getByRole("combobox", { name: "Load-balancing strategy" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit health checks →" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Edit discovery →" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Delete App / upstream…" })).toBeDisabled();
    expect(
      screen
        .getAllByRole("button", { name: "Remove →" })
        .every((button) => button.hasAttribute("disabled")),
    ).toBe(true);
    expect(
      screen.getAllByRole("note").some((note) => note.textContent.includes("config:write")),
    ).toBe(true);
  });

  it("disables both New App entry points in AppsPanel without config:write", () => {
    render(
      <Scope permissions={[]} query>
        <AppsPanel />
      </Scope>,
    );

    const buttons = screen.getAllByRole("button", { name: "New app" });
    expect(buttons).toHaveLength(2);
    expect(buttons.every((button) => button.hasAttribute("disabled"))).toBe(true);
    expect(
      screen.getAllByRole("note").every((note) => note.textContent.includes("config:write")),
    ).toBe(true);
  });
});
