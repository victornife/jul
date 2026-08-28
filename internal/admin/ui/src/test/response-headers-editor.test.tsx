/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the guided ordered response-header operations editor
 * (#147). They mount the drawer, exercise adding/reordering/removing rows, and
 * assert that saving posts the full location_response_headers_set patch in the
 * row order shown — order is semantically meaningful (ADR 0018 §8) — and stages
 * the resulting diff for the Config editor, never writing directly.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { ResponseHeadersEditor } from "@/features/routes/ResponseHeadersEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { ConfigPatch, RouteTarget } from "@/api/client.ts";

const realFetch = globalThis.fetch;
let seenBody = "";

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function target(): RouteTarget {
  return { listen: ":8080", server_names: [], match_type: "prefix", path: "/api" };
}

function previewResponse(op: string, summary: string) {
  return json({
    ok: true,
    summary,
    operation_summaries: [{ op_index: 0, op, summary }],
    diff: { summary: "1 change" },
    base_version: "deadbeef",
    valid: true,
    validation_errors: [],
    lifecycle: {
      changes: [],
      can_apply_hot: true,
      can_stage_restart: true,
      hot_paths: ["servers.locations.response_headers"],
      restart_required_paths: [],
      new_listener_only_paths: [],
      ignored_deprecated_paths: [],
      validation_rejected_paths: [],
      pending_subsystems: [],
    },
  });
}

beforeEach(() => {
  seenBody = "";
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch/preview");
    seenBody = typeof init?.body === "string" ? init.body : "";
    return Promise.resolve(previewResponse("location_response_headers_set", "response headers set"));
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("ResponseHeadersEditor", () => {
  it("adds two rows and posts them in order", async () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );

    fireEvent.click(screen.getByText("+ Add operation"));
    fireEvent.change(screen.getByLabelText("Row 1 header name"), {
      target: { value: "X-Frame-Options" },
    });
    fireEvent.change(screen.getByLabelText("Row 1 value"), { target: { value: "DENY" } });

    fireEvent.click(screen.getByText("+ Add operation"));
    fireEvent.change(screen.getByLabelText("Row 2 operation"), { target: { value: "remove" } });
    fireEvent.change(screen.getByLabelText("Row 2 header name"), { target: { value: "Server" } });

    fireEvent.click(screen.getByText("Review lifecycle and diff →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_response_headers_set",
      listen: ":8080",
      response_headers: [
        { op: "set", name: "X-Frame-Options", value: "DENY" },
        { op: "remove", name: "Server" },
      ],
    });
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("reorders rows with the move-up/move-down buttons", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add operation"));
    fireEvent.change(screen.getByLabelText("Row 1 header name"), { target: { value: "First" } });
    fireEvent.click(screen.getByText("+ Add operation"));
    fireEvent.change(screen.getByLabelText("Row 2 header name"), { target: { value: "Second" } });

    fireEvent.click(screen.getByLabelText("Move row 2 up"));

    expect(screen.getByLabelText("Row 1 header name")).toHaveValue("Second");
    expect(screen.getByLabelText("Row 2 header name")).toHaveValue("First");
  });

  it("blocks save when a row is missing a name", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add operation"));
    expect(screen.getByText(/needs a header name/i)).toBeInTheDocument();
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
  });

  it("blocks save with zero rows, suggesting Clear instead", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/use clear to remove them all/i)).toBeInTheDocument();
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
  });

  it("posts location_response_headers_clear when clearing existing operations", async () => {
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      expect(input).toBe("/api/config/patch/preview");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return Promise.resolve(previewResponse("location_response_headers_clear", "response headers cleared"));
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} existing onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Clear operations"));
    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_response_headers_clear",
      listen: ":8080",
    });
  });

  it("warns that existing operations are not read back from the API", () => {
    render(
      <Wrapper>
        <ResponseHeadersEditor target={target()} existing onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/not read back from the API/i)).toBeInTheDocument();
  });
});
