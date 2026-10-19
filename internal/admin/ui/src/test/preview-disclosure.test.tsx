/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import {
  act,
  cleanup,
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
} from "@testing-library/react";
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { useState } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PermissionProvider } from "@/auth/PermissionProvider.tsx";
import { AdminSession } from "@/app/AdminSession.tsx";
import { PermissionContext, usePermission } from "@/auth/usePermission.ts";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { TLSEditor } from "@/features/tls/TLSEditor.tsx";
import { WAFEditor } from "@/features/security/WAFEditor.tsx";
import { TrafficControlEditor } from "@/features/traffic-controls/TrafficControlEditor.tsx";
import {
  authToken,
  fetchRawConfig,
  invalidateAdminResponses,
  FORBIDDEN_EVENT,
  SecurityProjectionSchema,
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
  vi.restoreAllMocks();
  sessionStorage.clear();
  localStorage.clear();
  takePendingDraft();
});

describe("comparison disclosure boundary in the Console", () => {
  it("keeps panels hidden on identity failure and permits a successful explicit retry", async () => {
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const fetch = vi
      .fn()
      .mockRejectedValueOnce(new Error("temporary identity lookup failure"))
      .mockResolvedValueOnce(json(identity(true)));
    vi.stubGlobal("fetch", fetch);
    render(
      <QueryClientProvider client={qc}>
        <PermissionProvider>
          <p>{marker}</p>
        </PermissionProvider>
      </QueryClientProvider>,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Unable to verify admin permissions",
    );
    expect(screen.queryByText(marker)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Retry permission check" }));
    expect(await screen.findByText(marker)).toBeInTheDocument();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  it("clears privileged memory and query data when a replacement credential is verified", async () => {
    authToken.set("synthetic-original-credential");
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    let resolve: ((value: Response) => void) | undefined;
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(json(identity(true)))
        .mockImplementationOnce(
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
    expect(await screen.findByText(marker)).toBeInTheDocument();
    qc.setQueryData(["privileged-preview"], marker);
    setPendingDraft({ kind: "toml", toml: marker });
    act(() => {
      authToken.set("synthetic-replacement-credential");
    });
    expect(screen.queryByText(marker)).toBeNull();
    expect(qc.getQueryData(["privileged-preview"])).toBeUndefined();
    expect(takePendingDraft()).toBeNull();
    await waitFor(() => {
      expect(resolve).toBeDefined();
    });
    act(() => {
      resolve?.(json(identity(false)));
    });
    expect(await screen.findByText(marker)).toBeInTheDocument();
  });

  it("refuses a structured preview without raw permission before sending any request", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const { result } = renderHook(() => useRunPatchBatch(), {
      wrapper: ({ children }) => (
        <MemoryRouter>
          <PermissionContext.Provider
            value={{
              identity: identity(false),
              ready: true,
              isLoading: false,
              has: (permission) => permission !== "config:raw",
            }}
          >
            {children}
          </PermissionContext.Provider>
        </MemoryRouter>
      ),
    });
    await act(async () => {
      await result.current.run([{ op: "compression_set", compression: { enabled: false } }]);
    });
    expect(result.current.error?.message).toContain("previews require config:raw");
    expect(result.current.busy).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
    expect(takePendingDraft()).toBeNull();
  });

  it("rejects handing off a previously reviewed draft after the originating identity changes", () => {
    const draft = {
      kind: "patch" as const,
      ops: [{ op: "compression_set" as const, compression: { enabled: false } }],
      previewDiff: { summary: marker },
    };
    setPendingDraft(draft);
    const reviewed = takePendingDraft();
    if (reviewed?.kind !== "patch") throw new Error("expected patch draft");
    const { result } = renderHook(() => useRunPatchBatch(), {
      wrapper: ({ children }) => <MemoryRouter>{children}</MemoryRouter>,
    });
    invalidateAdminResponses();
    act(() => {
      result.current.handoff(reviewed);
    });
    expect(result.current.error?.message).toContain("Admin identity changed");
    expect(takePendingDraft()).toBeNull();
  });

  it.each(["TLS", "WAF"])(
    "allows same-identity %s handoff and discards a late response after permission revocation",
    async (editor) => {
      let resolve: ((value: Response) => void) | undefined;
      const fetch = vi
        .fn()
        .mockResolvedValueOnce(json({ raw: `# ${marker}` }))
        .mockImplementationOnce(
          () =>
            new Promise<Response>((done) => {
              resolve = done;
            }),
        );
      vi.stubGlobal("fetch", fetch);
      render(
        <MemoryRouter>
          {editor === "TLS" ? (
            <TLSEditor onClose={() => undefined} />
          ) : (
            <WAFEditor
              current={SecurityProjectionSchema.parse({
                auth_enabled: false,
                require_cert_count: 0,
                waf_enabled: false,
                waf_locations: 0,
                secret_refs: 0,
              })}
              onClose={() => undefined}
            />
          )}
        </MemoryRouter>,
      );
      fireEvent.click(screen.getByRole("button", { name: "Review in editor →" }));
      await waitFor(() => {
        expect(fetch).toHaveBeenCalledTimes(1);
      });
      let handedOff: ReturnType<typeof takePendingDraft> = null;
      await waitFor(() => {
        handedOff = takePendingDraft();
        expect(handedOff).toMatchObject({ kind: "toml" });
      });
      expect(JSON.stringify(handedOff)).toContain(marker);
      fireEvent.click(screen.getByRole("button", { name: "Review in editor →" }));
      await waitFor(() => {
        expect(resolve).toBeDefined();
      });
      invalidateAdminResponses();
      act(() => {
        resolve?.(json({ raw: `# ${marker}` }));
      });
      expect(
        await screen.findByText(/Could not load the current configuration to merge/),
      ).toBeInTheDocument();
      expect(takePendingDraft()).toBeNull();
    },
  );

  it("keeps a handoff in memory when session storage rejects cleanup", () => {
    vi.spyOn(Storage.prototype, "removeItem").mockImplementation(() => {
      throw new Error("storage unavailable");
    });
    setPendingDraft({ kind: "toml", toml: marker });
    expect(takePendingDraft()).toEqual({ kind: "toml", toml: marker });
    expect(takePendingDraft()).toBeNull();
  });

  it.each([false, true])(
    "guards an assessed cache handoff against permission revocation (revoked: %s)",
    async (revoked) => {
      const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
      let resolve: ((value: Response) => void) | undefined;
      vi.stubGlobal(
        "fetch",
        vi.fn((url: string) => {
          if (url === "/api/config/preview")
            return new Promise<Response>((done) => {
              resolve = done;
            });
          if (url === "/api/config")
            return Promise.resolve(json({ raw: `# ${marker}`, base_version: "cache-base" }));
          if (url.endsWith("pending-restart")) return Promise.resolve(json({ pending: false }));
          if (url.endsWith("routes")) return Promise.resolve(json([]));
          return Promise.resolve(json({}));
        }),
      );
      render(
        <QueryClientProvider client={qc}>
          <MemoryRouter>
            <TrafficControlEditor kind="cache" current={{}} onClose={() => undefined} />
          </MemoryRouter>
        </QueryClientProvider>,
      );
      fireEvent.change(screen.getByLabelText("Default TTL"), { target: { value: "90s" } });
      const review = screen.getByRole("button", { name: "Review staged change" });
      await waitFor(() => {
        expect(review).toBeEnabled();
      });
      fireEvent.click(review);
      await waitFor(() => {
        expect(resolve).toBeDefined();
      });
      if (revoked) invalidateAdminResponses();
      act(() => {
        resolve?.(
          json({
            ok: true,
            base_version: "cache-base",
            valid: true,
            diff: { summary: marker },
            lifecycle: { changes: [], can_apply_hot: false, can_stage_restart: true },
          }),
        );
      });
      if (revoked) {
        expect(await screen.findByText(/Admin identity changed/)).toBeInTheDocument();
        expect(takePendingDraft()).toBeNull();
      } else {
        let draft: ReturnType<typeof takePendingDraft> = null;
        await waitFor(() => {
          draft = takePendingDraft();
          expect(draft).toMatchObject({
            kind: "toml",
            baseVersion: "cache-base",
            recommendedAction: "stage_restart",
          });
        });
        expect(draft).toMatchObject({ previewDiff: { summary: marker } });
      }
      expect(sessionStorage.getItem(pendingDraftStorageKey)).toBeNull();
    },
  );

  it("discards a preview and handoff when its editor unmounts before the response arrives", async () => {
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
    const { result, unmount } = renderHook(() => useRunPatchBatch(), {
      wrapper: ({ children }) => <MemoryRouter>{children}</MemoryRouter>,
    });
    let action: Promise<void> | undefined;
    act(() => {
      action = result.current.run([{ op: "compression_set", compression: { enabled: false } }]);
    });
    await waitFor(() => {
      expect(resolve).toBeDefined();
    });
    unmount();
    await act(async () => {
      resolve?.(json({ ok: true, summary: marker, diff: { summary: marker } }));
      await action;
    });
    expect(takePendingDraft()).toBeNull();
  });

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
