/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Component tests for the Operations Log tab (Phase 4g). They mount the live
 * access-log tail with a mocked subscribeLogs transport and assert it renders
 * streamed entries, reflects connection status, pauses (dropping incoming
 * lines), filters, clears, and unsubscribes on unmount.
 */
import { describe, it, expect, vi } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import type { LogEntry } from "@/api/client.ts";

const h = vi.hoisted(() => ({
  captured: null as null | {
    onEntry: (e: LogEntry) => void;
    handlers?: { onOpen?: () => void; onError?: (e: unknown) => void } | undefined;
  },
  stop: vi.fn(),
}));

vi.mock("@/api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client.ts")>();
  return {
    ...actual,
    subscribeLogs: (
      onEntry: (e: LogEntry) => void,
      handlers?: { onOpen?: () => void; onError?: (e: unknown) => void },
    ) => {
      h.captured = { onEntry, handlers };
      return h.stop;
    },
  };
});

const { LogTailPanel } = await import("@/features/observability/LogTailPanel.tsx");

function entry(over: Partial<LogEntry> = {}): LogEntry {
  return {
    time: new Date().toISOString(),
    method: "GET",
    host: "api.test",
    path: "/alpha",
    status: 200,
    bytes: 0,
    duration_ms: 1.2,
    ...over,
  };
}

function open() {
  act(() => {
    h.captured?.handlers?.onOpen?.();
  });
}

function emit(e: LogEntry) {
  act(() => {
    h.captured?.onEntry(e);
  });
}

describe("LogTailPanel", () => {
  it("shows live status and renders streamed entries", () => {
    render(<LogTailPanel />);
    expect(screen.getByText("connecting…")).toBeInTheDocument();

    open();
    expect(screen.getByText("live")).toBeInTheDocument();

    emit(entry({ path: "/alpha", status: 200 }));
    expect(screen.getByText("/alpha")).toBeInTheDocument();
    expect(screen.getByText("200")).toBeInTheDocument();
  });

  it("pause drops incoming lines until resumed", () => {
    render(<LogTailPanel />);
    open();
    emit(entry({ path: "/alpha" }));
    expect(screen.getByText("/alpha")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Pause"));
    expect(screen.getByText("paused")).toBeInTheDocument();

    emit(entry({ path: "/beta" }));
    expect(screen.queryByText("/beta")).not.toBeInTheDocument();
    expect(screen.getByText("/alpha")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Resume"));
    emit(entry({ path: "/gamma" }));
    expect(screen.getByText("/gamma")).toBeInTheDocument();
  });

  it("filters the visible tail", () => {
    render(<LogTailPanel />);
    open();
    emit(entry({ path: "/alpha" }));
    emit(entry({ path: "/beta" }));

    fireEvent.change(screen.getByPlaceholderText("Filter by method, path, status, host…"), {
      target: { value: "beta" },
    });
    expect(screen.getByText("/beta")).toBeInTheDocument();
    expect(screen.queryByText("/alpha")).not.toBeInTheDocument();
  });

  it("clear empties the tail", () => {
    render(<LogTailPanel />);
    open();
    emit(entry({ path: "/alpha" }));
    expect(screen.getByText("/alpha")).toBeInTheDocument();

    fireEvent.click(screen.getByText("Clear"));
    expect(screen.queryByText("/alpha")).not.toBeInTheDocument();
    expect(screen.getByText("Waiting for requests…")).toBeInTheDocument();
  });

  it("unsubscribes on unmount", () => {
    const { unmount } = render(<LogTailPanel />);
    unmount();
    expect(h.stop).toHaveBeenCalled();
  });
});
