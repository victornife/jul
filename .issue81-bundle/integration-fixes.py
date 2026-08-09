from __future__ import annotations

from pathlib import Path


def replace_once(path: Path, old: str, new: str, label: str) -> None:
    text = path.read_text(encoding="utf-8")
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: {label}: expected one anchor, found {count}")
    path.write_text(text.replace(old, new, 1), encoding="utf-8")


# Preserve legacy raw-editor handoffs without weakening the enhanced cache/raw
# handoff. Legacy TOML stays memory-only and follows the historical raw-editor
# validation/diff/apply path; only metadata-complete handoffs enter rawHandoff.
handoff = Path("internal/admin/ui/src/lib/configDraftHandoff.ts")
replace_once(
    handoff,
    '''export interface PendingRawDraft {
  readonly kind: "toml";
  readonly toml: string;
  readonly baseVersion: string;
  readonly previewDiff: ConfigDiff;
  readonly lifecycle: PatchLifecycle;
  readonly recommendedAction: "stage_restart" | "update_staged" | "none";
  readonly pendingRestart: PendingRestartSnapshot;
  readonly candidateState: "memory_only";
}

export interface LegacyPendingPatchDraftInput {''',
    '''export interface PendingRawDraft {
  readonly kind: "toml";
  readonly toml: string;
  readonly baseVersion: string;
  readonly previewDiff: ConfigDiff;
  readonly lifecycle: PatchLifecycle;
  readonly recommendedAction: "stage_restart" | "update_staged" | "none";
  readonly pendingRestart: PendingRestartSnapshot;
  readonly candidateState: "memory_only";
}

/**
 * Compatibility input for raw editors that have not yet migrated to the
 * lifecycle-aware candidate preview. It remains same-tab and memory-only and is
 * deliberately never treated as an assessed cache/raw handoff.
 */
export interface LegacyPendingRawDraft {
  readonly kind: "toml";
  readonly toml: string;
}

export interface LegacyPendingPatchDraftInput {''',
    "legacy raw handoff type",
)
replace_once(
    handoff,
    '''export type PendingDraft = PendingRawDraft | PendingPatchDraft;
export type PendingDraftInput = PendingRawDraft | LegacyPendingPatchDraftInput;''',
    '''export type PendingDraft = PendingRawDraft | LegacyPendingRawDraft | PendingPatchDraft;
export type PendingDraftInput = PendingRawDraft | LegacyPendingRawDraft | LegacyPendingPatchDraftInput;''',
    "pending draft unions",
)
replace_once(
    handoff,
    '''  if (object.kind === "toml") {
    // Raw handoffs are accepted only through setPendingDraft's in-memory value;
    // normalization supports tests/direct callers but parseStored rejects them.
    const diff = ConfigDiffSchema.safeParse(object.previewDiff);
    const lifecycle = PatchLifecycleSchema.safeParse(object.lifecycle);
    const pendingRestart = parsePendingSnapshot(object.pendingRestart);
    if (
      typeof object.toml !== "string" ||
      typeof object.baseVersion !== "string" ||
      !diff.success ||
      !lifecycle.success ||
      pendingRestart === undefined
    ) {
      return null;
    }
    const action = object.recommendedAction;
    if (action !== "stage_restart" && action !== "update_staged" && action !== "none") return null;
    return {
      kind: "toml",
      toml: object.toml,
      baseVersion: object.baseVersion,
      previewDiff: diff.data,
      lifecycle: lifecycle.data,
      recommendedAction: action,
      pendingRestart,
      candidateState: "memory_only",
    };
  }''',
    '''  if (object.kind === "toml") {
    // Raw handoffs are accepted only through setPendingDraft's in-memory value;
    // parseStored still rejects every raw form. Metadata-complete handoffs use
    // the lifecycle-aware path; legacy editors retain the historical raw flow.
    if (typeof object.toml !== "string") return null;
    const diff = ConfigDiffSchema.safeParse(object.previewDiff);
    const lifecycle = PatchLifecycleSchema.safeParse(object.lifecycle);
    const pendingRestart = parsePendingSnapshot(object.pendingRestart);
    const action = object.recommendedAction;
    if (
      typeof object.baseVersion === "string" &&
      diff.success &&
      lifecycle.success &&
      pendingRestart !== undefined &&
      (action === "stage_restart" || action === "update_staged" || action === "none")
    ) {
      return {
        kind: "toml",
        toml: object.toml,
        baseVersion: object.baseVersion,
        previewDiff: diff.data,
        lifecycle: lifecycle.data,
        recommendedAction: action,
        pendingRestart,
        candidateState: "memory_only",
      };
    }
    return { kind: "toml", toml: object.toml };
  }''',
    "normalize raw handoff compatibility",
)

