import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Drawer } from "@/components/Drawer.tsx";
import { fetchRawConfig, type SecurityProjection } from "@/api/client.ts";
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
    (p.waf_directives_files?.length ?? 0) > 0
  );
}

// seedDraftFromProjection maps the projected global [waf] policy into an editor
// draft so every field round-trips: opening and re-saving the editor without
// changes must reproduce the same policy rather than clobber CRS, paranoia,
// response-body inspection, rule files, or inline rules.
function seedDraftFromProjection(p: SecurityProjection): WAFDraft {
  return {
    enabled: p.waf_global_enabled,
    mode: p.waf_global_mode === "block" ? "block" : "detect",
    blockStatus: p.waf_block_status ?? 0,
    crsEnabled: p.waf_crs_enabled,
    paranoia: p.waf_paranoia && p.waf_paranoia >= 1 ? p.waf_paranoia : 1,
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
export function WAFEditor({ current, onClose }: WAFEditorProps) {
  const navigate = useNavigate();
  const [error, setError] = useState<string | null>(null);
  const [draft, setDraft] = useState<WAFDraft>(() =>
    hasGlobalWAFConfig(current) ? seedDraftFromProjection(current) : emptyWAFDraft(),
  );

  const fragment = generateWafToml(draft);
  const warnings = wafWarnings(draft);

  function set<K extends keyof WAFDraft>(key: K, value: WAFDraft[K]): void {
    setDraft((d) => ({ ...d, [key]: value }));
  }

  async function openInEditor(): Promise<void> {
    setError(null);
    try {
      const raw = await fetchRawConfig();
      setPendingDraft(upsertTopLevelTable(raw.raw ?? "", "waf", fragment));
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
              <label className="block space-y-1">
                <span className="text-sm font-medium text-jul-text">CRS paranoia level</span>
                <select
                  value={String(draft.paranoia)}
                  onChange={(e) => {
                    set("paranoia", Number(e.target.value));
                  }}
                  className="w-full rounded-md border border-jul-border bg-jul-surface px-3 py-1.5 text-sm text-jul-text focus:outline-none focus:ring-1 focus:ring-jul-accent"
                >
                  <option value="1">1 — fewest false positives (recommended)</option>
                  <option value="2">2 — stricter</option>
                  <option value="3">3 — aggressive</option>
                  <option value="4">4 — paranoid (many false positives)</option>
                </select>
              </label>
            )}

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
                SecLang files loaded before the CRS and inline rules.
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
