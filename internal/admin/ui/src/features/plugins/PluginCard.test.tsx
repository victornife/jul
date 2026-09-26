/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import type { PluginProjection } from "@/api/client.ts";
import { PluginCard } from "./PluginCard.tsx";

const base: PluginProjection = {
  name: "authz",
  source: "path",
  path: "/opt/authz.wasm",
  type: "middleware",
  kv: false,
  fetch: false,
  abi: "jul-abi/v1",
  response_phase: false,
};

function renderCard(plugin: PluginProjection) {
  const noop = () => undefined;
  render(<PluginCard plugin={plugin} onEdit={noop} onAttach={noop} onRemove={noop} onDetach={noop} />);
}

describe("PluginCard module identity (#429)", () => {
  it("shows the server-computed short digest and pin state", () => {
    renderCard({ ...base, pinned: true, digest: `sha256:${"a".repeat(64)}`, digest_short: "aaaaaaaaaaaa" });
    expect(screen.getByText("sha256:aaaaaaaaaaaa")).toBeTruthy();
    expect(screen.getByText(/pinned/)).toBeTruthy();
  });

  it("does not invent a digest for a plugin that is not serving", () => {
    renderCard(base);
    expect(screen.getByText("not serving")).toBeTruthy();
    expect(screen.queryByText(/pinned/)).toBeNull();
  });
});

describe("PluginCard resource limits (#462)", () => {
  it("shows every explicitly configured limit", () => {
    renderCard({
      ...base,
      limits: { max_invocations: 250, kv_max_entries: 7, max_response_body: "4m", fetch_timeout: "3s" },
    });
    const text = screen.getByText(/max_invocations=250/).textContent;
    expect(text).toContain("kv_max_entries=7");
    expect(text).toContain("max_response_body=4m");
    expect(text).toContain("fetch_timeout=3s");
  });

  it("shows no limits line when every limit is the default", () => {
    renderCard(base);
    expect(screen.queryByText(/limits:/)).toBeNull();
  });
});

describe("PluginCard ABI (#430)", () => {
  it("shows the v1 ABI without any v2 field", () => {
    renderCard(base);
    expect(screen.getByText("jul-abi/v1")).toBeTruthy();
    expect(screen.queryByText(/response phase/)).toBeNull();
  });

  it("shows a serving v2 module's response phase and body bound", () => {
    renderCard({ ...base, abi: "jul-abi/v2", response_phase: true, response_body_max: "1m" });
    expect(screen.getByText(/response phase \(body ≤ 1m\)/)).toBeTruthy();
  });

  it("says when a v2 module has no response phase", () => {
    renderCard({ ...base, abi: "jul-abi/v2", response_phase: false, response_body_max: "8m" });
    expect(screen.getByText(/request phase only/)).toBeTruthy();
  });
});
