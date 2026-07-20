// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"jul/internal/auth"
	"jul/internal/config"
	"jul/internal/rbac"
	"jul/internal/server"
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
	var certRisk *CertRiskProjection
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil && cfg != nil {
			status = s.runtimeStatus(cfg)
			var certs []CertStatus
			if s.deps.Certs != nil {
				certs = s.deps.Certs()
			}
			certRisk = projectCertRisk(projectTLS(cfg, certs))
		}
	}
	out := RuntimeOverview{
		Product:  s.deps.Product,
		Version:  s.deps.Version,
		Status:   status,
		CertRisk: certRisk,
	}
	if s.deps.PendingRestartCheck != nil {
		var live server.LiveSnapshot
		if s.deps.LiveSnapshot != nil {
			live = s.deps.LiveSnapshot()
		}
		if subsystems := s.deps.PendingRestartCheck(live); len(subsystems) > 0 {
			out.PendingRestart = subsystems
		}
	}
	// Structured managed planned-restart status (P2-04). Present only when a
	// managed staged restart is pending; supplements the flat subsystem list.
	if s.deps.PendingRestart != nil {
		out.PendingRestartStatus = s.deps.PendingRestart()
	}
	// Last correlated reload result (P2-04).
	if s.deps.LastReload != nil {
		out.LastReload = s.deps.LastReload()
	}
	// Terminal managed-apply outcome including async restoration (H-05).
	if s.deps.LastManagedApply != nil {
		out.LastManagedApply = s.deps.LastManagedApply()
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
	// Admin subsystem health is surfaced only when degraded (F-05).
	out.AdminHealth = s.adminHealthProjection()
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
				if _, err := waf.New(context.Background(), wcfg, waf.Options{}); err != nil {
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
				if _, err := auth.New(context.Background(), *loc.Auth, auth.Options{}); err != nil {
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
	s.recordAudit(r, action, "config", "failure", "rejected: restart required")
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
// applyRequestContext extracts the authenticated identity and source IP from
// an admin request so it can be threaded through to the coordinator's async
// finalizer for attribution in the terminal audit event (H-05).
func applyRequestContext(r *http.Request) ApplyRequestContext {
	ctx := ApplyRequestContext{SourceIP: adminClientIP(r)}
	if id, ok := rbac.IdentityFromContext(r.Context()); ok {
		ctx.Actor = id.Principal
		ctx.TokenID = id.TokenID
	}
	return ctx
}

func (s *Server) handleConfigApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	reqCtx := applyRequestContext(r)
	// Prefer the new correlated apply path; fall back to the legacy
	// WriteConfigRaw closure for tests and callers that have not migrated.
	applyRaw := s.deps.ApplyConfigRaw
	if applyRaw == nil && s.deps.WriteConfigRaw != nil {
		applyRaw = func(_ ApplyRequestContext, data []byte, mode string) (ConfigApplyResult, error) {
			if err := s.deps.WriteConfigRaw(data); err != nil {
				result := ConfigApplyResult{OK: false, Mode: mode, Message: err.Error()}
				if errors.Is(err, ErrRestartRequired) {
					result.RestartRequired = true
					return result, nil
				}
				return result, err
			}
			return ConfigApplyResult{
				OK:             true,
				Mode:           mode,
				Version:        configVersion(data),
				ServingVersion: configVersion(data),
				Message:        "Configuration validated and saved.",
			}, nil
		}
	}
	if applyRaw == nil {
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
					s.recordAudit(r, "config.apply", "config", "failure", "rejected: base version stale (concurrent change)")
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
					s.recordAudit(r, "config.apply", "config", "failure", "rejected: admin-reachability change needs confirmation")
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

	// Object-level admin:manage guard (P3-02 / Wave 1): any change in the
	// [admin] subtree requires admin:manage in addition to config:apply.
	if next, perr := config.Parse(body); perr == nil && next != nil {
		if !s.authorizeConfigCandidate(w, r, "config.apply", next) {
			return
		}
	}

	// Snapshot the current config before applying so the apply is reversible.
	prev := s.currentRaw()

	mode := r.URL.Query().Get("mode")
	switch mode {
	case "", "hot":
		mode = "hot"
	case "stage_restart":
		// valid
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown apply mode %q; valid values: hot, stage_restart", mode),
		})
		return
	}
	result, err := applyRaw(reqCtx, body, mode)
	if err != nil {
		s.recordAudit(r, "config.apply", "config", "failure", "coordinator error: "+err.Error())
		s.emit("config", "apply_failed", "error", "Configuration apply coordinator failed.")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if result.RestartRequired {
		// Return the full structured result at 409 so the client receives
		// can_stage, subsystem list, and version context — not just a
		// plain message. The TypeScript client already parses restart_required
		// from this shape.
		s.recordAudit(r, "config.apply", "config", "failure",
			"rejected: restart required (can_stage="+strconv.FormatBool(result.CanStage)+")")
		s.emit("config", "apply_failed", "warn",
			"Configuration apply needs a restart to take effect; no change was applied.")
		writeJSON(w, http.StatusConflict, result)
		return
	}
	if len(result.ValidationErrors) > 0 {
		s.recordAudit(r, "config.apply", "config", "failure", "rejected: validation failed")
		s.emit("config", "apply_failed", "warn", "Configuration apply rejected: validation failed.")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: result.Message,
			Errors:  humanizeErr(strings.Join(result.ValidationErrors, "\n")),
		})
		return
	}

	if result.OK {
		s.recordHistory(prev)
		// Use distinct audit/timeline events for stage_restart vs hot apply so
		// the timeline clearly distinguishes which transaction type ran.
		// StagedRestartIsUpdate is set by the serve.go closure BEFORE the apply
		// so we get the correct created/updated distinction without re-reading
		// disk state post-apply (M-04 fix).
		if mode == "stage_restart" {
			// StagedRestartIsUpdate is set by the coordinator based on whether a
			// managed staged restart was already pending when the apply ran (H-03).
			if result.StagedRestartIsUpdate {
				s.recordAudit(r, "config.stage_restart.updated", "config", "success",
					"staged configuration updated for next process restart")
				s.emit("config", "stage_restart_updated", "info",
					"Staged configuration updated; previous staged config replaced.")
			} else {
				s.recordAudit(r, "config.stage_restart.created", "config", "success",
					"configuration staged for next process restart")
				s.emit("config", "stage_restart_created", "info",
					"Configuration validated and staged for the next process restart.")
			}
		} else {
			s.recordAudit(r, "config.apply.accepted", "config", "success",
				"configuration validated and saved")
			s.emit("config", "apply", "info", "Configuration validated and saved.")
		}
	} else if !result.Restored && result.RestoreError != "" {
		// Restoration was attempted but failed; emit a distinct audit/timeline
		// event so operators can detect that the rejected candidate remains on
		// disk (F-03).
		s.recordAudit(r, "config.apply.restoration_failed", "config", "failure",
			"candidate rejected but restoration failed: "+result.RestoreError)
		s.emit("config", "apply_restoration_failed", "error",
			"Configuration apply was rejected and restoration to the previous configuration failed.")
	}

	status := http.StatusOK
	if !result.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, result)
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

