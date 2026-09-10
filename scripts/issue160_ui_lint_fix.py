from pathlib import Path

p = Path("internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx")
s = p.read_text()

repls = [
    ('setAuditFile(data.audit_log_file ?? "");', 'setAuditFile(data.audit_log_file);'),
    ('setAuditRotateMaxMB(String(data.audit_log_rotate_max_mb ?? 100));', 'setAuditRotateMaxMB(String(data.audit_log_rotate_max_mb));'),
    ('setAuditRotateKeep(String(data.audit_log_rotate_keep ?? 14));', 'setAuditRotateKeep(String(data.audit_log_rotate_keep));'),
    ('normalizedAuditFile !== (data?.audit_log_file ?? "").trim()', 'normalizedAuditFile !== data?.audit_log_file.trim()'),
    ('parsedAuditMax !== (data.audit_log_rotate_max_mb ?? 100)', 'parsedAuditMax !== data.audit_log_rotate_max_mb'),
    ('parsedAuditKeep !== (data.audit_log_rotate_keep ?? 14)', 'parsedAuditKeep !== data.audit_log_rotate_keep'),
    ('normalizedAuditFile !== (data.audit_log_file ?? "").trim()', 'normalizedAuditFile !== data.audit_log_file.trim()'),
    ('if (parsedAuditMax !== (data.audit_log_rotate_max_mb ?? 100))', 'if (parsedAuditMax !== data.audit_log_rotate_max_mb)'),
    ('if (parsedAuditKeep !== (data.audit_log_rotate_keep ?? 14))', 'if (parsedAuditKeep !== data.audit_log_rotate_keep)'),
    ('    auditFile,\n    auditRotateKeep,\n    auditRotateMaxMB,\n', ''),
    ('onChange={(event) => setAuditFile(event.target.value)}', 'onChange={(event) => {\n                  setAuditFile(event.target.value);\n                }}'),
    ('onChange={(event) => setAuditRotateMaxMB(event.target.value)}', 'onChange={(event) => {\n                    setAuditRotateMaxMB(event.target.value);\n                  }}'),
    ('onChange={(event) => setAuditRotateKeep(event.target.value)}', 'onChange={(event) => {\n                    setAuditRotateKeep(event.target.value);\n                  }}'),
    ('` · generation ${data.audit_sink.generation}`', '` · generation ${String(data.audit_sink.generation)}`'),
]
for old, new in repls:
    if old not in s:
        raise SystemExit(f"missing lint anchor: {old!r}")
    s = s.replace(old, new, 1)
p.write_text(s)
print("HR-07C frontend strict-contract cleanup staged")
