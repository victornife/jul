from pathlib import Path
import re

def rw(path): return Path(path).read_text()
def ww(path, s): Path(path).write_text(s)
def once(s, old, new, path):
    if old not in s: raise SystemExit(f'missing anchor {path}: {old[:120]!r}')
    return s.replace(old, new, 1)

# Backend legacy assertion.
p='internal/admin/consolev2_test.go'; s=rw(p)
s=once(s, 'if out.AuditSink.Error == "" {\n\t\tt.Error("audit_sink.error should explain the degradation")\n\t}', 'if out.AuditSink.LastFailureCategory == "" {\n\t\tt.Error("audit_sink.last_failure_category should explain the degradation")\n\t}', p)
ww(p,s)

# Client contracts.
p='internal/admin/ui/src/api/client.ts'; s=rw(p)
old='''export const AuditSinkStatusSchema = z.object({
  configured: z.boolean(),
  path: z.string().optional(),
  healthy: z.boolean(),
  error: z.string().optional(),
  write_failures: z.number().optional(),
});'''
new='''export const AuditSinkStatusSchema = z.object({
  configured: z.boolean(),
  active: z.boolean(),
  healthy: z.boolean(),
  generation: z.number().int().nonnegative().optional(),
  write_failures: z.number().int().nonnegative().optional(),
  rotate_failures: z.number().int().nonnegative().optional(),
  cleanup_failures: z.number().int().nonnegative().optional(),
  retirement_failures: z.number().int().nonnegative().optional(),
  last_failure_category: z.string().optional(),
  last_failure_at: z.string().optional(),
});'''
s=once(s,old,new,p)
# ConfigPatch union.
s=once(s, '  | { op: "admin_limits_set"; admin_limits: { read_per_min?: number; write_per_min?: number; apply_per_min?: number; max_event_conns?: number } }', '  | { op: "admin_limits_set"; admin_limits: { read_per_min?: number; write_per_min?: number; apply_per_min?: number; max_event_conns?: number } }\n  | { op: "admin_audit_sink_set"; audit_sink: { file?: string; rotate_max_mb?: number; rotate_keep?: number } }', p)
# Settings projection: locate schema and inject after max_event_conns.
marker='export const AdminRuntimeSettingsProjectionSchema = z.object({'
pos=s.find(marker)
if pos < 0: raise SystemExit('settings schema not found')
end=s.find('});',pos)
block=s[pos:end]
block2=once(block,'  max_event_conns: z.number().int().positive(),','  max_event_conns: z.number().int().positive(),\n  audit_log_file: z.string(),\n  audit_log_rotate_max_mb: z.number().int().nonnegative(),\n  audit_log_rotate_keep: z.number().int().nonnegative(),\n  audit_sink: AuditSinkStatusSchema.optional(),',p)
# lifecycle object keys (anchor max_event_conns key within block)
block2=once(block2,'    max_event_conns: LifecycleFieldProjectionSchema,','    max_event_conns: LifecycleFieldProjectionSchema,\n    audit_log_file: LifecycleFieldProjectionSchema,\n    audit_log_rotate_max_mb: LifecycleFieldProjectionSchema,\n    audit_log_rotate_keep: LifecycleFieldProjectionSchema,',p)
s=s[:pos]+block2+s[end:]
ww(p,s)

# Drawer: add audit state + sparse patch + warnings/status.
p='internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx'; s=rw(p)
s=once(s,'  const [maxEventConns, setMaxEventConns] = useState("4");\n  const [confirmDisable, setConfirmDisable] = useState(false);','  const [maxEventConns, setMaxEventConns] = useState("4");\n  const [auditFile, setAuditFile] = useState("");\n  const [auditRotateMaxMB, setAuditRotateMaxMB] = useState("100");\n  const [auditRotateKeep, setAuditRotateKeep] = useState("14");\n  const [confirmDisable, setConfirmDisable] = useState(false);',p)
s=once(s,'    setMaxEventConns(String(data.max_event_conns));\n    setConfirmDisable(false);','    setMaxEventConns(String(data.max_event_conns));\n    setAuditFile(data.audit_log_file);\n    setAuditRotateMaxMB(String(data.audit_log_rotate_max_mb));\n    setAuditRotateKeep(String(data.audit_log_rotate_keep));\n    setConfirmDisable(false);',p)
s=once(s,'  const loweringSSE = Boolean(data && effectiveConns !== null && effectiveConns < data.max_event_conns);','''  const loweringSSE = Boolean(data && effectiveConns !== null && effectiveConns < data.max_event_conns);
  const parsedAuditMax = Number(auditRotateMaxMB);
  const parsedAuditKeep = Number(auditRotateKeep);
  const auditRotationValid = Number.isInteger(parsedAuditMax) && parsedAuditMax >= 0 && Number.isInteger(parsedAuditKeep) && parsedAuditKeep >= 0;
  const normalizedAuditFile = auditFile.trim();
  const auditPathChanging = normalizedAuditFile !== data?.audit_log_file.trim();
  const auditDisabling = Boolean(data?.audit_log_file && normalizedAuditFile === "");
  const auditRotationChanging = Boolean(data && (parsedAuditMax !== data.audit_log_rotate_max_mb || parsedAuditKeep !== data.audit_log_rotate_keep));''',p)