// candidateRequiresAdminManage reports whether the proposed config change
// touches any setting under the [admin] subtree.
//
// Deprecated: use adminConfigEqual directly. This function is retained only
// for compatibility with tests that assert the old allow-list behavior; the
// Wave 1 object-level guard now treats any [admin] difference as requiring
// admin:manage.
func candidateRequiresAdminManage(cur, next *config.Config) bool {
	if cur == nil || next == nil {
		return false
	}
	return !adminConfigEqual(cur.Admin, next.Admin)
}

// rbacPrincipalsEqual reports whether two principal lists are equivalent
// regardless of order. It compares role, token, disabled flag, and expiry.
// A principal whose name appears in only one list makes the lists unequal.
func rbacPrincipalsEqual(a, b []config.AdminPrincipal) bool {
	if len(a) != len(b) {
		return false
	}
	byName := make(map[string]config.AdminPrincipal, len(a))
	for _, p := range a {
		byName[p.Name] = p
	}
	for _, p := range b {
		existing, ok := byName[p.Name]
		if !ok {
			return false
		}
		if existing.Role != p.Role ||
			existing.Token != p.Token ||
			existing.Disabled != p.Disabled ||
			!existing.ExpiresAt.Equal(p.ExpiresAt) {
			return false
		}
	}
	return true
}

// rbacRolesEqual reports whether two role lists are equivalent regardless of
// order. It compares permission sets so a same-length role list cannot bypass
// the admin:manage guard by editing permissions.
func rbacRolesEqual(a, b []config.AdminRole) bool {
	if len(a) != len(b) {
		return false
	}
	byName := make(map[string]config.AdminRole, len(a))
	for _, r := range a {
		byName[r.Name] = r
	}
	for _, r := range b {
		existing, ok := byName[r.Name]
		if !ok {
			return false
		}
		if !stringSlicesEqualUnordered(existing.Permissions, r.Permissions) {
			return false
		}
	}
	return true
}

// stringSlicesEqualUnordered reports whether two string slices contain the
// same elements with the same multiplicities, ignoring order.
func stringSlicesEqualUnordered(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	count := make(map[string]int, len(a))
	for _, s := range a {
		count[s]++
	}
	for _, s := range b {
		count[s]--
		if count[s] < 0 {
			return false
		}
	}
	return true
}

// rbacIdentityFromRequest retrieves the rbac.Identity from the request context.
// Returns false when no identity is present (unauthenticated request or legacy
// caller that has not yet been migrated to the new stack).
func rbacIdentityFromRequest(r *http.Request) (rbac.Identity, bool) {
	return rbac.IdentityFromContext(r.Context())
}

