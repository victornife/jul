import { useEffect, type ReactNode } from "react";

export interface DrawerProps {
  readonly title: string;
  readonly subtitle?: string;
  readonly onClose: () => void;
  readonly children: ReactNode;
  readonly footer?: ReactNode;
}

/**
 * Right-hand slide-over panel used for route and app detail/edit surfaces.
 * Escape closes it and focus stays within the document; the backdrop click
 * also dismisses it. It is intentionally simple (no focus trap library) to keep
 * the bundle lean.
 */
export function Drawer({ title, subtitle, onClose, children, footer }: DrawerProps) {
  useEffect(() => {
    function onKey(e: KeyboardEvent): void {
      if (e.key === "Escape") onClose();
    }
    window.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("keydown", onKey);
    };
  }, [onClose]);

  return (
    <div className="fixed inset-0 z-40 flex justify-end" role="dialog" aria-modal="true" aria-label={title}>
      <button
        type="button"
        aria-label="Close"
        onClick={onClose}
        className="absolute inset-0 bg-black/50"
      />
      <div className="relative flex h-full w-full max-w-xl flex-col border-l border-jul-border bg-jul-bg shadow-2xl">
        <div className="flex items-start justify-between gap-4 border-b border-jul-border px-6 py-4">
          <div className="min-w-0">
            <h2 className="truncate text-lg font-semibold text-jul-text">{title}</h2>
            {subtitle && <p className="truncate text-xs text-jul-muted">{subtitle}</p>}
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-jul-border px-2 py-1 text-sm text-jul-muted hover:text-jul-text"
          >
            Close
          </button>
        </div>
        <div className="flex-1 overflow-auto px-6 py-5">{children}</div>
        {footer && (
          <div className="border-t border-jul-border px-6 py-4">{footer}</div>
        )}
      </div>
    </div>
  );
}