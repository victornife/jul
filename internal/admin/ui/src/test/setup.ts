/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import "@testing-library/jest-dom";
import { vi } from "vitest";

type FetchMockWithImplementation = typeof fetch & {
  getMockImplementation?: () =>
    | ((...args: Parameters<typeof fetch>) => ReturnType<typeof fetch>)
    | undefined;
};

vi.mock("@/api/client.ts", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/api/client.ts")>();
  return {
    ...actual,
    fetchPendingRestart: async () => {
      const currentFetch = globalThis.fetch as FetchMockWithImplementation;
      const implementation = currentFetch.getMockImplementation?.();
      if (
        typeof implementation === "function" &&
        implementation.toString().includes("pending-restart")
      ) {
        return actual.fetchPendingRestart();
      }
      return { pending: false };
    },
  };
});
