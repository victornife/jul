#!/usr/bin/env python3
from pathlib import Path

p = Path("internal/admin/ui/src/api/client.ts")
text = p.read_text()

def rep(old: str, new: str, n: int = 1) -> None:
    global text
    got = text.count(old)
    if got != n:
        raise SystemExit(f"client.ts marker count {got}, want {n}: {old[:100]!r}")
    text = text.replace(old, new, n)

admin_health = '''export type AdminHealthStatus = z.infer<typeof AdminHealthStatusSchema>;
'''
admin_runtime = admin_health + '''
// HR-06B bounded effective admin policy. No configured upload path, filename,
// token, or raw preparation error is exposed here.
export const AdminRuntimeStatusSchema = z.object({
  generation: z.string(),
  console_compiled: z.boolean(),
  console_configured: z.boolean(),
  console_effective: z.boolean(),
  plugins_compiled: z.boolean(),
  upload_enabled: z.boolean(),
  upload_max_size_mb: z.number().int().nonnegative(),
  upload_directory_health: z.string(),
  preparation_failure: z.string().optional(),
  last_upload_rejection: z.string().optional(),
});
export type AdminRuntimeStatus = z.infer<typeof AdminRuntimeStatusSchema>;
'''
rep(admin_health, admin_runtime)

rep(
    '  admin_health: AdminHealthStatusSchema.optional(),\n',
    '  admin_health: AdminHealthStatusSchema.optional(),\n'
    '  // HR-06B request-generation policy and bounded health metadata.\n'
    '  admin_runtime: AdminRuntimeStatusSchema.optional(),\n',
)

lifecycle_type = '''export type GlobalSettingsProjection = z.infer<typeof GlobalSettingsProjectionSchema>;
'''
settings_schema = lifecycle_type + '''

// Secret-safe HR-06B settings projection. The upload directory is configuration
// data gated by config:read; it is intentionally absent from runtime metrics/status.
export const AdminRuntimeSettingsProjectionSchema = z.object({
  console: z.boolean(),
  console_compiled: z.boolean(),
  console_effective: z.boolean(),
  plugin_upload_enabled: z.boolean(),
  plugin_upload_max_size_mb: z.number().int().nonnegative(),
  plugin_upload_dir: z.string(),
  plugin_upload_effective: z.boolean(),
  upload_directory_health: z.string(),
  lifecycle: z.record(z.string(), LifecycleFieldProjectionSchema).default({}),
});
export type AdminRuntimeSettingsProjection = z.infer<typeof AdminRuntimeSettingsProjectionSchema>;
'''
rep(lifecycle_type, settings_schema)

rep(
    '  | { op: "global_set"; global: GlobalPatch }\n',
    '  | { op: "global_set"; global: GlobalPatch }\n'
    '  | { op: "admin_console_set"; enabled: boolean }\n'
    '  | {\n'
    '      op: "admin_plugin_upload_set";\n'
    '      plugin_upload: { enabled?: boolean; max_size_mb?: number; directory?: string };\n'
    '    }\n',
)

rep(
    '''export function fetchTrafficControls(): Promise<TrafficControls> {
  return api<unknown>("/traffic-controls").then((d) => TrafficControlsSchema.parse(d));
}
''',
    '''export function fetchTrafficControls(): Promise<TrafficControls> {
  return api<unknown>("/traffic-controls").then((d) => TrafficControlsSchema.parse(d));
}

export function fetchAdminRuntimeSettings(): Promise<AdminRuntimeSettingsProjection> {
  return api<unknown>("/config/settings").then((d) => AdminRuntimeSettingsProjectionSchema.parse(d));
}
''',
)

p.write_text(text)
