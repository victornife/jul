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
