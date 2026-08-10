/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, type ReactNode } from "react";
import { useFocusTrap } from "@/lib/useFocusTrap.ts";

export interface ConfirmDialogProps {
  readonly title: string;
  readonly confirmLabel: string;
  readonly busy?: boolean;
  readonly confirmDisabled?: boolean;
  readonly cancelDisabled?: boolean;
  readonly danger?: boolean;
  readonly onConfirm: () => void;
  readonly onCancel: () => void;
  readonly children: ReactNode;
}

/**
 * Accessible confirmation modal for irreversible operations (config apply,
 * rollback). Confirmation is always explicit — never a single unguarded click.
 * Focus moves to the primary action on open, is trapped within the dialog while
 * it is open, and returns to the trigger on close; Escape cancels.
 */
export function ConfirmDialog({
  title,
  confirmLabel,
  busy = false,
  confirmDisabled = false,
  cancelDisabled = false,
  danger = false,
  onConfirm,
  onCancel,
  children,
}: ConfirmDialogProps) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const confirmRef = useRef<HTMLButtonElement | null>(null);
  useFocusTrap(dialogRef);

  useEffect(() => {
    confirmRef.current?.focus();
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent): void {
      if (e.key === "Escape" && !busy && !cancelDisabled) onCancel();
    }
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [busy, cancelDisabled, onCancel]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={title}
    >
      <div
        className="flex w-full max-w-lg flex-col gap-4 overflow-hidden rounded-lg border border-jul-border bg-jul-bg p-6 shadow-xl outline-none"
        ref={dialogRef}
        tabIndex={-1}
        style={{ maxHeight: "calc(100vh - 2rem)" }}
      >
        <h2 className="shrink-0 text-lg font-semibold text-jul-text">{title}</h2>
        <div className="min-h-0 flex-1 overflow-y-auto text-sm text-jul-muted">{children}</div>
        <div className="flex shrink-0 justify-end gap-3 pt-2">
          <button
            type="button"
            onClick={onCancel}
            disabled={busy || cancelDisabled}
            className="rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-muted hover:text-jul-text disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            ref={confirmRef}
            type="button"
            onClick={onConfirm}
            disabled={busy || confirmDisabled}
            className={`rounded-md px-4 py-1.5 text-sm font-medium disabled:opacity-50 ${
              danger
                ? "bg-jul-danger/90 text-jul-bg hover:bg-jul-danger"
                : "bg-jul-accent text-jul-bg hover:brightness-110"
            }`}
          >
            {busy ? "Working…" : confirmLabel}
          </button>
        </div>
      </div>
    </div>
  );
}
