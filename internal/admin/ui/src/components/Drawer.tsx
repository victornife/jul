/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useRef, type ReactNode } from "react";
import { useFocusTrap } from "@/lib/useFocusTrap.ts";

export interface DrawerProps {
  readonly title: string;
  readonly subtitle?: string;
  readonly onClose: () => void;
  readonly closeLabel?: string | undefined;
  readonly children: ReactNode;
  readonly footer?: ReactNode;
}

/**
 * Right-hand slide-over panel used for route and app detail/edit surfaces.
 * Escape closes it and the backdrop click also dismisses it. While open, focus
 * is trapped within the panel (via useFocusTrap, no external library to keep the
 * bundle lean) so keyboard users cannot tab into the obscured page behind it,
 * and focus returns to the trigger on close.
 */
export function Drawer({ title, subtitle, onClose, closeLabel, children, footer }: DrawerProps) {
  const panelRef = useRef<HTMLDivElement>(null);
  useFocusTrap(panelRef);

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
      <div
        ref={panelRef}
        tabIndex={-1}
        className="relative flex h-full w-full max-w-3xl flex-col overflow-hidden border-l border-jul-border bg-jul-bg shadow-2xl outline-none"
      >
        <div className="flex shrink-0 items-start justify-between gap-4 border-b border-jul-border px-4 py-4 sm:px-6">
          <div className="min-w-0">
            <h2 className="break-words text-lg font-semibold text-jul-text">{title}</h2>
            {subtitle && <p className="break-words text-xs text-jul-muted">{subtitle}</p>}
          </div>
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-jul-border px-2 py-1 text-sm text-jul-muted hover:text-jul-text"
          >
            {closeLabel ?? "Close"}
          </button>
        </div>
        <div className="drawer-body min-h-0 min-w-0 flex-1 overflow-x-hidden overflow-y-auto px-4 py-5 sm:px-6">{children}</div>
        {footer && (
          <div className="max-h-[35%] shrink-0 overflow-y-auto border-t border-jul-border px-4 py-4 sm:px-6">{footer}</div>
        )}
      </div>
    </div>
  );
}