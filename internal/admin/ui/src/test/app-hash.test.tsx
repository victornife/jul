/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { AppProjection, ConfigPatch } from "@/api/client.ts";
import { AppDetail } from "@/features/apps/AppDetail.tsx";
import {
  describeHash,
  emptyHashDraft,
  hashApplicability,
  hashDraftFromProjection,
  hashDraftIssues,
  hashDraftToPatch,
  missingKeyExplanation,
} from "@/lib/appHash.ts";
import { AppPatchValidationError, buildAppCreationBatch } from "@/lib/appPatch.ts";
import { takePendingDraft } from "@/lib/configDraftHandoff.ts";

function app(overrides: Partial<AppProjection> = {}): AppProjection {
  return {
    name: "api",
    strategy: "round_robin",
    backends: [{ address: "10.0.0.1:8080", weight: 1 }],
    health_check: false,
    routes_using: [],
    ...overrides,
  };
}

const hashed = app({
  strategy: "consistent_hash",
  hash: {
    key: "header",
    name: "X-Tenant",
    fallback: "least_conn",
    algorithm: "rendezvous_v1",
    applies_to: "http",
  },
});

describe("appHash helpers", () => {
  it("validates the key source like the server does", () => {
    expect(hashDraftIssues(emptyHashDraft)).toEqual([]);
    expect(hashDraftIssues({ key: "header", name: "", fallback: "round_robin" })[0]).toMatch(
      /needs the header name/,
    );
    expect(hashDraftIssues({ key: "cookie", name: "a;b", fallback: "round_robin" })[0]).toMatch(
      /not a valid cookie name/,
    );
    expect(
      hashDraftIssues({ key: "header", name: "x".repeat(129), fallback: "round_robin" })[0],
    ).toMatch(/longer than 128/);
    expect(hashDraftIssues({ key: "header", name: "Cookie", fallback: "round_robin" })[0]).toMatch(
      /single cookie/,
    );
    expect(hashDraftIssues({ key: "header", name: "X-Tenant", fallback: "round_robin" })).toEqual(
      [],
    );
  });

  it("serializes only what applies to the key source", () => {
    expect(hashDraftToPatch({ key: "client_ip", name: "stale", fallback: "least_conn" })).toEqual({
      key: "client_ip",
      fallback: "least_conn",
    });
    expect(hashDraftToPatch({ key: "cookie", name: " sid ", fallback: "round_robin" })).toEqual({
      key: "cookie",
      name: "sid",
      fallback: "round_robin",
    });
  });

  it("seeds from the projection and describes the policy", () => {
    expect(hashDraftFromProjection(hashed)).toEqual({
      key: "header",
      name: "X-Tenant",
      fallback: "least_conn",
    });
    expect(hashDraftFromProjection(app())).toEqual(emptyHashDraft);
    expect(
      hashDraftFromProjection(
        app({
          hash: {
            key: "client_ip",
            fallback: "weighted_round_robin",
            algorithm: "rendezvous_v1",
            applies_to: "http_and_stream",
          },
        }),
      ),
    ).toEqual({ key: "client_ip", name: "", fallback: "weighted_round_robin" });
    expect(describeHash(hashed)).toBe("header X-Tenant · without a key: least_conn · rendezvous_v1");
    expect(describeHash(app())).toBeNull();
    expect(hashApplicability("client_ip")).toMatch(/stream/);
    expect(hashApplicability("cookie")).toMatch(/HTTP routes only/);
    expect(missingKeyExplanation("client_ip", "round_robin")).toMatch(/cannot be attributed/);
    expect(missingKeyExplanation("header", "least_conn")).toMatch(/placed by least_conn/);
  });
});

