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
import { TrafficControlEditor } from "@/features/traffic-controls/TrafficControlEditor.tsx";
import { fireEvent } from "@testing-library/react";

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

  it("renders cache tranche summary and edits tranche fields in TrafficControlEditor", async () => {
    const current = {
      cache: {
        enabled: true,
        memory_max_size: "64MiB",
        disk_path: "/var/cache/jul",
        disk_max_size: "1GiB",
        max_entry_size: "32MiB",
        default_ttl: "1m0s",
        stale_while_revalidate: "30s",
        stale_if_error: "5m0s",
        lock: false,
        lock_timeout: "5s",
        range_enabled: false,
        max_ranges: 1,
      },
    };
    mocks.fetchTrafficControls.mockResolvedValue(current);
    const client = new QueryClient({
      defaultOptions: { queries: { retry: false, refetchInterval: false } },
    });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <TrafficControlsPanel />
          <TrafficControlEditor kind="cache" current={current} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText("32MiB")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Maximum entry size"), { target: { value: "64MiB" } });
    fireEvent.click(screen.getByLabelText("Enable cold-miss fill lock"));
    fireEvent.change(screen.getByLabelText("Fill-lock timeout"), { target: { value: "8s" } });
    fireEvent.click(screen.getByLabelText("Enable range-aware caching"));
    fireEvent.change(screen.getByLabelText("Maximum ranges"), { target: { value: "4" } });

    expect(screen.getByLabelText("Maximum entry size")).toHaveValue("64MiB");
    expect(screen.getByLabelText("Enable cold-miss fill lock")).toBeChecked();
    expect(screen.getByLabelText("Fill-lock timeout")).toHaveValue("8s");
    expect(screen.getByLabelText("Enable range-aware caching")).toBeChecked();
    expect(screen.getByLabelText("Maximum ranges")).toHaveValue(4);

    // Disabling fill lock and range caching clears their dependent fields.
    fireEvent.click(screen.getByLabelText("Enable cold-miss fill lock"));
    fireEvent.click(screen.getByLabelText("Enable range-aware caching"));
    expect(screen.getByLabelText("Enable cold-miss fill lock")).not.toBeChecked();
    expect(screen.getByLabelText("Fill-lock timeout")).toHaveValue("");
    expect(screen.getByLabelText("Enable range-aware caching")).not.toBeChecked();
    expect(screen.getByLabelText("Maximum ranges")).toHaveValue(0);

    // Fractional, negative, and over-limit max_ranges values show validation error and are not truncated.
    fireEvent.click(screen.getByLabelText("Enable range-aware caching"));
    for (const invalidVal of ["1.9", "-1", "17"]) {
      fireEvent.change(screen.getByLabelText("Maximum ranges"), { target: { value: invalidVal } });
      expect(screen.getByText(/Maximum ranges must be an integer between 0 and 16/)).toBeInTheDocument();
    }
    fireEvent.change(screen.getByLabelText("Maximum ranges"), { target: { value: "2" } });
    expect(screen.queryByText(/Maximum ranges must be an integer between 0 and 16/)).not.toBeInTheDocument();
  });
});
