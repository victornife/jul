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
  CodeEditor: ({
    value,
    onChange,
  }: {
    value: string;
    onChange: (v: string) => void;
  }) => (
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
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

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
      <ConfirmDialog
        title="Sure?"
        confirmLabel="Do it"
        onConfirm={onConfirm}
        onCancel={onCancel}
      >
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

    // Confirm → apply happens, summary shown.
    fireEvent.click(screen.getByRole("button", { name: "Apply now" }));
    await waitFor(() => {
      expect(counters.apply).toBe(1);
    });
    expect(await screen.findByText("Configuration applied.")).toBeInTheDocument();
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
    fireEvent.click(open);
    expect(takePendingDraft()).toContain('listen = ":80"');
  });
});
