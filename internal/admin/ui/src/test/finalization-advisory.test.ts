/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { describe, it, expect } from "vitest";
import { deriveFinalizationAdvisory } from "@/lib/finalizationAdvisory.ts";
import { ManagedApplyRecordSchema, type ManagedApplyRecord } from "@/api/client.ts";

function terminalRecord(overrides: Record<string, unknown> = {}): ManagedApplyRecord {
  return ManagedApplyRecordSchema.parse({
    id: "rl_abc",
    state: "terminal",
    result: { ok: true, apply_id: "rl_abc", mode: "hot" },
    ...overrides,
  });
}

describe("deriveFinalizationAdvisory", () => {
  it("returns null for a missing record", () => {
    expect(deriveFinalizationAdvisory(null)).toBeNull();
  });

  it("returns null when the record finalized cleanly", () => {
    expect(deriveFinalizationAdvisory(terminalRecord())).toBeNull();
  });

  it("surfaces a degraded configuration-history snapshot as an advisory", () => {
    const advisory = deriveFinalizationAdvisory(
      terminalRecord({ history_error: "snapshot write failed" }),
    );
    expect(advisory).not.toBeNull();
    expect(advisory?.title).toBe(
      "Configuration applied, but recovery/audit finalization degraded",
    );
    expect(advisory?.messages).toEqual(["Configuration history degraded: snapshot write failed"]);
  });

  it("surfaces a degraded ledger finalization as an advisory", () => {
    const advisory = deriveFinalizationAdvisory(
      terminalRecord({ finalization_error: "ledger append degraded" }),
    );
    expect(advisory?.messages).toEqual([
      "Managed apply finalization degraded: ledger append degraded",
    ]);
  });

  it("combines both degradations and passes through the history snapshot id", () => {
    const advisory = deriveFinalizationAdvisory(
      terminalRecord({
        history_error: "snapshot write failed",
        finalization_error: "ledger append degraded",
        history_snapshot_id: "snap-42",
      }),
    );
    expect(advisory?.messages).toEqual([
      "Configuration history degraded: snapshot write failed",
      "Managed apply finalization degraded: ledger append degraded",
    ]);
    expect(advisory?.historySnapshotID).toBe("snap-42");
  });

  it("is advisory only — a degraded sidecar on an ok=true record is never an error", () => {
    const advisory = deriveFinalizationAdvisory(
      terminalRecord({
        result: { ok: true, apply_id: "rl_abc", mode: "hot" },
        finalization_error: "audit sink unavailable",
      }),
    );
    // The advisory exists but carries no failure/severity signal — it only
    // describes the degraded finalization, never the apply outcome.
    expect(advisory).not.toBeNull();
    expect(advisory?.historySnapshotID).toBeUndefined();
  });
});
