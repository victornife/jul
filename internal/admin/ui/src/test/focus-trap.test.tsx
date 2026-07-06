/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect, afterEach } from "vitest";
import { useRef, useState } from "react";
import { render, screen, fireEvent, cleanup } from "@testing-library/react";
import { useFocusTrap } from "@/lib/useFocusTrap.ts";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";

afterEach(() => {
  cleanup();
  document.body.innerHTML = "";
});

function Trap() {
  const ref = useRef<HTMLDivElement>(null);
  useFocusTrap(ref);
  return (
    <div ref={ref} tabIndex={-1} role="dialog" aria-label="trap">
      <button type="button">first</button>
      <button type="button">middle</button>
      <button type="button">last</button>
    </div>
  );
}

/** Self-gating dialog that mirrors AuthGate/CommandPalette (`if (!open) return null`). */
function GatedTrap() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useFocusTrap(ref, open);
  return (
    <div>
      <button
        type="button"
        data-testid="opener"
        onClick={() => {
          setOpen(true);
        }}
      >
        open
      </button>
      {open && (
        <div ref={ref} tabIndex={-1} role="dialog" aria-label="gated">
          <button type="button">inner-first</button>
          <button type="button">inner-last</button>
        </div>
      )}
    </div>
  );
}

describe("useFocusTrap", () => {
  it("moves focus to the first focusable element on mount", () => {
    render(<Trap />);
    expect(document.activeElement).toBe(screen.getByText("first"));
  });

  it("wraps Tab from the last focusable element back to the first", () => {
    render(<Trap />);
    const last = screen.getByText("last");
    last.focus();
    fireEvent.keyDown(last, { key: "Tab" });
    expect(document.activeElement).toBe(screen.getByText("first"));
  });

  it("wraps Shift+Tab from the first focusable element to the last", () => {
    render(<Trap />);
    const first = screen.getByText("first");
    first.focus();
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(screen.getByText("last"));
  });

  it("does not hijack Tab between interior elements", () => {
    render(<Trap />);
    const first = screen.getByText("first");
    first.focus();
    // Tab off a non-boundary element is left to the browser (no preventDefault),
    // so the trap must not move focus itself.
    fireEvent.keyDown(first, { key: "Tab" });
    expect(document.activeElement).toBe(first);
  });

  it("restores focus to the previously focused element on unmount", () => {
    const trigger = document.createElement("button");
    document.body.appendChild(trigger);
    trigger.focus();
    expect(document.activeElement).toBe(trigger);

    const { unmount } = render(<Trap />);
    expect(document.activeElement).toBe(screen.getByText("first"));

    unmount();
    expect(document.activeElement).toBe(trigger);
  });

  it("arms only once a self-gating dialog opens", () => {
    render(<GatedTrap />);
    const opener = screen.getByTestId("opener");
    opener.focus();
    // Closed: the trap is inert and focus stays on the trigger.
    expect(document.activeElement).toBe(opener);

    fireEvent.click(opener);
    // Open: focus is pulled into the dialog.
    expect(document.activeElement).toBe(screen.getByText("inner-first"));
  });
});

describe("ConfirmDialog focus management", () => {
  it("focuses the primary action on open and restores focus on close", () => {
    const trigger = document.createElement("button");
    document.body.appendChild(trigger);
    trigger.focus();

    const { unmount } = render(
      <ConfirmDialog
        title="Apply config?"
        confirmLabel="Apply"
        onConfirm={() => undefined}
        onCancel={() => undefined}
      >
        Confirm the change.
      </ConfirmDialog>,
    );
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Apply" }));

    unmount();
    expect(document.activeElement).toBe(trigger);
  });

  it("traps Tab within the dialog", () => {
    render(
      <ConfirmDialog
        title="Apply config?"
        confirmLabel="Apply"
        onConfirm={() => undefined}
        onCancel={() => undefined}
      >
        Confirm the change.
      </ConfirmDialog>,
    );
    const confirm = screen.getByRole("button", { name: "Apply" });
    confirm.focus();
    // Apply is the last focusable control, so Tab wraps back to Cancel.
    fireEvent.keyDown(confirm, { key: "Tab" });
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Cancel" }));
  });
});
