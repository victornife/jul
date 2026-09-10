/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  fetchSettings: vi.fn(),
  run: vi.fn(() => Promise.resolve(undefined)),
  runnerError: null as Error | null,
  runnerBusy: false,
}));

vi.mock("@/api/client.ts", async () => {
  const actual = await vi.importActual<typeof import("@/api/client.ts")>("@/api/client.ts");
  return { ...actual, fetchAdminRuntimeSettings: mocks.fetchSettings };
});

vi.mock("@/lib/useRunPatchBatch.ts", async () => {
  const actual = await vi.importActual<typeof import("@/lib/useRunPatchBatch.ts")>("@/lib/useRunPatchBatch.ts");
  return {
    ...actual,
    useRunPatchBatch: () => ({
      error: mocks.runnerError,
      busy: mocks.runnerBusy,
      preview: vi.fn(),
      handoff: vi.fn(),
      run: mocks.run,
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

function renderDrawer(onClose = vi.fn()) {
  return { onClose, ...render(<AdminRuntimeSettingsDrawer onClose={onClose} />, { wrapper: Wrapper }) };
}

function rateInput(label: string): HTMLInputElement {
  const element = screen.getByLabelText(label);
  if (!(element instanceof HTMLInputElement)) {
    throw new Error("rate control is not an input");
  }
  return element;
}

function spinbutton(index: number): HTMLInputElement {
  const element = screen.getAllByRole("spinbutton")[index];
  if (!(element instanceof HTMLInputElement)) {
    throw new Error("spinbutton is not an input");
  }
  return element;
}

describe("AdminRuntimeSettingsDrawer HR-07A limits", () => {
  beforeEach(() => {
    mocks.run.mockClear();
    mocks.fetchSettings.mockReset();
    mocks.fetchSettings.mockResolvedValue(baseSettings);
    mocks.runnerError = null;
    mocks.runnerBusy = false;
  });

  it("renders canonical rate/SSE semantics and submits a sparse limits operation", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    expect(screen.getByText(/Zero means the canonical default/)).toBeInTheDocument();
    expect(screen.getByText(/Zero selects the canonical default \(4\)/)).toBeInTheDocument();

    fireEvent.change(rateInput("Read / min"), { target: { value: "-1" } });
    fireEvent.change(spinbutton(3), { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));

    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([
        { op: "admin_limits_set", admin_limits: { read_per_min: -1, max_event_conns: 0 } },
      ]);
    });
  });

  it("submits all rate classes independently without rewriting unrelated settings", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    fireEvent.change(rateInput("Write / min"), { target: { value: "12" } });
    fireEvent.change(rateInput("Apply / min"), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));

    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([
        { op: "admin_limits_set", admin_limits: { write_per_min: 12, apply_per_min: 7 } },
      ]);
    });
  });

  it("warns on SSE tightening without treating existing sessions as drain candidates", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");
    fireEvent.change(spinbutton(3), { target: { value: "2" } });

    expect(screen.getByText(/Existing event\/log streams remain connected/)).toBeInTheDocument();
    expect(screen.getByText(/cannot open another stream until their active count falls below/)).toBeInTheDocument();
  });

  it("rejects negative/non-integer SSE caps while negative request rates remain valid", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    fireEvent.change(rateInput("Read / min"), { target: { value: "-20" } });
    expect(screen.getByRole("button", { name: "Review changes" })).toBeEnabled();

    fireEvent.change(spinbutton(3), { target: { value: "-1" } });
    expect(screen.getByText(/Use a non-negative whole number/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();

    fireEvent.change(spinbutton(3), { target: { value: "1.5" } });
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
  });

  it("keeps save disabled for unchanged or non-integer request-rate values", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    expect(rateInput("Read / min").value).toBe("240");
    expect(rateInput("Write / min").value).toBe("60");
    expect(rateInput("Apply / min").value).toBe("30");
    expect(spinbutton(3).value).toBe("4");
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();

    fireEvent.change(rateInput("Read / min"), { target: { value: "1.25" } });
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();
  });

  it("preserves the existing Console self-lockout acknowledgement", async () => {
    renderDrawer();
    await screen.findByText("Admin request admission");

    fireEvent.click(screen.getByLabelText(/Serve the embedded Console/));
    expect(screen.getByText(/Disabling the Console removes this web UI/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Review changes" })).toBeDisabled();

    fireEvent.click(screen.getByLabelText(/I understand how to re-enable the Console/));
    const review = screen.getByRole("button", { name: "Review changes" });
    expect(review).toBeEnabled();
    fireEvent.click(review);
    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([{ op: "admin_console_set", enabled: false }]);
    });
  });

  it("keeps upload edits orthogonal to the limits operation", async () => {
    mocks.fetchSettings.mockResolvedValue({
      ...baseSettings,
      plugin_upload_enabled: true,
      plugin_upload_effective: true,
    });
    renderDrawer();
    await screen.findByText("Admin request admission");

    fireEvent.click(screen.getByLabelText(/Accept authenticated WASM uploads/));
    expect(screen.getByText(/blocks new request bodies before multipart parsing/)).toBeInTheDocument();

    fireEvent.change(spinbutton(4), { target: { value: "22" } });
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "/tmp/next" } });
    expect(screen.getByText(/does not copy, migrate, or delete files/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Review changes" }));
    await waitFor(() => {
      expect(mocks.run).toHaveBeenCalledWith([
        {
          op: "admin_plugin_upload_set",
          plugin_upload: { enabled: false, max_size_mb: 22, directory: "/tmp/next" },
        },
      ]);
    });
  });

  it("renders loading, request errors, preview errors and busy state without bypassing guards", async () => {
    mocks.fetchSettings.mockReturnValueOnce(new Promise(() => undefined));
    const loading = renderDrawer();
    expect(screen.getByText(/Loading admin runtime settings/)).toBeInTheDocument();
    loading.unmount();

    mocks.fetchSettings.mockRejectedValueOnce(new Error("settings unavailable"));
    const failed = renderDrawer();
    await screen.findByRole("alert");
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(failed.onClose).toHaveBeenCalled();
    failed.unmount();

    mocks.fetchSettings.mockResolvedValueOnce(baseSettings);
    mocks.runnerError = new Error("preview failed");
    mocks.runnerBusy = true;
    renderDrawer();
    await screen.findByText("Admin request admission");
    expect(screen.getByText("preview failed")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preparing preview…" })).toBeDisabled();
  });
});
