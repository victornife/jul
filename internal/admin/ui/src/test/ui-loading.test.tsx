/**
 * Render tests for the shared async-feedback primitives (Phase 2): Spinner is a
 * decorative animated indicator and Loading is the standard role="status"
 * progress line used by every panel.
 */
import { describe, it, expect, afterEach } from "vitest";
import { render, screen, cleanup } from "@testing-library/react";
import { Spinner, Loading } from "@/components/ui.tsx";

afterEach(() => {
  cleanup();
});

describe("Spinner", () => {
  it("renders a decorative, animated indicator", () => {
    const { container } = render(<Spinner />);
    const el = container.querySelector("span");
    expect(el).not.toBeNull();
    expect(el).toHaveClass("animate-spin");
    expect(el).toHaveAttribute("aria-hidden", "true");
  });

  it("merges caller classes onto the base styles", () => {
    const { container } = render(<Spinner className="text-jul-accent" />);
    const el = container.querySelector("span");
    expect(el).toHaveClass("text-jul-accent");
    expect(el).toHaveClass("animate-spin");
  });
});

describe("Loading", () => {
  it("announces progress via role=status with the default label", () => {
    render(<Loading />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading…");
  });

  it("uses the provided label", () => {
    render(<Loading label="Loading routes…" />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading routes…");
  });
});
