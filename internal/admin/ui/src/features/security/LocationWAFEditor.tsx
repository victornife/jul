import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  patchConfig,
  ConfigRejectedError,
  type ConfigPatch,
  type LocationWAF,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";

export interface LocationWAFEditorProps {
  /** The per-location override being edited, from the security projection. */
  readonly target: LocationWAF;
  /**
   * Whether the location already has a [waf] override. When false (adding a
   * fresh override to a route that currently inherits the global policy) the
   * "Clear override" action is hidden — there is nothing to clear yet — and the
   * intro copy reflects that a new override is being created.
   */
  readonly existing?: boolean;
  readonly onClose: () => void;
}

// routeTarget builds the structured location selector the patch ops require —
// the same coordinates the security projection exposes — so the edit lands on
// exactly one location and never on the wrong vhost or a repeated path.
function routeTarget(w: LocationWAF) {
  return {
    listen: w.listen,
    server_names: w.server_names ?? [],
    match_type: w.match_type ?? "",
    path: w.path ?? "",
  };
}

/**
 * Guided editor for a single per-location [waf] override. It edits only the
 * three knobs the security panel discloses (enabled, mode, CRS) and routes the
 * change through the structured patch ops — location_waf_set / location_waf_clear
 * — so advanced SecLang fields an override may carry (block status, paranoia,
 * rule files, inline rules) are preserved by the backend rather than spliced by
 * hand. Like every console editor it never writes directly: it previews the
 * patch, then hands the diff to the Config editor for Validate → Diff → Apply.
 */
export function LocationWAFEditor({ target, existing = true, onClose }: LocationWAFEditorProps) {
  const navigate = useNavigate();
  const [enabled, setEnabled] = useState(target.enabled);
  const [mode, setMode] = useState<"block" | "detect">(
    target.mode === "block" ? "block" : "detect",
  );
  const [crsEnabled, setCrsEnabled] = useState(target.crs_enabled);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;

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
    void runPatch({
      op: "location_waf_set",
      ...routeTarget(target),
      waf: { enabled, mode, crs_enabled: crsEnabled },
    });
  }

  function clearOverride(): void {
    void runPatch({ op: "location_waf_clear", ...routeTarget(target) });
  }

  return (
    <Drawer
      title={existing ? "Edit per-location WAF" : "Add per-location WAF"}
      subtitle={`Override for ${where}`}
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          {existing && (
            <button
              type="button"
              disabled={busy}
              onClick={clearOverride}
              className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
            >
              Clear override
            </button>
          )}
          <button
            type="button"
            disabled={busy}
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
          {existing ? (
            <>
              This override <strong>replaces</strong> the global{" "}
              <span className="font-mono">[waf]</span> policy for{" "}
              <span className="font-mono">{where}</span> wholesale — it is not merged. Clearing it
              makes the location inherit the global policy again. Advanced rule files and inline
              rules on this override are preserved; only the controls below are changed.
            </>
          ) : (
            <>
              This creates a per-location override for <span className="font-mono">{where}</span>{" "}
              that <strong>replaces</strong> the global <span className="font-mono">[waf]</span>{" "}
              policy for this location wholesale — it is not merged. Roll out in{" "}
              <strong>detect</strong> first, then switch to <strong>block</strong>.
            </>
          )}
        </p>

        <label className="flex items-center gap-2 text-sm text-jul-text">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(e) => {
              setEnabled(e.target.checked);
            }}
            className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
          />
          Enable the WAF for this location
        </label>

        {enabled && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Mode</span>
              <select
                value={mode}
                onChange={(e) => {
                  setMode(e.target.value === "block" ? "block" : "detect");
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="detect">Detect (log only — recommended first)</option>
                <option value="block">Block (reject matching requests)</option>
              </select>
            </label>

            <label className="flex items-center gap-2 text-sm text-jul-text">
              <input
                type="checkbox"
                checked={crsEnabled}
                onChange={(e) => {
                  setCrsEnabled(e.target.checked);
                }}
                className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
              />
              Load the embedded OWASP Core Rule Set (CRS)
            </label>
          </>
        )}
      </div>
    </Drawer>
  );
}
