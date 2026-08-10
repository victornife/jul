/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, expect, it, vi } from "vitest";
import type { QueryClient } from "@tanstack/react-query";
import {
  configInvalidationKeys,
  invalidateConfigurationState,
} from "@/lib/configInvalidation.ts";

describe("issue #81 targeted invalidation", () => {
  it("invalidates every required query family instead of relying on one broad call", async () => {
    const invalidateQueries = vi.fn().mockResolvedValue(undefined);
    await invalidateConfigurationState({ invalidateQueries } as unknown as QueryClient);
    expect(invalidateQueries).toHaveBeenCalledTimes(configInvalidationKeys.length);
    for (const queryKey of configInvalidationKeys) {
      expect(invalidateQueries).toHaveBeenCalledWith({ queryKey: [...queryKey] });
    }
    expect(invalidateQueries).not.toHaveBeenCalledWith({});
  });
});
