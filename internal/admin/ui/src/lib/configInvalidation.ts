/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { QueryClient } from "@tanstack/react-query";

/** Targeted post-mutation query families required by the configuration contract. */
export const configInvalidationKeys = [
  ["raw-config"],
  ["traffic-controls"],
  ["routes"],
  ["apps"],
  ["overview"],
  ["runtime-overview"],
  ["pending-restart"],
  ["history"],
  ["config-history"],
  ["managed-apply-record"],
  ["managed-apply"],
] as const;

export async function invalidateConfigurationState(queryClient: QueryClient): Promise<void> {
  await Promise.all(
    configInvalidationKeys.map((queryKey) => queryClient.invalidateQueries({ queryKey: [...queryKey] })),
  );
}
