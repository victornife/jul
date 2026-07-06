/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

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
import {
  seedWAFOverride,
  wafOverrideToPatch,
  wafOverrideWarnings,
  type WAFOverrideDraft,
} from "@/lib/wafOverride.ts";

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
 * Guided editor for a single per-location [waf] override. As of Phase 4e it
 * surfaces the full override — the basic knobs (enabled, mode, CRS) plus the
 * advanced SecLang fields (block status, paranoia, request-body limit,
 * response-body inspection, rule files, inline rules) — seeding every field from
 * the security projection so a save round-trips faithfully instead of clobbering
 * unshown rules. It routes the change through the structured patch ops
 * (location_waf_set / location_waf_clear) and, like every console editor, never
 * writes directly: it previews the patch, then hands the diff to the Config
 * editor for Validate → Diff → Apply.
 */
export function LocationWAFEditor({ target, existing = true, onClose }: LocationWAFEditorProps) {
  const navigate = useNavigate();
  const [draft, setDraft] = useState<WAFOverrideDraft>(() => seedWAFOverride(target));
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const warnings = wafOverrideWarnings(draft);

  const where = `${target.listen}${target.path ? ` ${target.path}` : ""}`;

  function set<K extends keyof WAFOverrideDraft>(key: K, val: WAFOverrideDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

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
      waf: wafOverrideToPatch(draft),
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
          {existing ? (
            <>
              This override <strong>replaces</strong> the global{" "}
              <span className="font-mono">[waf]</span> policy for{" "}
              <span className="font-mono">{where}</span> wholesale — it is not merged. Clearing it
              makes the location inherit the global policy again.
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
            checked={draft.enabled}
            onChange={(e) => {
              set("enabled", e.target.checked);
            }}
            className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
          />
          Enable the WAF for this location
        </label>

        {draft.enabled && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Mode</span>
              <select
                value={draft.mode}
                onChange={(e) => {
                  set("mode", e.target.value === "block" ? "block" : "detect");
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="detect">Detect (log only — recommended first)</option>
                <option value="block">Block (reject matching requests)</option>
              </select>
            </label>

            {draft.mode === "block" && (
              <TextField
                label="Block status"
                hint="HTTP status returned when a request is blocked. Blank applies 403."
                value={draft.blockStatus}
                placeholder="403"
                onChange={(v) => {
                  set("blockStatus", v);
                }}
              />
            )}

            <label className="flex items-center gap-2 text-sm text-jul-text">
              <input
                type="checkbox"
                checked={draft.crsEnabled}
                onChange={(e) => {
                  set("crsEnabled", e.target.checked);
                }}
                className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
              />
              Load the embedded OWASP Core Rule Set (CRS)
            </label>

            {draft.crsEnabled && (
              <label className="block space-y-1">
                <span className="text-sm font-medium text-jul-text">Paranoia level</span>
                <select
                  value={draft.paranoia}
                  onChange={(e) => {
                    set("paranoia", e.target.value);
                  }}
                  className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                >
                  <option value="">Default (1)</option>
                  <option value="1">1 — fewest false positives</option>
                  <option value="2">2</option>
                  <option value="3">3</option>
                  <option value="4">4 — most aggressive</option>
                </select>
              </label>
            )}

            <TextField
              label="Request body limit"
              hint="Bytes of request body buffered for inspection. Blank applies 128k."
              value={draft.requestBodyLimit}
              placeholder="128k"
              onChange={(v) => {
                set("requestBodyLimit", v);
              }}
            />

            <label className="flex items-center gap-2 text-sm text-jul-text">
              <input
                type="checkbox"
                checked={draft.responseBodyCheck}
                onChange={(e) => {
                  set("responseBodyCheck", e.target.checked);
                }}
                className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
              />
              Inspect response bodies (adds latency and memory)
            </label>

            <TextArea
              label="Rule files"
              hint="SecLang rule files to load, one path per line, after the CRS and before inline rules."
              value={draft.directivesFiles}
              placeholder={"/etc/jul/waf/custom.conf"}
              rows={3}
              onChange={(v) => {
                set("directivesFiles", v);
              }}
            />

            <TextArea
              label="Inline rules"
              hint="SecLang snippet appended last — handy for small allow-list or tuning rules."
              value={draft.inlineRules}
              placeholder={'SecRule REQUEST_URI "@contains /x" "id:200,phase:1,deny"'}
              rows={4}
              onChange={(v) => {
                set("inlineRules", v);
              }}
            />

            {warnings.length > 0 && (
              <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-3">
                {warnings.map((wn, i) => (
                  <p key={`waf-w-${String(i)}`} className="text-xs text-jul-text">
                    {wn}
                  </p>
                ))}
              </div>
            )}
          </>
        )}
      </div>
    </Drawer>
  );
}

function TextField({
  label,
  hint,
  value,
  placeholder,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <input
        type="text"
        value={value}
        placeholder={placeholder}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}

function TextArea({
  label,
  hint,
  value,
  placeholder,
  rows,
  onChange,
}: {
  readonly label: string;
  readonly hint?: string;
  readonly value: string;
  readonly placeholder?: string;
  readonly rows: number;
  readonly onChange: (v: string) => void;
}) {
  return (
    <label className="block space-y-1">
      <span className="text-sm font-medium text-jul-text">{label}</span>
      <textarea
        value={value}
        placeholder={placeholder}
        rows={rows}
        onChange={(e) => {
          onChange(e.target.value);
        }}
        className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-sm text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
      />
      {hint && <span className="text-xs text-jul-muted">{hint}</span>}
    </label>
  );
}