// authorizeConfigCandidate enforces the authorization required to mutate the
// running configuration to next. It is the single chokepoint for the object-
// level guard (P3-02 / Wave 1):
//
//   - any configuration mutation requires config:apply;
//   - any change in the [admin] subtree additionally requires admin:manage.
//
// The function writes a 403 and returns false when the identity lacks a
// required permission. Callers must return immediately on false.
func (s *Server) authorizeConfigCandidate(w http.ResponseWriter, r *http.Request, action string, next *config.Config) bool {
	id, ok := rbacIdentityFromRequest(r)
	if ok && !id.Legacy && !id.Has(rbac.ConfigApply) {
		s.recordAudit(r, action, "config", "failure", "rejected: lacks config:apply")
		writeForbidden(w, rbac.ConfigApply, id)
		return false
	}
	return s.requireAdminManageForCandidate(w, r, action, next)
}

// requireAdminManageForCandidate enforces only the admin-subtree guard. It is
// used by write paths whose route-level permission is not config:apply (e.g.
// history:rollback) but whose candidate may still affect admin settings.
func (s *Server) requireAdminManageForCandidate(w http.ResponseWriter, r *http.Request, action string, next *config.Config) bool {
	if s.deps.LoadConfig == nil || next == nil {
		return true
	}
	cur, err := s.deps.LoadConfig()
	if err != nil || cur == nil {
		return true
	}
	if adminConfigEqual(cur.Admin, next.Admin) {
		return true
	}
	id, ok := rbacIdentityFromRequest(r)
	if !ok {
		s.recordAudit(r, action, "config", "failure", "rejected: admin change without identity")
		writeForbidden(w, rbac.AdminManage, rbac.Identity{})
		return false
	}
	if !id.Has(rbac.AdminManage) {
		s.recordAudit(r, action, "config", "failure", "rejected: admin change lacks admin:manage")
		writeForbidden(w, rbac.AdminManage, id)
		return false
	}
	return true
}

// authorizeRawCandidate parses body as a candidate config and runs the same
// object-level authorization as authorizeConfigCandidate. It returns the parsed
// config on success, or nil when it has already written an HTTP response.
func (s *Server) authorizeRawCandidate(w http.ResponseWriter, r *http.Request, action string, body []byte) *config.Config {
	next, err := config.Parse(body)
	if err != nil {
		// Caller will handle parse/validation errors.
		return nil
	}
	if !s.authorizeConfigCandidate(w, r, action, next) {
		return nil
	}
	return next
}

// adminConfigEqual reports whether two AdminConfig values are semantically
// equivalent for authorization purposes. Any difference in the [admin]
// subtree means the candidate requires admin:manage, regardless of whether
// the field is restart-required or hot-swappable.
func adminConfigEqual(a, b config.AdminConfig) bool {
	if a.Enabled != b.Enabled ||
		a.Listen != b.Listen ||
		a.Token != b.Token {
		return false
	}
	if !adminRBACEqual(a.RBAC, b.RBAC) {
		return false
	}
	if a.ConsoleEnabled() != b.ConsoleEnabled() {
		return false
	}
	if a.HistoryDir != b.HistoryDir || a.HistoryKeep != b.HistoryKeep {
		return false
	}
	if a.RateLimitReadPerMin != b.RateLimitReadPerMin ||
		a.RateLimitWritePerMin != b.RateLimitWritePerMin ||
		a.RateLimitApplyPerMin != b.RateLimitApplyPerMin ||
		a.MaxEventConns != b.MaxEventConns {
		return false
	}
	if a.AuditLogFile != b.AuditLogFile ||
		a.AuditLogRotateMaxMB != b.AuditLogRotateMaxMB ||
		a.AuditLogRotateKeep != b.AuditLogRotateKeep {
		return false
	}
	if a.PluginUploadDir != b.PluginUploadDir ||
		a.PluginUploadMaxSize != b.PluginUploadMaxSize {
		return false
	}
	if (a.PluginUploadEnabled == nil) != (b.PluginUploadEnabled == nil) {
		return false
	}
	if a.PluginUploadEnabled != nil && b.PluginUploadEnabled != nil &&
		*a.PluginUploadEnabled != *b.PluginUploadEnabled {
		return false
	}
	return true
}

// adminRBACEqual compares two AdminRBACConfig values, including role and
// principal contents.
func adminRBACEqual(a, b config.AdminRBACConfig) bool {
	if a.Enabled != b.Enabled || a.DefaultRole != b.DefaultRole {
		return false
	}
	if !rbacPrincipalsEqual(a.Principals, b.Principals) ||
		!rbacRolesEqual(a.Roles, b.Roles) {
		return false
	}
	return true
}
