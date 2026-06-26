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
      baseVersion?: string;
      previewDiff: ConfigDiff;
      /** Candidate TOML for read-only display in the config editor. */
      candidate?: string;
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
