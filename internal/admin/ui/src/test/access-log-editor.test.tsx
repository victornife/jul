/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { AccessLogEditor } from "@/features/traffic-controls/AccessLogEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { TrafficControls } from "@/api/client.ts";

const realFetch = globalThis.fetch;

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

beforeEach(() => {
  takePendingDraft();
  globalThis.fetch = vi.fn(() =>
    Promise.resolve(
      new Response(JSON.stringify({ raw: 'listen = ":8080"\n' }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      }),
    ),
  );
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("AccessLogEditor", () => {
  it("seeds from projection and stages the complete access-log block", async () => {
    const current: TrafficControls = {
      access_log: {
        enabled: false,
        sinks: ["file"],
        file: "/var/log/jul/access.log",
        format: "json",
        rotate_max_mb: 64,
        rotate_keep: 5,
      },
    };
    render(
      <Wrapper>
        <AccessLogEditor current={current} onClose={() => undefined} />
      </Wrapper>,
    );

    expect(screen.getByDisplayValue("/var/log/jul/access.log")).toBeTruthy();
    expect(
      screen.getByRole("checkbox", { name: "Enable request access logging" }),
    ).not.toBeChecked();

    fireEvent.click(screen.getByText("Review in editor →"));

    let staged: string | null = null;
    await waitFor(() => {
      const draft = takePendingDraft();
      if (draft?.kind === "toml") staged = draft.toml;
      expect(staged).not.toBeNull();
    });

    expect(staged).toContain("[observability.access_log]");
    expect(staged).toContain("enabled = false");
    expect(staged).toContain('sinks = ["file"]');
    expect(staged).toContain('file = "/var/log/jul/access.log"');
    expect(staged).toContain('format = "json"');
  });

  it("blocks review when enabled with no selected sink", () => {
    const current: TrafficControls = { access_log: { enabled: true, sinks: [] } };
    render(
      <Wrapper>
        <AccessLogEditor current={current} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/no sink is selected/)).toBeInTheDocument();
    expect(screen.getByText("Review in editor →")).toBeDisabled();
  });
});
