// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package admin exposes a separate operational HTTP listener bound to loopback
// by default. It serves health checks, Prometheus metrics, cache purge, and a
// configuration reload trigger. It must never be attached to the main traffic
// listeners.
package admin

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/server"
)

// Purger clears cached responses. A nil Purger means caching is disabled.
type Purger interface {
	Purge()
	Delete(key string)
}

// Deps wires the admin server to runtime components owned by the composition
// root. All fields are optional; endpoints degrade gracefully when a
// dependency is nil.
type Deps struct {
	// Product is the commercial product name shown in the GUI (e.g. "Jul.IA").
	Product string
	// Version is the running build version shown in the GUI.
	Version string
	// ConfigPath is the path to the active configuration file, shown in the GUI.
	ConfigPath string
	// Metrics serves the Prometheus exposition format.
	Metrics http.Handler
	// Stats returns a point-in-time snapshot of runtime metrics for the console
	// dashboard, polled by the /api/stats endpoint. Nil yields an
	// "unavailable" snapshot so the console can render an empty state.
	Stats func() observability.StatsSnapshot
	// TrafficSources returns the bounded top-N projection of request hosts,
	// origins, and referers for the Console Overview Traffic Sources panel
	// (Milestone 1.4). Nil omits the panel.
	TrafficSources func() observability.TrafficSources
	// Cache, when non-nil, backs the /cache/purge endpoint.
	Cache Purger
	// Reload triggers a configuration reload. It returns an error if the
	// reload could not be enqueued within the implementation's timeout.
	Reload func() error
	// Ready reports whether the server is ready to receive traffic.
	Ready func() bool
	// LiveSnapshot returns the current bound listener state from the runtime
	// server. Used by PendingRestartCheck to evaluate listener rebind against
	// actually-bound addresses rather than the on-disk baseline.
	LiveSnapshot func() server.LiveSnapshot

	// StreamStatus reports the most recent L4 stream-proxy reload outcome for the
	// Console Overview. Because stream listeners reload asynchronously after the
	// HTTP swap, their success/failure cannot be returned in the synchronous
	// apply response; the console polls this instead. It returns "" when no
	// stream is configured (panel omitted), "ok" when the running stream set
	// matches the applied config, and "failed: <reason>" when the last reload was
	// rejected and the prior listeners are still serving. Nil omits the field.
	StreamStatus func() string
	// ReadConfigRaw returns the raw configuration file contents for the GUI
	// editor. Nil disables config viewing/editing.
	ReadConfigRaw func() ([]byte, error)
	// WriteConfigRaw validates and persists raw configuration text, then
	// triggers a reload. It returns a non-nil error (without writing) when the
	// content is invalid. Nil disables raw editing.
	WriteConfigRaw func([]byte) error
	// LoadConfig returns the current parsed configuration for the simple
	// settings form. Nil disables the settings form.
	LoadConfig func() (*config.Config, error)
	// SaveConfig persists a modified configuration (re-serialized to TOML),
	// validating and reloading. Nil disables the settings form.
	SaveConfig func(*config.Config) error

	// Upstreams returns a snapshot of the live upstream pools and per-backend
	// health for the console upstream panel. Nil yields an empty list.
	Upstreams func() []UpstreamStatus
	// Certs returns configured-certificate metadata (subject, expiry; never key
	// material) for the console certificate panel. Nil yields an empty list.
	Certs func() []CertStatus

	// RequestSamples returns the bounded ring buffer of recent requests for the
	// Console v2 Request Samples panel (Milestone 5.1). Nil omits the panel.
	RequestSamples func() []observability.RequestSample
	// FailingRoutes returns the top-n paths ranked by recent failures for the
	// Console v2 Top Failing Routes panel (Milestone 5.2). Nil omits the panel.
	FailingRoutes func(n int) []observability.RouteFailure
	// UpstreamHealthHistory returns per-backend up/down history for the Console
	// v2 Upstream Health History panel (Milestone 5.5). Nil omits the panel.
	UpstreamHealthHistory func() []observability.BackendHealthHistory
	// CertRenewalHistory returns per-domain certificate renewal history for the
	// Console v2 Certificate Renewal History panel (Milestone 5.6). Nil omits
	// the panel.
	CertRenewalHistory func() []observability.CertRenewalHistory

	// RecentLogs returns up to limit recent access-log entries (newest first)
	// from the bounded ring buffer for the Console v2 Operations Log tab
	// (Phase 4g). Nil omits the tab.
	RecentLogs func(limit int) []observability.LogEntry
	// SubscribeLogs registers a live follower for the Operations Log stream. It
	// returns a receive channel of new entries and an unsubscribe function the
	// caller must invoke when the stream closes. Nil disables log streaming.
	SubscribeLogs func() (<-chan observability.LogEntry, func())

	// PluginsCompiled reports whether this binary includes the WASM plugin
	// runtime (the wasmplugins build tag). The Console v2 Plugins panel (Phase
	// 4h) surfaces it so the guided editor can warn that declarations validate
	// but the apply preflight rejects them on a lean build.
	PluginsCompiled bool

	// StreamCompiled reports whether this binary includes the L4 stream proxy
	// (the stream build tag). The Console v2 Streams panel (Phase 4i) surfaces it
	// so the guided editor can warn that declarations validate but a lean binary
	// refuses to start with them.
	StreamCompiled bool

	// WAFCompiled reports whether this binary includes the web application
	// firewall (the waf build tag). The Console Security panel surfaces it so
	// the guided WAF editor can warn that a policy validates here but the apply
	// preflight rejects an enabled WAF on a non-waf build.
	WAFCompiled bool

	// LastReload reports the structured outcome of the most recent runtime
	// reload. Nil when no reload has been attempted yet. Used by the apply
	// handler to include the previous reload snapshot in the apply response.
	LastReload func() *server.ReloadResult
	// PendingRestartCheck reports which startup-bound subsystems have changed
	// on disk relative to the values the running process was built from,
	// meaning they require a process restart to take effect. The live snapshot
	// lets the check compare against actually-bound listeners instead of the
	// on-disk baseline. Returns an empty slice (or nil) when no restart is
	// pending. Used by the Overview to show a persistent indicator when saved
	// changes are not yet live.
	PendingRestartCheck func(server.LiveSnapshot) []string

	// ApplyConfigRaw validates, persists, and reloads raw configuration bytes.
	// The mode string is "hot" or "stage_restart". It returns a structured
	// result correlated with the live reload outcome.
	ApplyConfigRaw func([]byte, string) (ConfigApplyResult, error)
	// ApplyConfig validates, persists, and reloads a structured config object.
	// The mode string is "hot" or "stage_restart". It returns a structured
	// result correlated with the live reload outcome.
	ApplyConfig func(*config.Config, string) (ConfigApplyResult, error)
}

