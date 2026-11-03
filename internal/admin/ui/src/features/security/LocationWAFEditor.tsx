/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { Drawer } from "@/components/Drawer.tsx";
import type { LocationWAF, RouteTarget } from "@/api/client.ts";
import { ForbiddenAction } from "@/components/ForbiddenAction.tsx";
import { usePermission } from "@/auth/usePermission.ts";
import { useRunPatch } from "@/lib/useRunPatch.ts";
import {
  seedWAFOverride,
  wafOverrideToPatch,
  wafOverrideWarnings,
  type WAFOverrideDraft,
} from "@/lib/wafOverride.ts";

export interface LocationWAFEditorProps {
  /** The per-location override being edited, from the security projection. */
  readonly target: LocationWAF;
 readonly baseVersion?: string | undefined;
 readonly selector?: RouteTarget | undefined;
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
function routeTarget(w: LocationWAF): RouteTarget {
  return {
    listen: w.listen,
    server_names: w.server_names ?? [],
    match_type: w.match_type ?? "",
    path: w.path ?? "",
    ...(w.match_ordinal !== undefined ? { match_ordinal: w.match_ordinal } : {}),
    ...(w.selector_version !== undefined ? { selector_version: w.selector_version } : {}),
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
export function LocationWAFEditor({ target, baseVersion, selector, existing = true, onClose }: LocationWAFEditorProps) {
  // Freeze the target, selector, and base_version captured when the editor
  // opened so background polling refreshes cannot mutate the route ordinal or
  // renew the CAS version fence of an in-progress draft.
  const [snapshot] = useState(() => ({
    target,
    baseVersion,
    selector: selector ?? routeTarget(target),
    existing,
  }));
  const [draft, setDraft] = useState<WAFOverrideDraft>(() => seedWAFOverride(snapshot.target));
  const { run: runPatch, error, busy } = useRunPatch(snapshot.baseVersion);
  const { has } = usePermission();
  const canWrite = has("config:write");
  const warnings = wafOverrideWarnings(draft);

  const where = `${snapshot.target.listen}${snapshot.target.path ? ` ${snapshot.target.path}` : ""}`;

  function set<K extends keyof WAFOverrideDraft>(key: K, val: WAFOverrideDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: val }));
  }

  function save(): void {
    runPatch({
      op: "location_waf_set",
      ...snapshot.selector,
      waf: wafOverrideToPatch(draft),
    });
  }

  function clearOverride(): void {
    runPatch({ op: "location_waf_clear", ...snapshot.selector });
  }

