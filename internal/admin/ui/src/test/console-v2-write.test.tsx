/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the Console v2 write flows. The CodeMirror editor is
 * mocked to a plain textarea so these tests stay fast and deterministic and
 * exercise the panel logic (validate-on-edit, diff preview, and the mandatory
 * apply/rollback confirmation gates) rather than the editor internals.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

vi.mock("@/features/config/CodeEditor.tsx", () => ({
  CodeEditor: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <textarea
      aria-label="editor"
      value={value}
      onChange={(e) => {
        onChange(e.target.value);
      }}
    />
  ),
}));

import { ConfigPanel } from "@/features/config/ConfigPanel.tsx";
import { HistoryPanel } from "@/features/history/HistoryPanel.tsx";
import { WizardPanel } from "@/features/wizard/WizardPanel.tsx";
import { DiffView } from "@/features/config/DiffView.tsx";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { takePendingDraft, setPendingDraft } from "@/lib/configDraftHandoff.ts";

const realFetch = globalThis.fetch;

function Wrapper({ children }: { readonly children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return (
    <QueryClientProvider client={qc}>
      <MemoryRouter>{children}</MemoryRouter>
    </QueryClientProvider>
  );
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

interface Counters {
  apply: number;
  rollback: number;
}

function installRouter(): Counters {
  const counters: Counters = { apply: 0, rollback: 0 };
  globalThis.fetch = vi.fn((input: string) => {
    const url = input;
    if (url === "/api/config") {
      return Promise.resolve(json({ raw: 'listen = ":8443"\n', path: "/etc/jul.toml" }));
    }
    if (url === "/api/config/validate") {
      return Promise.resolve(json({ ok: true, message: "Configuration is valid." }));
    }
    if (url === "/api/config/diff") {
      return Promise.resolve(
        json({ summary: "1 change", additions: [{ kind: "listener", name: ":9000" }] }),
      );
    }
    if (url === "/api/config/apply") {
      counters.apply += 1;
      return Promise.resolve(
        json({ ok: true, status: [{ group: "Traffic", name: "TLS", active: true }] }),
      );
    }
    if (url === "/api/runtime/overview") {
      // The panel polls the overview after an accepted apply to observe the
      // async stream-reload outcome; a clean snapshot settles it to fully live.
      return Promise.resolve(json({ product: "jul", version: "1", status: [] }));
    }
    if (url === "/api/config/history") {
      return Promise.resolve(json([{ id: "s1", time: "2026-01-01T00:00:00Z", size: 120 }]));
    }
    if (url === "/api/config/history/s1") {
      return Promise.resolve(json({ id: "s1", raw: 'listen = ":80"\n' }));
    }
    if (url === "/api/config/rollback") {
      counters.rollback += 1;
      return Promise.resolve(json({ status: "rolled back", id: "s1" }));
    }
    if (url === "/api/wizard/generate") {
      return Promise.resolve(json({ toml: 'listen = ":80"\n' }));
    }
    if (url === "/api/wizard/generate?format=patch") {
      return Promise.resolve(
        json({
          ops: [
            { op: "server_add", listen: ":80" },
            {
              op: "location_add",
              listen: ":80",
              match_set: { type: "prefix", path: "/" },
              action: { kind: "static", target: "/srv/site" },
            },
          ],
        }),
      );
    }
    if (url === "/api/config/patch/preview") {
      return Promise.resolve(
        json({
          ok: true,
          summary: "server :80 added; route / added",
          candidate: 'listen = ":80"\n',
          diff: { summary: "1 change", additions: [{ kind: "server", name: ":80" }] },
          base_version: "base",
        }),
      );
    }
    throw new Error(`unexpected fetch: ${url}`);
  }) as unknown as typeof fetch;
  return counters;
}

beforeEach(() => {
  installRouter();
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("DiffView", () => {
  it("renders summary and grouped changes", () => {
    render(
      <DiffView
        diff={{
          summary: "2 changes",
          additions: [{ kind: "listener", name: ":443" }],
          removals: [{ kind: "upstream", name: "old" }],
        }}
      />,
    );
    expect(screen.getByText("2 changes")).toBeInTheDocument();
    expect(screen.getByText(/Added/)).toBeInTheDocument();
    expect(screen.getByText(/Removed/)).toBeInTheDocument();
  });

  it("shows an empty state when nothing changed", () => {
    render(<DiffView diff={{ summary: "no changes" }} />);
    expect(screen.getByText(/No structural changes/)).toBeInTheDocument();
  });
});

describe("ConfirmDialog", () => {
  it("invokes confirm and cancel callbacks", () => {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog title="Sure?" confirmLabel="Do it" onConfirm={onConfirm} onCancel={onCancel}>
        body
      </ConfirmDialog>,
    );
    fireEvent.click(screen.getByText("Do it"));
    expect(onConfirm).toHaveBeenCalledOnce();
    fireEvent.click(screen.getByText("Cancel"));
    expect(onCancel).toHaveBeenCalledOnce();
  });
});

describe("ConfigPanel apply flow", () => {
  it("requires explicit confirmation before applying", async () => {
    const counters = installRouter();
    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );

    const editor = await screen.findByLabelText<HTMLTextAreaElement>("editor");
    expect(editor.value).toContain('listen = ":8443"');

    // Edit → becomes dirty → validates → Apply enabled.
    fireEvent.change(editor, { target: { value: 'listen = ":9000"\n' } });
    const applyBtn = await screen.findByRole("button", { name: "Apply changes" });
    await waitFor(() => {
      expect(applyBtn).toBeEnabled();
    });

    // Opening the confirm dialog must NOT apply yet.
    fireEvent.click(applyBtn);
    expect(await screen.findByText("Apply configuration?")).toBeInTheDocument();
    expect(counters.apply).toBe(0);

    // Confirm → apply happens, outcome banner shown.
    fireEvent.click(screen.getByRole("button", { name: "Apply now" }));
    await waitFor(() => {
      expect(counters.apply).toBe(1);
    });
    expect(await screen.findByText("Applied -' runtime reloading")).toBeInTheDocument();
  });

  it("shows an apply-progress spinner while the apply request is in flight", async () => {
    let resolveApply: (r: Response) => void = () => {
      /* set below */
    };
    const applyInFlight = new Promise<Response>((res) => {
      resolveApply = res;
    });
    let applied = 0;
    globalThis.fetch = vi.fn((input: string) => {
      const url = input;
      if (url === "/api/config") {
        return Promise.resolve(json({ raw: 'listen = ":8443"\n', path: "/etc/jul.toml" }));
      }
      if (url === "/api/config/validate") {
        return Promise.resolve(json({ ok: true, message: "Configuration is valid." }));
      }
      if (url === "/api/config/diff") {
        return Promise.resolve(
          json({ summary: "1 change", additions: [{ kind: "listener", name: ":9000" }] }),
        );
      }
      if (url === "/api/config/apply") {
        applied += 1;
        return applyInFlight;
      }
      if (url === "/api/runtime/overview") {
        // The panel polls the overview after an accepted apply to learn the
        // async stream-reload outcome; a clean snapshot settles it to fully live.
        return Promise.resolve(json({ product: "jul", version: "1", status: [] }));
      }
      throw new Error(`unexpected fetch: ${url}`);
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );

    const editor = await screen.findByLabelText<HTMLTextAreaElement>("editor");
    fireEvent.change(editor, { target: { value: 'listen = ":9000"\n' } });
    const applyBtn = await screen.findByRole("button", { name: "Apply changes" });
    await waitFor(() => {
      expect(applyBtn).toBeEnabled();
    });
    fireEvent.click(applyBtn);
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));

    // While the request is pending the button reports progress.
    expect(await screen.findByRole("button", { name: "Applying…" })).toBeInTheDocument();

    // Resolve and let the panel settle so no state updates leak past the test.
    resolveApply(json({ ok: true, status: [] }));
    await waitFor(() => {
      expect(applied).toBe(1);
    });
    await screen.findByText("Applied -' runtime reloading");
  });

  it("renders structured validation issues with their config path", async () => {
    globalThis.fetch = vi.fn((input: string) => {
      const url = input;
      if (url === "/api/config") {
        return Promise.resolve(json({ raw: 'listen = ":8443"\n', path: "/etc/jul.toml" }));
      }
      if (url === "/api/config/validate") {
        return Promise.resolve(
          json({
            ok: false,
            message: "The draft configuration is invalid.",
            errors: [
              {
                code: "unknown_upstream",
                path: "servers[0].locations[1]",
                summary: "Upstream reference points to a pool that does not exist.",
                detail: "Create the upstream in the config or choose an existing one.",
                severity: "error",
              },
            ],
          }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );

    const editor = await screen.findByLabelText<HTMLTextAreaElement>("editor");
    fireEvent.change(editor, { target: { value: 'listen = ":9000"\n' } });

    // The config path is surfaced as a chip in front of the human summary.
    expect(await screen.findByText("servers[0].locations[1]")).toBeInTheDocument();
    expect(
      screen.getByText(/Upstream reference points to a pool that does not exist\./),
    ).toBeInTheDocument();

    // An invalid draft must keep Apply disabled.
    expect(screen.getByRole("button", { name: "Apply changes" })).toBeDisabled();
  });

  it("reconciles the raw editor after an atomic patch apply", async () => {
    takePendingDraft(); // clear any leftover handoff state
    let patchApplies = 0;
    let rawApplyBaseVersion: string | null = null;
    let configReads = 0;
    globalThis.fetch = vi.fn((input: string) => {
      const url = input;
      if (url === "/api/config") {
        configReads += 1;
        return Promise.resolve(
          json({
            raw: configReads === 1 ? 'listen = ":8443"\n' : 'listen = ":9000"\n',
            path: "/etc/jul.toml",
            base_version: configReads === 1 ? "v1" : "v2",
          }),
        );
      }
      if (url === "/api/config/patch/apply") {
        patchApplies += 1;
        return Promise.resolve(
          json({
            ok: true,
            version: "v2",
            summary: ["1 change"],
            diff: { summary: "1 change", additions: [{ kind: "listener", name: ":9000" }] },
            status: [{ group: "Traffic", name: "TLS", active: true }],
          }),
        );
      }
      if (url.startsWith("/api/config/apply")) {
        rawApplyBaseVersion = new URL(url, "http://x").searchParams.get("base_version");
        return Promise.resolve(json({ ok: true, status: [], version: "v3" }));
      }
      if (url === "/api/config/validate") {
        return Promise.resolve(json({ ok: true, message: "Configuration is valid." }));
      }
      if (url === "/api/config/diff") {
        return Promise.resolve(json({ summary: "1 change" }));
      }
      throw new Error(`unexpected fetch: ${url}`);
    }) as unknown as typeof fetch;

    setPendingDraft({
      kind: "patch",
      ops: [{ op: "server_toggle_http3", listen: ":9000", enabled: true }],
      baseVersion: "v1",
      previewDiff: { summary: "1 change", additions: [{ kind: "listener", name: ":9000" }] },
      candidate: 'listen = ":9000"\n',
    });

    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );

    // The panel lands in patch mode showing the read-only candidate.
    const editor = await screen.findByLabelText<HTMLTextAreaElement>("editor");
    expect(editor.value).toContain('listen = ":9000"');
    const patchBtn = await screen.findByRole("button", { name: "Apply patch" });
    await waitFor(() => {
      expect(patchBtn).toBeEnabled();
    });

    // Apply the patch through the confirm gate.
    fireEvent.click(patchBtn);
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));
    await waitFor(() => {
      expect(patchApplies).toBe(1);
    });

    // Patch mode is exited only after fetching the authoritative persisted raw
    // configuration, so a fresh "Apply changes" button is disabled.
    const rawBtn = await screen.findByRole("button", { name: "Apply changes" });
    await waitFor(() => {
      expect(rawBtn).toBeDisabled();
      expect(editor.value).toContain('listen = ":9000"');
    });

    // A follow-up raw edit applies with the *new* base_version (v2) returned by
    // the patch apply — not the stale v1 — so there is no spurious 409.
    fireEvent.change(editor, { target: { value: 'listen = ":9001"\n' } });
    await waitFor(() => {
      expect(rawBtn).toBeEnabled();
    });
    fireEvent.click(rawBtn);
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));
    await waitFor(() => {
      expect(rawApplyBaseVersion).toBe("v2");
    });
  });

  it("allows structured patch review when raw config is forbidden", async () => {
    takePendingDraft(); // clear any leftover handoff state
    let patchApplies = 0;
    globalThis.fetch = vi.fn((input: string) => {
      const url = input;
      if (url === "/api/config") {
        // Operator lacks config:raw.
        return Promise.resolve(json({ error: "forbidden" }, 403));
      }
      if (url === "/api/config/patch/apply") {
        patchApplies += 1;
        return Promise.resolve(
          json({
            ok: true,
            version: "v2",
            summary: ["1 change"],
            diff: { summary: "1 change", additions: [{ kind: "listener", name: ":9000" }] },
            status: [{ group: "Traffic", name: "TLS", active: true }],
          }),
        );
      }
      throw new Error(`unexpected fetch: ${url}`);
    }) as unknown as typeof fetch;

    setPendingDraft({
      kind: "patch",
      ops: [{ op: "server_toggle_http3", listen: ":9000", enabled: true }],
      baseVersion: "v1",
      previewDiff: { summary: "1 change", additions: [{ kind: "listener", name: ":9000" }] },
      // No candidate: the backend omits it for operators without config:raw.
    });

    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );

    // The raw editor is replaced by a permission notice; the diff is still shown.
    expect(await screen.findByText(/Raw configuration preview is hidden/)).toBeInTheDocument();
    expect(screen.getByText(/1 change/)).toBeInTheDocument();

    const patchBtn = await screen.findByRole("button", { name: "Apply patch" });
    await waitFor(() => {
      expect(patchBtn).toBeEnabled();
    });

    fireEvent.click(patchBtn);
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));
    await waitFor(() => {
      expect(patchApplies).toBe(1);
    });
  });

  it("blocks hot apply and shows a banner when external disk divergence is reported", async () => {
    globalThis.fetch = vi.fn((input: string) => {
      const url = input;
      if (url === "/api/config") {
        return Promise.resolve(json({ raw: 'listen = ":8443"\n', path: "/etc/jul.toml" }));
      }
      if (url === "/api/config/pending-restart") {
        return Promise.resolve(
          json({
            pending: true,
            status: {
              state: "external_divergence",
              managed: false,
              staged: false,
              external: true,
              subsystems: ["listener"],
              discard_available: false,
              inconsistent: false,
            },
          }),
        );
      }
      if (url === "/api/config/validate") {
        return Promise.resolve(json({ ok: true, message: "Configuration is valid." }));
      }
      throw new Error(`unexpected fetch: ${url}`);
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );

    const editor = await screen.findByLabelText<HTMLTextAreaElement>("editor");
    fireEvent.change(editor, { target: { value: 'listen = ":9000"\n' } });

    // The external-divergence banner is shown.
    expect(
      await screen.findByText(/Configuration on disk differs from runtime/),
    ).toBeInTheDocument();
    expect(screen.getByText(/listener/)).toBeInTheDocument();

    // Apply is disabled because hot applies are blocked by external divergence.
    const applyBtn = await screen.findByRole("button", { name: "Apply changes" });
    await waitFor(() => {
      expect(applyBtn).toBeDisabled();
    });
  });

  it("retries an admin-confirmed structured patch as the same patch operation", async () => {
    takePendingDraft();
    const urls: string[] = [];
    globalThis.fetch = vi.fn((input: string) => {
      urls.push(input);
      if (input === "/api/config")
        return Promise.resolve(json({ raw: 'listen = ":8443"\n', base_version: "v1" }));
      if (
        input === "/api/config/patch/apply" &&
        urls.filter((u) => u.includes("patch/apply")).length === 1
      ) {
        return Promise.resolve(
          json(
            { ok: false, admin_change: true, message: "confirm", changes: ["admin token changes"] },
            409,
          ),
        );
      }
      if (input === "/api/config/patch/apply?confirm_admin=true") {
        return Promise.resolve(
          json({
            ok: true,
            mode: "hot",
            version: "v2",
            summary: [],
            diff: { summary: "done" },
            reload: { id: "rl_1", outcome: "applied_live", published: true },
          }),
        );
      }
      throw new Error(`unexpected fetch: ${input}`);
    }) as unknown as typeof fetch;
    setPendingDraft({
      kind: "patch",
      ops: [{ op: "server_toggle_http3", listen: ":8443", enabled: true }],
      baseVersion: "v1",
      previewDiff: { summary: "admin patch" },
      candidate: 'listen = ":8443"\n',
    });
    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Apply patch" }));
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));
    expect(await screen.findByText("Confirm admin access change?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Apply and change admin access" }));
    await waitFor(() => {
      expect(urls).toContain("/api/config/patch/apply?confirm_admin=true");
    });
    expect(urls.filter((url) => url.includes("/api/config/apply"))).toHaveLength(0);
  });

  it("stages a restart-required patch without raw config access", async () => {
    takePendingDraft();
    const urls: string[] = [];
    globalThis.fetch = vi.fn((input: string) => {
      urls.push(input);
      if (input === "/api/config") return Promise.resolve(json({ error: "forbidden" }, 403));
      if (input === "/api/config/patch/apply") {
        return Promise.resolve(
          json(
            {
              ok: false,
              restart_required: true,
              can_stage: true,
              message: "restart",
              pending_restart: {
                state: "none",
                managed: false,
                staged: false,
                subsystems: ["global"],
                discard_available: false,
                inconsistent: false,
              },
            },
            409,
          ),
        );
      }
      if (input === "/api/config/patch/apply?mode=stage_restart") {
        return Promise.resolve(
          json({
            ok: true,
            mode: "stage_restart",
            version: "v2",
            summary: [],
            diff: { summary: "staged" },
            staged_restart_is_update: false,
          }),
        );
      }
      throw new Error(`unexpected fetch: ${input}`);
    }) as unknown as typeof fetch;
    setPendingDraft({
      kind: "patch",
      ops: [{ op: "server_toggle_http3", listen: ":8443", enabled: true }],
      baseVersion: "v1",
      previewDiff: { summary: "restart patch" },
    });
    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Apply patch" }));
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));
    const offer = await screen.findByText("Save for next restart?");
    const offerBox = offer.parentElement;
    const offerButton = offerBox?.querySelector("button");
    if (!offerButton) throw new Error("stage offer button missing");
    fireEvent.click(offerButton);
    const dialog = await screen.findByRole("dialog", { name: "Save for next restart?" });
    const confirm = dialog.querySelector("button.bg-jul-accent");
    if (!confirm) throw new Error("stage confirmation button missing");
    fireEvent.click(confirm);
    await waitFor(() => {
      expect(urls).toContain("/api/config/patch/apply?mode=stage_restart");
    });
    expect(urls.some((url) => url.startsWith("/api/config/apply"))).toBe(false);
  });

  it("correlates saved-not-live only to the matching apply id and refreshes restored config", async () => {
    let overviewReads = 0;
    let configReads = 0;
    globalThis.fetch = vi.fn((input: string) => {
      if (input === "/api/config") {
        configReads += 1;
        return Promise.resolve(
          json({
            raw: configReads === 1 ? 'listen = ":8443"\n' : 'listen = ":restored"\n',
            base_version: configReads === 1 ? "v1" : "v-restored",
          }),
        );
      }
      if (input === "/api/config/validate") return Promise.resolve(json({ ok: true }));
      if (input === "/api/config/diff") return Promise.resolve(json({ summary: "change" }));
      if (input === "/api/config/apply?base_version=v1") {
        return Promise.resolve(
          json(
            {
              ok: true,
              apply_id: "rl_7",
              mode: "hot",
              version: "v2",
              persisted: true,
              reload: {
                id: "rl_7",
                outcome: "saved_not_live",
                persisted: true,
                timed_out: true,
                http: { status: "" },
                stream: { status: "" },
                admin: { status: "" },
              },
            },
            202,
          ),
        );
      }
      if (input === "/api/runtime/overview") {
        overviewReads += 1;
        if (overviewReads === 1) {
          return Promise.resolve(
            json({
              product: "jul",
              version: "1",
              status: [],
              last_managed_apply: {
                id: "rl_99",
                mode: "hot",
                ok: true,
                outcome: "applied_live",
                completed_at: "2026-01-01T00:00:00Z",
              },
            }),
          );
        }
        return Promise.resolve(
          json({
            product: "jul",
            version: "1",
            status: [],
            last_managed_apply: {
              id: "rl_7",
              mode: "hot",
              ok: false,
              outcome: "not_applied",
              restored: true,
              final_disk_version: "v-restored",
              final_serving_version: "live-v1",
              completed_at: "2026-01-01T00:00:01Z",
            },
            last_reload: {
              id: "rl_7",
              outcome: "not_applied",
              failed_phase: "prepare",
              error: "build failed",
            },
          }),
        );
      }
      throw new Error(`unexpected fetch: ${input}`);
    }) as unknown as typeof fetch;
    render(
      <Wrapper>
        <ConfigPanel />
      </Wrapper>,
    );
    const editor = await screen.findByLabelText<HTMLTextAreaElement>("editor");
    fireEvent.change(editor, { target: { value: 'listen = ":9000"\n' } });
    const applyButton = await screen.findByRole("button", { name: "Apply changes" });
    await waitFor(() => {
      expect(applyButton).toBeEnabled();
    });
    fireEvent.click(applyButton);
    fireEvent.click(await screen.findByRole("button", { name: "Apply now" }));
    expect(await screen.findByText("Saved — final outcome pending")).toBeInTheDocument();
    await waitFor(
      () => {
        expect(overviewReads).toBeGreaterThanOrEqual(2);
      },
      { timeout: 4000 },
    );
    expect(
      await screen.findByText("Apply rejected — previous configuration restored"),
    ).toBeInTheDocument();
    await waitFor(() => {
      expect(editor.value).toContain(":restored");
    });
  });
});

