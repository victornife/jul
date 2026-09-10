from pathlib import Path
import re


def load(path):
    return Path(path).read_text()

def save(path, text):
    Path(path).write_text(text)

def replace_once(text, old, new, path):
    if old not in text:
        raise SystemExit(f"missing anchor in {path}: {old[:100]!r}")
    return text.replace(old, new, 1)

# audit.go: keep the process-lifetime ring/API here; durable resource mechanics
# live in audit_sink_runtime.go.
p = "internal/admin/audit.go"
s = load(p)
for imp in ['\t"io"\n', '\t"os"\n', '\t"path/filepath"\n', '\n\t"gopkg.in/natefinch/lumberjack.v2"\n']:
    s = s.replace(imp, "")
start = s.index("type auditLog struct {")
end = s.index("// snapshot returns retained events", start)
replacement = r'''type auditLog struct {
	// mu is the single event linearization lock. ID assignment, ring placement,
	// sink-generation selection and write-lease/ticket allocation happen while
	// it is held; physical filesystem I/O never does.
	mu     sync.Mutex
	buf    []AuditEvent
	next   int
	full   bool
	nextID int64

	currentSink        *auditSinkGeneration
	sinkCfg            auditSinkConfig
	sinkConfigured     bool
	nextSinkGeneration uint64

	activeFailure      auditFailureCategory
	activeFailureAt    time.Time
	activeFailureErr   error // operator-log detail only; never serialized
	writeFailures      uint64
	rotateFailures     uint64
	cleanupFailures    uint64
	retirementFailures uint64
	log                *slog.Logger
}

// AuditSinkStatus is the bounded machine-safe view of the currently configured
// durable resource. Filesystem paths and raw OS errors deliberately live only
// in config:read settings/operator logs, never generic runtime health or readyz.
type AuditSinkStatus struct {
	Configured         bool      `json:"configured"`
	Active             bool      `json:"active"`
	Healthy            bool      `json:"healthy"`
	Generation         uint64    `json:"generation,omitempty"`
	WriteFailures      uint64    `json:"write_failures,omitempty"`
	RotateFailures     uint64    `json:"rotate_failures,omitempty"`
	CleanupFailures    uint64    `json:"cleanup_failures,omitempty"`
	RetireFailures     uint64    `json:"retirement_failures,omitempty"`
	LastFailureCategory string   `json:"last_failure_category,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitempty"`
}

'''
s = s[:start] + replacement + s[end:]
s = s.replace("// best-effort and never blocks request handling. actor/detail are redacted", "// best-effort with respect to the audited operation: a sink failure never rolls it back.\n// Durable persistence is synchronous and filesystem latency may therefore contribute\n// to request/finalizer latency. actor/detail are redacted")
save(p, s)

# PreparedAuth owns the candidate audit resource until the reload transaction
# either commits or aborts.
p = "internal/admin/rbac.go"
s = load(p)
s = replace_once(s, "type PreparedAuth struct {\n\tsnapshot *authSnapshot\n}\n", "type PreparedAuth struct {\n\tsnapshot *authSnapshot\n\taudit    *preparedAuditSink\n}\n", p)
save(p, s)

# Prepare candidate audit resource as part of the same admin artifact.
p = "internal/admin/runtime_snapshot.go"
s = load(p)
old = '''\ts.clearAdminPrepareFailure()\n\tif prepared == nil || prepared.snapshot == nil {\n\t\treturn prepared, nil\n\t}\n\tout := *prepared.snapshot\n\tout.cfg = cfg\n\treturn &PreparedAuth{snapshot: s.completeAdminRuntimeSnapshot(&out)}, nil\n'''
new = '''\tif prepared == nil || prepared.snapshot == nil {\n\t\ts.clearAdminPrepareFailure()\n\t\treturn prepared, nil\n\t}\n\tout := *prepared.snapshot\n\tout.cfg = cfg\n\tresult := &PreparedAuth{snapshot: s.completeAdminRuntimeSnapshot(&out)}\n\tif err := s.prepareAuditRuntime(cfg, result); err != nil {\n\t\treturn nil, err\n\t}\n\ts.clearAdminPrepareFailure()\n\treturn result, nil\n'''
s = replace_once(s, old, new, p)
save(p, s)

