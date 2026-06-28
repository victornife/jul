import { useEffect, type RefObject } from "react";

const FOCUSABLE_SELECTOR = [
  "a[href]",
  "button:not([disabled])",
  "input:not([disabled])",
  "select:not([disabled])",
  "textarea:not([disabled])",
  '[tabindex]:not([tabindex="-1"])',
].join(",");

function focusable(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)).filter(
    (el) => !el.hasAttribute("disabled") && !el.hidden && el.getAttribute("aria-hidden") !== "true",
  );
}

/**
 * useFocusTrap confines keyboard focus to the referenced dialog while it is
 * mounted (WCAG 2.4.3 Focus Order). On open it moves focus into the dialog —
 * unless a control inside already holds focus, so components that focus a
 * specific element (a primary button, a search input) keep that behaviour. Tab
 * and Shift+Tab wrap within the dialog, and on unmount focus returns to the
 * element that was focused before the dialog opened, so keyboard users land back
 * where they triggered it. It deliberately uses no library to keep the bundle
 * lean; Escape-to-close stays with each dialog that already owns that handler.
 *
 * `active` lets dialogs that self-gate with an early `return null` (rather than
 * being conditionally mounted by a parent) re-arm the trap when they open: pass
 * the open flag so the effect re-runs as the dialog DOM appears and disappears.
 */
export function useFocusTrap(ref: RefObject<HTMLElement | null>, active = true): void {
  useEffect(() => {
    if (!active) return;
    const container = ref.current;
    if (!container) return;
    const node: HTMLElement = container;

    const previouslyFocused = document.activeElement as HTMLElement | null;

    // Move focus into the dialog unless a child already holds it (e.g. an input
    // with autoFocus, or a component that focused its primary action).
    if (!node.contains(document.activeElement)) {
      (focusable(node)[0] ?? node).focus();
    }

    function onKeyDown(e: KeyboardEvent): void {
      if (e.key !== "Tab") return;
      const els = focusable(node);
      const first = els[0];
      const last = els[els.length - 1];
      if (!first || !last) {
        e.preventDefault();
        node.focus();
        return;
      }
      const activeEl = document.activeElement;
      if (e.shiftKey) {
        if (activeEl === first || !node.contains(activeEl)) {
          e.preventDefault();
          last.focus();
        }
      } else if (activeEl === last || !node.contains(activeEl)) {
        e.preventDefault();
        first.focus();
      }
    }

    node.addEventListener("keydown", onKeyDown);
    return () => {
      node.removeEventListener("keydown", onKeyDown);
      // Restore focus to where it was before the dialog opened. The optional
      // chaining guards against the previously focused element being null.
      previouslyFocused?.focus();
    };
  }, [ref, active]);
}
