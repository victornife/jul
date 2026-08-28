/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the guided match-predicates editor (#147). They mount
 * the drawer, seed the methods field from the projection (the one facet that
 * is not sensitive and so is projected in full), exercise header/query rows,
 * and assert that saving always names all three facets in the
 * location_set_predicates patch — the editor replaces the whole predicate set
 * wholesale, the same convention every other per-location editor here uses.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { PredicatesEditor } from "@/features/routes/PredicatesEditor.tsx";
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
      hot_paths: ["servers.locations.match"],
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
    return Promise.resolve(previewResponse("location_set_predicates", "predicates updated"));
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("PredicatesEditor", () => {
  it("seeds methods from the projection and posts all three facets on save", async () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} seedMethods={["GET", "POST"]} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByDisplayValue("GET, POST")).toBeTruthy();

    fireEvent.click(screen.getByText("+ Add header predicate"));
    fireEvent.change(screen.getByLabelText("Header row 1 name"), {
      target: { value: "X-Tenant" },
    });
    fireEvent.change(screen.getByLabelText("Header row 1 operator"), {
      target: { value: "exact" },
    });
    fireEvent.change(screen.getByLabelText("Header row 1 value"), { target: { value: "acme" } });

    fireEvent.click(screen.getByText("+ Add query predicate"));
    fireEvent.change(screen.getByLabelText("Query row 1 name"), { target: { value: "debug" } });

    fireEvent.click(screen.getByText("Review lifecycle and diff →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_set_predicates",
      listen: ":8080",
      predicates: {
        methods: ["GET", "POST"],
        headers: [{ name: "X-Tenant", op: "exact", value: "acme" }],
        query: [{ name: "debug", op: "present" }],
      },
    });
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("removes the value field when a header predicate op is present", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add header predicate"));
    expect(screen.queryByLabelText("Header row 1 value")).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Header row 1 operator"), { target: { value: "exact" } });
    expect(screen.getByLabelText("Header row 1 value")).toBeInTheDocument();
  });

  it("blocks save when no predicate is configured at all", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/at least one predicate/i)).toBeInTheDocument();
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
  });

  it("blocks save when a header predicate needs a value it does not have", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("+ Add header predicate"));
    fireEvent.change(screen.getByLabelText("Header row 1 name"), { target: { value: "X-A" } });
    fireEvent.change(screen.getByLabelText("Header row 1 operator"), { target: { value: "regex" } });
    expect(screen.getByText(/needs a value for "regex"/i)).toBeInTheDocument();
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
  });

  it("posts location_clear_predicates when clearing existing predicates", async () => {
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      expect(input).toBe("/api/config/patch/preview");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return Promise.resolve(previewResponse("location_clear_predicates", "predicates cleared"));
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <PredicatesEditor target={target()} seedMethods={["GET"]} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Clear predicates"));
    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_clear_predicates",
      listen: ":8080",
    });
  });

  it("warns that existing header/query predicates are not read back from the API", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} seedMethods={["GET"]} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/not read back from the API/i)).toBeInTheDocument();
  });

  it("hides Clear predicates when adding fresh predicates", () => {
    render(
      <Wrapper>
        <PredicatesEditor target={target()} existing={false} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.queryByText("Clear predicates")).not.toBeInTheDocument();
  });
});