# PreparedCommit gets a typed post-Publish retirement hook. Existing callers keep
# NewPreparedCommit unchanged; admin audit uses the explicit constructor.
p = "internal/server/server.go"
s = load(p)
old = '''type PreparedCommit struct {\n\tcommitFn func()\n\tabortFn  func()\n\tonce     sync.Once\n}\n\n// NewPreparedCommit creates an exactly-once prepared artifact.\nfunc NewPreparedCommit(commit, abort func()) *PreparedCommit {\n\treturn &PreparedCommit{commitFn: commit, abortFn: abort}\n}\n\n// Commit installs the prepared artifact exactly once.\nfunc (p *PreparedCommit) Commit() {\n\tif p != nil {\n\t\tp.once.Do(func() {\n\t\t\tif p.commitFn != nil {\n\t\t\t\tp.commitFn()\n\t\t\t}\n\t\t})\n\t}\n}\n\n// Abort releases the prepared artifact exactly once without installing it.\nfunc (p *PreparedCommit) Abort() {\n\tif p != nil {\n\t\tp.once.Do(func() {\n\t\t\tif p.abortFn != nil {\n\t\t\t\tp.abortFn()\n\t\t\t}\n\t\t})\n\t}\n}\n'''
new = '''type PreparedCommit struct {\n\tcommitFn func()\n\tabortFn  func()\n\tretireFn func(context.Context)\n\tonce       sync.Once\n\tretireOnce sync.Once\n\tcommitted  atomic.Bool\n}\n\n// NewPreparedCommit creates an exactly-once prepared artifact without a\n// post-Publish resource retirement.\nfunc NewPreparedCommit(commit, abort func()) *PreparedCommit {\n\treturn NewPreparedCommitWithRetire(commit, abort, nil)\n}\n\n// NewPreparedCommitWithRetire creates a prepared admin artifact whose retired\n// resource is drained by ReloadPlan's existing bounded retirement phase.\nfunc NewPreparedCommitWithRetire(commit, abort func(), retire func(context.Context)) *PreparedCommit {\n\treturn &PreparedCommit{commitFn: commit, abortFn: abort, retireFn: retire}\n}\n\n// Commit installs the prepared artifact exactly once.\nfunc (p *PreparedCommit) Commit() {\n\tif p != nil {\n\t\tp.once.Do(func() {\n\t\t\tif p.commitFn != nil {\n\t\t\t\tp.commitFn()\n\t\t\t}\n\t\t\tp.committed.Store(true)\n\t\t})\n\t}\n}\n\n// Abort releases the prepared artifact exactly once without installing it.\nfunc (p *PreparedCommit) Abort() {\n\tif p != nil {\n\t\tp.once.Do(func() {\n\t\t\tif p.abortFn != nil {\n\t\t\t\tp.abortFn()\n\t\t\t}\n\t\t})\n\t}\n}\n\n// Retire runs post-Publish cleanup exactly once. It is a no-op for an aborted\n// artifact and is intentionally separate from Commit so Publish cannot block on\n// filesystem drain/close.\nfunc (p *PreparedCommit) Retire(ctx context.Context) {\n\tif p == nil || !p.committed.Load() {\n\t\treturn\n\t}\n\tp.retireOnce.Do(func() {\n\t\tif p.retireFn != nil {\n\t\t\tp.retireFn(ctx)\n\t\t}\n\t})\n}\n'''
s = replace_once(s, old, new, p)
save(p, s)

# Reuse ReloadPlan's existing bounded retirement context for admin resources.
p = "internal/server/reload_plan.go"
s = load(p)
anchor = "\tp.Runtime.Retire(ctx)\n"
if anchor not in s:
    raise SystemExit("missing runtime retire anchor")
s = s.replace(anchor, "\tp.PreparedAdmin.Retire(ctx)\n\tp.Runtime.Retire(ctx)\n", 1)
save(p, s)

