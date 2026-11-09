/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

export function openResourceDisclosure(details: HTMLDetailsElement | null): void {
  if (details) details.open = true;
}
