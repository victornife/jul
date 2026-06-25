import { describe, it, expect, afterEach } from "vitest";
import { render, screen, fireEvent, cleanup, act } from "@testing-library/react";
import { AuthGate } from "@/app/AuthGate.tsx";
import { UNAUTHORIZED_EVENT } from "@/api/client.ts";

function fireUnauthorized(): void {
  act(() => {
    window.dispatchEvent(new CustomEvent(UNAUTHORIZED_EVENT));
  });
}

afterEach(() => {
  cleanup();
  sessionStorage.clear();
});

describe("AuthGate", () => {
  it("renders nothing until an unauthorized event fires", () => {
    render(<AuthGate />);
    expect(screen.queryByRole("dialog")).toBeNull();
    fireUnauthorized();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Admin token required")).toBeInTheDocument();
  });

  it("disables Save until a token is entered", () => {
    render(<AuthGate />);
    fireUnauthorized();
    const save = screen.getByRole("button", { name: /save & retry/i });
    expect(save).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Token"), { target: { value: "secret-token" } });
    expect(save).toBeEnabled();
  });

  it("can be dismissed without saving", () => {
    render(<AuthGate />);
    fireUnauthorized();
    fireEvent.click(screen.getByRole("button", { name: /dismiss/i }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("warns against the ?token= pattern in the prompt copy", () => {
    render(<AuthGate />);
    fireUnauthorized();
    expect(screen.getByText(/leaks into logs, history, and referrers/i)).toBeInTheDocument();
  });
});
