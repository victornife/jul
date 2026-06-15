// Package admin exposes a separate operational HTTP listener bound to loopback
// by default. It serves health checks, Prometheus metrics, cache purge, and a
// configuration reload trigger. It must never be attached to the main traffic
// listeners.
package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	// Cache, when non-nil, backs the /cache/purge endpoint.
	Cache Purger
	// Reload triggers a configuration reload. It must not block.
	Reload func()
	// Ready reports whether the server is ready to receive traffic.
	Ready func() bool

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
}

// Server is the admin HTTP listener.
type Server struct {
	cfg   config.AdminConfig
	log   *slog.Logger
	deps  Deps
	hist  *history
	httpd *http.Server
}

// New builds an admin Server from config. It returns nil when admin is
// disabled so callers can skip starting it.
func New(cfg config.AdminConfig, log *slog.Logger, deps Deps) *Server {
	if !cfg.Enabled {
		return nil
	}
	s := &Server{cfg: cfg, log: log, deps: deps, hist: newHistory(cfg.HistoryDir, cfg.HistoryKeep)}
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

	// Web console + configuration GUI. The pages themselves are static (no
	// secrets) and load their data from the auth-protected API endpoints below.
	// "/" serves the console dashboard when it is compiled in (-tags console)
	// and enabled; otherwise it serves the configuration page. "/config" and
	// "/ui" always serve the configuration page so the editor stays reachable
	// regardless of the console setting.
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/config", s.handleConfigPage)
	mux.HandleFunc("/ui", s.handleConfigPage)
	mux.Handle("/api/stats", s.auth(http.HandlerFunc(s.handleStats)))
	mux.Handle("/api/config", s.auth(http.HandlerFunc(s.handleConfigGet)))
	mux.Handle("/api/config/raw", s.auth(http.HandlerFunc(s.handleConfigRaw)))
	mux.Handle("/api/config/settings", s.auth(http.HandlerFunc(s.handleConfigSettings)))
	mux.Handle("/api/upstreams", s.auth(http.HandlerFunc(s.handleUpstreams)))
	mux.Handle("/api/certs", s.auth(http.HandlerFunc(s.handleCerts)))
	mux.Handle("/api/wizard", s.auth(http.HandlerFunc(s.handleWizard)))
	mux.Handle("/api/history", s.auth(http.HandlerFunc(s.handleHistoryList)))
	mux.Handle("/api/history/get", s.auth(http.HandlerFunc(s.handleHistoryGet)))
	mux.Handle("/api/history/rollback", s.auth(http.HandlerFunc(s.handleHistoryRollback)))
	return mux
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
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.httpd.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// auth enforces bearer-token authentication when a token is configured.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			const prefix = "Bearer "
			h := r.Header.Get("Authorization")
			if len(h) <= len(prefix) || h[:len(prefix)] != prefix ||
				subtle.ConstantTimeCompare([]byte(h[len(prefix):]), []byte(s.cfg.Token)) != 1 {
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
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "reload triggered"})
}

// handleRoot serves the admin root page. When the console is compiled in
// (-tags console) and enabled in config it serves the console dashboard shell;
// otherwise it falls back to the configuration page. Any non-root path that
// reaches this catch-all handler is a 404.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if consoleCompiled && s.cfg.ConsoleEnabled() {
		s.writeSecurityHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, consolePage())
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
	s.writeSecurityHeaders(w)
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

// writeSecurityHeaders applies defensive headers to the admin HTML pages. Both
// the console and config pages are self-contained (inline styles/scripts) and
// only talk to same-origin /api endpoints, so a strict content-security policy
// applies (inline is permitted because the assets are first-party and embedded,
// not user-controlled).
func (s *Server) writeSecurityHeaders(w http.ResponseWriter) {
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
		"product":      s.deps.Product,
		"version":      s.deps.Version,
		"path":         s.deps.ConfigPath,
		"authRequired": s.cfg.Token != "",
		"rawEditable":  s.deps.WriteConfigRaw != nil,
		"formEditable": s.deps.LoadConfig != nil && s.deps.SaveConfig != nil,
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