# Composition root: audit prepare is aborted if TLS preparation fails, Commit is
# no-fail in-memory publication, and old sink retirement uses ReloadPlan.
p = "internal/app/serve.go"
s = load(p)
old = '''\t\t\tpreparedTLS, err := adminSrv.PrepareTLS(adminCfg)\n\t\t\tif err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n\t\t\treturn server.NewPreparedCommit(func() {\n\t\t\t\tadminSrv.CommitPreparedAuth(preparedRuntime)\n\t\t\t\tadminSrv.CommitPreparedTLS(preparedTLS)\n\t\t\t}, nil), nil\n'''
new = '''\t\t\tpreparedTLS, err := adminSrv.PrepareTLS(adminCfg)\n\t\t\tif err != nil {\n\t\t\t\tadminSrv.AbortPreparedAdminRuntime(preparedRuntime)\n\t\t\t\treturn nil, err\n\t\t\t}\n\t\t\treturn server.NewPreparedCommitWithRetire(func() {\n\t\t\t\tadminSrv.CommitPreparedAdminRuntime(preparedRuntime)\n\t\t\t\tadminSrv.CommitPreparedTLS(preparedTLS)\n\t\t\t}, func() {\n\t\t\t\tadminSrv.AbortPreparedAdminRuntime(preparedRuntime)\n\t\t\t}, func(retireCtx context.Context) {\n\t\t\t\tadminSrv.RetirePreparedAdminRuntime(retireCtx, preparedRuntime)\n\t\t\t}), nil\n'''
s = replace_once(s, old, new, p)
save(p, s)

# Lifecycle: promote exactly the three #160 fields and leave history/listener
# restart-bound.
p = "internal/lifecycle/registry.go"
s = load(p)
old = '''\tadminPaths := []string{\n\t\t"admin.audit_log_file",\n\t\t"admin.audit_log_rotate_keep",\n\t\t"admin.audit_log_rotate_max_mb",\n\t\t"admin.enabled",\n\t\t"admin.history_dir",\n\t\t"admin.history_keep",\n\t\t"admin.listen",\n\t}\n'''
new = '''\tadminPaths := []string{\n\t\t"admin.enabled",\n\t\t"admin.history_dir",\n\t\t"admin.history_keep",\n\t\t"admin.listen",\n\t}\n'''
s = replace_once(s, old, new, p)
needle = '''\tout = append(out,\n\t\thot("admin.console", SubAdmin, "the live admin server dispatches Console mode from one immutable per-request runtime snapshot published atomically"),\n'''
repl = '''\tout = append(out,\n\t\thot("admin.audit_log_file", SubAdmin, "the durable audit writer is fully prepared before Publish and atomically selected for new audit events while the process-lifetime ring and event IDs remain unchanged"),\n\t\thot("admin.audit_log_rotate_keep", SubAdmin, "path and rotation policy publish as one prepared durable-sink generation; existing audit events and files are not migrated"),\n\t\thot("admin.audit_log_rotate_max_mb", SubAdmin, "path and rotation policy publish as one prepared durable-sink generation; existing audit events and files are not migrated"),\n\t\thot("admin.console", SubAdmin, "the live admin server dispatches Console mode from one immutable per-request runtime snapshot published atomically"),\n'''
s = replace_once(s, needle, repl, p)
save(p, s)

# Typed sparse patch.
p = "internal/admin/patch_types.go"
s = load(p)
s = replace_once(s, '\tAdminLimits       *adminLimitsPatch       `json:"admin_limits,omitempty"`\n', '\tAdminLimits       *adminLimitsPatch       `json:"admin_limits,omitempty"`\n\tAdminAuditSink    *adminAuditSinkPatch    `json:"audit_sink,omitempty"`\n', p)
save(p, s)

p = "internal/admin/patch.go"
s = load(p)
s = replace_once(s, 'req.Op == "admin_console_set" || req.Op == "admin_plugin_upload_set" || req.Op == "admin_limits_set"', 'req.Op == "admin_console_set" || req.Op == "admin_plugin_upload_set" || req.Op == "admin_limits_set" || req.Op == "admin_audit_sink_set"', p)
save(p, s)

