import { useCallback, useEffect, useState } from "react";

// Generic localStorage-backed state for UI preferences (Milestone 4.6). Values
// are JSON-serialized under a namespaced key. Reads are defensive: a missing or
// corrupt entry falls back to the supplied default, and storage failures (e.g.
// private mode) degrade to in-memory state rather than throwing. A custom
// `validate` guard lets callers reject stale shapes after a schema change.

const PREFIX = "jul_pref:";

export function loadPreference<T>(
  key: string,
  fallback: T,
  validate?: (v: unknown) => v is T,
): T {
  try {
    const raw = localStorage.getItem(PREFIX + key);
    if (raw === null) return fallback;
    const parsed: unknown = JSON.parse(raw);
    if (validate && !validate(parsed)) return fallback;
    return parsed as T;
  } catch {
    return fallback;
  }
}

export function savePreference(key: string, value: unknown): void {
  try {
    localStorage.setItem(PREFIX + key, JSON.stringify(value));
  } catch {
    // ignore persistence failure
  }
}

/**
 * usePersistentState is useState that mirrors its value to localStorage under a
 * namespaced preference key, restoring it on next load.
 */
export function usePersistentState<T>(
  key: string,
  fallback: T,
  validate?: (v: unknown) => v is T,
): [T, (v: T) => void] {
  const [value, setValue] = useState<T>(() => loadPreference(key, fallback, validate));

  useEffect(() => {
    savePreference(key, value);
  }, [key, value]);

  const set = useCallback((v: T) => {
    setValue(v);
  }, []);

  return [value, set];
}