/** Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { HistoryPanel } from "@/features/history/HistoryPanel.tsx";
import { PermissionContext } from "@/auth/usePermission.ts";
import { setPendingDraft, takePendingDraft } from "@/lib/configDraftHandoff.ts";

vi.unmock("@/api/client.ts");
vi.mock("@/features/config/CodeEditor.tsx", () => ({
  CodeEditor: ({
    value,
    onChange,
    readOnly,
  }: {
    value: string;
    onChange: (value: string) => void;
    readOnly?: boolean;
  }) => (
    <textarea
      aria-label="editor"
      value={value}
      readOnly={readOnly ?? false}
      onChange={(e) => {
        onChange(e.target.value);
      }}
    />
  ),
}));
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });
let qc: QueryClient;
type NetworkFetch = (url: string, init?: RequestInit) => Promise<Response>;
let fetchMock: ReturnType<typeof vi.fn<NetworkFetch>>;
function Scope({
  children,
  permissions = ["config:raw", "config:write", "config:apply", "history:rollback"],
  ready = true,
}: {
  children: ReactNode;
  permissions?: string[];
  ready?: boolean;
}) {
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <PermissionContext.Provider
          value={{
            ready,
            isLoading: false,
            identity: {
              principal: "test",
              role: "custom",
              token_id: "",
              legacy: false,
              permissions,
            },
            has: (p) => !ready || permissions.includes(p),
          }}
        >
          {children}
        </PermissionContext.Provider>
      </MemoryRouter>
    </QueryClientProvider>
  );
}
function network(
  override: (url: string, init: RequestInit) => Response | Promise<Response> | undefined,
) {
  fetchMock = vi.fn((url: string, init: RequestInit = {}) => {
    const response = override(url, init);
    if (response !== undefined) return Promise.resolve(response);
    if (url === "/api/config")
      return Promise.resolve(json({ raw: "# baseline", base_version: "v1" }));
    if (url === "/api/config/pending-restart") return Promise.resolve(json({ pending: false }));
    if (url === "/api/config/validate") return Promise.resolve(json({ ok: true }));
    if (url === "/api/config/diff") return Promise.resolve(json({ summary: "one edit" }));
    if (url === "/api/config/history")
      return Promise.resolve(json([{ id: "snap-1", time: "2026-10-13T00:00:00Z", size: 2048 }]));
    if (url === "/api/config/history/snap-1/diff")
      return Promise.resolve(json({ summary: "restore snapshot", base_version: "v1" }));
    if (url === "/api/config/history/snap-1")
      return Promise.resolve(json({ id: "snap-1", raw: "# saved snapshot" }));
    throw new Error(`Unexpected network request: ${url}`);
  });
  vi.stubGlobal("fetch", fetchMock);
}
const mutations = (fragment: string) =>
  fetchMock.mock.calls.filter(([url]) => url.includes(fragment));
async function edit() {
  fireEvent.change(await screen.findByLabelText("editor"), { target: { value: "# changed" } });
  const apply = screen.getByRole("button", { name: "Apply live" });
  await waitFor(() => {
    expect(apply).toBeEnabled();
  });
  fireEvent.click(apply);
}
async function confirm(label = "Apply live") {
  const dialog = await screen.findByRole("dialog");
  fireEvent.click(within(dialog).getByRole("button", { name: label }));
}
beforeEach(() => {
  qc = new QueryClient({
    defaultOptions: {
      queries: { retry: false, refetchInterval: false },
      mutations: { retry: false },
    },
  });
  sessionStorage.clear();
  takePendingDraft();
});
afterEach(() => {
  cleanup();
  qc.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  sessionStorage.clear();
  takePendingDraft();
});

describe("open confirmations follow the current permission grants", () => {
  it("blocks a prepared rollback after revocation and requires an explicit click after recovery", async () => {
    network((url) =>
      url === "/api/config/rollback"
        ? json({ ok: true, reload: { outcome: "no_change" } })
        : undefined,
    );
    const view = render(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Roll back" });
    await waitFor(() => {
      expect(submit).toBeEnabled();
    });
    view.rerender(
      <Scope permissions={["config:read"]}>
        <HistoryPanel />
      </Scope>,
    );
    expect(submit).toBeDisabled();
    expect(
      within(dialog).getByText("Requires the history:rollback permission."),
    ).toBeInTheDocument();
    fireEvent.click(submit);
    expect(mutations("/config/rollback")).toHaveLength(0);
    view.rerender(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    expect(submit).toBeEnabled();
    expect(mutations("/config/rollback")).toHaveLength(0);
    fireEvent.click(submit);
    await waitFor(() => {
      expect(mutations("/config/rollback")).toHaveLength(1);
    });
    expect(JSON.parse(mutations("/config/rollback")[0]?.[1]?.body as string)).toEqual({
      id: "snap-1",
      base_version: "v1",
    });
  });

  it("keeps cancellation available after permission loss", async () => {
    network(() => undefined);
    const view = render(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    await screen.findByRole("dialog");
    view.rerender(
      <Scope permissions={[]}>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(mutations("/config/rollback")).toHaveLength(0);
  });

  it("preserves unknown-identity gating and truthful server rejection", async () => {
    network((url) => (url === "/api/config/rollback" ? json({ error: "denied" }, 403) : undefined));
    render(
      <Scope ready={false} permissions={[]}>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    const dialog = await screen.findByRole("dialog");
    const submit = within(dialog).getByRole("button", { name: "Roll back" });
    await waitFor(() => {
      expect(submit).toBeEnabled();
    });
    fireEvent.click(submit);
    expect((await screen.findAllByText("HTTP 403")).length).toBeGreaterThan(0);
    expect(mutations("/config/rollback")).toHaveLength(1);
    expect(screen.queryByText("Rolled back successfully")).toBeNull();
  });

  it.each([
    ["raw live", false, false],
    ["structured live", true, false],
    ["raw staged", false, true],
    ["structured staged", true, true],
  ] as const)(
    "blocks %s confirmation after config:apply is revoked",
    async (_name, patch, stage) => {
      const grants = patch
        ? ["config:write", "config:apply"]
        : ["config:raw", "config:write", "config:apply"];
      const label = stage ? "Save for next restart" : "Apply live";
      const endpoint = patch ? "/api/config/patch/apply" : "/api/config/apply";
      network((url) =>
        url.startsWith(endpoint) ? json({ error: "server denies this request" }, 403) : undefined,
      );
      if (patch || stage) {
        const assessment = {
          baseVersion: "v1",
          previewDiff: { summary: "reviewed edit" },
          lifecycle: {
            changes: [],
            can_apply_hot: !stage,
            can_stage_restart: true,
            hot_paths: stage ? [] : ["servers"],
            restart_required_paths: stage ? ["global.config_authority"] : [],
            new_listener_only_paths: [],
            ignored_deprecated_paths: [],
            validation_rejected_paths: [],
            pending_subsystems: stage ? ["runtime"] : [],
          },
          recommendedAction: stage ? ("stage_restart" as const) : ("hot" as const),
          pendingRestart: { state: "none" as const, subsystems: [] },
        };
        if (patch)
          setPendingDraft({
            ...assessment,
            kind: "patch",
            ops: [{ op: "server_add", listen: ":9090" }],
            valid: true,
            validationErrors: [],
            candidateState: "hidden",
            requiresFreshPreview: false,
          });
        else
          setPendingDraft({
            ...assessment,
            recommendedAction: "stage_restart",
            kind: "toml",
            toml: "# changed",
            candidateState: "memory_only",
          });
      }
      const view = render(
        <Scope permissions={grants}>
          <ConfigPanel />
        </Scope>,
      );
      if (!patch && !stage) await edit();
      else {
        const apply = await screen.findByRole("button", { name: label });
        await waitFor(() => {
          expect(apply).toBeEnabled();
        });
        fireEvent.click(apply);
      }
      const dialog = await screen.findByRole("dialog");
      const submit = within(dialog).getByRole("button", { name: label });
      expect(submit).toBeEnabled();
      view.rerender(
        <Scope permissions={grants.filter((p) => p !== "config:apply")}>
          <ConfigPanel />
        </Scope>,
      );
      expect(submit).toBeDisabled();
      expect(within(dialog).getByText("Requires the config:apply permission.")).toBeInTheDocument();
      fireEvent.click(submit);
      expect(mutations(endpoint)).toHaveLength(0);
      view.rerender(
        <Scope permissions={grants}>
          <ConfigPanel />
        </Scope>,
      );
      expect(submit).toBeEnabled();
      expect(mutations(endpoint)).toHaveLength(0);
      fireEvent.click(submit);
      await waitFor(() => {
        expect(mutations(endpoint)).toHaveLength(1);
      });
      const [url, init] = mutations(endpoint)[0] ?? [];
      if (stage) expect(url).toContain("mode=stage_restart");
      else expect(url).toBe(patch ? endpoint : "/api/config/apply?base_version=v1");
      if (patch)
        expect(JSON.parse(init?.body as string)).toMatchObject({
          base_version: "v1",
          ops: [{ op: "server_add", listen: ":9090" }],
        });
      else expect(init?.body).toBe("# changed");
      expect((await screen.findAllByText(stage ? "HTTP 403" : "Apply failed.")).length).toBeGreaterThan(0);
      expect(mutations(endpoint)).toHaveLength(1);
    },
  );
});

describe("raw configuration recovery preserves reviewed operation identity", () => {
  it("rejects a stale base, then reloads authoritative bytes without resending the mutation", async () => {
    let conflicted = false;
    network((url) => {
      if (url === "/api/config" && conflicted)
        return json({ raw: "# authoritative v2", base_version: "v2" });
      if (url.startsWith("/api/config/apply")) {
        conflicted = true;
        return json({ conflict: true, current_version: "v2" }, 409);
      }
      return undefined;
    });
    render(
      <Scope>
        <ConfigPanel />
      </Scope>,
    );
    await edit();
    await confirm();
    const reload = await screen.findByRole("button", { name: "Reload latest config" });
    expect(mutations("/config/apply")).toHaveLength(1);
    expect(mutations("/config/apply")[0]?.[0]).toBe("/api/config/apply?base_version=v1");
    fireEvent.click(reload);
    await waitFor(() => {
      expect(screen.getByLabelText("editor")).toHaveValue("# authoritative v2");
    });
    expect(mutations("/config/apply")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Apply live" })).toBeDisabled();
  });

  it("reconfirms the same raw admin-affecting operation; reset drops dirty text and confirmation", async () => {
    network((url) => {
      if (url === "/api/config/apply?base_version=v1")
        return json({ admin_change: true, changes: ["admin.token"] }, 409);
      if (url === "/api/config/apply?base_version=v1&confirm_admin=true")
        return json({
          ok: true,
          version: "v2",
          pending_reload: false,
          reload: { outcome: "no_change" },
        });
      return undefined;
    });
    render(
      <Scope>
        <ConfigPanel />
      </Scope>,
    );
    await edit();
    await confirm();
    expect(await screen.findByText("admin.token")).toBeInTheDocument();
    expect(mutations("/config/apply")).toHaveLength(1);
    await confirm();
    expect(await screen.findByText("Configuration already effective")).toBeInTheDocument();
    expect(mutations("/config/apply")).toHaveLength(2);
    fireEvent.change(screen.getByLabelText("editor"), { target: { value: "# another edit" } });
    fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect(screen.getByLabelText("editor")).toHaveValue("# changed");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(mutations("/config/apply")).toHaveLength(2);
  });

  it("canceling confirmation emits no mutation and reset discards the unsaved draft", async () => {
    network(() => undefined);
    render(
      <Scope>
        <ConfigPanel />
      </Scope>,
    );
    await edit();
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Cancel" }));
    expect(mutations("/config/apply")).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Reset" }));
    expect(screen.getByLabelText("editor")).toHaveValue("# baseline");
  });

  it("failed staged discard stays visible and a successful retry refreshes pending state", async () => {
    let discarded = false;
    let attempts = 0;
    network((url) => {
      if (url === "/api/config/pending-restart")
        return json(
          discarded
            ? { pending: false }
            : {
                pending: true,
                status: {
                  state: "managed_staged",
                  managed: true,
                  staged: true,
                  discard_available: true,
                  inconsistent: false,
                  staged_version: "v2",
                  serving_version: "v1",
                  subsystems: ["runtime"],
                },
              },
        );
      if (url.endsWith("/pending-restart/discard")) {
        attempts++;
        if (attempts === 1) return json({ error: "disk changed" }, 409);
        discarded = true;
        return json({ ok: true });
      }
      return undefined;
    });
    render(
      <Scope>
        <ConfigPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Discard staged configuration" }));
    expect(await screen.findByText("Discard failed: disk changed")).toBeInTheDocument();
    expect(screen.getByText("Restart required — configuration staged")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Discard staged configuration" }));
    await waitFor(() => {
      expect(screen.queryByText("Restart required — configuration staged")).toBeNull();
    });
    expect(mutations("/pending-restart/discard")).toHaveLength(2);
  });
});

describe("history identity, permission and recovery boundaries", () => {
  it("revoking rollback permission disables the action and cannot emit a mutation", async () => {
    network(() => undefined);
    const view = render(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    const rollback = await screen.findByRole("button", { name: "Rollback" });
    expect(rollback).toBeEnabled();
    view.rerender(
      <Scope permissions={["config:read"]}>
        <HistoryPanel />
      </Scope>,
    );
    expect(rollback).toBeDisabled();
    fireEvent.click(rollback);
    expect(mutations("/config/rollback")).toHaveLength(0);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("views the selected snapshot and closing it emits no rollback", async () => {
    network(() => undefined);
    render(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "View" }));
    expect(await screen.findByText("# saved snapshot")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "✕" }));
    expect(screen.queryByText("# saved snapshot")).toBeNull();
    expect(mutations("/config/rollback")).toHaveLength(0);
  });

  it("server 403 keeps the rollback dialog truthful; cancel never retries", async () => {
    network((url) =>
      url === "/api/config/rollback" ? json({ error: "permission revoked" }, 403) : undefined,
    );
    render(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Roll back" })).toBeEnabled();
    });
    await confirm("Roll back");
    expect(await screen.findByText("HTTP 403")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(JSON.parse(mutations("/config/rollback")[0]?.[1]?.body as string)).toEqual({
      id: "snap-1",
      base_version: "v1",
    });
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(mutations("/config/rollback")).toHaveLength(1);
  });

  it("a pending rollback blocks double submission and success refreshes the history", async () => {
    let complete: ((response: Response) => void) | undefined;
    network((url) =>
      url === "/api/config/rollback"
        ? new Promise<Response>((resolve) => {
            complete = resolve;
          })
        : undefined,
    );
    const view = render(
      <Scope>
        <HistoryPanel />
      </Scope>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Roll back" })).toBeEnabled();
    });
    await confirm("Roll back");
    await waitFor(() => {
      expect(
        within(screen.getByRole("dialog")).getByRole("button", { name: /Working|Roll back/ }),
      ).toBeDisabled();
    });
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: /Working|Roll back/ }),
    );
    expect(mutations("/config/rollback")).toHaveLength(1);
    view.rerender(<Scope permissions={[]}><HistoryPanel /></Scope>);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Cancel" })).toBeDisabled();
    await act(async () => {
      complete?.(json({ ok: true, reload: { outcome: "applied_live" } }));
      await Promise.resolve();
    });
    await waitFor(() => {
      expect(screen.queryByRole("dialog")).toBeNull();
    });
    expect(
      mutations("/config/history").filter(([url]) => url === "/api/config/history").length,
    ).toBeGreaterThan(1);
  });
  it.each(["unverifiable", "different_context"])("explains %s effective versions without offering discard", async (comparison) => {
    network((url) => url === "/api/config/pending-restart" ? json({ pending: true, status: {
      state: "managed_staged", managed: true, staged: true, discard_available: false,
      inconsistent: false, subsystems: ["cache"], version_comparison: comparison,
    } }) : undefined);
    render(<Scope><ConfigPanel /></Scope>);
    expect(await screen.findByText(/restore the original version key/i)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Discard staged configuration" })).toBeNull();
  });

});
