// In-memory, single-session handoff for moving a generated TOML draft from the
// setup wizard into the Config editor. A module-level slot is sufficient: the
// SPA keeps it across client-side navigation and intentionally drops it on a
// full reload (a stale wizard draft should not resurface).
let pending: string | null = null;

export function setPendingDraft(toml: string): void {
  pending = toml;
}

/** Returns the pending draft (if any) and clears it. */
export function takePendingDraft(): string | null {
  const value = pending;
  pending = null;
  return value;
}
