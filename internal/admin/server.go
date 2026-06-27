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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
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
	// Reload triggers a configuration reload. It must not block.
	Reload func()
	// Ready reports whether the server is ready to receive traffic.
	Ready func() bool

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
	// applyMu serializes config writes (raw apply and structured patch apply) so
	// optimistic-concurrency checks and the write they guard are atomic, closing
	// the read-modify-write race between concurrent edits (P2-12).
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

// routes builds the admin mux. /metrics is unauthenticated for scrapers only
// when no token is set; all mutating endpoints always require the token when
// configured.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealthz)
	mux.HandleFunc("/readyz", s.handleReadyz)
	mux.Handle("/metrics", s.auth(s.handleMetrics()))
	mux.Handle("/cache/purge", s.auth(http.HandlerFunc(s.handlePurge)))
	mux.Handle("/reload", s.auth(http.HandlerFunc(s.handleReload)))

	// Web console + configuration GUI. When the Console v2 SPA is compiled in
	// (-tags console) and enabled it is the default admin UI: mounted at the
	// root, it serves its hashed assets and falls back to the SPA shell for the
	// bare root and every client-side route (so a hard refresh at /config,
	// /wizard, … resolves). Otherwise — the lean build or an explicitly disabled
	// console — the dependency-free configuration GUI is served at the root and
	// stays reachable at /config and /ui.
	if consoleV2Compiled && s.cfg.ConsoleEnabled() {
		mux.Handle("/", s.handleConsoleV2())
	} else {
		mux.HandleFunc("/", s.handleRoot)
		mux.HandleFunc("/config", s.handleConfigPage)
		mux.HandleFunc("/ui", s.handleConfigPage)
	}
	mux.Handle("/api/stats", s.auth(http.HandlerFunc(s.handleStats)))
	mux.Handle("/api/status", s.auth(http.HandlerFunc(s.handleStatus)))
	mux.Handle("/api/config", s.auth(http.HandlerFunc(s.handleConfigGet)))
	mux.Handle("/api/config/raw", s.auth(http.HandlerFunc(s.handleConfigRaw)))
	mux.Handle("/api/config/settings", s.auth(http.HandlerFunc(s.handleConfigSettings)))
	mux.Handle("/api/upstreams", s.auth(http.HandlerFunc(s.handleUpstreams)))
	mux.Handle("/api/certs", s.auth(http.HandlerFunc(s.handleCerts)))
	mux.Handle("/api/wizard", s.auth(http.HandlerFunc(s.handleWizard)))
	mux.Handle("/api/history", s.auth(http.HandlerFunc(s.handleHistoryList)))
	mux.Handle("/api/history/get", s.auth(http.HandlerFunc(s.handleHistoryGet)))
	mux.Handle("/api/history/rollback", s.auth(http.HandlerFunc(s.handleHistoryRollback)))

	// Console v2 API routes (read/view endpoints, additive; no /api/v2/ prefix).
	// These expose structured projections so the SPA never re-parses raw TOML.
	mux.Handle("/api/runtime/overview", s.auth(http.HandlerFunc(s.handleRuntimeOverview)))
	mux.Handle("/api/routes", s.auth(http.HandlerFunc(s.handleRoutes)))
	mux.Handle("/api/routes/test", s.auth(http.HandlerFunc(s.handleRouteTest)))
	mux.Handle("/api/apps", s.auth(http.HandlerFunc(s.handleApps)))
	mux.Handle("/api/tls", s.auth(http.HandlerFunc(s.handleTLS)))
	mux.Handle("/api/security", s.auth(http.HandlerFunc(s.handleSecurity)))
	mux.Handle("/api/traffic-controls", s.auth(http.HandlerFunc(s.handleTrafficControls)))
	mux.Handle("/api/plugins", s.auth(http.HandlerFunc(s.handlePlugins)))
	mux.Handle("/api/streams", s.auth(http.HandlerFunc(s.handleStreams)))
	mux.Handle("/api/mtls", s.auth(http.HandlerFunc(s.handleMTLS)))
	mux.Handle("/api/search", s.auth(http.HandlerFunc(s.handleSearch)))
	mux.Handle("/api/events", s.auth(http.HandlerFunc(s.handleEvents)))

	// Console v2 Phase 5 — operational depth: recent request samples, top
	// failing routes, the merged config/runtime timeline, upstream-health and
	// certificate-renewal histories, plus the Console's own health and a
	// frontend-error sink (Milestones 5.1–5.7).
	mux.Handle("/api/observability/requests", s.auth(http.HandlerFunc(s.handleRequestSamples)))
	mux.Handle("/api/observability/failing-routes", s.auth(http.HandlerFunc(s.handleFailingRoutes)))
	mux.Handle("/api/observability/timeline", s.auth(http.HandlerFunc(s.handleTimeline)))
	mux.Handle("/api/observability/upstream-history", s.auth(http.HandlerFunc(s.handleUpstreamHistory)))
	mux.Handle("/api/observability/cert-history", s.auth(http.HandlerFunc(s.handleCertHistory)))
	mux.Handle("/api/observability/logs", s.auth(http.HandlerFunc(s.handleLogs)))
	mux.Handle("/api/observability/logs/stream", s.auth(http.HandlerFunc(s.handleLogsStream)))
	mux.Handle("/api/admin/health", s.auth(http.HandlerFunc(s.handleConsoleHealth)))
	mux.Handle("/api/admin/client-errors", s.auth(http.HandlerFunc(s.handleClientError)))

	// Console v2 Milestone 6.6 — audit log and export.
	mux.Handle("/api/audit", s.auth(http.HandlerFunc(s.handleAudit)))
	mux.Handle("/api/audit/export", s.auth(http.HandlerFunc(s.handleAuditExport)))

	// Console v2 write + history endpoints.
	mux.Handle("/api/config/validate", s.auth(http.HandlerFunc(s.handleConfigValidate)))
	mux.Handle("/api/config/diff", s.auth(http.HandlerFunc(s.handleConfigDiff)))
	mux.Handle("/api/config/apply", s.auth(http.HandlerFunc(s.handleConfigApply)))
	mux.Handle("/api/config/patch", s.auth(http.HandlerFunc(s.handleConfigPatch)))
	mux.Handle("/api/config/patch/apply", s.auth(http.HandlerFunc(s.handleConfigPatchApply)))
	mux.Handle("/api/config/history", s.auth(http.HandlerFunc(s.handleConfigHistoryList)))
	mux.Handle("/api/config/history/{id}", s.auth(http.HandlerFunc(s.handleConfigHistoryGet)))
	mux.Handle("/api/config/rollback", s.auth(http.HandlerFunc(s.handleConfigRollback)))
	mux.Handle("/api/wizard/generate", s.auth(http.HandlerFunc(s.handleWizardGenerate)))

	// Admin API security hardening (Console v2 Milestone 1.6): per-client rate
	// limiting wraps the whole mux so every endpoint is protected. The SSE
	// connection cap is enforced inside handleEvents via the same limiter.
	// The console-health observer (Milestone 5.7) wraps the limited mux so it
	// records the real per-request latency and status of every admin call.
	return s.observeConsole(s.limiter.rateLimit(mux))
}

// Run starts the admin listener and shuts it down when ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Info("admin listener started", "addr", s.cfg.Listen, "auth", s.cfg.Token != "")

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

// handleReload triggers a configuration reload. POST only.
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
	s.deps.Reload()
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
		"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'"
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
	prev := s.currentRaw()
	if err := s.deps.WriteConfigRaw(body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
}

// handleConfigSettings applies the curated settings subset (simple form),
// re-serializing the whole configuration to TOML.
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
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := applySettings(cfg, in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	prev := s.currentRaw()
	if err := s.deps.SaveConfig(cfg); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.recordHistory(prev)
	writeJSON(w, http.StatusOK, map[string]string{"status": "saved"})
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
