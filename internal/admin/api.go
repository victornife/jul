// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"jul/internal/auth"
	"jul/internal/config"
	"jul/internal/waf"
)

// UpstreamStatus is the console view of one upstream pool. The composition root
// adapts its internal pool snapshot into this shape so the admin package stays
// decoupled from the upstream package.
type UpstreamStatus struct {
	Name     string          `json:"name"`
	Strategy string          `json:"strategy"`
	Backends []BackendStatus `json:"backends"`
}

// BackendStatus is the console view of one backend within a pool.
type BackendStatus struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Healthy  bool   `json:"healthy"`
	Inflight int64  `json:"inflight"`
}

// CertStatus is the console view of one configured certificate. It never
// carries private-key material.
type CertStatus struct {
	ServerNames []string  `json:"server_names"`
	Source      string    `json:"source"`
	Subject     string    `json:"subject,omitempty"`
	Issuer      string    `json:"issuer,omitempty"`
	DNSNames    []string  `json:"dns_names,omitempty"`
	NotBefore   time.Time `json:"not_before,omitempty"`
	NotAfter    time.Time `json:"not_after,omitempty"`
	Error       string    `json:"error,omitempty"`
}

// handleUpstreams, handleCerts, handleStatus, and the runtime feature-status
// projection live in api_status.go. The setup-wizard config generators
// (handleWizard, wizardAppConfig, handleWizardGenerate) live in api_wizard.go.
// The console view types below are shared by those handlers and by the
// composition-root adapters (internal/app).

// methodNotAllowed writes a 405 with the permitted method advertised.
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "405 Method Not Allowed", http.StatusMethodNotAllowed)
}

// ── Console v2 API handlers ─────────────────────────────────────────────────

// withConfig wraps a handler that needs a parsed config. When LoadConfig is
// unavailable it returns a clean empty-state response.
func (s *Server) withConfig(next func(*config.Config, http.ResponseWriter)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if s.deps.LoadConfig == nil {
			writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
			return
		}
		cfg, err := s.deps.LoadConfig()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		next(cfg, w)
	}
}

func (s *Server) handleRuntimeOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	var status []FeatureStatus
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
		}
	}
	out := RuntimeOverview{
		Product: s.deps.Product,
		Version: s.deps.Version,
		Status:  status,
	}
	if s.deps.Stats != nil {
		out.Stats = s.deps.Stats()
	}
	if s.deps.TrafficSources != nil {
		out.TrafficSources = s.deps.TrafficSources()
	}
	if s.deps.StreamStatus != nil {
		out.StreamStatus = s.deps.StreamStatus()
	}
	if s.audit != nil {
		out.AuditSink = s.audit.statusReport()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRoutes(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectRoutes(c))
	})(w, r)
}

func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.deps.LoadConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ups := map[string]UpstreamStatus{}
	if s.deps.Upstreams != nil {
		for _, u := range s.deps.Upstreams() {
			ups[u.Name] = u
		}
	}
	writeJSON(w, http.StatusOK, projectApps(cfg, ups))
}

func (s *Server) handleTLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.deps.LoadConfig == nil {
		writeJSON(w, http.StatusOK, map[string]any{"loaded": false})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var certs []CertStatus
	if s.deps.Certs != nil {
		certs = s.deps.Certs()
	}
	writeJSON(w, http.StatusOK, projectTLS(cfg, certs))
}

func (s *Server) handleSecurity(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectSecurity(c, s.deps.WAFCompiled))
	})(w, r)
}

func (s *Server) handleTrafficControls(w http.ResponseWriter, r *http.Request) {
	s.withConfig(func(c *config.Config, w http.ResponseWriter) {
		writeJSON(w, http.StatusOK, projectTrafficControls(c))
	})(w, r)
}

