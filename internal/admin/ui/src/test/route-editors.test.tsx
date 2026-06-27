/**
 * Component tests for the in-place route editors (Phase 4f). They mount each
 * drawer, seed it from the route projection, and assert that saving posts the
 * structured patch (location_set_match / location_set_action / route_rename) and
 * stages the resulting diff for the Config editor — never writing directly — and
 * that a no-op or invalid edit keeps the save button disabled.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import {
  RouteActionEditor,
  RouteMatchEditor,
  RouteRenameEditor,
} from "@/features/routes/RouteEditors.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { LocationProjection, RouteProjection } from "@/api/client.ts";

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

function loc(over: Partial<LocationProjection> = {}): LocationProjection {
  return {
    index: 0,
    match: "/api",
    type: "prefix",
    action: "proxy",
    target: "http://app",
    auth: false,
    cache: false,
    compression: false,
    rate_limit: false,
    secure: false,
    ...over,
  };
}

function route(over: Partial<RouteProjection> = {}): RouteProjection {
  return {
    listen: ":443",
    server_names: ["a.example"],
    http3: false,
    h2c: false,
    locations: [loc()],
    ...over,
  };
}

beforeEach(() => {
  seenBody = "";
  takePendingDraft();
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch");
    seenBody = typeof init?.body === "string" ? init.body : "";
    return Promise.resolve(
      json({
        ok: true,
        summary: "route changed",
        candidate: 'listen = ":443"\n',
        diff: { summary: "1 change" },
        base_version: "deadbeef",
      }),
    );
  }) as unknown as typeof fetch;
});

afterEach(() => {
  globalThis.fetch = realFetch;
  vi.restoreAllMocks();
});

describe("RouteMatchEditor", () => {
  it("posts location_set_match with the new type + path and stages the diff", async () => {
    render(
      <Wrapper>
        <RouteMatchEditor route={route()} loc={loc()} onClose={() => undefined} />
      </Wrapper>,
    );

    fireEvent.change(screen.getByDisplayValue("/api"), { target: { value: "/v2" } });
    fireEvent.click(screen.getByText("Preview change →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_set_match",
      listen: ":443",
      server_names: ["a.example"],
      match_type: "prefix",
      path: "/api",
      match_set: { type: "prefix", path: "/v2" },
    });
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("keeps save disabled until the match changes", () => {
    render(
      <Wrapper>
        <RouteMatchEditor route={route()} loc={loc()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.change(screen.getByDisplayValue("/api"), { target: { value: "/api" } });
    expect(screen.getByText("Preview change →")).toBeDisabled();
  });
});

describe("RouteActionEditor", () => {
  it("posts location_set_action switching proxy → deny", async () => {
    render(
      <Wrapper>
        <RouteActionEditor route={route()} loc={loc()} onClose={() => undefined} />
      </Wrapper>,
    );

    fireEvent.change(screen.getByDisplayValue("proxy"), { target: { value: "deny" } });
    fireEvent.click(screen.getByText("Preview change →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_set_action",
      match_type: "prefix",
      path: "/api",
      action: { kind: "deny" },
    });
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("blocks save when the proxy target is cleared", () => {
    render(
      <Wrapper>
        <RouteActionEditor route={route()} loc={loc()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.change(screen.getByDisplayValue("http://app"), { target: { value: "" } });
    expect(screen.getByText("Preview change →")).toBeDisabled();
  });
});

describe("RouteRenameEditor", () => {
  it("posts route_rename with the new host names", async () => {
    render(
      <Wrapper>
        <RouteRenameEditor route={route()} onClose={() => undefined} />
      </Wrapper>,
    );

    fireEvent.change(screen.getByDisplayValue("a.example"), {
      target: { value: "c.example\nd.example" },
    });
    fireEvent.click(screen.getByText("Preview change →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "route_rename",
      listen: ":443",
      server_names: ["a.example"],
      new_server_names: ["c.example", "d.example"],
    });
    expect(takePendingDraft()?.kind).toBe("patch");
  });

  it("blocks save on a duplicate host name", () => {
    render(
      <Wrapper>
        <RouteRenameEditor route={route()} onClose={() => undefined} />
      </Wrapper>,
    );
    fireEvent.change(screen.getByDisplayValue("a.example"), {
      target: { value: "x.example\nx.example" },
    });
    expect(screen.getByText("Preview change →")).toBeDisabled();
  });
});
