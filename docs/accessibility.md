# Console accessibility

The admin Console is part of Jul.IA's *Friendliest* pillar
([ADR 0004](adr/0004-console-ui-invariants.md)): *anyone should be able to
operate Jul.IA easily* — and that explicitly includes operators who navigate by
keyboard or screen reader. This page records the accessibility behaviours the
Console guarantees so they are testable and do not regress as panels grow.

## Keyboard operation

Every interactive control in the Console is a real, focusable element (a
`button`, `a`, `input`, `select`, or `textarea`) reachable with `Tab` /
`Shift+Tab` and actionable with `Enter` / `Space`. The Console adds no
mouse-only affordances: anything you can click you can also reach and trigger
from the keyboard.

| Shortcut | Action |
| --- | --- |
| `Ctrl/Cmd + K` | Open the command palette (jump to any primary destination) |
| `↑` / `↓` | Move the selection within the command palette |
| `Enter` | Activate the focused control / selected palette destination |
| `Escape` | Dismiss the command palette, a drawer, or a confirmation dialog |

## Dialogs trap and restore focus

Modal surfaces — the route/app slide-over **Drawer**, the **Confirm** dialog
guarding irreversible applies and rollbacks, the shared **Modal** primitive, the
**command palette**, and the re-authentication **token prompt** — all behave as
proper modal dialogs (WCAG 2.4.3 *Focus Order*; WAI-ARIA Authoring Practices for
the dialog pattern):

1. **Focus moves in on open.** When a dialog opens, focus moves into it — to the
   primary action where one exists (for example the Confirm button), otherwise to
   the first focusable control. Keyboard users never have to tab in from the page
   behind it.
2. **Focus is trapped while open.** `Tab` from the last focusable control wraps to
   the first, and `Shift+Tab` from the first wraps to the last, so focus cannot
   escape to the obscured page underneath the dialog.
3. **Focus is restored on close.** When the dialog closes, focus returns to the
   element that was focused before it opened — typically the control that opened
   it — so keyboard users land back where they were.

This is implemented by a single shared hook, `useFocusTrap`
(`internal/admin/ui/src/lib/useFocusTrap.ts`), wired into each dialog container.
It deliberately uses no third-party library, keeping the embedded SPA within its
size budget (invariant 4 of [ADR 0004](adr/0004-console-ui-invariants.md)).
`Escape`-to-close stays with each dialog that already owns that handler.

## Semantics and announcements

- **Dialog roles.** Modal surfaces carry `role="dialog"`, `aria-modal="true"`,
  and an `aria-label`, so assistive technology announces them as modal dialogs.
- **Live error states.** A panel's typed failure state
  ([console.md → Loading and failure states](console.md#loading-and-failure-states))
  renders as `role="alert"`, so the cause and recommended action are announced
  rather than silently appearing.
- **Tabs and lists.** Navigational tab strips use `role="tablist"` / `role="tab"`
  / `role="tabpanel"`, and the active tab exposes `aria-selected`.

## Known limitations

- The Console targets keyboard operability and screen-reader-friendly semantics
  for its core flows; it has not yet been audited against the full WCAG 2.2 AA
  success-criteria set.
- Colour themes are tuned for contrast in both light and dark modes, but a
  formal contrast-ratio audit across every token pairing is still open.

## Verification

Focus-trap behaviour is covered by
`internal/admin/ui/src/test/focus-trap.test.tsx` (initial focus, `Tab` /
`Shift+Tab` wrapping, focus restoration on close, and self-gating dialogs that
arm only once opened), plus the dialog-specific render tests.
