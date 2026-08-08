/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useCallback } from "react";
import type { ConfigPatch } from "@/api/client.ts";
import {
  describePatchBatchError,
  useRunPatchBatch,
} from "@/lib/useRunPatchBatch.ts";

/** Thin one-operation compatibility wrapper around the shared ordered batch hook. */
export function useRunPatch(): {
  readonly error: string | null;
  readonly busy: boolean;
  readonly run: (patch: ConfigPatch) => void;
} {
  const { error, busy, run: runBatch } = useRunPatchBatch();
  const run = useCallback(
    (patch: ConfigPatch): void => {
      void runBatch([patch]);
    },
    [runBatch],
  );
  return { error: describePatchBatchError(error), busy, run };
}
