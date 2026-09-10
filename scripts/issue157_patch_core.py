#!/usr/bin/env python3
from pathlib import Path


def rep(path: str, old: str, new: str, n: int = 1) -> None:
    p = Path(path)
    text = p.read_text()
    got = text.count(old)
    if got != n:
        raise SystemExit(f"{path}: marker count {got}, want {n}: {old[:100]!r}")
    p.write_text(text.replace(old, new, n))


# Canonical lifecycle authority: exactly the four #157 fields become hot.
p = Path("internal/lifecycle/registry.go")
text = p.read_text()
for field in (
    "admin.console",
    "admin.plugin_upload_dir",
    "admin.plugin_upload_enabled",
    "admin.plugin_upload_max_size",
):
    marker = f'\t\t"{field}",\n'
    if text.count(marker) != 1:
        raise SystemExit(f"registry marker mismatch: {field}")
    text = text.replace(marker, "", 1)
marker = "\tout := restartGroup(SubAdmin, reasonAdminStartup, adminPaths...)\n"
hot = marker + (
    '\tout = append(out,\n'
    '\t\thot("admin.console", SubAdmin, "the live admin server dispatches Console mode from one immutable per-request runtime snapshot published atomically"),\n'
    '\t\thot("admin.plugin_upload_enabled", SubAdmin, "new upload requests read admission state from the immutable admin runtime snapshot captured at request start"),\n'
    '\t\thot("admin.plugin_upload_max_size", SubAdmin, "each upload captures its size limit from the immutable admin runtime snapshot before body processing"),\n'
    '\t\thot("admin.plugin_upload_dir", SubAdmin, "candidate storage is preflighted before Publish and each upload is confined to the directory captured at request start"),\n'
    '\t)\n'
)
if text.count(marker) != 1:
    raise SystemExit("registry restart marker mismatch")
p.write_text(text.replace(marker, hot, 1))
Path("internal/lifecycle/admin_runtime.go").unlink()

# Shared composition root prepares the operational snapshot explicitly before TLS/Publish.
rep(
    "internal/app/serve.go",
    "\t\t\tprepared := admin.PrepareAuth(adminCfg, policy)\n\t\t\tpreparedTLS, err := adminSrv.PrepareTLS(adminCfg)\n",
    "\t\t\tprepared := admin.PrepareAuth(adminCfg, policy)\n"
    "\t\t\tpreparedRuntime, err := adminSrv.PrepareAdminRuntime(adminCfg, prepared)\n"
    "\t\t\tif err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n"
    "\t\t\tpreparedTLS, err := adminSrv.PrepareTLS(adminCfg)\n",
)
rep(
    "internal/app/serve.go",
    "\t\t\t\tadminSrv.CommitPreparedAuth(prepared)\n",
    "\t\t\t\tadminSrv.CommitPreparedAuth(preparedRuntime)\n",
)

# TLS preparation remains single-purpose.
p = Path("internal/admin/tls.go")
text = p.read_text()
start = text.index("// PrepareTLS is the existing no-side-effect admin resource preparation hook")
fn = text.index("\tif s.certProvider == nil", start)
prefix = (
    "// PrepareTLS builds only candidate TLS certificate state. Operational admin\n"
    "// policy (Console/upload) is prepared by PrepareAdminRuntime before the common\n"
    "// Publish boundary.\n"
    "func (s *Server) PrepareTLS(cfg config.AdminConfig) (*PreparedTLS, error) {\n"
)
p.write_text(text[:start] + prefix + text[fn:])

# Bounded diagnostics and safe GET settings projection.
rep(
    "internal/admin/server.go",
    "\tauthGen atomic.Uint64\n\t// applyMu serializes config writes",
    "\tauthGen atomic.Uint64\n"
    "\t// Low-cardinality #157 diagnostics only; never paths, filenames or secrets.\n"
    "\tadminPrepareFailure   atomic.Pointer[string]\n"
    "\tpluginUploadRejection atomic.Pointer[string]\n"
    "\t// applyMu serializes config writes",
)
rep(
    "internal/admin/server.go",
    "func (s *Server) handleConfigSettings(w http.ResponseWriter, r *http.Request) {\n"
    "\tif r.Method != http.MethodPost && r.Method != http.MethodPut {\n"
    "\t\tw.Header().Set(\"Allow\", \"POST, PUT\")",
    "func (s *Server) handleConfigSettings(w http.ResponseWriter, r *http.Request) {\n"
    "\tif r.Method == http.MethodGet {\n\t\ts.handleAdminRuntimeSettingsRead(w, r)\n\t\treturn\n\t}\n"
    "\tif r.Method != http.MethodPost && r.Method != http.MethodPut {\n"
    "\t\tw.Header().Set(\"Allow\", \"GET, POST, PUT\")",
)

