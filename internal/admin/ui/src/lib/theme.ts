/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useEffect, useState } from "react";

// Theme preference for the Console (Milestone 4.1). "system" follows the OS via
// prefers-color-scheme; "light"/"dark" pin the palette. The choice is persisted
// in localStorage and applied by setting data-theme on <html>, which the light
// token overrides in globals.css key off.

export type ThemePreference = "system" | "light" | "dark";

const STORAGE_KEY = "jul_theme";

export function loadThemePreference(): ThemePreference {
  try {
    const v = localStorage.getItem(STORAGE_KEY);
    if (v === "light" || v === "dark" || v === "system") return v;
  } catch {
    // localStorage unavailable (private mode / SSR) — fall back to system.
  }
  return "system";
}

function systemPrefersDark(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: dark)").matches
  );
}

/** Resolves a preference to the concrete theme to apply right now. */
export function resolveTheme(pref: ThemePreference): "light" | "dark" {
  if (pref === "system") return systemPrefersDark() ? "dark" : "light";
  return pref;
}

/** Sets data-theme on <html> to the resolved concrete theme. */
function applyTheme(pref: ThemePreference): void {
  if (typeof document === "undefined") return;
  document.documentElement.setAttribute("data-theme", resolveTheme(pref));
}

/**
 * useTheme exposes the persisted preference and a setter that updates
 * localStorage and the document attribute. While the preference is "system" it
 * also tracks live OS changes.
 */
export function useTheme(): {
  preference: ThemePreference;
  setPreference: (p: ThemePreference) => void;
} {
  const [preference, setPreferenceState] = useState<ThemePreference>(loadThemePreference);

  useEffect(() => {
    applyTheme(preference);
  }, [preference]);

  useEffect(() => {
    if (preference !== "system") return;
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    const onChange = (): void => {
      applyTheme("system");
    };
    mq.addEventListener("change", onChange);
    return () => {
      mq.removeEventListener("change", onChange);
    };
  }, [preference]);

  const setPreference = (p: ThemePreference): void => {
    try {
      localStorage.setItem(STORAGE_KEY, p);
    } catch {
      // ignore persistence failure
    }
    setPreferenceState(p);
  };

  return { preference, setPreference };
}

/**
 * Applies the persisted theme as early as possible (called from main.tsx before
 * React renders) so there is no flash of the wrong theme on load.
 */
export function initThemeEarly(): void {
  applyTheme(loadThemePreference());
}