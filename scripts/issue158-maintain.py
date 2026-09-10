from pathlib import Path

# Some historical tests/embedders build Server directly. Establish the same
# stable-manager invariant before the mux is constructed. Current branch may
# already contain the invariant from the first HR-07A core pass, so this edit is
# deliberately idempotent.
p = Path("internal/admin/routes.go")
s = p.read_text()
if "if s.limiter == nil {" not in s:
    old = "func (s *Server) routes() http.Handler {\n\tmux := http.NewServeMux()\n"
    new = "func (s *Server) routes() http.Handler {\n\tif s.limiter == nil {\n\t\ts.limiter = newAdminLimiter(s.log)\n\t}\n\tmux := http.NewServeMux()\n"
    if old not in s:
        raise SystemExit("routes initializer anchor not found")
    s = s.replace(old, new, 1)
p.write_text(s)

# Keep RouteSpec as the single route authority. Legacy internal validate/diff
# predate ExternalOperation metadata and therefore need an explicit bounded
# per-method admission override in the catalogue itself.
p = Path("internal/admin/route_catalog.go")
s = p.read_text()
anchor = "\tOperations map[string]ExternalOperation\n"
addition = anchor + "\tLimitClasses map[string]limitKind\n"
if "LimitClasses map[string]limitKind" not in s:
    if anchor not in s:
        raise SystemExit("RouteSpec anchor not found")
    s = s.replace(anchor, addition, 1)
replacements = {
    "\t\tPattern:    \"/api/config/validate\",\n\t\tMethods:    []string{http.MethodPost},\n\t\tPermission: rbac.ConfigWrite,\n\t\tHandler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigValidate) },":
    "\t\tPattern:      \"/api/config/validate\",\n\t\tMethods:      []string{http.MethodPost},\n\t\tPermission:   rbac.ConfigWrite,\n\t\tLimitClasses: map[string]limitKind{http.MethodPost: limitApply},\n\t\tHandler:      func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigValidate) },",
    "\t\tPattern:    \"/api/config/diff\",\n\t\tMethods:    []string{http.MethodPost},\n\t\tPermission: rbac.ConfigWrite,\n\t\tHandler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigDiff) },":
    "\t\tPattern:      \"/api/config/diff\",\n\t\tMethods:      []string{http.MethodPost},\n\t\tPermission:   rbac.ConfigWrite,\n\t\tLimitClasses: map[string]limitKind{http.MethodPost: limitApply},\n\t\tHandler:      func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigDiff) },",
}
for old_block, new_block in replacements.items():
    if new_block not in s:
        if old_block not in s:
            raise SystemExit("legacy limiter route anchor not found")
        s = s.replace(old_block, new_block, 1)
p.write_text(s)

# Derive rate class from route metadata, not path strings and not generic
# ConfigWrite permission. Only higher-cost config assessment/mutation operations
# use the apply budget; ordinary mutations remain write.
p = Path("internal/admin/ratelimit.go")
s = p.read_text()
begin = s.index("func limitClassForSpec(spec RouteSpec, method string) limitKind {")
end = s.index("\nfunc (l *adminLimiter) maybeLogRejection", begin)
replacement = '''func limitClassForSpec(spec RouteSpec, method string) limitKind {
\tif explicit, ok := spec.LimitClasses[method]; ok {
\t\treturn explicit
\t}
\tif safeMethod(method) {
\t\treturn limitRead
\t}

\tperm := permissionForMethod(spec, method)
\tif perm == rbac.ConfigApply || perm == rbac.HistoryRollback || perm == rbac.ConfigAdopt || hasPermission(spec.AnyPermissions, rbac.ConfigApply) || hasPermission(spec.AnyPermissions, rbac.HistoryRollback) || hasPermission(spec.AnyPermissions, rbac.ConfigAdopt) {
\t\treturn limitApply
\t}
\tif op, ok := spec.Operations[method]; ok {
\t\tswitch op.ID {
\t\tcase "validateConfig", "planConfig", "previewConfigPatch", "applyConfig", "applyConfigPatch", "rollbackConfig", "previewAdoptExternal", "adoptExternal", "discardPendingRestart":
\t\t\treturn limitApply
\t\t}
\t}
\treturn limitWrite
}
'''
s = s[:begin] + replacement + s[end:]
p.write_text(s)

# Remove now-unused frontend helper after the explicit Number validation cleanup.
p = Path("internal/admin/ui/src/features/plugins/AdminRuntimeSettingsDrawer.tsx")
s = p.read_text()
helper = '''function integerValue(value: string): number | null {
  const parsed = Number(value);
  return Number.isInteger(parsed) ? parsed : null;
}

'''
s = s.replace(helper, "")
p.write_text(s)

# Move the exact four HR-07A lifecycle decisions into the authoritative registry
# instead of mutating Registry later from an init hook.
p = Path("internal/lifecycle/registry.go")
s = p.read_text()
for line in (
    '\t\t"admin.max_event_conns",\n',
    '\t\t"admin.rate_limit_apply_per_min",\n',
    '\t\t"admin.rate_limit_read_per_min",\n',
    '\t\t"admin.rate_limit_write_per_min",\n',
):
    s = s.replace(line, "", 1)
marker = '\t\thot("admin.plugin_upload_dir", SubAdmin, "candidate storage is preflighted before Publish and each upload is confined to the directory captured at request start"),\n'
hot = marker + '\t\thot("admin.rate_limit_read_per_min", SubAdmin, "new admin requests use the rate policy from the immutable admin runtime generation captured at request start while stable per-client bucket state survives reload"),\n\t\thot("admin.rate_limit_write_per_min", SubAdmin, "new admin requests use the rate policy from the immutable admin runtime generation captured at request start while stable per-client bucket state survives reload"),\n\t\thot("admin.rate_limit_apply_per_min", SubAdmin, "new admin requests use the rate policy from the immutable admin runtime generation captured at request start while stable per-client bucket state survives reload"),\n\t\thot("admin.max_event_conns", SubAdmin, "new SSE admissions use the captured per-client connection cap while existing leases and connection counts survive policy reload"),\n'
if 'hot("admin.max_event_conns", SubAdmin' not in s:
    if marker not in s:
        raise SystemExit("lifecycle anchor not found")
    s = s.replace(marker, hot, 1)
p.write_text(s)
Path("internal/lifecycle/admin_limits.go").unlink(missing_ok=True)
