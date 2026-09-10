/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const run = vi.fn(async () => undefined);
const fetchSettings = vi.fn();

vi.mock("@/api/client.ts", async () => {
  const actual = await vi.importActual<typeof import("@/api/client.ts")>("@/api/client.ts");
  return {
    ...actual,
    fetchAdminRuntimeSettings: fetchSettings,
  };
});

vi.mock("@/lib/useRunPatchBatch.ts", async () => {
  const actual = await vi.importActual<typeof import("@/lib/useRunPatchBatch.ts")>("@/lib/useRunPatchBatch.ts");
  return {
    ...actual,
    useRunPatchBatch: () => ({
      error: null,
      busy: false,
      preview: vi.fn(),
      handoff: vi.fn(),
      run,
      clearError: vi.fn(),
    }),
  };
});

import { AdminRuntimeSettingsDrawer } from "./AdminRuntimeSettingsDrawer.tsx";

const hot = {
  class: "hot_reload",
  subsystem: "admin",
  reason: "hot",
  conditional: false,
};

const baseSettings = {
  console: true,
  console_compiled: true,
  console_effective: true,
  plugin_upload_enabled: false,
  plugin_upload_max_size_mb: 10,
  plugin_upload_dir: "/tmp/plugins",
  plugin_upload_effective: false,
  upload_directory_health: "disabled",
  rate_limit_read_per_min: 240,
  rate_limit_write_per_min: 60,
  rate_limit_apply_per_min: 30,
  max_event_conns: 4,
  lifecycle: {
    console: hot,
    plugin_upload_enabled: hot,
    plugin_upload_max_size: hot,
    plugin_upload_dir: hot,
    rate_limit_read_per_min: hot,
    rate_limit_write_per_min: hot,
    rate_limit_apply_per_min: hot,
    max_event_conns: hot,
  },
};

function Wrapper({ children }: { readonly children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

function renderDrawer() {
  return render(<AdminRuntimeSettingsDrawer onClose={vi.fn()} />, { wrapper: Wrapper });
}

function numberInput(label: string): HTMLInputElement {
  return screen.getByLabelText(label) as HTMLInputElement;
}

describe("AdminRuntimeSettingsDrawer HR-07A limits", () => {
  beforeEach(() => {
    run.mockClear();
    fetchSettings.mockReset();
    fetchSettings.mockResolvedValue(baseSettings);
  });

  it("renders canonical rate/SSE semantics and submits a sparse limits operation", async () => {
    renderDrawer();

    await screen.findByText("Admin request admission");
    expect(screen.getByText(/Zero means the canonical default/)).toBeInTheDocument();
    expect(screen.getByText(/Zero selects the canonical default \(4\)/)).toBeInTheDocument();

    fireEvent.change(numberInput("Read / min"), { target: { value: "-1" } });
    fireEvent.change(numberInput("Concurrent event/log streams per client"), { target: { value: "0" } });

    const review = screen.getByRole("button", { name: "Review changes" });
    expect(review).toBeEnabled();
    fireEvent.click(review);

    await waitFor(() => {
      expect(run).toHaveBeenCalledWith([
        {
          op: "admin_limits_set",
          admin_limits: { read_per_min: -1, max_event_conns: 0 },
        },
      ]);
    });
  });

  it("warns on SSE tightening without treating existing sessions as drain candidates", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    fireEvent.change(numberInput("Concurrent event/log streams per client"), { target: { value: "2" } });

    expect(
      screen.getByText(/Existing event\/log streams remain connected/),
    ).toBeInTheDocument();
    expect(screen.getByText(/cannot open another stream until their active count falls below/)).toBeInTheDocument();
  });

  it("rejects negative SSE caps while allowing negative request-rate disable values", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    fireEvent.change(numberInput("Read / min"), { target: { value: "-20" } });
    expect(screen.getByRole("button", { name: "Review changes" })).toBeEnabled();

    fireEvent.change(numberInput("Concurrent event/log streams per client"), { target: { value: "-1" } });
    expect(screen.getByText(/Use a non-negative whole number/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
  });

  it("keeps the save action disabled when no admission policy value changed", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    expect(numberInput("Read / min").value).toBe("240");
    expect(numberInput("Write / min").value).toBe("60");
    expect(numberInput("Apply / min").value).toBe("30");
    expect(numberInput("Concurrent event/log streams per client").value).toBe("4");
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
  });
});
