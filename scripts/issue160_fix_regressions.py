from pathlib import Path


def patch(path: str, old: str, new: str) -> None:
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f"missing anchor in {path}: {old[:100]!r}")
    p.write_text(s.replace(old, new, 1))

# /readyz deliberately exposes only the bounded reason, never AdminHealth detail.
patch(
    "internal/admin/server_test.go",
    '''\tif !strings.Contains(body, "rbac policy update failed") {\n\t\tt.Errorf("readyz body should include detail, got %q", body)\n\t}\n''',
    '''\tif strings.Contains(body, "rbac policy update failed") {\n\t\tt.Errorf("readyz body leaked admin health detail, got %q", body)\n\t}\n''',
)

# The audit sink now owns its own prepared filesystem protocol. Generic admin
# preflight must not create/probe that path independently.
patch("internal/admin/preflight.go", '\t"path/filepath"\n', '')
patch(
    "internal/admin/preflight.go",
    '''\tif cfg.AuditLogFile != "" {\n\t\tdir := filepath.Dir(cfg.AuditLogFile)\n\t\tif err := probeWritable(dir, "[admin] audit_log_file directory"); err != nil {\n\t\t\treturn err\n\t\t}\n\t}\n''',
    '',
)

# Overview must render only bounded sink failure categories.
patch(
    "internal/admin/ui/src/features/overview/OverviewPanel.tsx",
    '''          {data.audit_sink.error && (\n            <span className="text-jul-muted">{data.audit_sink.error}</span>\n          )}\n''',
    '''          {data.audit_sink.last_failure_category && (\n            <span className="text-jul-muted">\n              Failure category: {data.audit_sink.last_failure_category}.\n            </span>\n          )}\n''',
)

# Keep the unified drawer backward-compatible with older fixtures/cached payloads
# while the server contract now always supplies these fields. Canonical defaults
# make an absent legacy field a no-op rather than an accidental audit mutation.
patch(
    "internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx",
    '''    setAuditFile(data.audit_log_file);\n    setAuditRotateMaxMB(String(data.audit_log_rotate_max_mb));\n    setAuditRotateKeep(String(data.audit_log_rotate_keep));\n''',
    '''    setAuditFile(data.audit_log_file ?? "");\n    setAuditRotateMaxMB(String(data.audit_log_rotate_max_mb ?? 100));\n    setAuditRotateKeep(String(data.audit_log_rotate_keep ?? 14));\n''',
)
patch(
    "internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx",
    '''  const auditPathChanging = normalizedAuditFile !== data?.audit_log_file.trim();\n''',
    '''  const auditPathChanging = normalizedAuditFile !== (data?.audit_log_file ?? "").trim();\n''',
)
patch(
    "internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx",
    '''    (parsedAuditMax !== data.audit_log_rotate_max_mb ||\n      parsedAuditKeep !== data.audit_log_rotate_keep),\n''',
    '''    (parsedAuditMax !== (data.audit_log_rotate_max_mb ?? 100) ||\n      parsedAuditKeep !== (data.audit_log_rotate_keep ?? 14)),\n''',
)
patch(
    "internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx",
    '''    if (normalizedAuditFile !== data.audit_log_file.trim()) auditSink.file = normalizedAuditFile;\n    if (parsedAuditMax !== data.audit_log_rotate_max_mb) auditSink.rotate_max_mb = parsedAuditMax;\n    if (parsedAuditKeep !== data.audit_log_rotate_keep) auditSink.rotate_keep = parsedAuditKeep;\n''',
    '''    if (normalizedAuditFile !== (data.audit_log_file ?? "").trim())\n      auditSink.file = normalizedAuditFile;\n    if (parsedAuditMax !== (data.audit_log_rotate_max_mb ?? 100))\n      auditSink.rotate_max_mb = parsedAuditMax;\n    if (parsedAuditKeep !== (data.audit_log_rotate_keep ?? 14))\n      auditSink.rotate_keep = parsedAuditKeep;\n''',
)

print("HR-07C regression fixes applied")