# Extend #95's one immutable pointer with operational metadata; no independent policy atomics.
rep(
    "internal/admin/rbac.go",
    "type authSnapshot struct {\n\tmode   authMode\n\tcfg    config.AdminConfig\n\tpolicy *rbac.Policy\n\tgen    string\n}",
    "type authSnapshot struct {\n"
    "\tmode            authMode\n\tcfg             config.AdminConfig\n\tpolicy          *rbac.Policy\n\tgen             string\n"
    "\tconsoleCompiled bool\n\tpluginsCompiled bool\n\tuploadDirHealth string\n}",
)
rep(
    "internal/admin/rbac.go",
    "\t_, _ = h.Write([]byte(strconv.FormatBool(cfg.ConsoleEnabled())))\n"
    "\t_, _ = h.Write([]byte{0})\n"
    "\t_, _ = h.Write([]byte(strconv.FormatBool(cfg.RBAC.Enabled)))",
    "\t_, _ = h.Write([]byte(strconv.FormatBool(cfg.ConsoleEnabled())))\n"
    "\t_, _ = h.Write([]byte{0})\n"
    "\t_, _ = h.Write([]byte(strconv.FormatBool(pluginUploadEnabled(cfg))))\n"
    "\t_, _ = h.Write([]byte{0})\n"
    "\t_, _ = h.Write([]byte(strconv.Itoa(cfg.PluginUploadMaxSize)))\n"
    "\t_, _ = h.Write([]byte{0})\n"
    "\t_, _ = h.Write([]byte(normalizePluginUploadDir(cfg.PluginUploadDir)))\n"
    "\t_, _ = h.Write([]byte{0})\n"
    "\t_, _ = h.Write([]byte(strconv.FormatBool(cfg.RBAC.Enabled)))",
)
rep(
    "internal/admin/rbac.go",
    "\ts.authState.Store(deriveAuthSnapshot(cfg, p, gen))\n",
    "\ts.authState.Store(s.completeAdminRuntimeSnapshot(deriveAuthSnapshot(cfg, p, gen)))\n",
)
rep(
    "internal/admin/rbac.go",
    "\treturn deriveAuthSnapshot(s.cfg, nil, \"0\")\n",
    "\treturn s.completeAdminRuntimeSnapshot(deriveAuthSnapshot(s.cfg, nil, \"0\"))\n",
)

# Typed operational candidate preparation with reversible storage preflight.
p = Path("internal/admin/runtime_snapshot.go")
text = p.read_text()
a = text.index("// PrepareAdminRuntime is an importable form")
b = text.index("// Nil means the documented/default-enabled state", a)
new = (
    "// PrepareAdminRuntime is the typed #157 candidate-resource seam shared by\n"
    "// managed, SIGHUP and file-watch reloads. It normalizes and validates candidate\n"
    "// upload storage without publishing policy or creating the configured final directory.\n"
    "func (s *Server) PrepareAdminRuntime(cfg config.AdminConfig, prepared *PreparedAuth) (*PreparedAuth, error) {\n"
    "\tcfg.PluginUploadDir = normalizePluginUploadDir(cfg.PluginUploadDir)\n"
    "\tif pluginUploadEnabled(cfg) && cfg.PluginUploadMaxSize > 0 {\n"
    "\t\tif err := preflightPluginUploadDir(cfg.PluginUploadDir); err != nil {\n"
    "\t\t\ts.recordAdminPrepareFailure(adminPrepareFailureUploadDirectory)\n"
    "\t\t\treturn nil, newAdminRuntimePrepareError(adminPrepareFailureUploadDirectory, err)\n"
    "\t\t}\n\t}\n"
    "\ts.clearAdminPrepareFailure()\n"
    "\tif prepared == nil || prepared.snapshot == nil {\n\t\treturn prepared, nil\n\t}\n"
    "\tout := *prepared.snapshot\n\tout.cfg = cfg\n"
    "\treturn &PreparedAuth{snapshot: s.completeAdminRuntimeSnapshot(&out)}, nil\n}\n\n"
    "func (s *Server) completeAdminRuntimeSnapshot(in *authSnapshot) *authSnapshot {\n"
    "\tif in == nil {\n\t\treturn nil\n\t}\n"
    "\tout := *in\n"
    "\tout.consoleCompiled = consoleV2Compiled\n"
    "\tout.pluginsCompiled = s.deps.PluginsCompiled\n"
    "\tout.uploadDirHealth = inspectPluginUploadDirHealth(out.cfg.PluginUploadDir)\n"
    "\treturn &out\n}\n\n"
)
p.write_text(text[:a] + new + text[b:])

