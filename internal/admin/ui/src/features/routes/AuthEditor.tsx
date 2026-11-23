/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import {
  type ConfigDiff,
  type ConfigPatch,
  type LocationAuthPatch,
  type LocationAuthState,
  type RouteTarget,
} from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { describePatchBatchError, useRunPatchBatch } from "@/lib/useRunPatchBatch.ts";
import { ConfigConsequences } from "@/features/config/DiffView.tsx";
import {
  authWarnings,
  AUTH_METHODS,
  emptyAuthDraft,
  type AuthDraft,
  type AuthMethod,
} from "@/lib/routeToml.ts";
import { AuthFields } from "@/features/routes/RouteEditor.tsx";

export interface AuthEditorProps {
  /** The location whose access-control rule is being edited. */
  readonly target: RouteTarget;
 readonly baseVersion?: string | undefined;
  /**
   * The location's current rule, from the route projection, used to seed the
   * form. Absent when adding a rule to a location that has none.
   */
  readonly seed?: LocationAuthState | undefined;
  /**
   * Whether the location already has an auth rule. When false (adding a fresh
   * rule) the "Clear" action is hidden — there is nothing to remove yet.
   * Defaults to whether a seed was supplied.
   */
  readonly existing?: boolean;
  readonly onClose: () => void;
}

// EDITABLE_METHODS are the concrete mechanisms the editor offers. "none" is not
// one of them: removing a rule is the explicit Clear action, never a method that
// would persist an inert, allow-all auth block.
const EDITABLE_METHODS = AUTH_METHODS.filter((m) => m.value !== "none");

// seedDraft converts the no-secrets projection state into the editor's draft. A
// fresh add (no seed) starts on the CIDR method.
function seedDraft(seed: LocationAuthState | undefined): AuthDraft {
  const d = emptyAuthDraft();
  if (!seed) return { ...d, method: "cidr" };
  return {
    ...d,
    method: (seed.method || "cidr") as AuthMethod,
    allow: (seed.allow ?? []).join(", "),
    deny: (seed.deny ?? []).join(", "),
    basicFile: seed.basic_file ?? "",
    basicRealm: seed.basic_realm ?? "",
    jwtJwksUrl: seed.jwt_jwks_url ?? "",
    jwtIssuer: seed.jwt_issuer ?? "",
    jwtAudience: seed.jwt_audience ?? "",
    forwardUrl: seed.forward_url ?? "",
  };
}

// splitList mirrors the backend: split on commas/whitespace into trimmed,
// non-empty entries.
function splitList(s: string): string[] {
  return s
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter((x) => x.length > 0);
}

// toPatch maps the draft to the location_set_auth payload, sending only the
// fields the chosen method needs. No secrets are sent — these are identifiers
// (paths, URLs, CIDRs), not credentials.
function toPatch(d: AuthDraft): LocationAuthPatch {
  switch (d.method) {
    case "basic": {
      const realm = d.basicRealm.trim();
      return {
        method: "basic",
        basic_file: d.basicFile.trim(),
        ...(realm ? { basic_realm: realm } : {}),
      };
    }
    case "jwt": {
      const issuer = d.jwtIssuer.trim();
      const audience = d.jwtAudience.trim();
      return {
        method: "jwt",
        jwt_jwks_url: d.jwtJwksUrl.trim(),
        ...(issuer ? { jwt_issuer: issuer } : {}),
        ...(audience ? { jwt_audience: audience } : {}),
      };
    }
    case "forward":
      return { method: "forward", forward_url: d.forwardUrl.trim() };
    case "cidr":
    default:
      return { method: "cidr", allow: splitList(d.allow), deny: splitList(d.deny) };
  }
}

/**
 * Guided editor for a single location's access-control rule (Phase 4a). It edits
 * exactly one method — CIDR allow/deny, HTTP Basic, JWT, or forward-auth — and
 * routes the change through the structured patch ops (location_set_auth /
 * location_clear_auth), so the running config is replaced wholesale for that
 * location and reviewed as a diff. Like every console editor it never writes
 * directly: it previews the patch, then hands the diff to the Config editor for
 * Validate → Diff → Apply.
 */
