import { useEffect, useRef, type ReactNode } from "react";

export interface ConfirmDialogProps {
  readonly title: string;
  readonly confirmLabel: string;
  readonly busy?: boolean;
  readonly danger?: boolean;
  readonly onConfirm: () => void;
  readonly onCancel: () => void;
  readonly children: ReactNode;
}

/**
 * Accessible confirmation modal for irreversible operations (config apply,
 * rollback). Confirmation is always explicit — never a single unguarded click.
 * Focus moves to the dialog on open and Escape cancels.
 */
export function ConfirmDialog({
  title,
  confirmLabel,
  busy = false,
  danger = false,
  onConfirm,
  onCancel,
  children,
}: ConfirmDialogProps) {
  const confirmRef = useRef<HTMLButtonElement | null>(null);

  useEffect(() => {
    confirmRef.current?.focus();
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent): void {
      if (e.key === "Escape" && !busy) onCancel();
    }
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [busy, onCancel]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4"
      role="dialog"
      aria-modal="true"
      aria-label={title}
    >
      <div className="flex w-full max-w-lg flex-col gap-4 rounded-lg border border-jul-border bg-jul-bg p-6 shadow-xl">
        <h2 className="text-lg font-semibold text-jul-text">{title}</h2>
        <div className="text-sm text-jul-muted">{children}</div>
        <div className="flex justify-end gap-3 pt-2">
          <button
            type="button"
            onClick={onCancel}
            disabled={busy}
            className="rounded-md border border-jul-border px-4 py-1.5 text-sm text-jul-muted hover:text-jul-text disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            ref={confirmRef}
            type="button"
            onClick={onConfirm}
            disabled={busy}
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
