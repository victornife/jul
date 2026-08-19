// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/rbac"
)

// RouteSpec declares the authorization metadata for a single admin route.
// Every non-public route MUST declare permissions for every method it accepts
// so the guard test can assert completeness — there is no implicit default.
type RouteSpec struct {
	// Pattern is the mux path pattern registered with http.ServeMux.
	Pattern string
	// Methods is the set of HTTP methods this route accepts. Used by the guard
	// test to assert that the handler only accepts declared methods.
	Methods []string
	// Permissions maps each accepted method to the rbac.Permission required to
	// invoke it. A nil map is treated as a single Permission applied to every
	// method, preserving compatibility while the catalog is migrated.
	//
	// Exactly one of Permission, Permissions, AnyPermissions, or Public must be
	// set, and every method in Methods must have a corresponding entry in
	// Permissions.
	Permissions map[string]rbac.Permission
	// Permission is the legacy single permission applied to all Methods when
	// Permissions is nil. New entries should use Permissions.
	Permission rbac.Permission
	// AnyPermissions grants access to the route when the authenticated
	// principal holds ANY one of the listed permissions (logical OR). It is
	// used where a more-privileged capability implies a less-privileged read:
	// e.g. a principal that can apply configuration may retrieve the
	// secret-free result of its own class of operation without also being
	// granted status:read (AC-02). Authentication still happens exactly once;
	// only authorization loops over the allowed permissions.
	AnyPermissions []rbac.Permission
	// Public, when true, skips authentication entirely. Only use this for
	// truly public endpoints (health, readiness, and console static shell).
	Public bool
	// Authenticated, when true, requires any valid credential but no specific
	// permission. It is used by endpoints that only expose the caller's own
	// server-derived identity (e.g. /api/admin/me), where authentication is the
	// authorization: every authenticated principal may read who it is. It is
	// mutually exclusive with Permission/Permissions/AnyPermissions/Public.
	Authenticated bool
	// Handler returns the http.Handler for this route bound to the server.
	Handler func(*Server) http.Handler
}