describe("App creation with consistent_hash", () => {
  const inventory = { apps: [], routes: [] };
  const base = {
    name: "sticky",
    backends: [{ address: "10.0.0.1:80", weight: 1 }],
    mount: { mode: "none" as const },
  };

  it("carries the hash block on upstream_add", () => {
    const ops = buildAppCreationBatch(
      {
        ...base,
        strategy: "consistent_hash",
        hash: { key: "cookie", name: "sid", fallback: "round_robin" },
      },
      inventory,
    );
    expect(ops[0]).toEqual({
      op: "upstream_add",
      upstream: "sticky",
      address: "10.0.0.1:80",
      weight: 1,
      strategy: "consistent_hash",
      hash: { key: "cookie", name: "sid", fallback: "round_robin" },
    });
  });

  it("refuses consistent_hash without a usable key and omits hash otherwise", () => {
    expect(() =>
      buildAppCreationBatch({ ...base, strategy: "consistent_hash" }, inventory),
    ).toThrow(AppPatchValidationError);
    expect(() =>
      buildAppCreationBatch(
        {
          ...base,
          strategy: "consistent_hash",
          hash: { key: "header", name: "", fallback: "round_robin" },
        },
        inventory,
      ),
    ).toThrow(/header name/);
    const ops = buildAppCreationBatch(
      { ...base, strategy: "least_conn", hash: emptyHashDraft },
      inventory,
    );
    expect(ops[0]).not.toHaveProperty("hash");
  });
});

const realFetch = globalThis.fetch;
let requests: ConfigPatch[][] = [];

function Wrapper({ children }: { readonly children: ReactNode }) {
  return <MemoryRouter initialEntries={["/apps"]}>{children}</MemoryRouter>;
}

describe("AppDetail affinity", () => {
  beforeEach(() => {
    requests = [];
    takePendingDraft();
    globalThis.fetch = vi.fn((_input: string, init?: RequestInit) => {
      const body = JSON.parse(typeof init?.body === "string" ? init.body : "null") as {
        ops: ConfigPatch[];
      };
      requests.push(body.ops);
      return Promise.resolve(
        new Response(
          JSON.stringify({
            ok: true,
            summary: "previewed",
            operation_summaries: [],
            diff: { summary: "1 change" },
            base_version: "v1",
            valid: true,
            validation_errors: [],
          }),
          { status: 200, headers: { "Content-Type": "application/json" } },
        ),
      );
    }) as unknown as typeof fetch;
  });

  afterEach(() => {
    globalThis.fetch = realFetch;
    vi.restoreAllMocks();
    takePendingDraft();
  });

  it("shows the key source, missing-key fallback and applicability", () => {
    render(<AppDetail app={hashed} onClose={() => undefined} />, { wrapper: Wrapper });
    expect(
      screen.getByText("header X-Tenant · without a key: least_conn · rendezvous_v1"),
    ).toBeInTheDocument();
    expect(screen.getByText("HTTP routes only")).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Consistent hash settings" })).toBeInTheDocument();
  });

  it("switches to consistent_hash with an explicit key in one operation", async () => {
    render(<AppDetail app={app()} onClose={() => undefined} />, { wrapper: Wrapper });
    fireEvent.change(screen.getByDisplayValue("Round robin"), {
      target: { value: "consistent_hash" },
    });
    fireEvent.change(screen.getByDisplayValue("Client IP (canonical client address)"), {
      target: { value: "header" },
    });
    const review = screen.getAllByRole("button", { name: "Review →" })[0] as HTMLButtonElement;
    expect(review).toBeDisabled();
    expect(screen.getByText(/needs the header name/)).toBeInTheDocument();
    fireEvent.change(screen.getByPlaceholderText("X-Tenant"), { target: { value: "X-Tenant" } });
    expect(review).not.toBeDisabled();
    fireEvent.click(review);
    await waitFor(() => {
      expect(requests[0]).toEqual([
        {
          op: "upstream_set_strategy",
          upstream: "api",
          strategy: "consistent_hash",
          hash: { key: "header", name: "X-Tenant", fallback: "round_robin" },
        },
      ]);
    });
  });

  it("edits only the fallback of an existing consistent_hash pool", async () => {
    render(<AppDetail app={hashed} onClose={() => undefined} />, { wrapper: Wrapper });
    const review = screen.getAllByRole("button", { name: "Review →" })[0] as HTMLButtonElement;
    expect(review).toBeDisabled();
    fireEvent.change(screen.getByDisplayValue("Least connections"), {
      target: { value: "round_robin" },
    });
    fireEvent.click(review);
    await waitFor(() => {
      expect(requests[0]?.[0]).toMatchObject({
        op: "upstream_set_strategy",
        strategy: "consistent_hash",
        hash: { key: "header", name: "X-Tenant", fallback: "round_robin" },
      });
    });
  });
});
