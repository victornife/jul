/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { ConfirmDialog } from "@/components/ConfirmDialog.tsx";
import { Drawer } from "@/components/Drawer.tsx";
import { ApplyOutcomeBanner } from "@/features/config/ApplyOutcomeBanner.tsx";
import type { ApplyOutcome } from "@/lib/applyOutcome.ts";

function DrawerHarness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => { setOpen(true); }}>
        Open drawer
      </button>
      {open && (
        <Drawer title="Keyboard drawer" onClose={() => { setOpen(false); }}>
          <button type="button">First action</button>
          <button type="button">Last action</button>
        </Drawer>
      )}
    </>
  );
}

function ConfirmHarness() {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button type="button" onClick={() => { setOpen(true); }}>
        Open confirmation
      </button>
      {open && (
        <ConfirmDialog
          title="Apply live?"
          confirmLabel="Apply live"
          onConfirm={() => { setOpen(false); }}
          onCancel={() => { setOpen(false); }}
        >
          The exact reviewed change will be applied.
        </ConfirmDialog>
      )}
    </>
  );
}

describe("Phase 5 accessibility closure", () => {
  it("keeps drawer keyboard focus inside, closes on Escape, and restores focus", () => {
    render(<DrawerHarness />);
    const trigger = screen.getByRole("button", { name: "Open drawer" });
    trigger.focus();
    fireEvent.click(trigger);

    const dialog = screen.getByRole("dialog", { name: "Keyboard drawer" });
    const buttons = within(dialog).getAllByRole("button");
    const panelClose = buttons.find((button) => button.textContent === "Close");
    const last = within(dialog).getByRole("button", { name: "Last action" });
    expect(panelClose).toBeDefined();
    expect(panelClose).toHaveFocus();

    last.focus();
    fireEvent.keyDown(last, { key: "Tab" });
    expect(panelClose).toHaveFocus();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Keyboard drawer" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("focuses the confirmation action, traps both Tab directions, and restores focus", () => {
    render(<ConfirmHarness />);
    const trigger = screen.getByRole("button", { name: "Open confirmation" });
    trigger.focus();
    fireEvent.click(trigger);

    const dialog = screen.getByRole("dialog", { name: "Apply live?" });
    const confirm = within(dialog).getByRole("button", { name: "Apply live" });
    const cancel = within(dialog).getByRole("button", { name: "Cancel" });
    expect(confirm).toHaveFocus();

    fireEvent.keyDown(confirm, { key: "Tab" });
    expect(cancel).toHaveFocus();
    fireEvent.keyDown(cancel, { key: "Tab", shiftKey: true });
    expect(confirm).toHaveFocus();

    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Apply live?" })).not.toBeInTheDocument();
    expect(trigger).toHaveFocus();
  });

  it("communicates apply state with text and an ARIA status/alert role, not colour alone", () => {
    const warning: ApplyOutcome = {
      kind: "partial-reload",
      severity: "warning",
      blocking: false,
      title: "Applied with a degraded subsystem",
      message: "HTTP is live; one subsystem still serves the previous generation.",
      failures: [{ name: "stream", detail: "listener reload failed" }],
    };
    const { rerender } = render(<ApplyOutcomeBanner outcome={warning} />);
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("Applied with a degraded subsystem");
    expect(alert).toHaveTextContent("stream");

    const success: ApplyOutcome = {
      kind: "full-live",
      severity: "success",
      blocking: false,
      title: "Configuration is live",
      message: "The reviewed configuration is serving.",
      failures: [],
    };
    rerender(<ApplyOutcomeBanner outcome={success} />);
    expect(screen.getByRole("status")).toHaveTextContent("Configuration is live");
  });
});