describe("HistoryPanel rollback flow", () => {
  it("requires explicit confirmation before rolling back", async () => {
    const counters = installRouter();
    render(
      <Wrapper>
        <HistoryPanel />
      </Wrapper>,
    );

    const rollbackBtn = await screen.findByRole("button", { name: "Rollback" });
    fireEvent.click(rollbackBtn);

    expect(await screen.findByText("Roll back to this snapshot?")).toBeInTheDocument();
    expect(counters.rollback).toBe(0);

    fireEvent.click(screen.getByRole("button", { name: "Roll back" }));
    await waitFor(() => {
      expect(counters.rollback).toBe(1);
    });
  });

  it("keeps rollback open and retries the same snapshot after admin confirmation", async () => {
    let rollbacks = 0;
    const urls: string[] = [];
    globalThis.fetch = vi.fn((input: string) => {
      urls.push(input);
      if (input === "/api/config/history")
        return Promise.resolve(json([{ id: "s1", time: "2026-01-01T00:00:00Z", size: 120 }]));
      if (input === "/api/config/history/s1")
        return Promise.resolve(json({ id: "s1", raw: 'listen = ":80"\n' }));
      if (input === "/api/config/diff") return Promise.resolve(json({ summary: "admin change" }));
      if (input.startsWith("/api/config/rollback")) {
        rollbacks += 1;
        if (rollbacks === 1)
          return Promise.resolve(
            json(
              {
                ok: false,
                admin_change: true,
                message: "confirm rollback",
                changes: ["admin token changes"],
              },
              409,
            ),
          );
        return Promise.resolve(json({ ok: true, mode: "hot", status: "rolled back", id: "s1" }));
      }
      throw new Error(`unexpected fetch: ${input}`);
    }) as unknown as typeof fetch;
    render(
      <Wrapper>
        <HistoryPanel />
      </Wrapper>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    fireEvent.click(await screen.findByRole("button", { name: "Roll back" }));
    expect(await screen.findByText("Confirm admin access rollback?")).toBeInTheDocument();
    expect(screen.getByText("admin token changes")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Confirm and roll back" }));
    await waitFor(() => {
      expect(urls).toContain("/api/config/rollback?confirm_admin=true");
    });
  });

  it("keeps a provisional rollback open until its correlated terminal result", async () => {
    let overviewReads = 0;
    globalThis.fetch = vi.fn((input: string) => {
      if (input === "/api/config/history")
        return Promise.resolve(json([{ id: "s1", time: "2026-01-01T00:00:00Z", size: 120 }]));
      if (input === "/api/config/history/s1")
        return Promise.resolve(json({ id: "s1", raw: 'listen = ":80"\n' }));
      if (input === "/api/config/diff") return Promise.resolve(json({ summary: "rollback" }));
      if (input === "/api/config/rollback") {
        return Promise.resolve(
          json(
            {
              ok: true,
              apply_id: "rl_rb",
              mode: "hot",
              reload: {
                id: "rl_rb",
                outcome: "saved_not_live",
                http: { status: "" },
                stream: { status: "" },
                admin: { status: "" },
              },
            },
            202,
          ),
        );
      }
      if (input === "/api/runtime/overview") {
        overviewReads += 1;
        return Promise.resolve(
          json({
            product: "jul",
            version: "1",
            status: [],
            ...(overviewReads > 1
              ? {
                  last_managed_apply: {
                    id: "rl_rb",
                    mode: "hot",
                    ok: true,
                    outcome: "applied_live",
                    completed_at: "2026-01-01T00:00:01Z",
                  },
                }
              : {
                  last_managed_apply: {
                    id: "other",
                    mode: "hot",
                    ok: true,
                    outcome: "applied_live",
                    completed_at: "2026-01-01T00:00:00Z",
                  },
                }),
          }),
        );
      }
      throw new Error(`unexpected fetch: ${input}`);
    }) as unknown as typeof fetch;
    render(
      <Wrapper>
        <HistoryPanel />
      </Wrapper>,
    );
    fireEvent.click(await screen.findByRole("button", { name: "Rollback" }));
    fireEvent.click(await screen.findByRole("button", { name: "Roll back" }));
    expect(await screen.findByText(/live result is still pending/i)).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "Roll back to this snapshot?" })).toBeInTheDocument();
    await waitFor(
      () => {
        expect(
          screen.queryByRole("dialog", { name: "Roll back to this snapshot?" }),
        ).not.toBeInTheDocument();
      },
      { timeout: 4000 },
    );
  });
});

describe("HistoryPanel empty state", () => {
  it("shows a friendly empty state when there are no snapshots", async () => {
    globalThis.fetch = vi.fn((input: string) => {
      if (input === "/api/config/history") {
        return Promise.resolve(json([]));
      }
      throw new Error(`unexpected fetch: ${input}`);
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <HistoryPanel />
      </Wrapper>,
    );

    expect(await screen.findByText("No snapshots yet")).toBeInTheDocument();
  });
});

describe("WizardPanel", () => {
  it("generates a config and hands it off to the editor", async () => {
    render(
      <Wrapper>
        <WizardPanel />
      </Wrapper>,
    );

    fireEvent.change(screen.getByPlaceholderText("/var/www/site"), {
      target: { value: "/srv/site" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Generate config" }));

    const open = await screen.findByRole("button", { name: /Open in editor/ });
    await waitFor(() => {
      expect(open).toBeEnabled();
    });
    fireEvent.click(open);
    await waitFor(() => {
      const draft = takePendingDraft();
      expect(draft?.kind).toBe("patch");
      expect(draft && draft.kind === "patch" ? draft.ops : []).toHaveLength(2);
    });
  });
});
