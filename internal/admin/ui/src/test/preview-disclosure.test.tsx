/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { act, cleanup, render, renderHook, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PermissionProvider } from "@/auth/PermissionProvider.tsx";
import { AdminSession } from "@/app/AdminSession.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import {
  authToken,
  fetchRawConfig,
  invalidateAdminResponses,
  FORBIDDEN_EVENT,
} from "@/api/client.ts";
import { useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";
import {
  pendingDraftStorageKey,
  setPendingDraft,
  takePendingDraft,
} from "@/lib/configDraftHandoff.ts";

vi.unmock("@/api/client.ts");
const marker = "SYNTHETIC_PRIVILEGED_COMPARISON";
const json = (value: unknown) => new Response(JSON.stringify(value));
const identity = (raw: boolean) => ({
  principal: "writer",
  role: "custom",
  token_id: "same-token",
  legacy: false,
  permissions: raw
    ? ["config:read", "config:write", "config:raw"]
    : ["config:read", "config:write"],
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  sessionStorage.clear();
  localStorage.clear();
  takePendingDraft();
});

describe("comparison disclosure boundary in the Console", () => {
  it("purges historical privileged previews before mounting a writer without raw access", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    qc.setQueryData(["raw-config"], { raw: marker, base_version: marker });
    sessionStorage.setItem(
      pendingDraftStorageKey,
      JSON.stringify({
        kind: "patch",
        previewDiff: { summary: marker },
        ops: [{ op: "compression_set" }],
      }),
    );
    const fetch = vi.fn(() => Promise.resolve(json(identity(false))));
    vi.stubGlobal("fetch", fetch);
    render(
      <QueryClientProvider client={qc}>
        <PermissionProvider>
          <MemoryRouter>
            <ConfigPanel />
          </MemoryRouter>
        </PermissionProvider>
      </QueryClientProvider>,
    );
    expect(screen.queryByText(marker)).toBeNull();
    expect(await screen.findByRole("note")).toHaveTextContent(
      "Configuration previews require config:raw",
    );
    expect(screen.getByRole("note")).toHaveTextContent("Ask an administrator");
    expect(fetch.mock.calls).toHaveLength(1); // identity only, no comparison/source request
    expect(JSON.stringify(qc.getQueryData(["raw-config"]))).toBeUndefined();
    expect(sessionStorage.getItem(pendingDraftStorageKey)).toBeNull();
    expect(document.body.textContent).not.toContain(marker);
    expect(localStorage.length).toBe(0);
  });

  it("unmounts privileged component state and removes cached results on live permission loss", async () => {
    let raw = true;
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(json(identity(raw)))),
    );
    function ComparisonPanel() {
      const { has } = usePermission();
      const client = useQueryClient();
      const [comparison] = useState(() => {
        if (has("config:raw")) {
          client.setQueryData(["comparison"], marker);
          return marker;
        }
        return "Review requires config:raw";
      });
      return <p>{comparison}</p>;
    }
    render(
      <QueryClientProvider client={qc}>
        <PermissionProvider>
          <ComparisonPanel />
        </PermissionProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByText(marker)).toBeInTheDocument();
    raw = false;
    act(() => {
      window.dispatchEvent(new CustomEvent(FORBIDDEN_EVENT));
    });
    expect(await screen.findByText("Review requires config:raw")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(marker);
    expect(qc.getQueryData(["comparison"])).toBeUndefined();
  });

  it("never stores supplied plugin secrets or comparison artifacts, and refuses a cross-credential handoff", () => {
    authToken.set("synthetic-first-credential");
    setPendingDraft({
      kind: "patch",
      ops: [
        {
          op: "plugin_set",
          plugin_name: "auth",
          plugin: { source: "path", path: "auth.wasm", config: { credential: marker } },
        },
      ],
      previewDiff: { summary: marker },
    });
    expect(sessionStorage.getItem(pendingDraftStorageKey)).toBeNull();
    expect(JSON.stringify(Object.values(sessionStorage))).not.toContain(marker);
    authToken.set("synthetic-second-credential");
    expect(takePendingDraft()).toBeNull();
  });

  it("does not render cached children while the identity has not been verified", async () => {
    const qc = new QueryClient();
    let resolve: ((value: Response) => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((done) => {
            resolve = done;
          }),
      ),
    );
    render(
      <QueryClientProvider client={qc}>
        <PermissionProvider>
          <p>{marker}</p>
        </PermissionProvider>
      </QueryClientProvider>,
    );
    expect(screen.getByRole("status")).toHaveTextContent("Checking admin permissions");
    expect(document.body.textContent).not.toContain(marker);
    act(() => {
      resolve?.(json(identity(true)));
    });
    await waitFor(() => {
      expect(screen.getByText(marker)).toBeInTheDocument();
    });
  });

  it.each([false, true])(
    "keeps credential recovery available after an initial or later 401 (initially authenticated: %s)",
    async (initiallyAuthenticated) => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          Promise.resolve(
            url === "/api/admin/me"
              ? initiallyAuthenticated
                ? json(identity(true))
                : new Response("unauthorized", { status: 401 })
              : json({ enabled: false }),
          ),
        ),
      );
      render(
        <QueryClientProvider client={qc}>
          <AdminSession>
            <p>{marker}</p>
          </AdminSession>
        </QueryClientProvider>,
      );
      if (initiallyAuthenticated) {
        expect(await screen.findByText(marker)).toBeInTheDocument();
        act(() => {
          window.dispatchEvent(new CustomEvent("jul:unauthorized"));
        });
      }
      expect(
        await screen.findByRole("dialog", { name: "Admin token required" }),
      ).toBeInTheDocument();
      expect(document.body.textContent).not.toContain(marker);
      expect(screen.getByLabelText("Token")).toBeInTheDocument();
    },
  );

  it.each(["credential", "permission"])(
    "discards a late typed assessment after a %s change",
    async (transition) => {
      authToken.set("synthetic-original-credential");
      let resolve: ((value: Response) => void) | undefined;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) =>
          url.endsWith("pending-restart")
            ? Promise.resolve(json({ pending: false }))
            : new Promise<Response>((done) => {
                resolve = done;
              }),
        ),
      );
      const { result } = renderHook(() => useRunPatchBatch(), {
        wrapper: ({ children }) => <MemoryRouter>{children}</MemoryRouter>,
      });
      let action: Promise<void> | undefined;
      act(() => {
        action = result.current.run([{ op: "compression_set", compression: { enabled: false } }]);
      });
      await waitFor(() => {
        expect(resolve).toBeDefined();
      });
      if (transition === "credential") authToken.set("synthetic-replacement-credential");
      else invalidateAdminResponses();
      await act(async () => {
        resolve?.(json({ ok: true, summary: marker, diff: { summary: marker } }));
        await action;
      });
      expect(takePendingDraft()).toBeNull();
      expect(result.current.error?.message).toContain("Admin identity changed");
      expect(JSON.stringify(result.current)).not.toContain(marker);
    },
  );

  it("does not return an old privileged raw response after credentials change", async () => {
    authToken.set("synthetic-original-credential");
    let resolve: ((value: Response) => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () =>
          new Promise<Response>((done) => {
            resolve = done;
          }),
      ),
    );
    const raw = fetchRawConfig();
    const rejected = expect(raw).rejects.toThrow("Admin identity changed");
    authToken.set("synthetic-replacement-credential");
    resolve?.(json({ raw: marker, base_version: marker }));
    await rejected;
    expect(takePendingDraft()).toBeNull();
  });
});
