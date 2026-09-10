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
		if spec.Stability.External() && !spec.Public {
			h = s.withExternalContract(h)
		}
		mux.Handle(spec.Pattern, h)
	}
	return s.captureAdminRuntimeSnapshot(
		s.requireSecureTransport(s.observeConsole(s.limiter.rateLimit(mux))),
	)
}

// handleConsoleOrRoot keeps one stable mux registration and selects the UI at
// request time from the pinned admin generation. In lean builds the Console
// handler is never constructed: the stub intentionally reports misuse through
// the logger, and several tests construct Server with a nil logger.
func (s *Server) handleConsoleOrRoot() http.Handler {
	var console http.Handler
	if consoleV2Compiled {
		console = s.handleConsoleV2()
	}
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config", "/ui":
			s.handleConfigPage(w, r)
		default:
			s.handleRoot(w, r)
		}
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := s.requestAdminSnapshot(r)
		if console != nil && snap.cfg.ConsoleEnabled() {
			console.ServeHTTP(w, r)
			return
		}
		fallback.ServeHTTP(w, r)
	})
}