export function AuthEditor({ target, baseVersion, seed, existing = Boolean(seed), onClose }: AuthEditorProps) {
  const [draft, setDraft] = useState<AuthDraft>(() => seedDraft(seed));
  const batch = useRunPatchBatch();
  const { busy } = batch;
  const error = describePatchBatchError(batch.error);
  const [assessment, setAssessment] = useState<{
    key: string;
    diff: ConfigDiff | undefined;
    valid: boolean;
    action: "change" | "removal";
  } | null>(null);
  const { has } = usePermission();
  const canWrite = has("config:write");
  const canReadRaw = has("config:raw");

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;
  const warnings = authWarnings(draft);
  const currentPatch: ConfigPatch = { op: "location_set_auth", ...target, auth: toPatch(draft) };
  const clearPatch: ConfigPatch = { op: "location_clear_auth", ...target };
  const assessmentKey = (patch: ConfigPatch) => JSON.stringify([baseVersion, patch]);
  const displayedAssessment =
    canReadRaw && assessment !== null && (
      assessment.key === assessmentKey(currentPatch) ||
      (existing && assessment.key === assessmentKey(clearPatch))
    ) ? assessment : null;

  function save(): void {
    void batch.run([currentPatch], baseVersion);
  }

  function clearRule(): void {
    void batch.run([clearPatch], baseVersion);
  }

  async function previewConsequences(patch: ConfigPatch, action: "change" | "removal"): Promise<void> {
    setAssessment(null);
    const result = await batch.preview([patch], baseVersion);
    if (result !== null) {
      setAssessment({ key: assessmentKey(patch), diff: result.previewDiff, valid: result.valid, action });
    }
  }

  return (
    <Drawer
      title={existing ? "Edit access control" : "Add access control"}
      subtitle={`Auth for ${where}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            <button
              type="button"
              disabled={busy || warnings.length > 0 || !canWrite}
              onClick={() => { void previewConsequences(currentPatch, "change"); }}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text disabled:opacity-40"
            >
              Preview consequences
            </button>
            {existing && (
              <>
              <button
                type="button"
                disabled={busy || !canWrite}
                onClick={() => { void previewConsequences(clearPatch, "removal"); }}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-text disabled:opacity-40"
              >
                Preview removal
              </button>
              <button
                type="button"
                disabled={busy || !canWrite}
                onClick={clearRule}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
              >
                Clear rule
              </button>
              </>
            )}
            <button
              type="button"
              disabled={busy || warnings.length > 0 || !canWrite}
              onClick={save}
              className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
            >
              {busy ? "Previewing…" : "Review lifecycle and diff →"}
            </button>
          </div>
          <ForbiddenAction permission="config:write" className="justify-end" />
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          This rule <strong>replaces</strong> the access-control policy for{" "}
          <span className="font-mono">{where}</span> wholesale — it is not merged. Choose one method;
          its fields below are the only ones applied.{" "}
          {existing && "Clear the rule to leave the location open (no auth)."}
        </p>

        <label className="block space-y-1">
          <span className="text-sm font-medium text-jul-text">Method</span>
          <select
            value={draft.method}
            onChange={(e) => {
              setDraft((d) => ({ ...d, method: e.target.value as AuthMethod }));
            }}
            className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
          >
            {EDITABLE_METHODS.map((m) => (
              <option key={m.value} value={m.value}>
                {m.label}
              </option>
            ))}
          </select>
          <span className="text-xs text-jul-muted">
            {EDITABLE_METHODS.find((m) => m.value === draft.method)?.hint}
          </span>
        </label>

        <AuthFields auth={draft} onChange={setDraft} />

        {displayedAssessment !== null && (
          <div className="rounded-md border border-jul-border bg-jul-bg p-3" aria-live="polite">
            <p className="text-xs font-medium text-jul-text">
              {displayedAssessment.action === "removal" ? "Removal preview" : "Change preview"}
            </p>
            <p className="text-xs text-jul-muted">This assessment uses the selected base version; review refreshes before apply.</p>
            {!displayedAssessment.valid && (
              <p className="text-xs text-jul-danger">The candidate is invalid; review validation before applying.</p>
            )}
            {displayedAssessment.diff
              ? <ConfigConsequences diff={displayedAssessment.diff} />
              : <p className="text-xs text-jul-muted">Consequence preview unavailable.</p>}
          </div>
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
            {warnings.map((w, i) => (
              <p key={`aw-${String(i)}`} className="text-xs text-jul-text">
                {w}
              </p>
            ))}
          </div>
        )}
      </div>
    </Drawer>
  );
}
