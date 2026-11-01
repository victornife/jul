/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import type { CacheCutoverMode } from "@/api/client.ts";

export function CacheCutoverChoice({
  value,
  onChange,
  disabled = false,
}: {
  readonly value: CacheCutoverMode;
  readonly onChange: (value: CacheCutoverMode) => void;
  readonly disabled?: boolean;
}) {
  return (
    <label className="block space-y-1 text-sm">
      <span>Cache behavior for this change</span>
      <select
        aria-label="Cache behavior for this change"
        className="block rounded border border-jul-border bg-jul-surface px-2 py-1"
        value={value}
        disabled={disabled}
        onChange={(event) => {
          onChange(event.target.value as CacheCutoverMode);
        }}
      >
        <option value="">Use configured reload policy</option>
        <option value="retain">Retain compatible cached responses</option>
        <option value="invalidate">Invalidate changed route/backend cache</option>
      </select>
      <span className="block text-xs text-jul-muted">
        Applies to this live change only. Security and policy compatibility remain enforced. For a
        staged restart, set reload_policy in the configuration.
      </span>
    </label>
  );
}
