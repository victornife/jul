/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import {
  captureAdminIdentity,
  fetchRawConfig,
  type SecurityProjection,
  type WAFException,
} from "@/api/client.ts";
import { setPendingDraft } from "@/lib/configDraftHandoff.ts";
import { upsertTopLevelTable } from "@/lib/trafficToml.ts";
import {
  emptyWAFDraft,
  generateWafToml,
  wafWarnings,
  type WAFDraft,
  type WAFMode,
} from "@/lib/wafToml.ts";

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

function Toggle({
  label,
  checked,
  onChange,
}: {
  readonly label: string;
  readonly checked: boolean;
  readonly onChange: (v: boolean) => void;
}) {
  return (
    <label className="flex items-center gap-2 text-sm text-jul-text">
      <input
        type="checkbox"
        checked={checked}
        onChange={(e) => {
          onChange(e.target.checked);
        }}
        className="h-4 w-4 rounded border-jul-border bg-jul-surface accent-jul-accent"
      />
      {label}
    </label>
  );
}

export interface WAFEditorProps {
  readonly current: SecurityProjection;
  readonly baseVersion?: string | undefined;
  readonly initialException?: WAFException;
  readonly onClose: () => void;
}

// hasGlobalWAFConfig reports whether a global [waf] policy already exists in the
// running config. When it does, the editor must seed from it so a save never
// silently drops fields the form surfaces from a prior configuration; when it
// does not, the editor falls back to the safe new-user defaults.
function hasGlobalWAFConfig(p: SecurityProjection): boolean {
  return (
    p.waf_global_enabled ||
    p.waf_crs_enabled ||
    p.waf_response_body_check ||
    Boolean(p.waf_global_mode) ||
    Boolean(p.waf_request_body_limit) ||
    Boolean(p.waf_inline_rules) ||
    (p.waf_block_status ?? 0) > 0 ||
    (p.waf_paranoia ?? 0) > 0 ||
    (p.waf_blocking_paranoia ?? 0) > 0 ||
    (p.waf_detection_paranoia ?? 0) > 0 ||
    (p.waf_directives_files?.length ?? 0) > 0 ||
    (p.waf_exceptions?.length ?? 0) > 0
  );
}

// seedDraftFromProjection maps the projected global [waf] policy into an editor
// draft so every field round-trips: opening and re-saving the editor without
// changes must reproduce the same policy rather than clobber CRS, paranoia,
// response-body inspection, rule files, or inline rules.
function seedDraftFromProjection(
  p: SecurityProjection,
  initialException?: WAFException,
): WAFDraft {
  const baseExceptions = [...(p.waf_exceptions ?? [])];
  if (
    initialException &&
    !baseExceptions.some(
      (e) =>
        e.rule_id === initialException.rule_id &&
        (e.target ?? "") === (initialException.target ?? ""),
    )
  ) {
    baseExceptions.push(initialException);
  }
  const effectiveParanoia =
    p.waf_blocking_paranoia && p.waf_blocking_paranoia >= 1
      ? p.waf_blocking_paranoia
      : p.waf_paranoia && p.waf_paranoia >= 1
        ? p.waf_paranoia
        : 1;
  return {
    enabled: p.waf_global_enabled,
    mode: p.waf_global_mode === "block" ? "block" : "detect",
    blockStatus: p.waf_block_status ?? 0,
    crsEnabled: p.waf_crs_enabled,
    paranoia: effectiveParanoia,
    ...(p.waf_blocking_paranoia ? { blockingParanoia: p.waf_blocking_paranoia } : {}),
    ...(p.waf_detection_paranoia ? { detectionParanoia: p.waf_detection_paranoia } : {}),
    ...(baseExceptions.length > 0 ? { exceptions: baseExceptions } : {}),
    directivesFiles: (p.waf_directives_files ?? []).join("\n"),
    inlineRules: p.waf_inline_rules ?? "",
    requestBodyLimit: p.waf_request_body_limit ?? "",
    responseBodyCheck: p.waf_response_body_check,
  };
}

/**
 * Guided WAF editor (Wave A). It edits the global [waf] table, upserts it into
 * the running config, and hands the draft to the Config editor where it flows
 * through Validate → Diff → Apply → Rollback. It never writes directly. The
 * editor defaults to detect mode and warns on a block-mode CRS rollout so the
 * operator observes events before enforcing.
 */