p = "internal/admin/patch_admin_runtime.go"
s = load(p)
insert = '''\n// adminAuditSinkPatch is #160's narrow sparse operation. File is a pointer so\n// an explicit empty string remains distinguishable from omission and therefore\n// represents the canonical durable-sink disable operation.\ntype adminAuditSinkPatch struct {\n\tFile        *string `json:"file,omitempty"`\n\tRotateMaxMB *int    `json:"rotate_max_mb,omitempty"`\n\tRotateKeep  *int    `json:"rotate_keep,omitempty"`\n}\n'''
pos = s.index("func applyAdminRuntimePatch")
s = s[:pos] + insert + "\n" + s[pos:]
case_anchor = '\tcase "admin_limits_set":\n'
case = '''\tcase "admin_audit_sink_set":\n\t\tif req.AdminAuditSink == nil {\n\t\t\treturn "", fmt.Errorf("admin_audit_sink_set: audit_sink payload is required")\n\t\t}\n\t\tpatch := req.AdminAuditSink\n\t\tvar changed []string\n\t\tif patch.File != nil {\n\t\t\tc.Admin.AuditLogFile = strings.TrimSpace(*patch.File)\n\t\t\tchanged = append(changed, "audit_log_file")\n\t\t}\n\t\tif patch.RotateMaxMB != nil {\n\t\t\tif *patch.RotateMaxMB < 0 {\n\t\t\t\treturn "", fmt.Errorf("admin_audit_sink_set: rotate_max_mb must be non-negative")\n\t\t\t}\n\t\t\tc.Admin.AuditLogRotateMaxMB = *patch.RotateMaxMB\n\t\t\tchanged = append(changed, "audit_log_rotate_max_mb")\n\t\t}\n\t\tif patch.RotateKeep != nil {\n\t\t\tif *patch.RotateKeep < 0 {\n\t\t\t\treturn "", fmt.Errorf("admin_audit_sink_set: rotate_keep must be non-negative")\n\t\t\t}\n\t\t\tc.Admin.AuditLogRotateKeep = *patch.RotateKeep\n\t\t\tchanged = append(changed, "audit_log_rotate_keep")\n\t\t}\n\t\tif len(changed) == 0 {\n\t\t\treturn "", fmt.Errorf("admin_audit_sink_set: at least one field is required")\n\t\t}\n\t\tsort.Strings(changed)\n\t\treturn "admin audit sink updated (" + strings.Join(changed, ", ") + ")", nil\n\n'''
s = replace_once(s, case_anchor, case + case_anchor, p)
save(p, s)

# Settings projection: path belongs only in config:read; operational status is
# bounded and generation-based.
p = "internal/admin/admin_runtime_status.go"
s = load(p)
s = replace_once(s, '\tMaxEventConns         int                                 `json:"max_event_conns"`\n\tLifecycle', '\tMaxEventConns         int                                 `json:"max_event_conns"`\n\tAuditLogFile          string                              `json:"audit_log_file"`\n\tAuditLogRotateMaxMB   int                                 `json:"audit_log_rotate_max_mb"`\n\tAuditLogRotateKeep    int                                 `json:"audit_log_rotate_keep"`\n\tAuditSink             *AuditSinkStatus                    `json:"audit_sink,omitempty"`\n\tLifecycle', p)
s = replace_once(s, '\t\tMaxEventConns:         cfg.MaxEventConns,\n\t\tLifecycle:', '\t\tMaxEventConns:         cfg.MaxEventConns,\n\t\tAuditLogFile:          cfg.AuditLogFile,\n\t\tAuditLogRotateMaxMB:   cfg.AuditLogRotateMaxMB,\n\t\tAuditLogRotateKeep:    cfg.AuditLogRotateKeep,\n\t\tAuditSink:             s.audit.statusReport(),\n\t\tLifecycle:', p)
s = replace_once(s, '\t\t\t"max_event_conns":          lifecycleFieldProjection("admin.max_event_conns"),\n', '\t\t\t"max_event_conns":          lifecycleFieldProjection("admin.max_event_conns"),\n\t\t\t"audit_log_file":            lifecycleFieldProjection("admin.audit_log_file"),\n\t\t\t"audit_log_rotate_max_mb":   lifecycleFieldProjection("admin.audit_log_rotate_max_mb"),\n\t\t\t"audit_log_rotate_keep":     lifecycleFieldProjection("admin.audit_log_rotate_keep"),\n', p)
save(p, s)

print("issue160 source transformations applied")