// handleConfigValidate accepts a candidate config and returns structured
// human-readable validation errors without persisting anything. POST /api/config/validate
func (s *Server) handleConfigValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{OK: false, Message: err.Error()})
		return
	}
	// Validation is a pure function of the candidate bytes: parse + validate
	// with no persistence and no reload. It must never call WriteConfigRaw,
	// which would briefly apply (and reload) the draft as live configuration.
	// This keeps /api/config/validate side-effect-free and safe under
	// concurrent validate/apply requests.
	if err := validateRaw(body); err != nil {
		writeJSON(w, http.StatusOK, validationErrorResponse{
			OK:      false,
			Message: "The draft configuration contains errors.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	writeJSON(w, http.StatusOK, validationErrorResponse{OK: true, Message: "Configuration is valid."})
}

// validateRaw parses and fully validates candidate configuration bytes without
// mutating any runtime state. It mirrors the parse+validate that a write path
// performs internally, minus persistence and reload, so callers can check a
// draft safely and idempotently.
func validateRaw(body []byte) error {
	cfg, err := config.Parse(body)
	if err != nil {
		return err
	}
	// Preflight: expand secrets on a clone so structural checks (file paths,
	// URLs) work against resolved values, then dry-run every runtime component
	// that can fail during reload (WAF rule compilation, auth init, etc.).
	wafExtra := func(c *config.Config) error {
		if !waf.Compiled {
			return waf.Check(c)
		}
		for i := range c.Servers {
			for j := range c.Servers[i].Locations {
				loc := c.Servers[i].Locations[j]
				wcfg, ok := effectiveWAF(c, loc)
				if !ok {
					continue
				}
				if _, err := waf.New(wcfg, waf.Options{}); err != nil {
					return fmt.Errorf("waf: %w", err)
				}
			}
		}
		for i := range c.Servers {
			for j := range c.Servers[i].Locations {
				loc := c.Servers[i].Locations[j]
				if loc.Auth == nil {
					continue
				}
				if _, err := auth.New(*loc.Auth, auth.Options{}); err != nil {
					return fmt.Errorf("auth: %w", err)
				}
			}
		}
		return nil
	}
	return config.PreflightClone(cfg, wafExtra)
}

// effectiveWAF resolves the WAF policy for a location: the location override
// when present, otherwise the global policy. The bool reports whether an
// enabled policy applies.
func effectiveWAF(c *config.Config, loc config.LocationConfig) (config.WAFConfig, bool) {
	if loc.WAF != nil {
		return *loc.WAF, loc.WAF.Enabled
	}
	return c.WAF, c.WAF.Enabled
}

// handleConfigDiff accepts a candidate config and returns a structured diff
// against the current running config. POST /api/config/diff
func (s *Server) handleConfigDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	before, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "cannot load current config: " + err.Error()})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	after, err := config.Parse(body)
	if err != nil {
		writeJSON(w, http.StatusOK, validationErrorResponse{
			OK:      false,
			Message: "The draft is not valid TOML / config.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	writeJSON(w, http.StatusOK, diffConfigs(before, after))
}

// ErrRestartRequired marks a configuration apply that is valid but cannot be
// hot-applied because it changes a setting fixed at process start (currently the
// ACME issued-domain set and issuer). The composition root's write path returns
// it — wrapped with a human-readable reason — instead of writing the file; the
// apply handlers map it to a 409 carrying restart_required:true so the UI can
// tell the operator a restart is needed rather than reporting a save.
var ErrRestartRequired = errors.New("restart required")

// restartRequiredResponse is the body returned when an apply is valid but cannot
// take effect without a restart. The write was NOT performed, so the live config
// is unchanged; the operator must restart for the change to apply.
type restartRequiredResponse struct {
	OK              bool   `json:"ok"`
	RestartRequired bool   `json:"restart_required"`
	Message         string `json:"message"`
}

// writeRestartRequired emits the shared restart-required response and audits the
// refusal. action is the audit verb of the calling write path (e.g.
// "config.apply" or "config.patch").
func (s *Server) writeRestartRequired(w http.ResponseWriter, r *http.Request, action string, err error) {
	s.recordAudit(action, "config", "failure", "rejected: restart required", adminClientIP(r))
	s.emit("config", "apply_failed", "warn", "Configuration apply needs a restart to take effect; no change was applied.")
	writeJSON(w, http.StatusConflict, restartRequiredResponse{
		OK:              false,
		RestartRequired: true,
		Message:         restartRequiredMessage(err),
	})
}

// restartRequiredMessage renders the operator-facing message for a restart
// required error, stripping the sentinel prefix so only the reason shows.
func restartRequiredMessage(err error) string {
	msg := strings.TrimSpace(strings.TrimPrefix(err.Error(), ErrRestartRequired.Error()+":"))
	if msg == "" {
		msg = "This change requires a server restart to take effect."
		return msg
	}
	return "This change requires a server restart to take effect: " + msg + "."
}

// handleConfigApply is the authoritative v2 write path: validate → snapshot →
// write (which triggers reload) → return post-apply runtime delta.
//
// Truthfulness contract: WriteConfigRaw runs the composition root's full apply
// preflight (deep validation; a dry-run build of every runtime builder that can
// fail — WAF, auth, compression, the WASM plugin set, and the L4 stream route
// set; plus a bind-probe of every newly added HTTP and stream listen address)
// BEFORE persisting the file. A configuration that passes therefore cannot fail
// the subsequent build or fail to bind, so the only remaining gap between
// "saved" and "serving" is the asynchronous reload itself. The response and
// audit/timeline copy say "saved; reloading" rather than the past-tense
// "reloaded" so the operator is not told the live runtime switched at a moment
// when the swap may still be in flight. See docs/reload-semantics.md.
// POST /api/config/apply
func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
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
	// Serialize with the structured-patch apply path so concurrent writes cannot
	// interleave (P2-12): the snapshot, write, and history record are atomic.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// Optimistic concurrency: when the client sends the base_version it read the
	// config at, reject the write with 409 if the live config changed since, so a
	// stale raw edit cannot silently clobber a concurrent change. An empty
	// base_version skips the check (an explicit force-apply). The version basis is
	// the canonical marshaled form, identical to the structured-patch path, so a
	// base_version from either editor is interchangeable.
	if baseVersion := r.URL.Query().Get("base_version"); baseVersion != "" && s.deps.LoadConfig != nil {
		if cur, lerr := s.deps.LoadConfig(); lerr == nil && cur != nil {
			if marshaled, merr := config.Marshal(cur); merr == nil {
				if currentVersion := configVersion(marshaled); baseVersion != currentVersion {
					s.recordAudit("config.apply", "config", "failure", "rejected: base version stale (concurrent change)", adminClientIP(r))
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

	// Self-lockout guard: refuse an apply that would change how the operator
	// reaches the admin console (disabling admin, moving its listen address,
	// rotating its token, or disabling the web console) unless the client
	// explicitly confirms with ?confirm_admin=true. This stops an operator from
	// silently locking themselves out with a single raw edit — the one change
	// that no rollback can undo from the console, because the console would be
	// gone. The guard is best-effort: it only runs when both the running config
	// and the proposed config parse; a parse failure falls through to the normal
	// validation path below, which reports the error.
	if r.URL.Query().Get("confirm_admin") != "true" && s.deps.LoadConfig != nil {
		if cur, lerr := s.deps.LoadConfig(); lerr == nil && cur != nil {
			if next, perr := config.Parse(body); perr == nil && next != nil {
				if changes := adminLockoutChanges(cur.Admin, next.Admin); len(changes) > 0 {
					s.recordAudit("config.apply", "config", "failure", "rejected: admin-reachability change needs confirmation", adminClientIP(r))
					s.emit("config", "apply_failed", "warn", "Configuration apply would change admin access and was held for confirmation.")
					writeJSON(w, http.StatusConflict, adminGuardResponse{
						OK:          false,
						AdminChange: true,
						Message:     "This change affects how you reach the admin console; re-apply with confirmation to proceed.",
						Changes:     changes,
					})
					return
				}
			}
		}
	}

	// Snapshot the current config before applying so the apply is reversible.
	prev := s.currentRaw()

	if err := s.deps.WriteConfigRaw(body); err != nil {
		if errors.Is(err, ErrRestartRequired) {
			s.writeRestartRequired(w, r, "config.apply", err)
			return
		}
		s.recordAudit("config.apply", "config", "failure", "rejected: invalid configuration", adminClientIP(r))
		s.emit("config", "apply_failed", "error", "Configuration apply was rejected (invalid).")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The configuration contains errors; no change was applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	s.recordHistory(prev)
	s.recordAudit("config.apply", "config", "success", "configuration validated and saved; live runtime reloading", adminClientIP(r))

	// Record the apply on the timeline and broadcast it to SSE subscribers.
	s.emit("config", "apply", "info", "Configuration validated and saved; the live runtime is reloading.")

	// Return a post-apply status delta so the UI can reflect what changed. It is
	// derived from the persisted configuration: the apply preflight guarantees
	// the runtime will build this config, but the reload that swaps it in is
	// asynchronous, so "pending_reload" tells the UI this is the configuration
	// taking effect rather than a confirmation that the swap has completed.
	var status []FeatureStatus
	var version string
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
			if marshaled, merr := config.Marshal(cfg); merr == nil {
				version = configVersion(marshaled)
			}
		}
	}
	resp := map[string]any{
		"ok":             true,
		"pending_reload": true,
		"message":        "Configuration validated and saved. The live runtime is reloading to apply it.",
		"status":         status,
		"version":        version,
	}
	if s.deps.LastReload != nil {
		if snap := s.deps.LastReload(); snap != nil {
			resp["previous_reload"] = snap
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// adminGuardResponse is the 409 body when an apply would change a setting that
// governs admin reachability (disabling the admin interface, its listen address,
// its token, or the web console) without explicit confirmation. The write was
// NOT performed; the operator re-sends with ?confirm_admin=true to proceed. This
// guards against silently locking oneself out of the console with a single edit.
type adminGuardResponse struct {
	OK          bool     `json:"ok"`
	AdminChange bool     `json:"admin_change"`
	Message     string   `json:"message"`
	Changes     []string `json:"changes"`
}

// adminLockoutChanges reports the admin-reachability changes between the running
// config (prev) and a proposed one (next) that could lock an operator out of the
// console: disabling the admin interface, moving its listen address, rotating
// its token, or disabling the web console. It returns one human-readable
// description per such change, or nil when none apply. Changes that only widen
// access (enabling admin or the console) are intentionally not flagged, and the
// guard is a no-op when admin is not currently serving.
func adminLockoutChanges(prev, next config.AdminConfig) []string {
	if !prev.Enabled {
		// Admin is not currently serving (this handler is only reachable when it
		// is, but guard defensively): there is no live session to lock out.
		return nil
	}
	if !next.Enabled {
		return []string{"the admin interface would be disabled"}
	}
	var changes []string
	if prev.Listen != next.Listen {
		changes = append(changes, fmt.Sprintf("the admin listen address would change from %q to %q", prev.Listen, next.Listen))
	}
	if prev.Token != next.Token {
		changes = append(changes, "the admin token would change (your current session would need to re-authenticate)")
	}
	if prev.ConsoleEnabled() && !next.ConsoleEnabled() {
		changes = append(changes, "the web console would be disabled (only the basic config page would remain)")
	}
	return changes
}