export function WAFEditor({ current, baseVersion, initialException, onClose }: WAFEditorProps) {
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  // Freeze the base revision and projection captured when the editor opened so
  // background 15s polling refreshes cannot renew the CAS fence of an older draft.
  const [snapshot] = useState(() => ({
    projection: current,
    baseVersion: baseVersion ?? current.base_version,
  }));
  const [draft, setDraft] = useState<WAFDraft>(() => {
    if (hasGlobalWAFConfig(snapshot.projection)) {
      return seedDraftFromProjection(snapshot.projection, initialException);
    }
    const base = emptyWAFDraft();
    return initialException ? { ...base, exceptions: [initialException] } : base;
  });

  const fragment = generateWafToml(draft);
  const warnings = wafWarnings(draft);

  function set<K extends keyof WAFDraft>(key: K, value: WAFDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    const assertIdentity = captureAdminIdentity();
    setError(null);
    try {
      const raw = await fetchRawConfig();
      assertIdentity();
      if (
        snapshot.baseVersion !== undefined &&
        raw.base_version !== undefined &&
        raw.base_version !== snapshot.baseVersion
      ) {
        setError(
          "Configuration changed on the server since this editor was opened (409 Conflict). Close and reopen the editor to load the latest WAF policy.",
        );
        return;
      }
      const pinnedBaseVersion = snapshot.baseVersion ?? raw.base_version;
      setPendingDraft({
        kind: "toml",
        toml: upsertTopLevelTable(raw.raw ?? "", "waf", fragment),
        ...(pinnedBaseVersion !== undefined ? { pinnedBaseVersion } : {}),
      });
      void navigate("/config");
    } catch {
      setError("Could not load the current configuration to merge this WAF change.");
    }
  }

  return (
    <Drawer
      title="Edit web application firewall"
      subtitle="Tune the global WAF policy, then review and apply it safely in the editor."
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between gap-3">
          {error && <span className="text-xs text-jul-danger">{error}</span>}
          <button
            type="button"
            onClick={() => {
              void openInEditor();
            }}
            className="ml-auto rounded-md bg-jul-accent px-4 py-1.5 text-sm font-medium text-jul-bg hover:brightness-110"
          >
            Review in editor →
          </button>
        </div>
      }
    >
      <div className="space-y-5">
        <p className="rounded-md border border-jul-border bg-jul-surface p-3 text-xs text-jul-muted">
          The web application firewall inspects requests against SecLang rules (the embedded OWASP
          CRS and/or your own). Roll out in <strong>detect</strong> mode first to observe what would
          be blocked, review the WAF events, then switch to <strong>block</strong>.
        </p>

        <Toggle
          label="Enable the WAF"
          checked={draft.enabled}
          onChange={(v) => {
            set("enabled", v);
          }}
        />

        {draft.enabled && (
          <>
            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Mode</span>
              <select
                value={draft.mode}
                onChange={(e) => {
                  set("mode", e.target.value as WAFMode);
                }}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
              >
                <option value="detect">Detect (log only — recommended first)</option>
                <option value="block">Block (reject matching requests)</option>
              </select>
            </label>

            <Toggle
              label="Load the embedded OWASP Core Rule Set (CRS)"
              checked={draft.crsEnabled}
              onChange={(v) => {
                set("crsEnabled", v);
              }}
            />

            {draft.crsEnabled && (
              <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">CRS paranoia level</span>
                  <select
                    value={String(draft.blockingParanoia ?? draft.paranoia)}
                    onChange={(e) => {
                      const nextLevel = Number(e.target.value);
                      setDraft((d) => ({
                        ...d,
                        paranoia: nextLevel,
                        ...(d.blockingParanoia !== undefined || d.detectionParanoia !== undefined
                          ? { blockingParanoia: nextLevel }
                          : {}),
                      }));
                    }}
                    className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                  >
                    <option value="1">1 — fewest false positives (recommended)</option>
                    <option value="2">2 — stricter</option>
                    <option value="3">3 — aggressive</option>
                    <option value="4">4 — paranoid (many false positives)</option>
                  </select>
                </label>

                <label className="block space-y-1">
                  <span className="text-sm font-medium text-jul-text">
                    Detection paranoia (optional)
                  </span>
                  <select
                    value={draft.detectionParanoia ? String(draft.detectionParanoia) : ""}
                    onChange={(e) => {
                      const raw = e.target.value;
                      setDraft((d) => {
                        const copy: WAFDraft = { ...d };
                        if (!raw) {
                          delete copy.detectionParanoia;
                        } else {
                          copy.detectionParanoia = Number(raw);
                          copy.blockingParanoia = d.blockingParanoia ?? d.paranoia;
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
                    Exclude a single field (e.g. <code>ARGS:profile_bio</code>) from one rule
                    without disabling the rule globally.
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
                  key={`gexc-${String(idx)}`}
                  className="grid grid-cols-1 gap-2 rounded border border-jul-border bg-jul-bg/40 p-2 text-xs sm:grid-cols-3"
                >
                  <label className="space-y-0.5">
                    <span className="text-jul-muted">Rule ID</span>
                    <input
                      type="number"
                      aria-label={`Global exception ${String(idx + 1)} rule ID`}
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
                      aria-label={`Global exception ${String(idx + 1)} target`}
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
                        aria-label={`Global exception ${String(idx + 1)} rationale`}
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
              label="Block status (optional)"
              hint="HTTP status returned for blocked requests in block mode. Blank = 403."
              value={draft.blockStatus > 0 ? String(draft.blockStatus) : ""}
              placeholder="403"
              onChange={(v) => {
                set("blockStatus", Math.max(0, Number(v) || 0));
              }}
            />

            <TextField
              label="Request body limit (optional)"
              hint="How much request body to buffer for inspection, e.g. 128k. Blank = server default."
              value={draft.requestBodyLimit}
              placeholder="128k"
              onChange={(v) => {
                set("requestBodyLimit", v);
              }}
            />

            <div className="space-y-1">
              <Toggle
                label="Inspect response bodies (CRS phase 4)"
                checked={draft.responseBodyCheck}
                onChange={(v) => {
                  set("responseBodyCheck", v);
                }}
              />
              <span className="block text-xs text-jul-muted">
                Buffers responses to run outbound rules. Adds latency and memory; leave off unless
                you need response-side detections.
              </span>
            </div>

            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">
                Rule files (optional, one per line)
              </span>
              <textarea
                value={draft.directivesFiles}
                placeholder={"/etc/jul/waf/custom.conf\n/etc/jul/waf/exclusions.conf"}
                onChange={(e) => {
                  set("directivesFiles", e.target.value);
                }}
                rows={3}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-xs text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <span className="text-xs text-jul-muted">
                SecLang files loaded after the CRS and before inline rules — the place for rule
                exclusions and tuning.
              </span>
            </label>

            <label className="block space-y-1">
              <span className="text-sm font-medium text-jul-text">Inline rules (optional)</span>
              <textarea
                value={draft.inlineRules}
                placeholder={'SecRule REQUEST_HEADERS:User-Agent "@contains badbot" "id:1000,deny"'}
                onChange={(e) => {
                  set("inlineRules", e.target.value);
                }}
                rows={3}
                className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 font-mono text-xs text-jul-text placeholder:text-jul-muted focus:outline-none focus:ring-1 focus:ring-jul-accent"
              />
              <span className="text-xs text-jul-muted">
                A SecLang snippet appended last — handy for small tuning or allow-list rules.
              </span>
            </label>
          </>
        )}

        {warnings.length > 0 && (
          <div className="space-y-1 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2">
            {warnings.map((wn, i) => (
              <p key={`ww-${String(i)}`} className="text-xs text-jul-warning">
                {wn}
              </p>
            ))}
          </div>
        )}

        <div className="space-y-1">
          <span className="text-xs font-semibold uppercase tracking-wider text-jul-muted">
            Generated TOML
          </span>
          <pre className="overflow-auto rounded-md border border-jul-border bg-jul-surface p-3 font-mono text-xs leading-relaxed text-jul-text">
            {fragment}
          </pre>
        </div>
      </div>
    </Drawer>
  );
}