anchor='''    if (Object.keys(limits).length > 0) {
      next.push({ op: "admin_limits_set", admin_limits: limits });
    }
    return next;'''
repl='''    if (Object.keys(limits).length > 0) {
      next.push({ op: "admin_limits_set", admin_limits: limits });
    }
    const auditSink: { file?: string; rotate_max_mb?: number; rotate_keep?: number } = {};
    if (normalizedAuditFile !== data.audit_log_file.trim()) auditSink.file = normalizedAuditFile;
    if (parsedAuditMax !== data.audit_log_rotate_max_mb) auditSink.rotate_max_mb = parsedAuditMax;
    if (parsedAuditKeep !== data.audit_log_rotate_keep) auditSink.rotate_keep = parsedAuditKeep;
    if (Object.keys(auditSink).length > 0) {
      next.push({ op: "admin_audit_sink_set", audit_sink: auditSink });
    }
    return next;'''
s=once(s,anchor,repl,p)
# broaden deps by replacing exact tail of useMemo list.
s=once(s,'[consoleEnabled, data, directory, limitsValid, maxValid, parsedApply, parsedConns, parsedMax, parsedRead, parsedWrite, uploadEnabled]);','[auditFile, auditRotateKeep, auditRotateMaxMB, consoleEnabled, data, directory, limitsValid, maxValid, normalizedAuditFile, parsedApply, parsedAuditKeep, parsedAuditMax, parsedConns, parsedMax, parsedRead, parsedWrite, uploadEnabled]);',p)
s=once(s,'runner.busy || ops.length === 0 || !maxValid || !limitsValid || directory.trim() === "" || (consoleDisabling && !confirmDisable);','runner.busy || ops.length === 0 || !maxValid || !limitsValid || !auditRotationValid || directory.trim() === "" || (consoleDisabling && !confirmDisable);',p)
# Add section before final explanatory paragraph.
anchor='''          <p className="text-xs text-jul-muted">
            Save opens the authoritative server-side lifecycle preview.'''
section='''          <section className="rounded-lg border border-jul-border bg-jul-surface p-4">
            <div className="mb-3 flex items-center justify-between gap-2">
              <span className="text-sm font-medium text-jul-text">Durable audit sink</span>
              <LifecycleBadge field={data.lifecycle.audit_log_file} />
            </div>
            <label className="block text-xs text-jul-muted">
              Audit file (empty = durable persistence disabled)
              <input type="text" value={auditFile} onChange={(event) => setAuditFile(event.target.value)} className="mt-1 w-full rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 font-mono text-sm text-jul-text" />
            </label>
            <div className="mt-3 grid grid-cols-2 gap-3">
              <label className="text-xs text-jul-muted">
                Rotate max MB <LifecycleBadge field={data.lifecycle.audit_log_rotate_max_mb} />
                <input type="number" min={0} step={1} value={auditRotateMaxMB} onChange={(event) => setAuditRotateMaxMB(event.target.value)} className="mt-1 w-full rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text" />
              </label>
              <label className="text-xs text-jul-muted">
                Backups to keep <LifecycleBadge field={data.lifecycle.audit_log_rotate_keep} />
                <input type="number" min={0} step={1} value={auditRotateKeep} onChange={(event) => setAuditRotateKeep(event.target.value)} className="mt-1 w-full rounded-md border border-jul-border bg-jul-bg px-2 py-1.5 text-sm text-jul-text" />
              </label>
            </div>
            {!auditRotationValid && <p className="mt-1 text-xs text-jul-danger">Rotation values must be non-negative whole numbers. Zero selects the canonical default.</p>}
            {auditPathChanging && !auditDisabling && (
              <p className="mt-3 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2 text-xs text-jul-text">
                Existing audit files and backups at the previous path are not copied, moved, merged, or deleted. New durable events use the new destination only after the committed transition.
              </p>
            )}
            {auditDisabling && (
              <p className="mt-3 rounded-md border border-jul-warning/40 bg-jul-warning/10 p-2 text-xs text-jul-text">
                Durable persistence stops for new audit events after Publish. The in-memory audit ring and event IDs continue, and existing audit files/backups are retained.
              </p>
            )}
            {auditRotationChanging && (
              <p className="mt-3 rounded-md border border-jul-border p-2 text-xs text-jul-muted">
                The committed rotation policy governs the active sink. Preview never prunes backups; existing backups are only subject to normal retention during later active rotations.
              </p>
            )}
            <p className="mt-3 text-xs text-jul-muted">
              Status: {data.audit_sink ? (data.audit_sink.healthy ? "Healthy" : data.audit_sink.active ? "Degraded" : "Configured, inactive") : "Disabled"}
              {data.audit_sink?.generation ? ` · generation ${data.audit_sink.generation}` : ""}
              {data.audit_sink?.last_failure_category ? ` · ${data.audit_sink.last_failure_category}` : ""}
            </p>
          </section>

          <p className="text-xs text-jul-muted">
            Save opens the authoritative server-side lifecycle preview.'''
s=once(s,anchor,section,p)
ww(p,s)

print('HR-07C UI patch applied')
