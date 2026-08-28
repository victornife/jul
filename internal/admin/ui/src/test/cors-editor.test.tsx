/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the guided per-location CORS policy editor (#147).
 * They mount the drawer, seed it from the route projection, and assert that
 * saving posts the full location_cors_set patch and stages the resulting diff
 * for the Config editor — never writing directly — and that a save the backend
 * would reject is blocked in the UI.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { CORSEditor } from "@/features/routes/CORSEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { ConfigPatch, LocationCORSState, RouteTarget } from "@/api/client.ts";

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
      hot_paths: ["servers.locations.cors"],
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
    return Promise.resolve(previewResponse("location_cors_set", "CORS policy set"));
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("CORSEditor", () => {
  it("seeds the form from the projection and posts the full location_cors_set patch", async () => {
    const seed: LocationCORSState = {
      enabled: true,
      allowed_origins: ["https://app.example.test"],
      allowed_methods: ["GET", "POST"],
      exposed_headers: ["X-Request-Id"],
      allow_credentials: false,
      max_age: "10m",
    };
    render(
      <Wrapper>
        <CORSEditor target={target()} seed={seed} onClose={() => undefined} />
      </Wrapper>,
    );

    expect(screen.getByDisplayValue("https://app.example.test")).toBeTruthy();
    expect(screen.getByDisplayValue("10m")).toBeTruthy();

    fireEvent.click(screen.getByText("Review lifecycle and diff →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_cors_set",
      listen: ":8080",
      match_type: "prefix",
      path: "/api",
      cors_set: {
        enabled: true,
        allowed_origins: ["https://app.example.test"],
        allowed_methods: ["GET", "POST"],
        exposed_headers: ["X-Request-Id"],
        allow_credentials: false,
        max_age: "10m",
      },
    });
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("blocks save when enabled with no allowed origins", () => {
    render(
      <Wrapper>
        <CORSEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.getByText(/at least one allowed origin/i)).toBeInTheDocument();
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
  });

  it("blocks save when the wildcard origin is combined with credentials", () => {
    render(
      <Wrapper>
        <CORSEditor target={target()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.change(screen.getByPlaceholderText(/app.example.test, https/i), {
      target: { value: "*" },
    });
    fireEvent.click(screen.getByLabelText(/allow credentials/i));
    expect(screen.getByText(/cannot be combined with the "\*" wildcard/i)).toBeInTheDocument();
    expect(screen.getByText("Review lifecycle and diff →")).toBeDisabled();
  });

  it("hides Clear policy when adding a fresh policy", () => {
    render(
      <Wrapper>
        <CORSEditor target={target()} existing={false} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.queryByText("Clear policy")).not.toBeInTheDocument();
  });

  it("posts location_cors_clear when clearing an existing policy", async () => {
    globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
      expect(input).toBe("/api/config/patch/preview");
      seenBody = typeof init?.body === "string" ? init.body : "";
      return Promise.resolve(previewResponse("location_cors_clear", "CORS policy cleared"));
    }) as unknown as typeof fetch;

    render(
      <Wrapper>
        <CORSEditor
          target={target()}
          seed={{ enabled: true, allowed_origins: ["https://app.example.test"], allow_credentials: false }}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Clear policy"));
    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect((JSON.parse(seenBody) as { ops: ConfigPatch[] }).ops[0]).toMatchObject({
      op: "location_cors_clear",
      listen: ":8080",
    });
  });
});