# Secret-safe settings GET and narrow typed edit operations.
rep(
    "internal/admin/route_catalog.go",
    "\t{\n\t\tPattern: \"/api/config/settings\",\n"
    "\t\tMethods: []string{http.MethodPost, http.MethodPut},\n"
    "\t\tPermissions: map[string]rbac.Permission{\n"
    "\t\t\thttp.MethodPost: rbac.ConfigApply,\n"
    "\t\t\thttp.MethodPut:  rbac.ConfigApply,\n",
    "\t{\n\t\tPattern: \"/api/config/settings\",\n"
    "\t\tMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut},\n"
    "\t\tPermissions: map[string]rbac.Permission{\n"
    "\t\t\thttp.MethodGet:  rbac.ConfigRead,\n"
    "\t\t\thttp.MethodPost: rbac.ConfigApply,\n"
    "\t\t\thttp.MethodPut:  rbac.ConfigApply,\n",
)
rep(
    "internal/admin/patch_types.go",
    "\tGlobal *globalPatch `json:\"global,omitempty\"`\n",
    "\tGlobal *globalPatch `json:\"global,omitempty\"`\n\n"
    "\tAdminPluginUpload *adminPluginUploadPatch `json:\"plugin_upload,omitempty\"`\n",
)
rep(
    "internal/admin/patch.go",
    "func applyPatch(c *config.Config, req patchRequest) (string, error) {\n\tswitch req.Op {",
    "func applyPatch(c *config.Config, req patchRequest) (string, error) {\n"
    "\tif req.Op == \"admin_console_set\" || req.Op == \"admin_plugin_upload_set\" {\n"
    "\t\treturn applyAdminRuntimePatch(c, req)\n\t}\n\tswitch req.Op {",
)

# Bounded runtime overview projection.
rep(
    "internal/admin/projection_types.go",
    "\tAdminHealth *AdminHealthStatus `json:\"admin_health,omitempty\"`\n",
    "\tAdminHealth  *AdminHealthStatus  `json:\"admin_health,omitempty\"`\n"
    "\tAdminRuntime *AdminRuntimeStatus `json:\"admin_runtime,omitempty\"`\n",
)
rep(
    "internal/admin/api.go",
    "\tout.AdminHealth = s.adminHealthProjection()\n",
    "\tout.AdminHealth = s.adminHealthProjection()\n\tout.AdminRuntime = s.adminRuntimeStatus(r)\n",
)

# Bounded upload rejection categories; never record filenames/paths/tokens.
p = Path("internal/admin/plugin_upload.go")
text = p.read_text()

def before_once(marker: str, line: str) -> None:
    global text
    if marker not in text:
        raise SystemExit(f"plugin marker missing: {marker[:80]!r}")
    text = text.replace(marker, line + marker, 1)

before_once('\t\thttp.Error(w, "plugin upload disabled", http.StatusForbidden)\n', '\t\ts.recordPluginUploadRejection(uploadRejectDisabled)\n')
before_once('\t\t\thttp.Error(w, fmt.Sprintf("file exceeds %d MB limit", maxMB), http.StatusRequestEntityTooLarge)\n', '\t\t\ts.recordPluginUploadRejection(uploadRejectTooLarge)\n')
before_once('\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart form"})\n', '\t\ts.recordPluginUploadRejection(uploadRejectInvalidMultipart)\n')
before_once('\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": "missing \'wasm\' file field"})\n', '\t\ts.recordPluginUploadRejection(uploadRejectMissingFile)\n')
before_once('\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename: use a simple filename with letters, digits, \'.\', \'_\' or \'-\'"})\n', '\t\ts.recordPluginUploadRejection(uploadRejectInvalidFilename)\n')
before_once('\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid WASM module: magic number mismatch"})\n', '\t\ts.recordPluginUploadRejection(uploadRejectInvalidWASM)\n')
before_once('\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("unsupported WASM version: %d", magic[4])})\n', '\t\ts.recordPluginUploadRejection(uploadRejectUnsupportedVersion)\n')
before_once('\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid filename: valid WASM uploads must use a .wasm suffix"})\n', '\t\ts.recordPluginUploadRejection(uploadRejectInvalidFilename)\n')
post = '\tif int64(len(data)) > maxBytes {\n\t\thttp.Error(w, fmt.Sprintf("file exceeds %d MB limit", maxMB), http.StatusRequestEntityTooLarge)\n'
if post not in text:
    raise SystemExit("post-read oversize marker missing")
text = text.replace(post, '\tif int64(len(data)) > maxBytes {\n\t\ts.recordPluginUploadRejection(uploadRejectTooLarge)\n\t\thttp.Error(w, fmt.Sprintf("file exceeds %d MB limit", maxMB), http.StatusRequestEntityTooLarge)\n', 1)
before_once('\t\thttp.Error(w, "failed to prepare upload directory", http.StatusInternalServerError)\n', '\t\ts.recordPluginUploadRejection(uploadRejectStorageUnavailable)\n')
before_once('\t\thttp.Error(w, "failed to store upload", http.StatusInternalServerError)\n', '\t\ts.recordPluginUploadRejection(uploadRejectStorageUnavailable)\n')
short = '\t\twriteJSON(w, http.StatusBadRequest, map[string]string{"error": "file too short to be a valid WASM module"})\n'
if text.count(short) < 1:
    raise SystemExit("short WASM marker missing")
text = text.replace(short, '\t\ts.recordPluginUploadRejection(uploadRejectInvalidWASM)\n' + short)
p.write_text(text)

# Remove the temporary compile guard now the snapshot fields exist.
rep("internal/admin/admin_runtime_status.go", '\n\t"jul/internal/config"\n', "\n")
rep("internal/admin/admin_runtime_status.go", "\nvar _ = config.AdminConfig{}\n", "\n")