// ReloadSnapshot is the legacy admin-package view of the most recent reload
// outcome. It is kept for one compatibility release; new code should use
// server.ReloadResult.
type ReloadSnapshot struct {
	OK       bool          `json:"ok"`
	TimedOut bool          `json:"timed_out"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
	Error    string        `json:"error,omitempty"`
}

// Server is the admin HTTP listener.
type Server struct {
	cfg      config.AdminConfig
	log      *slog.Logger
	deps     Deps
	hist     *history
	hub      *Hub
	limiter  *adminLimiter
	timeline *eventHistory
	audit    *auditLog
	health   *consoleHealth
	quit     chan struct{}
	httpd    *http.Server
	// applyMu serializes config writes (raw apply, structured patch apply, and
	// history rollback) so optimistic-concurrency checks and the write they guard
	// are atomic, closing the read-modify-write race between concurrent edits
	// (P2-12).
	applyMu sync.Mutex
}

// New builds an admin Server from config. It returns nil when admin is
// disabled so callers can skip starting it.
func New(cfg config.AdminConfig, log *slog.Logger, deps Deps) *Server {
	if !cfg.Enabled {
		return nil
	}
	s := &Server{
		cfg:      cfg,
		log:      log,
		deps:     deps,
		hist:     newHistory(cfg.HistoryDir, cfg.HistoryKeep),
		hub:      newHub(),
		limiter:  newAdminLimiter(log, cfg.RateLimitReadPerMin, cfg.RateLimitWritePerMin, cfg.RateLimitApplyPerMin, cfg.MaxEventConns),
		timeline: newEventHistory(timelineCap),
		audit:    newAuditLogWithSink(auditCap, cfg.AuditLogFile, cfg.AuditLogRotateMaxMB, cfg.AuditLogRotateKeep, log),
		health:   newConsoleHealth(),
		quit:     make(chan struct{}),
	}
	s.httpd = &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

// Run starts the admin listener and shuts it down when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Info("admin listener started", "addr", s.cfg.Listen, "auth", s.cfg.Token != "")
	// The admin API grants full read/write control of the running server and
	// uses a single shared bearer token with no RBAC. It is designed for
	// single-operator, loopback-bound use. Binding to a routable address
	// without an external firewall, VPN, or mTLS layer is unsafe.
	if !adminIsLoopback(s.cfg.Listen) {
		s.log.Warn("admin listener bound to a non-loopback address — restrict access with firewall rules or a private network",
			"addr", s.cfg.Listen,
			"security", "single shared bearer token; no RBAC; full read/write access")
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.httpd.Serve(ln); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		close(s.quit)
		s.hub.Close()
		if s.audit != nil {
			_ = s.audit.Close()
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpd.Shutdown(shutdownCtx)
	case err := <-errCh:
		close(s.quit)
		s.hub.Close()
		if s.audit != nil {
			_ = s.audit.Close()
		}
		return err
	}
}

// auth enforces bearer-token authentication when a token is configured.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			ok := len(h) > len(prefix) && h[:len(prefix)] == prefix &&
				subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.cfg.Token)) == 1
			// The Console v2 SPA streams /api/events with the bearer token in the
			// Authorization header over fetch (never EventSource), so no query-token
			// fallback is offered: a ?token= parameter would leak the credential into
			// access logs, the browser history, and the Referer header (Milestone 1.5).
			if !ok {
				w.Header().Set("WWW-Authenticate", "Bearer")
				http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleHealthz reports process liveness.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReadyz reports readiness to serve traffic.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	ready := true
	if s.deps.Ready != nil {
		ready = s.deps.Ready()
	}
	if !ready {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	// Readiness gate: any expired certificate prevents traffic serving.
	if s.deps.LoadConfig != nil && s.deps.Certs != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			certs := projectTLS(cfg, s.deps.Certs())
			for _, c := range certs {
				if c.DaysLeft < 0 {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{
						"status": "not ready",
						"reason": "certificate expired for " + c.ServerNames[0],
					})
					return
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// handleMetrics serves Prometheus metrics, or 404 when metrics are disabled.
func (s *Server) handleMetrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Metrics == nil {
			http.NotFound(w, r)
			return
		}
		s.deps.Metrics.ServeHTTP(w, r)
	})
}

// handlePurge clears the response cache. POST only. An optional ?key=<key>
// removes a single entry; otherwise the whole cache is purged.
func (s *Server) handlePurge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Cache == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "cache disabled"})
		return
	}
	if key := r.URL.Query().Get("key"); key != "" {
		s.deps.Cache.Delete(key)
		writeJSON(w, http.StatusOK, map[string]string{"status": "purged", "key": key})
		return
	}
	s.deps.Cache.Purge()
	writeJSON(w, http.StatusOK, map[string]string{"status": "purged"})
}

// handleReload triggers a configuration reload. POST only. It returns 202
// when the reload is successfully enqueued and 503 when the enqueue times out
// or is otherwise rejected by the runtime coordinator.
func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.Reload == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	if err := s.deps.Reload(); err != nil {
		s.recordAudit("config.reload", "config", "failure", err.Error(), adminClientIP(r))
		http.Error(w, "503 Service Unavailable: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	s.recordAudit("config.reload", "config", "success", "reload triggered via admin API", adminClientIP(r))
	// Notify SSE subscribers and the timeline so the Console updates live.
	s.emit("config", "reload", "info", "Configuration reload triggered.")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "reload triggered"})
}

// handleRoot serves the dependency-free configuration GUI at the admin root.
// It is registered only when the Console v2 SPA is not the active UI (lean
// build or an explicitly disabled console); the SPA owns the root otherwise.
// Any non-root path that reaches this catch-all handler is a 404.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.serveConfigPage(w)
}

// handleConfigPage always serves the configuration GUI page, independent of the
// console setting, so the editor stays reachable at /config and /ui.
func (s *Server) handleConfigPage(w http.ResponseWriter, r *http.Request) {
	s.serveConfigPage(w)
}

// serveConfigPage writes the static configuration GUI page. The page fetches
// data from the API endpoints, sending the admin token when one is required.
func (s *Server) serveConfigPage(w http.ResponseWriter) {
	s.writeSecurityHeadersLegacy(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, configUIPage)
}

// handleStats serves the runtime dashboard metrics as JSON for the console. It
// is always registered (and cheap) so it is useful for scripts even when the
// console UI is compiled out. When no Stats hook is wired it returns an
// "unavailable" snapshot rather than an error.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if s.deps.Stats == nil {
		writeJSON(w, http.StatusOK, observability.StatsSnapshot{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Stats())
}

// writeSecurityHeaders applies defensive HTTP headers to the admin pages.
// The Console v2 SPA is served from same-origin embedded assets only — no CDN,
// no external fonts, no inline scripts — so script-src 'self' is safe.
// The legacy config page uses inline scripts/styles and is covered by
// writeSecurityHeadersLegacy below.
func (s *Server) writeSecurityHeaders(w http.ResponseWriter) {
	s.writeSecurityHeadersNonce(w, "")
}

// writeSecurityHeadersNonce is writeSecurityHeaders with an optional per-response
// style nonce. The SPA's only inline <style> source is the CodeMirror editor,
// which stamps the advertised nonce onto the theme elements it injects, so the
// policy stays free of 'unsafe-inline'. An empty nonce yields the strict
// style-src 'self' policy used for the hashed static assets.
func (s *Server) writeSecurityHeadersNonce(w http.ResponseWriter, styleNonce string) {
	h := w.Header()
	h.Set("Content-Security-Policy", securityCSP(styleNonce))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

// securityCSP builds the Console v2 Content-Security-Policy. When styleNonce is
// non-empty the style-src gains a 'nonce-…' source for editor-injected styles.
func securityCSP(styleNonce string) string {
	style := "style-src 'self'"
	if styleNonce != "" {
		style = "style-src 'self' 'nonce-" + styleNonce + "'"
	}
	return "default-src 'self'; " + style + "; " +
		"script-src 'self'; img-src 'self' data:; " +
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; " +
		"report-uri /csp-violation-report"
}

// writeSecurityHeadersLegacy applies the CSP for the legacy config/console v1
// page which contains first-party embedded inline scripts and styles.
func (s *Server) writeSecurityHeadersLegacy(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Security-Policy",
		"default-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"script-src 'self' 'unsafe-inline'; img-src 'self' data:; "+
			"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
}

// settings is the curated subset of configuration exposed by the simple GUI.
// All fields are strings/bools so they round-trip cleanly through the form.
type settings struct {
	LogLevel           string `json:"log_level"`
	LogFormat          string `json:"log_format"`
	ShutdownTimeout    string `json:"shutdown_timeout"`
	CacheEnabled       bool   `json:"cache_enabled"`
	CacheDefaultTTL    string `json:"cache_default_ttl"`
	CacheMemoryMaxSize string `json:"cache_memory_max_size"`
	AdminListen        string `json:"admin_listen"`
}

// extractSettings reads the curated subset from a parsed configuration.
func extractSettings(c *config.Config) settings {
	return settings{
		LogLevel:           c.Global.LogLevel,
		LogFormat:          c.Global.LogFormat,
		ShutdownTimeout:    c.Global.ShutdownTimeout.Std().String(),
		CacheEnabled:       c.Cache.Enabled,
		CacheDefaultTTL:    c.Cache.DefaultTTL.Std().String(),
		CacheMemoryMaxSize: string(must(c.Cache.MemoryMaxSize.MarshalText())),
		AdminListen:        c.Admin.Listen,
	}
}

// applySettings writes the curated subset onto a parsed configuration, parsing
// the string-typed durations and sizes. It returns an error on malformed input.
func applySettings(c *config.Config, in settings) error {
	c.Global.LogLevel = in.LogLevel
	c.Global.LogFormat = in.LogFormat
	if err := c.Global.ShutdownTimeout.UnmarshalText([]byte(in.ShutdownTimeout)); err != nil {
		return fmt.Errorf("shutdown_timeout: %w", err)
	}
	c.Cache.Enabled = in.CacheEnabled
	if err := c.Cache.DefaultTTL.UnmarshalText([]byte(in.CacheDefaultTTL)); err != nil {
		return fmt.Errorf("cache_default_ttl: %w", err)
	}
	if err := c.Cache.MemoryMaxSize.UnmarshalText([]byte(in.CacheMemoryMaxSize)); err != nil {
		return fmt.Errorf("cache_memory_max_size: %w", err)
	}
	c.Admin.Listen = in.AdminListen
	return nil
}

func must(b []byte, _ error) []byte { return b }

// handleConfigGet returns metadata, the raw config text, and the curated
// settings subset for the GUI.
func (s *Server) handleConfigGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	resp := map[string]any{
		"product":        s.deps.Product,
		"version":        s.deps.Version,
		"path":           s.deps.ConfigPath,
		"authRequired":   s.cfg.Token != "",
		"rawEditable":    s.deps.WriteConfigRaw != nil,
		"formEditable":   s.deps.LoadConfig != nil && s.deps.SaveConfig != nil,
		"consoleEnabled": consoleV2Compiled && s.cfg.ConsoleEnabled(),
	}
	if s.deps.ReadConfigRaw != nil {
		raw, err := s.deps.ReadConfigRaw()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp["raw"] = string(raw)
	}
	if s.deps.LoadConfig != nil {
		cfg, err := s.deps.LoadConfig()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		resp["settings"] = extractSettings(cfg)
		// base_version is the optimistic-concurrency fingerprint of the live
		// config (canonical marshaled form, identical to the structured-patch
		// preview's base_version). The raw editor sends it back on apply so a
		// stale edit cannot silently clobber a concurrent change.
		if marshaled, merr := config.Marshal(cfg); merr == nil {
			resp["base_version"] = configVersion(marshaled)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleConfigRaw persists raw configuration text (advanced editor). It
// validates before writing so an invalid edit never causes downtime.
// NOTE: This is a legacy endpoint; prefer /api/config/apply. This handler now
// uses the same applyMu lock and optimistic-version contract as v2 apply to
// close the read-modify-write race with concurrent writes (P2-12).
func (s *Server) handleConfigRaw(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Serialize with the apply path so concurrent writes cannot interleave (P2-12).
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// Optimistic concurrency: reject stale writes. An empty base_version skips
	// the check (explicit force-apply). The version basis matches /api/config/apply
	// so both editors can interoperate.
	if baseVersion := r.URL.Query().Get("base_version"); baseVersion != "" && s.deps.LoadConfig != nil {
		if cur, lerr := s.deps.LoadConfig(); lerr == nil && cur != nil {
			if marshaled, merr := config.Marshal(cur); merr == nil {
				if currentVersion := configVersion(marshaled); baseVersion != currentVersion {
					s.recordAudit("config.raw", "config", "failure", "rejected: base version stale (concurrent change)", adminClientIP(r))
					writeJSON(w, http.StatusConflict, conflictResponse{
						OK:             false,
						Conflict:       true,
						Message:        "The configuration changed since this edit was prepared; reload and try again.",
						CurrentVersion: currentVersion,
					})
					return
				}
			}
		}
	}

	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(body); err != nil {
		s.recordAudit("config.raw", "config", "failure", "rejected: invalid configuration", adminClientIP(r))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.raw", "config", "success", "configuration validated and saved; live runtime reloading", adminClientIP(r))
	var version string
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			if marshaled, merr := config.Marshal(cfg); merr == nil {
				version = configVersion(marshaled)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "version": version})
}

// handleConfigSettings applies the curated settings subset (simple form),
// re-serializing the whole configuration to TOML.
// NOTE: This is a legacy endpoint; prefer /api/config/patch + /api/config/patch/apply.
// This handler now uses the same applyMu lock and optimistic-version contract as v2 apply
// to close the read-modify-write race with concurrent writes (P2-12).
func (s *Server) handleConfigSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.deps.LoadConfig == nil || s.deps.SaveConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var in settings
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// Serialize with the apply path so concurrent writes cannot interleave (P2-12).
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// Load the current config inside the lock so the read-modify-write is atomic.
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		s.recordAudit("config.settings", "config", "failure", "cannot load current config: "+err.Error(), adminClientIP(r))
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Optimistic concurrency: reject stale writes. An empty base_version skips
	// the check (explicit force-apply). The version basis matches /api/config/patch
	// so both editors can interoperate.
	if baseVersion := r.URL.Query().Get("base_version"); baseVersion != "" {
		if marshaled, merr := config.Marshal(cfg); merr == nil {
			if currentVersion := configVersion(marshaled); baseVersion != currentVersion {
				s.recordAudit("config.settings", "config", "failure", "rejected: base version stale (concurrent change)", adminClientIP(r))
				writeJSON(w, http.StatusConflict, conflictResponse{
					OK:             false,
					Conflict:       true,
					Message:        "The configuration changed since this edit was prepared; reload and try again.",
					CurrentVersion: currentVersion,
				})
				return
			}
		}
	}

	if err := applySettings(cfg, in); err != nil {
		s.recordAudit("config.settings", "config", "failure", "rejected: invalid settings", adminClientIP(r))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	prev := s.currentRaw()
	if err := s.deps.SaveConfig(cfg); err != nil {
		s.recordAudit("config.settings", "config", "failure", "rejected: cannot save config", adminClientIP(r))
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.settings", "config", "success", "settings applied and saved; live runtime reloading", adminClientIP(r))
	var version string
	if s.deps.LoadConfig != nil {
		if cfg2, err := s.deps.LoadConfig(); err == nil && cfg2 != nil {
			if marshaled, merr := config.Marshal(cfg2); merr == nil {
				version = configVersion(marshaled)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "saved", "version": version})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// Readiness is a small helper the composition root can use to expose a boolean
// readiness flag flipped once listeners are bound.
type Readiness struct{ ready atomic.Bool }

// Set marks readiness state.
func (r *Readiness) Set(v bool) { r.ready.Store(v) }

// Ready reports the current readiness state.
func (r *Readiness) Ready() bool { return r.ready.Load() }

// handleConsoleV2 serves the embedded Console v2 SPA bundle with the
// appropriate security headers and index.html fallback for client-side routing.
func (s *Server) handleConsoleV2() http.Handler {
	fsys, err := fs.Sub(consoleV2Assets(), "assets/dist")
	if err != nil {
		s.log.Error("console v2: failed to open embedded filesystem", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Console v2 unavailable", http.StatusInternalServerError)
		})
	}
	indexHTML, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		s.log.Error("console v2: missing index.html in embedded bundle", "err", err)
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "Console v2 unavailable", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServer(http.FS(fsys))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hashed, content-addressed assets (JS/CSS/images) are served verbatim
		// under the strict static policy. Everything else — the bare root and
		// any unknown path — is a client-side route, so we serve the SPA shell
		// with a fresh per-response nonce and let react-router resolve it.
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name != "" && name != "index.html" {
			if f, ferr := fsys.Open(name); ferr == nil {
				_ = f.Close()
				s.writeSecurityHeaders(w)
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		s.serveConsoleV2Index(w, indexHTML)
	})
}

// serveConsoleV2Index writes the SPA shell with a fresh per-response style
// nonce. CodeMirror injects its theme as inline <style> elements, so the
// document CSP advertises a nonce that the editor stamps onto them; the SPA
// reads it from the injected <meta name="csp-nonce"> tag. The shell is marked
// no-store so each load gets a distinct nonce.
func (s *Server) serveConsoleV2Index(w http.ResponseWriter, indexHTML []byte) {
	nonce := newStyleNonce()
	body := bytes.Replace(indexHTML,
		[]byte("<head>"),
		[]byte("<head>\n<meta name=\"csp-nonce\" content=\""+nonce+"\" />"),
		1)
	s.writeSecurityHeadersNonce(w, nonce)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// newStyleNonce returns a base64-encoded 128-bit random CSP nonce.
func newStyleNonce() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand should never fail; if it ever does, returning an empty
		// nonce degrades to the strict style-src 'self' policy (editor styles
		// blocked) rather than panicking the request.
		return ""
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// adminIsLoopback reports whether addr (host:port or bare host) resolves to a
// loopback address. It is used to warn when the admin listener is bound to a
// routable address, since the admin API uses a single shared bearer token with
// no RBAC and is designed for local single-operator use.
func adminIsLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
