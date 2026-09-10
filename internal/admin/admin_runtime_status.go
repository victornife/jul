// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"io/fs"
	"net/http"
	"os"
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
// generation pinned to a request. Mutable limiter counters are sampled against
// that same captured policy, never against a later global config load.
type AdminRuntimeStatus struct {
	Generation            string `json:"generation"`
	ConsoleCompiled       bool   `json:"console_compiled"`
	ConsoleConfigured     bool   `json:"console_configured"`
	ConsoleEffective      bool   `json:"console_effective"`
	PluginsCompiled       bool   `json:"plugins_compiled"`
	UploadEnabled         bool   `json:"upload_enabled"`
	UploadMaxSizeMB       int    `json:"upload_max_size_mb"`
	UploadDirectoryHealth string `json:"upload_directory_health"`
	PreparationFailure    string `json:"preparation_failure,omitempty"`
	LastUploadRejection   string `json:"last_upload_rejection,omitempty"`

	RateLimitReadPerMin  int `json:"rate_limit_read_per_min"`
	RateLimitWritePerMin int `json:"rate_limit_write_per_min"`
	RateLimitApplyPerMin int `json:"rate_limit_apply_per_min"`
	MaxEventConns        int `json:"max_event_conns"`
	TrackedLimiterClients int `json:"tracked_limiter_clients"`
	SSEActiveTotal        int `json:"sse_active_total"`
	SSEActiveClients      int `json:"sse_active_clients"`
	SSEOverCapClients     int `json:"sse_over_cap_clients"`
	SSEMaxPerClient       int `json:"sse_max_per_client"`
	RateReadRejected      uint64 `json:"rate_read_rejected"`
	RateWriteRejected     uint64 `json:"rate_write_rejected"`
	RateApplyRejected     uint64 `json:"rate_apply_rejected"`
	SSERejected           uint64 `json:"sse_rejected"`
}

// AdminRuntimeSettingsProjection is the non-secret configuration surface used
// by the Console settings form. Unlike AdminRuntimeStatus this config:read-gated
// projection includes the configured upload directory so an operator can edit
// it without fetching raw TOML (which may contain credentials elsewhere).
type AdminRuntimeSettingsProjection struct {
	Console               bool                                `json:"console"`
	ConsoleCompiled       bool                                `json:"console_compiled"`
	ConsoleEffective      bool                                `json:"console_effective"`
	PluginUploadEnabled   bool                                `json:"plugin_upload_enabled"`
	PluginUploadMaxSizeMB int                                 `json:"plugin_upload_max_size_mb"`
	PluginUploadDir       string                              `json:"plugin_upload_dir"`
	PluginUploadEffective bool                                `json:"plugin_upload_effective"`
	UploadDirectoryHealth string                              `json:"upload_directory_health"`
	RateLimitReadPerMin   int                                 `json:"rate_limit_read_per_min"`
	RateLimitWritePerMin  int                                 `json:"rate_limit_write_per_min"`
	RateLimitApplyPerMin  int                                 `json:"rate_limit_apply_per_min"`
	MaxEventConns         int                                 `json:"max_event_conns"`
	Lifecycle             map[string]LifecycleFieldProjection `json:"lifecycle"`
}

func (s *Server) adminRuntimeStatus(r *http.Request) *AdminRuntimeStatus {
	snap := s.requestAdminSnapshot(r)
	if snap == nil {
		return nil
	}
	uploadEnabled := pluginUploadEnabled(snap.cfg) && snap.cfg.PluginUploadMaxSize > 0
	health := inspectPluginUploadDirHealth(snap.cfg.PluginUploadDir)
	if !uploadEnabled {
		health = "disabled"
	}
	policy := adminLimitPolicyFromConfig(snap.cfg)
	stats := s.limiter.stats(policy)
	out := &AdminRuntimeStatus{
		Generation:            snap.gen,
		ConsoleCompiled:       snap.consoleCompiled,
		ConsoleConfigured:     snap.cfg.ConsoleEnabled(),
		ConsoleEffective:      snap.consoleCompiled && snap.cfg.ConsoleEnabled(),
		PluginsCompiled:       snap.pluginsCompiled,
		UploadEnabled:         uploadEnabled,
		UploadMaxSizeMB:       snap.cfg.PluginUploadMaxSize,
		UploadDirectoryHealth: health,
		RateLimitReadPerMin:   policy.readPerMin,
		RateLimitWritePerMin:  policy.writePerMin,
		RateLimitApplyPerMin:  policy.applyPerMin,
		MaxEventConns:         policy.maxConns,
		TrackedLimiterClients: stats.TrackedClients,
		SSEActiveTotal:        stats.SSEActiveTotal,
		SSEActiveClients:      stats.SSEActiveClients,
		SSEOverCapClients:     stats.SSEOverCapClients,
		SSEMaxPerClient:       stats.SSEMaxPerClient,
		RateReadRejected:      stats.ReadRejected,
		RateWriteRejected:     stats.WriteRejected,
		RateApplyRejected:     stats.ApplyRejected,
		SSERejected:           stats.SSERejected,
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
	// The stable mux pins the serving generation before this handler. Use that
	// exact immutable AdminConfig for every projected field instead of mixing it
	// with a later persisted-config read if Publish races this request.
	snap := s.requestAdminSnapshot(r)
	if snap == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "admin runtime unavailable"})
		return
	}
	cfg := snap.cfg
	uploadEffective := pluginUploadEnabled(cfg) && cfg.PluginUploadMaxSize > 0
	health := inspectPluginUploadDirHealth(cfg.PluginUploadDir)
	if !uploadEffective {
		health = "disabled"
	}
	projection := AdminRuntimeSettingsProjection{
		Console:               cfg.ConsoleEnabled(),
		ConsoleCompiled:       snap.consoleCompiled,
		ConsoleEffective:      snap.consoleCompiled && cfg.ConsoleEnabled(),
		PluginUploadEnabled:   pluginUploadEnabled(cfg),
		PluginUploadMaxSizeMB: cfg.PluginUploadMaxSize,
		PluginUploadDir:       cfg.PluginUploadDir,
		PluginUploadEffective: uploadEffective,
		UploadDirectoryHealth: health,
		RateLimitReadPerMin:   cfg.RateLimitReadPerMin,
		RateLimitWritePerMin:  cfg.RateLimitWritePerMin,
		RateLimitApplyPerMin:  cfg.RateLimitApplyPerMin,
		MaxEventConns:         cfg.MaxEventConns,
		Lifecycle: map[string]LifecycleFieldProjection{
			"console":                  lifecycleFieldProjection("admin.console"),
			"plugin_upload_enabled":    lifecycleFieldProjection("admin.plugin_upload_enabled"),
			"plugin_upload_max_size":   lifecycleFieldProjection("admin.plugin_upload_max_size"),
			"plugin_upload_dir":        lifecycleFieldProjection("admin.plugin_upload_dir"),
			"rate_limit_read_per_min":  lifecycleFieldProjection("admin.rate_limit_read_per_min"),
			"rate_limit_write_per_min": lifecycleFieldProjection("admin.rate_limit_write_per_min"),
			"rate_limit_apply_per_min": lifecycleFieldProjection("admin.rate_limit_apply_per_min"),
			"max_event_conns":          lifecycleFieldProjection("admin.max_event_conns"),
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

func (e *adminRuntimePrepareError) Error() string {
	return "admin runtime prepare (" + e.category + "): " + e.err.Error()
}
func (e *adminRuntimePrepareError) Unwrap() error { return e.err }

func newAdminRuntimePrepareError(category string, err error) error {
	return &adminRuntimePrepareError{category: category, err: err}
}
