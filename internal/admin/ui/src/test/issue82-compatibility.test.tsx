/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";

const mocks = vi.hoisted(() => ({ fetchTrafficControls: vi.fn() }));

vi.mock("@/api/client.ts", async () => {
  const actual = await vi.importActual<typeof import("@/api/client.ts")>("@/api/client.ts");
  return { ...actual, fetchTrafficControls: mocks.fetchTrafficControls };
});

import { TrafficControlsPanel } from "@/features/traffic-controls/TrafficControlsPanel.tsx";

function renderPanel() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false, refetchInterval: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <TrafficControlsPanel />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("issue #82 optional traffic-control projections", () => {
  it("disables no-op actions and explains omitted compatibility projections", async () => {
    mocks.fetchTrafficControls.mockResolvedValue({});
    renderPanel();

    for (const name of [
      "Global settings",
      "Compression",
      "Rate limiting",
      "Cache",
      "Limits & Timeouts",
      "Access Logging",
      "Distributed Tracing",
    ]) {
      expect(await screen.findByText(name)).toBeInTheDocument();
    }

    const edits = await screen.findAllByRole("button", { name: "Edit" });
    expect(edits).toHaveLength(7);
    for (const edit of edits) {
      expect(edit).toBeDisabled();
      expect(edit).toHaveAttribute("aria-describedby");
    }
    expect(screen.getAllByText(/Unavailable on this server/)).toHaveLength(7);
    expect(screen.queryByText(/^enabled$/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/^disabled$/i)).not.toBeInTheDocument();
  });
});
