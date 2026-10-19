// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"

	"jul/internal/rbac"
)

// routes builds the admin mux from the authoritative Catalog. Every entry is
// either public (no auth) or wrapped with method-aware authorization so
// authorization is explicit and complete — there is no implicit default access
// level. The admission wrapper is also bound here, once, so rate classification
// cannot drift into a second path catalogue and reload never stacks middleware.
func (s *Server) routes() http.Handler {
	// Some tests and embedders construct Server directly. Establish the same
	// process-lifetime invariant as NewServer before building the stable mux.
	if s.limiter == nil {
		s.limiter = newAdminLimiter(s.log)
	}
	mux := http.NewServeMux()
	for _, spec := range Catalog {
		handler := spec.Handler(s)
		if spec.RequiresConfigRaw {
			handler = requireConfigComparisonRead(handler)
		}
		var h http.Handler
		switch {
		case spec.Public:
			h = handler
		case spec.Authenticated:
			h = s.authWithRBAC(handler)
		case len(spec.AnyPermissions) > 0:
			h = s.requireAnyPermission(spec.AnyPermissions, handler)
		case spec.Permissions != nil:
			h = s.requirePermissionForMethods(spec.Permissions, handler)
		default:
			h = s.requirePermission(spec.Permission, handler)
		}

		if !spec.Public {
			h = s.guardOIDCCredentials(h)
		}

		// Admission precedes authentication/authorization but follows the outer
		// secure-transport gate. External contract setup wraps admission so an
		// early 429 already has a server-minted request id and the v1 envelope.
		h = s.limitRoute(spec, h)
		if spec.Stability.External() && !spec.Public {
			h = s.withExternalContract(h)
		}
		mux.Handle(spec.Pattern, h)
	}
	return s.captureAdminRuntimeSnapshot(
		s.requireSecureTransport(s.requireBrowserSameOrigin(s.observeConsole(mux))),
	)
}

// This check runs after authentication and uses exactly its pinned identity,
// including wildcard grants. It precedes candidate parsing, baseline reads,
// CAS checks and all comparison/fallback producers. Internal comparisons and
// real mutation semantics remain exact; no lifecycle result is falsified.
func requireConfigComparisonRead(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := rbac.IdentityFromContext(r.Context())
		if !id.Has(rbac.ConfigRaw) {
			writeForbidden(w, r, rbac.ConfigRaw, id)
			return
		}
		next.ServeHTTP(w, r)
	})
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
