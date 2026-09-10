// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"io/fs"
	"net/http"
	"os"

	"jul/internal/config"
)

const (
	adminPrepareFailureUploadDirectory = "upload_directory_unusable"

	uploadRejectDisabled           = "disabled"
	uploadRejectTooLarge           = "too_large"
	uploadRejectInvalidMultipart   = "invalid_multipart"
	uploadRejectMissingFile        = "missing_file"
	uploadRejectInvalidFilename    = "invalid_filename"
	uploadRejectInvalidWASM        = "invalid_wasm"
	uploadRejectUnsupportedVersion = "unsupported_wasm_version"
	uploadRejectStorageUnavailable = "storage_unavailable"
)

// AdminRuntimeStatus is the bounded, secret-safe operational view of the admin
// generation pinned to a request. It deliberately exposes neither the upload
// path nor a filename/token. Generation is diagnostic correlation metadata and
// must never be used as a metric label.
type AdminRuntimeStatus struct {
	Generation             string `json:"generation"`
	ConsoleCompiled        bool   `json:"console_compiled"`
	ConsoleConfigured      bool   `json:"console_configured"`
	ConsoleEffective       bool   `json:"console_effective"`
	PluginsCompiled        bool   `json:"plugins_compiled"`
	UploadEnabled          bool   `json:"upload_enabled"`
	UploadMaxSizeMB        int    `json:"upload_max_size_mb"`
	UploadDirectoryHealth  string `json:"upload_directory_health"`
	PreparationFailure     string `json:"preparation_failure,omitempty"`
	LastUploadRejection    string `json:"last_upload_rejection,omitempty"`
}

// AdminRuntimeSettingsProjection is the non-secret configuration surface used
// by the Console settings form. Unlike AdminRuntimeStatus this config:read-gated
// projection includes the configured upload directory so an operator can edit
// it without fetching raw TOML (which may contain credentials elsewhere).
type AdminRuntimeSettingsProjection struct {
	Console                 bool                                `json:"console"`
	ConsoleCompiled         bool                                `json:"console_compiled"`
	ConsoleEffective        bool                                `json:"console_effective"`
	PluginUploadEnabled     bool                                `json:"plugin_upload_enabled"`
	PluginUploadMaxSizeMB   int                                 `json:"plugin_upload_max_size_mb"`
	PluginUploadDir         string                              `json:"plugin_upload_dir"`
	PluginUploadEffective   bool                                `json:"plugin_upload_effective"`
	UploadDirectoryHealth   string                              `json:"upload_directory_health"`
	Lifecycle               map[string]LifecycleFieldProjection `json:"lifecycle"`
}

func (s *Server) adminRuntimeStatus(r *http.Request) *AdminRuntimeStatus {
	snap := s.requestAdminSnapshot(r)
	if snap == nil {
		return nil
	}
	uploadEnabled := pluginUploadEnabled(snap.cfg) && snap.cfg.PluginUploadMaxSize > 0
	health := snap.uploadDirHealth
	if !uploadEnabled {
		health = "disabled"
	}
	out := &AdminRuntimeStatus{
		Generation:            snap.gen,
		ConsoleCompiled:       snap.consoleCompiled,
		ConsoleConfigured:     snap.cfg.ConsoleEnabled(),
		ConsoleEffective:      snap.consoleCompiled && snap.cfg.ConsoleEnabled(),
		PluginsCompiled:       snap.pluginsCompiled,
		UploadEnabled:         uploadEnabled,
		UploadMaxSizeMB:       snap.cfg.PluginUploadMaxSize,
		UploadDirectoryHealth: health,
	}
	if p := s.adminPrepareFailure.Load(); p != nil {
		out.PreparationFailure = *p
	}
	if p := s.pluginUploadRejection.Load(); p != nil {
		out.LastUploadRejection = *p
	}
	return out
}

func (s *Server) handleAdminRuntimeSettingsRead(w http.ResponseWriter, r *http.Request) {
	state, err := s.currentWriteState(false)
	if err != nil || state.Config == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "configuration unavailable"})
		return
	}
	snap := s.requestAdminSnapshot(r)
	cfg := state.Config.Admin
	uploadConfigured := pluginUploadEnabled(cfg) && cfg.PluginUploadMaxSize > 0
	projection := AdminRuntimeSettingsProjection{
		Console:               cfg.ConsoleEnabled(),
		ConsoleCompiled:       snap.consoleCompiled,
		ConsoleEffective:      snap.consoleCompiled && snap.cfg.ConsoleEnabled(),
		PluginUploadEnabled:   pluginUploadEnabled(cfg),
		PluginUploadMaxSizeMB: cfg.PluginUploadMaxSize,
		PluginUploadDir:       cfg.PluginUploadDir,
		PluginUploadEffective: uploadConfigured,
		UploadDirectoryHealth: snap.uploadDirHealth,
		Lifecycle: map[string]LifecycleFieldProjection{
			"console":                lifecycleFieldProjection("admin.console"),
			"plugin_upload_enabled":  lifecycleFieldProjection("admin.plugin_upload_enabled"),
			"plugin_upload_max_size": lifecycleFieldProjection("admin.plugin_upload_max_size"),
			"plugin_upload_dir":      lifecycleFieldProjection("admin.plugin_upload_dir"),
		},
	}
	writeJSON(w, http.StatusOK, projection)
}

func (s *Server) recordAdminPrepareFailure(reason string) {
	v := reason
	s.adminPrepareFailure.Store(&v)
}

func (s *Server) clearAdminPrepareFailure() { s.adminPrepareFailure.Store(nil) }

func (s *Server) recordPluginUploadRejection(reason string) {
	v := reason
	s.pluginUploadRejection.Store(&v)
}

// inspectPluginUploadDirHealth is read-only and returns a bounded category. It
// never creates the directory or a probe file; actual candidate writability is
// established by preflightPluginUploadDir before Publish.
func inspectPluginUploadDirHealth(raw string) string {
	dir := normalizePluginUploadDir(raw)
	fi, err := os.Lstat(dir)
	if err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return "invalid"
		}
		return "ready"
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "creatable"
	}
	return "unavailable"
}

// adminRuntimePrepareError keeps the observable failure reason low-cardinality
// while preserving the underlying error for operator logs and HTTP diagnostics.
type adminRuntimePrepareError struct {
	category string
	err      error
}

func (e *adminRuntimePrepareError) Error() string { return "admin runtime prepare (" + e.category + "): " + e.err.Error() }
func (e *adminRuntimePrepareError) Unwrap() error { return e.err }

func newAdminRuntimePrepareError(category string, err error) error {
	return &adminRuntimePrepareError{category: category, err: err}
}

var _ = config.AdminConfig{}