// Catalog is the authoritative route table. Every admin endpoint MUST appear
// here exactly once. The guard test derives the mux from this table and fails
// if any entry is incomplete or inconsistent.
//
// Ordering: public → unauthenticated-safe → read-only → write → mutating.
var Catalog = []RouteSpec{
	// ── Public (no authentication required) ──────────────────────────────────
	{
		Pattern: "/healthz",
		Methods: []string{http.MethodGet},
		Public:  true,
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleHealthz) },
	},
	{
		Pattern: "/readyz",
		Methods: []string{http.MethodGet},
		Public:  true,
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleReadyz) },
	},
	// Console v2 SPA shell + hashed assets. The browser must load the app
	// before it can prompt for a token, so the root and static sub-paths are
	// public. Actual API calls then require auth.
	{
		Pattern: "/",
		Methods: []string{http.MethodGet, http.MethodHead},
		Public:  true,
		Handler: func(s *Server) http.Handler { return s.handleConsoleOrRoot() },
	},

	// ── Identity (authenticated, any credential) ─────────────────────────────
	// Returns the caller's own server-derived identity so the Console can
	// display the current principal/role and gate controls proactively. It
	// requires authentication but no specific permission, so even a
	// least-privileged principal can discover who it is.
	{
		Pattern:       "/api/admin/me",
		Methods:       []string{http.MethodGet},
		Authenticated: true,
		Handler:       func(s *Server) http.Handler { return http.HandlerFunc(s.handleIdentity) },
	},

	// ── Observability (metrics:read) ──────────────────────────────────────────
	{
		Pattern:    "/metrics",
		Methods:    []string{http.MethodGet},
		Permission: rbac.MetricsRead,
		Handler:    func(s *Server) http.Handler { return s.handleMetrics() },
	},
	{
		Pattern:    "/api/stats",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleStats) },
	},
	{
		Pattern:    "/api/status",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleStatus) },
	},
	{
		Pattern:    "/api/runtime/overview",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleRuntimeOverview) },
	},
	{
		Pattern:    "/api/routes",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleRoutes) },
	},
	{
		Pattern:    "/api/apps",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleApps) },
	},
	{
		Pattern:    "/api/upstreams",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleUpstreams) },
	},
	{
		Pattern:    "/api/upstreams/{name}/resilience",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleUpstreamResilience) },
	},
	{
		Pattern:    "/api/certs",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleCerts) },
	},
	{
		Pattern:    "/api/tls",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleTLS) },
	},
	{
		Pattern:    "/api/security",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleSecurity) },
	},
	{
		Pattern: "/api/traffic-controls",
		Methods: []string{http.MethodGet},
		AnyPermissions: []rbac.Permission{
			rbac.StatusRead,
			rbac.ConfigRead,
			rbac.ConfigWrite,
			rbac.ConfigApply,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleTrafficControls) },
	},
	{
		Pattern:    "/api/plugins",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handlePlugins) },
	},
	{
		Pattern:    "/api/streams",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleStreams) },
	},
	{
		Pattern:    "/api/mtls",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleMTLS) },
	},
	{
		Pattern:    "/api/search",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleSearch) },
	},
	{
		Pattern:    "/api/events",
		Methods:    []string{http.MethodGet},
		Permission: rbac.StatusRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleEvents) },
	},

	// ── Config read (config:read / config:raw) ───────────────────────────────
	// /api/config returns the raw TOML body plus metadata; the raw body may
	// contain literal secrets, so the entire endpoint requires config:raw.
	// Secret-free structured projections are available through /api/routes,
	// /api/apps, /api/security, etc. under status:read/config:read.
	{
		Pattern:    "/api/config",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ConfigRaw,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigGet) },
	},
	{
		Pattern: "/api/config/raw",
		Methods: []string{http.MethodPost, http.MethodPut},
		Permissions: map[string]rbac.Permission{
			http.MethodPost: rbac.ConfigApply,
			http.MethodPut:  rbac.ConfigApply,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigRaw) },
	},
	{
		Pattern: "/api/config/settings",
		Methods: []string{http.MethodPost, http.MethodPut},
		Permissions: map[string]rbac.Permission{
			http.MethodPost: rbac.ConfigApply,
			http.MethodPut:  rbac.ConfigApply,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigSettings) },
	},
	{
		Pattern: "/api/config/pending-restart",
		Methods: []string{http.MethodGet},
		AnyPermissions: []rbac.Permission{
			rbac.ConfigRead,
			rbac.ConfigWrite,
			rbac.ConfigApply,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handlePendingRestart) },
	},
	// Exact-ID managed apply lookup (AC-02). Retrieves the terminal (or
	// pending) result of a managed apply transaction by its rl_N id. The
	// secret-free public view is authorized for EITHER status:read OR
	// config:apply OR history:rollback: a principal privileged enough to mutate
	// configuration is more privileged than one that can read the secret-free
	// transaction result, so a custom config:apply automation role can poll the
	// outcome of its own apply without also holding status:read. A rollback-only
	// custom role is likewise admitted here so it can retrieve the result of the
	// rollback it just submitted (N-01); the handler restricts such a principal
	// to its own rollback records. Actor and source IP remain available only
	// through the audit API.
	{
		Pattern: "/api/config/applies/{id}",
		Methods: []string{http.MethodGet},
		AnyPermissions: []rbac.Permission{
			rbac.StatusRead,
			rbac.ConfigApply,
			rbac.HistoryRollback,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleManagedApplyGet) },
	},
	{
		Pattern:    "/api/config/history",
		Methods:    []string{http.MethodGet},
		Permission: rbac.HistoryRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigHistoryList) },
	},
	{
		Pattern: "/api/config/history/{id}",
		Methods: []string{http.MethodGet},
		Permissions: map[string]rbac.Permission{
			http.MethodGet: rbac.HistoryReadRaw,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigHistoryGet) },
	},
	// Snapshot rollback preview (history:rollback). Reads the snapshot
	// server-side and diffs it against the running config, so a least-privilege
	// rollback-only role can preview what a rollback would change without
	// holding config:write and without POSTing arbitrary candidate TOML to the
	// generic /api/config/diff endpoint (N-02).
	{
		Pattern:    "/api/config/history/{id}/diff",
		Methods:    []string{http.MethodGet},
		Permission: rbac.HistoryRollback,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigHistoryDiff) },
	},

	// ── Config write/preview (config:write) ───────────────────────────────────
	{
		Pattern:    "/api/config/validate",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigValidate) },
	},
	{
		Pattern:    "/api/config/preview",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigRaw,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigPreview) },
	},
	{
		Pattern:    "/api/config/diff",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigDiff) },
	},
	{
		Pattern:    "/api/config/patch",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigPatch) },
	},
	{
		Pattern:    "/api/config/patch/preview",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigPatchPreview) },
	},
	{
		Pattern:    "/api/config/patch/candidate",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigRaw,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigPatchCandidate) },
	},
	{
		Pattern:    "/api/routes/test",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleRouteTest) },
	},
	{
		Pattern:    "/api/wizard",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleWizard) },
	},
	{
		Pattern:    "/api/wizard/generate",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleWizardGenerate) },
	},
	{
		Pattern:    "/api/transcode/descriptor-upload",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigWrite,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleTranscodeDescriptorUpload) },
	},

	// ── Config apply / stage (config:apply) ──────────────────────────────────
	{
		Pattern:    "/api/config/apply",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigApply,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigApply) },
	},
	{
		Pattern:    "/api/config/patch/apply",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigApply,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigPatchApply) },
	},
	// The trusted-proxy policy is listener scoped and privilege-escalation
	// adjacent, so it is written through its own route with its own permission
	// (config:trust) rather than through the general patch surface. Reading it
	// back needs only config:read.
	{
		Pattern:    "/api/listeners",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ConfigRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleListenersList) },
	},
	{
		Pattern: "/api/listeners/{addr}/client_address",
		Methods: []string{http.MethodGet, http.MethodPatch},
		Permissions: map[string]rbac.Permission{
			http.MethodGet:   rbac.ConfigRead,
			http.MethodPatch: rbac.ConfigTrust,
		},
		Handler: func(s *Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					s.handleListenerClientAddressRead(w, r)
					return
				}
				s.handleListenerClientAddress(w, r)
			})
		},
	},
	{
		Pattern:    "/api/config/pending-restart/discard",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ConfigApply,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleDiscardPendingRestart) },
	},

	// ── History rollback (history:rollback) ───────────────────────────────────
	{
		Pattern:    "/api/history",
		Methods:    []string{http.MethodGet},
		Permission: rbac.HistoryRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleHistoryList) },
	},
	{
		Pattern: "/api/history/get",
		Methods: []string{http.MethodGet, http.MethodPost},
		Permissions: map[string]rbac.Permission{
			// Both GET and POST retrieve the raw TOML body of a snapshot.
			http.MethodGet:  rbac.HistoryReadRaw,
			http.MethodPost: rbac.HistoryReadRaw,
		},
		Handler: func(s *Server) http.Handler { return http.HandlerFunc(s.handleHistoryGet) },
	},
	{
		Pattern:    "/api/history/rollback",
		Methods:    []string{http.MethodPost},
		Permission: rbac.HistoryRollback,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleHistoryRollback) },
	},
	{
		Pattern:    "/api/config/rollback",
		Methods:    []string{http.MethodPost},
		Permission: rbac.HistoryRollback,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConfigRollback) },
	},

	// ── Plugins upload (plugins:upload) ───────────────────────────────────────
	{
		Pattern:    "/api/plugins/upload",
		Methods:    []string{http.MethodPost},
		Permission: rbac.PluginsUpload,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handlePluginUpload) },
	},

	// ── Observability read ────────────────────────────────────────────────────
	{
		Pattern:    "/api/observability/requests",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleRequestSamples) },
	},
	{
		Pattern:    "/api/observability/failing-routes",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleFailingRoutes) },
	},
	{
		Pattern:    "/api/observability/timeline",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleTimeline) },
	},
	{
		Pattern:    "/api/observability/upstream-history",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleUpstreamHistory) },
	},
	{
		Pattern:    "/api/observability/cert-history",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleCertHistory) },
	},
	{
		Pattern:    "/api/observability/logs",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleLogs) },
	},
	{
		Pattern:    "/api/observability/logs/stream",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleLogsStream) },
	},
	{
		Pattern:    "/api/admin/health",
		Methods:    []string{http.MethodGet},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleConsoleHealth) },
	},
	{
		Pattern:    "/api/admin/client-errors",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ObservabilityRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleClientError) },
	},

	// ── Audit (audit:read / audit:export) ────────────────────────────────────
	{
		Pattern:    "/api/audit",
		Methods:    []string{http.MethodGet},
		Permission: rbac.AuditRead,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleAudit) },
	},
	{
		Pattern:    "/api/audit/export",
		Methods:    []string{http.MethodGet, http.MethodPost},
		Permission: rbac.AuditExport,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleAuditExport) },
	},

	// ── Cache purge (cache:purge) ─────────────────────────────────────────────
	{
		Pattern:    "/cache/purge",
		Methods:    []string{http.MethodPost},
		Permission: rbac.CachePurge,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handlePurge) },
	},

	// ── Reload trigger (reload:trigger) ──────────────────────────────────────
	{
		Pattern:    "/reload",
		Methods:    []string{http.MethodPost},
		Permission: rbac.ReloadTrigger,
		Handler:    func(s *Server) http.Handler { return http.HandlerFunc(s.handleReload) },
	},

	// ── Admin manage (admin:manage) ───────────────────────────────────────────
	// Runtime profiling is restricted to admin:manage because profiles expose
	// sensitive heap/goroutine state.
	{
		Pattern:    "/debug/pprof/",
		Methods:    []string{http.MethodGet},
		Permission: rbac.AdminManage,
		Handler:    func(s *Server) http.Handler { return http.DefaultServeMux },
	},
}
