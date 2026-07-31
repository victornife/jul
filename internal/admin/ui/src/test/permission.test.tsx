/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Vitest tests for the Console permission layer (P3-03 §33): the
 * PermissionProvider that fetches GET /api/admin/me, the usePermission hook's
 * fail-open-until-known contract, and the ForbiddenAction explanatory note.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { render, screen, waitFor, act } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

import { PermissionProvider } from "@/auth/PermissionProvider.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { IdentitySchema, UNAUTHORIZED_EVENT, FORBIDDEN_EVENT } from "@/api/client.ts";

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <PermissionProvider>{children}</PermissionProvider>
    </QueryClientProvider>
  );
}

function stubIdentity(identity: unknown): void {
  vi.stubGlobal(
    "fetch",
    vi.fn().mockResolvedValue({
      ok: true,
      json: () => Promise.resolve(identity),
    }),
  );
}

// Probe renders the boolean result of has(permission) so tests can assert gating.
function Probe({ permission }: { readonly permission: string }) {
  const { has, ready } = usePermission();
  return (
    <div>
      <span data-testid="ready">{String(ready)}</span>
      <span data-testid="can">{String(has(permission))}</span>
    </div>
  );
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("IdentitySchema", () => {
  it("defaults token_id to empty and requires permissions", () => {
    const parsed = IdentitySchema.parse({
      principal: "alice",
      role: "operator",
      permissions: ["config:apply"],
      legacy: false,
    });
    expect(parsed.token_id).toBe("");
    expect(parsed.permissions).toContain("config:apply");
  });
});

describe("usePermission default context", () => {
  it("fails open (has returns true) when rendered without a provider", () => {
    render(<Probe permission="config:apply" />);
    expect(screen.getByTestId("ready").textContent).toBe("false");
    expect(screen.getByTestId("can").textContent).toBe("true");
  });
});

describe("PermissionProvider gating", () => {
  it("grants a held permission and denies an unheld one once identity is known", async () => {
    stubIdentity({
      principal: "bob",
      role: "viewer",
      token_id: "abc123abc123",
      permissions: ["status:read", "config:read"],
      legacy: false,
    });
    render(<Probe permission="config:apply" />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByTestId("ready").textContent).toBe("true");
    });
    expect(screen.getByTestId("can").textContent).toBe("false");
  });

  it("grants a permission the identity holds", async () => {
    stubIdentity({
      principal: "op",
      role: "operator",
      token_id: "9f32a1b4c921",
      permissions: ["status:read", "config:apply"],
      legacy: false,
    });
    render(<Probe permission="config:apply" />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByTestId("ready").textContent).toBe("true");
    });
    expect(screen.getByTestId("can").textContent).toBe("true");
  });

  it("refetches identity and updates gating when a gated action is forbidden (N-02)", async () => {
    const withApply = {
      principal: "op",
      role: "operator",
      token_id: "9f32a1b4c921",
      permissions: ["status:read", "config:apply"],
      legacy: false,
    };
    const withoutApply = { ...withApply, permissions: ["status:read"] };
    let call = 0;
    vi.stubGlobal(
      "fetch",
      vi.fn().mockImplementation(() => {
        call += 1;
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve(call === 1 ? withApply : withoutApply),
        });
      }),
    );
    render(<Probe permission="config:apply" />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByTestId("ready").textContent).toBe("true");
    });
    expect(screen.getByTestId("can").textContent).toBe("true");

    // A gated action returns 403 after a hot RBAC change; the permission layer
    // refetches identity and the control reflects the now-revoked permission.
    act(() => {
      window.dispatchEvent(new CustomEvent(FORBIDDEN_EVENT));
    });
    await waitFor(() => {
      expect(screen.getByTestId("can").textContent).toBe("false");
    });
  });
});

describe("ForbiddenAction", () => {
  it("explains why an action is unavailable to a principal that lacks the permission", async () => {
    stubIdentity({
      principal: "bob",
      role: "viewer",
      token_id: "abc123abc123",
      permissions: ["status:read"],
      legacy: false,
    });
    render(<ForbiddenAction permission="config:apply" />, { wrapper: Wrapper });
    expect(await screen.findByText(/Requires the/)).toBeInTheDocument();
    expect(screen.getByText("config:apply")).toBeInTheDocument();
    expect(screen.getByText(/does not grant it/)).toBeInTheDocument();
  });

  it("renders nothing when the identity holds the permission", async () => {
    stubIdentity({
      principal: "op",
      role: "operator",
      token_id: "9f32a1b4c921",
      permissions: ["config:apply"],
      legacy: false,
    });
    const { container } = render(<ForbiddenAction permission="config:apply" />, {
      wrapper: Wrapper,
    });
    // Give the identity query a tick to resolve, then assert no note appeared.
    await waitFor(() => {
      expect(container.querySelector('[role="note"]')).toBeNull();
    });
  });
});

describe("PermissionProvider unauthorized handling", () => {
  it("drops the cached identity on a 401 so stale permissions do not linger", async () => {
    stubIdentity({
      principal: "op",
      role: "operator",
      token_id: "9f32a1b4c921",
      permissions: ["config:apply"],
      legacy: false,
    });
    render(<Probe permission="config:apply" />, { wrapper: Wrapper });
    await waitFor(() => {
      expect(screen.getByTestId("ready").textContent).toBe("true");
    });
    // A 401 broadcast clears the identity; gating reverts to fail-open (unknown).
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT));
    await waitFor(() => {
      expect(screen.getByTestId("ready").textContent).toBe("false");
    });
    expect(screen.getByTestId("can").textContent).toBe("true");
  });
});
