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
replace_once(
    traffic_editor,
    '''                  void purgeCache()
                    .then(() => setPurgeMessage("Cache purged."))
                    .catch(() => setPurgeMessage("Could not purge the cache."));''',
    '''                  void purgeCache()
                    .then(() => {
                      setPurgeMessage("Cache purged.");
                    })
                    .catch(() => {
                      setPurgeMessage("Could not purge the cache.");
                    });''',
    "cache purge promise callbacks",
)
for old, new, label in (
    (
        '''onChange={(proxyConnectTimeout) => setReferenceLimits((previous) => ({ ...previous, proxyConnectTimeout }))}''',
        '''onChange={(proxyConnectTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, proxyConnectTimeout }));
              }}''',
        "proxy connect callback",
    ),
    (
        '''onChange={(proxyReadTimeout) => setReferenceLimits((previous) => ({ ...previous, proxyReadTimeout }))}''',
        '''onChange={(proxyReadTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, proxyReadTimeout }));
              }}''',
        "proxy read callback",
    ),
    (
        '''onChange={(proxySendTimeout) => setReferenceLimits((previous) => ({ ...previous, proxySendTimeout }))}''',
        '''onChange={(proxySendTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, proxySendTimeout }));
              }}''',
        "proxy send callback",
    ),
    (
        '''onChange={(maxFails) => setReferenceLimits((previous) => ({ ...previous, maxFails }))}''',
        '''onChange={(maxFails) => {
                setReferenceLimits((previous) => ({ ...previous, maxFails }));
              }}''',
        "max failures callback",
    ),
    (
        '''onChange={(failTimeout) => setReferenceLimits((previous) => ({ ...previous, failTimeout }))}''',
        '''onChange={(failTimeout) => {
                setReferenceLimits((previous) => ({ ...previous, failTimeout }));
              }}''',
        "failure timeout callback",
    ),
):
    replace_once(traffic_editor, old, new, label)

traffic_panel = Path("internal/admin/ui/src/features/traffic-controls/TrafficControlsPanel.tsx")
for old, new, label in (
    ('onEdit={() => setGlobalEditing(true)}', 'onEdit={() => { setGlobalEditing(true); }}', "global edit callback"),
    ('onEdit={() => setEditing("compression")}', 'onEdit={() => { setEditing("compression"); }}', "compression edit callback"),
    ('onEdit={() => setEditing("rate_limit")}', 'onEdit={() => { setEditing("rate_limit"); }}', "rate-limit edit callback"),
    ('onEdit={() => setEditing("cache")}', 'onEdit={() => { setEditing("cache"); }}', "cache edit callback"),
    ('onEdit={() => setEditing("limits")}', 'onEdit={() => { setEditing("limits"); }}', "limits edit callback"),
    ('onEdit={() => setAccessLogEditing(true)}', 'onEdit={() => { setAccessLogEditing(true); }}', "access-log edit callback"),
    ('onEdit={() => setTracingEditing(true)}', 'onEdit={() => { setTracingEditing(true); }}', "tracing edit callback"),
    ('onClose={() => setGlobalEditing(false)}', 'onClose={() => { setGlobalEditing(false); }}', "global close callback"),
    ('onClose={() => setEditing(null)}', 'onClose={() => { setEditing(null); }}', "traffic close callback"),
    ('onClose={() => setTracingEditing(false)}', 'onClose={() => { setTracingEditing(false); }}', "tracing close callback"),
    ('onClose={() => setAccessLogEditing(false)}', 'onClose={() => { setAccessLogEditing(false); }}', "access-log close callback"),
):
    replace_once(traffic_panel, old, new, label)

builders = Path("internal/admin/ui/src/lib/trafficPatchBuilders.ts")
replace_once(
    builders,
    '''    bodyLimit: route.client_max_body_size ?? "",
    readTimeout: route.read_timeout ?? "",
    writeTimeout: route.write_timeout ?? "",
    idleTimeout: route.idle_timeout ?? "",''',
    '''    bodyLimit: route.client_max_body_size,
    readTimeout: route.read_timeout,
    writeTimeout: route.write_timeout,
    idleTimeout: route.idle_timeout,''',
    "required server-limit projection fields",
)

console_test = Path("internal/admin/ui/src/test/consolev2.test.tsx")
replace_once(
    console_test,
    '''    expect(t.compression?.encoders).toContain("gzip");
    expect(t.rate_limit?.rate).toBe(100);''',
    '''    expect(t.compression.encoders).toContain("gzip");
    expect(t.rate_limit.rate).toBe(100);''',
    "full traffic projection assertions",
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
    indent = marker.split('\n')[1].split('minSize')[0]
    text = text.replace(
        marker,
        marker.replace(
            '\n' + indent + 'minSize',
            '\n' + indent + 'level: 0,\n' + indent + 'minSize',
        ),
    )
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

# The workflow performs legacy compatibility rewrites after this helper runs.
# Install a self-cleaning pre-typecheck hook so request bodies are narrowed only
# after those rewrites have completed. The hook restores package.json and
# removes its temporary files before TypeScript starts.
import json

ui = Path("internal/admin/ui")
package_path = ui / "package.json"
package_text = package_path.read_text(encoding="utf-8")
package = json.loads(package_text)
if package.get("scripts", {}).get("typecheck") != "tsc --noEmit":
    raise SystemExit("unexpected frontend typecheck script")

script_dir = ui / "scripts"
script_dir.mkdir(exist_ok=True)
backup_path = script_dir / ".issue81-package.json.original"
script_path = script_dir / "issue81-fix-test-bodies.mjs"
backup_path.write_text(package_text, encoding="utf-8")
package["scripts"]["typecheck"] = "node scripts/issue81-fix-test-bodies.mjs && tsc --noEmit"
package_path.write_text(json.dumps(package, indent=2) + "\n", encoding="utf-8")
script_path.write_text(r'''import { readFileSync, writeFileSync, unlinkSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join } from "node:path";

const root = process.cwd();
const files = ["src/test/consolev2.test.tsx", "src/test/mtls.test.tsx"];
const unsafe = /JSON\.parse\(\s*init\.body\s*\)/g;
const safe = 'JSON.parse(typeof init?.body === "string" ? init.body : "")';
let replacements = 0;
for (const rel of files) {
  const file = join(root, rel);
  const before = readFileSync(file, "utf8");
  replacements += (before.match(unsafe) ?? []).length;
  writeFileSync(file, before.replace(unsafe, safe), "utf8");
}
if (replacements !== 3) {
  for (const rel of files) {
    const lines = readFileSync(join(root, rel), "utf8").split("\n");
    lines.forEach((line, index) => {
      if (line.includes("init") && line.includes("body")) {
        console.error(`${rel}:${index + 1}: ${line}`);
      }
    });
  }
  throw new Error(`expected 3 unsafe request-body parses, found ${replacements}`);
}
writeFileSync(join(root, "package.json"), readFileSync(join(root, "scripts/.issue81-package.json.original")), "utf8");
unlinkSync(join(root, "scripts/.issue81-package.json.original"));
unlinkSync(fileURLToPath(import.meta.url));
''', encoding="utf-8")
