// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "net/http"

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
	mux.Handle("/api/plugins/upload", s.auth(http.HandlerFunc(s.handlePluginUpload)))

	// Console v2 transcode designer (Phase 2): descriptor upload so operators
	// can inspect google.api.http annotations before generating a route.
	mux.Handle("/api/transcode/descriptor-upload", s.auth(http.HandlerFunc(s.handleTranscodeDescriptorUpload)))

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

	// Runtime profiling endpoints (goroutine, heap, cpu, etc.). Mounted behind
	// auth so only operators with the admin token can scrape profiles.
	mux.Handle("/debug/pprof/", s.auth(http.DefaultServeMux))

	// Admin API security hardening (Console v2 Milestone 1.6): per-client rate
	// limiting wraps the whole mux so every endpoint is protected. The SSE
	// connection cap is enforced inside handleEvents via the same limiter.
	// The console-health observer (Milestone 5.7) wraps the limited mux so it
	// records the real per-request latency and status of every admin call.
	return s.observeConsole(s.limiter.rateLimit(mux))
}
