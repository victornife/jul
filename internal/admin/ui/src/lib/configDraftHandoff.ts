import type { ConfigPatch, ConfigDiff } from "@/api/client.ts";

/**
 * A pending draft handed off between editors and the ConfigPanel.
 * - toml: a raw candidate string for review in the raw editor
 * - patch: a structured, server-side patch with a precomputed diff preview
 *   so the ConfigPanel can show the diff without re-calling /api/config/diff,
 *   and apply atomically via /api/config/patch/apply.
 */
export type PendingDraft =
  | { kind: "toml"; toml: string }
  | {
      kind: "patch";
      ops: ConfigPatch[];
      // Optional, and may be passed explicitly as undefined (the patch preview
      // does not always carry a base_version), so under exactOptionalPropertyTypes
      // the union member must accept undefined rather than only an absent key.
      baseVersion?: string | undefined;
      previewDiff: ConfigDiff;
      /** Candidate TOML for read-only display in the config editor. */
      candidate?: string | undefined;
    };

let pending: PendingDraft | null = null;

export function setPendingDraft(draft: PendingDraft): void {
  pending = draft;
}

/** Returns the pending draft (if any) and clears it. */
export function takePendingDraft(): PendingDraft | null {
  const value = pending;
  pending = null;
  return value;
}
