import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  patchConfig,
  ConfigRejectedError,
  type ConfigPatch,
  type LocationAuthPatch,
  type LocationAuthState,
  type RouteTarget,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
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
export function AuthEditor({ target, seed, existing = Boolean(seed), onClose }: AuthEditorProps) {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<AuthDraft>(() => seedDraft(seed));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;
  const warnings = authWarnings(draft);

  async function runPatch(patch: ConfigPatch): Promise<void> {
    setError(null);
    setBusy(true);
    try {
      const res = await patchConfig(patch);
      setPendingDraft({
        kind: "patch",
        ops: [patch],
        baseVersion: res.base_version,
        previewDiff: res.diff,
        candidate: res.candidate,
      });
      void navigate("/config");
    } catch (err) {
      setError(err instanceof ConfigRejectedError ? err.message : "The edit could not be applied.");
      setBusy(false);
    }
  }

  function save(): void {
    void runPatch({ op: "location_set_auth", ...target, auth: toPatch(draft) });
  }

  function clearRule(): void {
    void runPatch({ op: "location_clear_auth", ...target });
  }

  return (
    <Drawer
      title={existing ? "Edit access control" : "Add access control"}
      subtitle={`Auth for ${where}`}
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          {existing && (
            <button
              type="button"
              disabled={busy}
              onClick={clearRule}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
            >
              Clear rule
            </button>
          )}
          <button
            type="button"
            disabled={busy || warnings.length > 0}
            onClick={save}
            className="rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110 disabled:opacity-40"
          >
            Review in editor →
          </button>
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
