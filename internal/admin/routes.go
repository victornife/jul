// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "net/http"

// routes builds the admin mux from the authoritative Catalog. Every entry is
// either public (no auth) or wrapped with method-aware authorization so
// authorization is explicit and complete — there is no implicit default access
// level.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	for _, spec := range Catalog {
		var h http.Handler
		switch {
		case spec.Public:
			h = spec.Handler(s)
		case spec.Authenticated:
			h = s.authWithRBAC(spec.Handler(s))
		case len(spec.AnyPermissions) > 0:
			h = s.requireAnyPermission(spec.AnyPermissions, spec.Handler(s))
		case spec.Permissions != nil:
			h = s.requirePermissionForMethods(spec.Permissions, spec.Handler(s))
		default:
			h = s.requirePermission(spec.Permission, spec.Handler(s))
		}
		mux.Handle(spec.Pattern, h)
	}
	// Admin API security hardening (Console v2 Milestone 1.6): per-client rate
	// limiting wraps the whole mux so every endpoint is protected. The SSE
	// connection cap is enforced inside handleEvents via the same limiter.
	// The console-health observer (Milestone 5.7) wraps the limited mux so it
	// records the real per-request latency and status of every admin call.
	//
	// ADR 0019 §28.1's transport gate wraps everything, outermost, because it
	// must run before route lookup and before authentication: it is a property
	// of the listener, so it is answered without consulting the credential or
	// the target. Placing it inside the mux would make it a per-route decision
	// and reintroduce the ordering the record forbids.
	return s.requireSecureTransport(s.observeConsole(s.limiter.rateLimit(mux)))
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
