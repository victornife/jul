/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

/**
 * Render tests for PanelError: it surfaces the taxonomy headline + message,
 * exposes the kind via a data attribute, announces itself with role="alert",
 * and only offers Retry for retryable failures.
 */
import { describe, it, expect, afterEach, vi } from "vitest";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { ApiError } from "@/api/client.ts";
import { PanelError } from "@/components/PanelError.tsx";

afterEach(() => {
  cleanup();
});

describe("PanelError", () => {
  it("renders the taxonomy title and message for a server error", () => {
    render(
      <PanelError error={new ApiError("/api/routes", 500, "boom")} resource="routes" />,
    );
    const alert = screen.getByRole("alert");
    expect(alert).toHaveAttribute("data-error-kind", "server");
    expect(screen.getByText("Server error")).toBeInTheDocument();
    expect(screen.getByText(/while loading routes/i)).toBeInTheDocument();
  });

  it("offers Retry for a retryable error and invokes the callback", () => {
    const onRetry = vi.fn();
    render(
      <PanelError
        error={new ApiError("/api/routes", 503, "down")}
        resource="routes"
        onRetry={onRetry}
      />,
    );
    const retry = screen.getByRole("button", { name: /retry/i });
    fireEvent.click(retry);
    expect(onRetry).toHaveBeenCalledTimes(1);
  });

  it("hides Retry for a non-retryable error even when onRetry is provided", () => {
    render(
      <PanelError
        error={new ApiError("/api/routes", 401, "unauthorized")}
        resource="routes"
        onRetry={() => undefined}
      />,
    );
    expect(screen.getByRole("alert")).toHaveAttribute("data-error-kind", "unauthorized");
    expect(screen.queryByRole("button", { name: /retry/i })).toBeNull();
  });

  it("classifies a fetch network failure", () => {
    render(<PanelError error={new TypeError("Failed to fetch")} resource="streams" />);
    expect(screen.getByRole("alert")).toHaveAttribute("data-error-kind", "network");
    expect(screen.getByText("Can't reach the server")).toBeInTheDocument();
  });
});