config_panel = Path("internal/admin/ui/src/features/config/ConfigPanel.tsx")
replace_once(
    config_panel,
    '''        if (handoff.kind === "toml") {
          // Candidate and base are one inseparable unit. Never replace the
          // pinned token with the newer raw-config response.
          setRawHandoff(handoff);
          setBaseline(raw);
          setDraft(handoff.toml);
          setBaseVersion(handoff.baseVersion);
          if (data.base_version !== undefined && data.base_version !== handoff.baseVersion) {
            setConflictVersion(data.base_version);
          }
        } else {''',
    '''        if (handoff.kind === "toml") {
          setBaseline(raw);
          setDraft(handoff.toml);
          if ("baseVersion" in handoff) {
            // Candidate and base are one inseparable unit. Never replace the
            // pinned token with the newer raw-config response.
            setRawHandoff(handoff);
            setBaseVersion(handoff.baseVersion);
            if (data.base_version !== undefined && data.base_version !== handoff.baseVersion) {
              setConflictVersion(data.base_version);
            }
          } else {
            // Compatibility for raw editors outside issue #81. They retain the
            // existing raw validation/diff path and do not inherit cache's
            // lifecycle assessment or planned-restart claims.
            setBaseVersion(data.base_version);
          }
        } else {''',
    "legacy raw initialization",
)

# Keep component/test fixture compatibility while the server parser still
# returns the complete projection. Callers may supply only the panel slice they
# exercise; all issue #81 seed helpers already default absent sections safely.
client = Path("internal/admin/ui/src/api/client.ts")
replace_once(
    client,
    '''export type TrafficControls = z.infer<typeof TrafficControlsSchema>;''',
    '''export type TrafficControls = Partial<z.output<typeof TrafficControlsSchema>>;''',
    "traffic controls compatibility input",
)

traffic_editor = Path("internal/admin/ui/src/features/traffic-controls/TrafficControlEditor.tsx")
replace_once(
    traffic_editor,
    '''  const routeOptions = current.servers;''',
    '''  const routeOptions = current.servers ?? [];''',
    "optional server projection default",
)

# Bring older direct PendingPatchDraft fixtures up to the v4 internal contract.
mutation_test = Path("internal/admin/ui/src/test/config-mutation-machine.test.ts")
replace_once(
    mutation_test,
    '''        lifecycle: {
          changes: [],
          can_apply_hot: true,
          can_stage_restart: true,
          hot_paths: [],
          restart_required_paths: [],
          new_listener_only_paths: [],
          ignored_deprecated_paths: [],
          validation_rejected_paths: [],
          pending_subsystems: [],
        },
      },''',
    '''        lifecycle: {
          changes: [],
          can_apply_hot: true,
          can_stage_restart: true,
          hot_paths: [],
          restart_required_paths: [],
          new_listener_only_paths: [],
          ignored_deprecated_paths: [],
          validation_rejected_paths: [],
          pending_subsystems: [],
        },
        recommendedAction: "hot",
        pendingRestart: { state: "none", subsystems: [] },
        candidateState: "not_requested",
        requiresFreshPreview: false,
      },''',
    "mutation-machine v4 fixture",
)

preview_test = Path("internal/admin/ui/src/test/patch-preview-action.test.ts")
replace_once(
    preview_test,
    '''    previewDiff: { summary: "1 change" },
    lifecycle: lifecycle(),
    ...overrides,''',
    '''    previewDiff: { summary: "1 change" },
    lifecycle: lifecycle(),
    recommendedAction: "hot",
    pendingRestart: { state: "none", subsystems: [] },
    candidateState: "not_requested",
    requiresFreshPreview: false,
    ...overrides,''',
    "patch-preview v4 fixture",
)

traffic_test = Path("internal/admin/ui/src/test/traffic-toml.test.ts")
text = traffic_test.read_text(encoding="utf-8")
# Every legacy CompressionDraft literal gets the newly round-tripped level.
for marker in (
    '      encoders: ["gzip", "br"],\n      minSize: "1k",',
    '      encoders: ["gzip"],\n      minSize: "1k",',
    '        encoders: ["gzip"],\n        minSize: "1k",',
):
    if marker not in text:
        raise SystemExit(f"{traffic_test}: compression fixture marker missing: {marker!r}")
    text = text.replace(marker, marker.replace('\n' + marker.split('\n')[1].split('minSize')[0] + 'minSize', '\n' + marker.split('\n')[1].split('minSize')[0] + 'level: 0,\n' + marker.split('\n')[1].split('minSize')[0] + 'minSize'))
cache_marker = '''      diskPath: "/var/cache",
      defaultTTL: "60s",
      staleWhileRevalidate: "10s",'''
if cache_marker not in text:
    raise SystemExit(f"{traffic_test}: cache generator fixture marker missing")
text = text.replace(
    cache_marker,
    '''      diskPath: "/var/cache",
      diskMaxSize: "",
      defaultTTL: "60s",
      staleWhileRevalidate: "10s",
      staleIfError: "",''',
)
cache_warning = '''        diskPath: "",
        defaultTTL: "60s",
        staleWhileRevalidate: "",'''
if cache_warning not in text:
    raise SystemExit(f"{traffic_test}: cache warning fixture marker missing")
text = text.replace(
    cache_warning,
    '''        diskPath: "",
        diskMaxSize: "",
        defaultTTL: "60s",
        staleWhileRevalidate: "",
        staleIfError: "",''',
)
traffic_test.write_text(text, encoding="utf-8")
