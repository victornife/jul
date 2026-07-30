/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { ManagedApplyRecord } from "@/api/client.ts";

// deriveFinalizationAdvisory projects the AC-14 finalization provenance of a
// terminal managed-apply record into a single advisory, or null when the sidecar
// finalized cleanly. These fields are orthogonal to the reload success/failure:
// a committed (ok=true) apply or rollback can still have degraded its config
// history snapshot or its ledger/audit finalization, and that MUST surface as an
// advisory — never as an apply failure and never as a readiness/success signal.
// Sharing one derivation keeps ConfigPanel and HistoryPanel from diverging on
// wording or on the invariant that finalization degradation is advisory only.
export interface FinalizationAdvisory {
  readonly title: string;
  readonly messages: readonly string[];
  readonly historySnapshotID?: string;
}

export function deriveFinalizationAdvisory(
  record: ManagedApplyRecord | null,
): FinalizationAdvisory | null {
  if (!record) return null;

  const messages: string[] = [];
  if (record.history_error) {
    messages.push(`Configuration history degraded: ${record.history_error}`);
  }
  if (record.finalization_error) {
    messages.push(`Managed apply finalization degraded: ${record.finalization_error}`);
  }

  if (messages.length === 0) return null;

  return {
    title: "Configuration applied, but recovery/audit finalization degraded",
    messages,
    ...(record.history_snapshot_id !== undefined
      ? { historySnapshotID: record.history_snapshot_id }
      : {}),
  };
}
