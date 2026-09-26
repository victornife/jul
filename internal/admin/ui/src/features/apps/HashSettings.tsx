/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import {
  HASH_FALLBACK_OPTIONS,
  HASH_KEY_OPTIONS,
  hashApplicability,
  missingKeyExplanation,
  type AppHashDraft,
  type HashFallback,
  type HashKeyKind,
} from "@/lib/appHash.ts";

export interface HashSettingsFieldsProps {
  readonly value: AppHashDraft;
  readonly onChange: (next: AppHashDraft) => void;
  readonly disabled?: boolean;
}

/** Key source, name and missing-key fallback for strategy consistent_hash. */
export function HashSettingsFields({ value, onChange, disabled = false }: HashSettingsFieldsProps) {
  const needsName = value.key !== "client_ip";
  const selectClass =
    "w-full rounded-md border border-jul-border bg-jul-bg px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent disabled:opacity-50";
  return (
    <fieldset
      className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3"
      aria-label="Consistent hash settings"
    >
      <legend className="px-1 text-xs font-semibold uppercase tracking-wider text-jul-muted">
        Affinity key
      </legend>
      <label className="block space-y-1">
        <span className="text-xs text-jul-muted">Key source</span>
        <select
          value={value.key}
          disabled={disabled}
          onChange={(event) => {
            onChange({ ...value, key: event.target.value as HashKeyKind });
          }}
          className={selectClass}
        >
          {HASH_KEY_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>
      {needsName && (
        <label className="block space-y-1">
          <span className="text-xs text-jul-muted">
            {value.key === "header" ? "Header name" : "Cookie name"}
          </span>
          <input
            value={value.name}
            disabled={disabled}
            placeholder={value.key === "header" ? "X-Tenant" : "session_id"}
            onChange={(event) => {
              onChange({ ...value, name: event.target.value });
            }}
            className={`${selectClass} font-mono`}
          />
        </label>
      )}
      <label className="block space-y-1">
        <span className="text-xs text-jul-muted">Without a usable key</span>
        <select
          value={value.fallback}
          disabled={disabled}
          onChange={(event) => {
            onChange({ ...value, fallback: event.target.value as HashFallback });
          }}
          className={selectClass}
        >
          {HASH_FALLBACK_OPTIONS.map((option) => (
            <option key={option.value} value={option.value}>
              {option.label}
            </option>
          ))}
        </select>
      </label>
      <p className="text-xs text-jul-muted">Applies to: {hashApplicability(value.key)}.</p>
      <p className="text-xs text-jul-muted">{missingKeyExplanation(value.key, value.fallback)}</p>
      <p className="text-xs text-jul-muted">
        Key values are never logged or used as metric labels. Adding or removing a backend moves
        only the keys that must move.
      </p>
    </fieldset>
  );
}
