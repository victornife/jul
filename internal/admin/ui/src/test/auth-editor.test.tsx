/**
 * Component test for the per-location AuthEditor drawer (Phase 4a). It mounts
 * the editor, seeds it from a no-secrets projection state, and asserts that
 * saving posts a structured location_set_auth patch and hands the previewed
 * draft to the Config editor — never writing directly. A second case covers the
 * Clear action emitting location_clear_auth.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";

import { AuthEditor } from "@/features/routes/AuthEditor.tsx";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";
import type { LocationAuthState, RouteTarget } from "@/api/client.ts";

const realFetch = globalThis.fetch;

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter>{children}</MemoryRouter>;
}

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

const target: RouteTarget = {
  listen: ":8080",
  server_names: [],
  match_type: "prefix",
  path: "/api",
};

let seenBody = "";

beforeEach(() => {
  seenBody = "";
  takePendingDraft(); // clear any leftover handoff state
  globalThis.fetch = vi.fn((input: string, init?: RequestInit) => {
    expect(input).toBe("/api/config/patch");
    seenBody = typeof init?.body === "string" ? init.body : "";
    return Promise.resolve(
      json({
        ok: true,
        summary: "route :8080/api auth set",
        candidate: 'listen = ":8080"\n',
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

describe("AuthEditor", () => {
  it("seeds from the projection and posts a location_set_auth patch on save", async () => {
    const seed: LocationAuthState = {
      method: "jwt",
      jwt_jwks_url: "https://issuer.example/jwks.json",
      jwt_issuer: "https://issuer.example/",
    };
    render(
      <Wrapper>
        <AuthEditor target={target} seed={seed} onClose={() => undefined} />
      </Wrapper>,
    );

    // Seeded as an existing rule: the JWKS URL is prefilled and Clear is offered.
    expect(screen.getByDisplayValue("https://issuer.example/jwks.json")).toBeTruthy();
    expect(screen.getByText("Clear rule")).toBeTruthy();

    fireEvent.click(screen.getByText("Review in editor →"));

    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({
      op: "location_set_auth",
      listen: ":8080",
      path: "/api",
      auth: { method: "jwt", jwt_jwks_url: "https://issuer.example/jwks.json" },
    });
    // The edit is staged for diff review, never applied directly.
    const draft = takePendingDraft();
    expect(draft?.kind).toBe("patch");
  });

  it("blocks save while the draft is incomplete (no JWKS URL)", () => {
    render(
      <Wrapper>
        <AuthEditor
          target={target}
          seed={{ method: "jwt" }}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    const save = screen.getByRole("button", { name: "Review in editor →" });
    expect(save).toBeDisabled();
    expect(screen.getByText("JWT auth needs a JWKS URL.")).toBeTruthy();
  });

  it("emits location_clear_auth from the Clear action", async () => {
    render(
      <Wrapper>
        <AuthEditor
          target={target}
          seed={{ method: "cidr", allow: ["10.0.0.0/8"] }}
          onClose={() => undefined}
        />
      </Wrapper>,
    );
    fireEvent.click(screen.getByText("Clear rule"));
    await waitFor(() => {
      expect(seenBody).not.toBe("");
    });
    expect(JSON.parse(seenBody)).toMatchObject({ op: "location_clear_auth", path: "/api" });
  });

  it("hides Clear when adding a rule to a location with none", () => {
    render(
      <Wrapper>
        <AuthEditor target={target} onClose={() => undefined} />
      </Wrapper>,
    );
    expect(screen.queryByText("Clear rule")).toBeNull();
    expect(screen.getByText("Add access control")).toBeTruthy();
  });
});