  return (
    <Drawer
      title={snapshot.existing ? "Edit per-location WAF" : "Add per-location WAF"}
      subtitle={`Override for ${where}`}
      onClose={onClose}
      footer={
        <div className="w-full space-y-2">
          <div className="flex items-center justify-between gap-3">
            {error && <span className="text-xs text-jul-danger">{error}</span>}
            {snapshot.existing && (
              <button
                type="button"
                disabled={busy || !canWrite}
                onClick={clearOverride}
                className="rounded-md border border-jul-border px-3 py-1.5 text-sm text-jul-danger hover:bg-jul-bg disabled:opacity-40"
              >
                Clear override
              </button>
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
          {snapshot.existing ? (
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
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">Paranoia level</span>
                  <select
                    value={(draft.blockingParanoia ?? "").trim() || draft.paranoia}
                    onChange={(e) => {
                      const val = e.target.value;
                      setDraft((d) => ({
                        ...d,
                        paranoia: val,
                        ...(d.blockingParanoia !== undefined ||
                        (d.detectionParanoia ?? "").trim() !== ""
                          ? { blockingParanoia: val }
                          : {}),
                      }));
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
                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">
                    Detection paranoia (optional)
                  </span>
                  <select
                    value={draft.detectionParanoia ?? ""}
                    onChange={(e) => {
                      const val = e.target.value;
                      setDraft((d) => {
                        const copy: WAFOverrideDraft = { ...d };
                        if (!val) {
                          delete copy.detectionParanoia;
                        } else {
                          copy.detectionParanoia = val;
                          copy.blockingParanoia =
                            (d.blockingParanoia ?? "").trim() || d.paranoia || "1";
                        }
                        return copy;
                      });
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  >
                    <option value="">Same as blocking</option>
                    <option value="1">1</option>
                    <option value="2">2 — observe PL2 without blocking</option>
                    <option value="3">3 — observe PL3 without blocking</option>
                    <option value="4">4 — observe PL4 without blocking</option>
                  </select>
                </label>
              </div>
            )}

            <div className="space-y-2 rounded-md border border-jul-border bg-jul-surface p-3">
              <div className="flex items-center justify-between gap-2">
                <div>
                  <span className="text-sm font-medium text-jul-text">
                    Narrow rule exceptions ({String((draft.exceptions ?? []).length)})
                  </span>
                  <p className="text-xs text-jul-muted">
                    Exclude a single field (e.g. <code>ARGS:profile_bio</code>) from one rule on
                    this route while preserving protection on neighboring fields, methods, and
                    routes.
                  </p>
                </div>
                <button
                  type="button"
                  onClick={() => {
                    const next = [
                      ...(draft.exceptions ?? []),
                      {
                        rule_id: 942100,
                        scope: "narrow_target",
                        target: "ARGS:field_name",
                        rationale: "",
                      },
                    ];
                    set("exceptions", next);
                  }}
                  className="rounded border border-jul-border px-2 py-1 text-xs text-jul-text hover:bg-jul-bg"
                >
                  + Add exception
                </button>
              </div>
              {(draft.exceptions ?? []).map((exc, idx) => (
                <div
                  key={`exc-${String(idx)}`}
                  className="grid grid-cols-1 gap-2 rounded border border-jul-border bg-jul-bg/40 p-2 text-xs sm:grid-cols-3"
                >
                  <label className="space-y-0.5">
                    <span className="text-jul-muted">Rule ID</span>
                    <input
                      type="number"
                      aria-label={`Exception ${String(idx + 1)} rule ID`}
                      value={String(exc.rule_id)}
                      onChange={(e) => {
                        const list = [...(draft.exceptions ?? [])];
                        list[idx] = { ...exc, rule_id: Number(e.target.value) || 0 };
                        set("exceptions", list);
                      }}
                      className="w-full rounded border border-jul-border bg-jul-surface px-2 py-1 font-mono text-jul-text"
                    />
                  </label>
                  <label className="space-y-0.5">
                    <span className="text-jul-muted">Target (COLLECTION:KEY)</span>
                    <input
                      type="text"
                      aria-label={`Exception ${String(idx + 1)} target`}
                      value={exc.target ?? ""}
                      placeholder="ARGS:profile_bio"
                      onChange={(e) => {
                        const list = [...(draft.exceptions ?? [])];
                        list[idx] = { ...exc, target: e.target.value, scope: "narrow_target" };
                        set("exceptions", list);
                      }}
                      className="w-full rounded border border-jul-border bg-jul-surface px-2 py-1 font-mono text-jul-text"
                    />
                  </label>
                  <div className="flex items-end gap-2">
                    <label className="flex-1 space-y-0.5">
                      <span className="text-jul-muted">Rationale / review</span>
                      <input
                        type="text"
                        aria-label={`Exception ${String(idx + 1)} rationale`}
                        value={exc.rationale ?? ""}
                        placeholder="Why this field is excluded"
                        onChange={(e) => {
                          const list = [...(draft.exceptions ?? [])];
                          list[idx] = { ...exc, rationale: e.target.value };
                          set("exceptions", list);
                        }}
                        className="w-full rounded border border-jul-border bg-jul-surface px-2 py-1 text-jul-text"
                      />
                    </label>
                    <button
                      type="button"
                      onClick={() => {
                        const list = (draft.exceptions ?? []).filter((_, j) => j !== idx);
                        set("exceptions", list);
                      }}
                      className="rounded border border-jul-border px-2 py-1 text-jul-danger hover:bg-jul-bg"
                    >
                      Remove
                    </button>
                  </div>
                </div>
              ))}
            </div>

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
