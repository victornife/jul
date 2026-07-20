// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "net/http"

// routes builds the admin mux from the authoritative Catalog. Every entry is
// either public (no auth) or wrapped with requirePermission so authorization
// is explicit and complete — there is no implicit default access level.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	for _, spec := range Catalog {
		var h http.Handler
		if spec.Public {
			h = spec.Handler(s)
		} else {
			h = s.requirePermission(spec.Permission, spec.Handler(s))
		}
		mux.Handle(spec.Pattern, h)
	}
	// Admin API security hardening (Console v2 Milestone 1.6): per-client rate
	// limiting wraps the whole mux so every endpoint is protected. The SSE
	// connection cap is enforced inside handleEvents via the same limiter.
	// The console-health observer (Milestone 5.7) wraps the limited mux so it
	// records the real per-request latency and status of every admin call.
	return s.observeConsole(s.limiter.rateLimit(mux))
}

// handleConsoleOrRoot returns the console v2 SPA handler when compiled in and
// enabled, otherwise the legacy config page. This is a single public "/" entry.
func (s *Server) handleConsoleOrRoot() http.Handler {
	if consoleV2Compiled && s.cfg.ConsoleEnabled() {
		return s.handleConsoleV2()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config", "/ui":
			s.handleConfigPage(w, r)
		default:
			s.handleRoot(w, r)
		}
	})
}
