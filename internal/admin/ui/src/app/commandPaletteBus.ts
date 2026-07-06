/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

// Command-palette event bus. Kept in its own module (not in CommandPalette.tsx)
// so the component file only exports components — React Fast Refresh requires
// that, and it lets any affordance open the palette without lifting its state.

/**
 * Custom DOM event the palette listens for so any affordance (e.g. a header
 * button) can open it without the open state having to be lifted out of the
 * component.
 */
export const COMMAND_PALETTE_EVENT = "jul:open-command-palette";

/** Open the command palette from anywhere (header button, help menu, …). */
export function openCommandPalette(): void {
  window.dispatchEvent(new Event(COMMAND_PALETTE_EVENT));
}
