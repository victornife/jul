// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"

	"jul/internal/adminapi"
)

// requireBrowserSameOrigin refuses the two browser-borne attacks a local admin
// listener is exposed to, before authentication:
//
//   - cross-site request forgery: an unsafe-method request that a browser marks
//     as cross-origin (Sec-Fetch-Site, else Origin versus Host) is refused. The
//     Console is same-origin and the CLI sends neither header, so neither is
//     affected. Token and RBAC modes are already safe (a browser cannot attach
//     the bearer credential cross-origin); open mode is not.
//   - DNS rebinding: in open mode (no credential at all) a cleartext request
//     whose Host is not a loopback name or literal is refused, because a
//     rebound attacker page is same-origin with the listener but still carries
//     its own Host. Over TLS the attacker's name cannot match the certificate.
//
// Probes keep the transport gate's exemption.
func (s *Server) requireBrowserSameOrigin(next http.Handler) http.Handler {
	cop := http.NewCrossOriginProtection()
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.writeBrowserOriginRefusal(w, r, "cross_origin")
	}))
	guarded := cop.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.requestAdminSnapshot(r).mode == authModeOpen && r.TLS == nil && arrivedOverNetwork(r) && !hostIsLoopback(r.Host) {
			s.writeBrowserOriginRefusal(w, r, "host")
			return
		}
		next.ServeHTTP(w, r)
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if transportExemptPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		guarded.ServeHTTP(w, r)
	})
}

// arrivedOverNetwork reports a request net/http served from a connection, as
// opposed to an in-process call that no browser can originate.
func arrivedOverNetwork(r *http.Request) bool {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && addr != nil
}

// hostIsLoopback reports whether a Host header names loopback: "localhost" (or
// a subdomain of it), or a loopback IP literal, with an optional port. An
// absent Host is not something a browser sends.
func hostIsLoopback(host string) bool {
	if host == "" {
		return true
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (s *Server) writeBrowserOriginRefusal(w http.ResponseWriter, r *http.Request, reason string) {
	if s.log != nil {
		s.log.Warn("admin request refused: browser origin", "method", r.Method, "path", r.URL.Path, "reason", reason)
	}
	id := adminapi.NewRequestID()
	w.Header().Set(requestIDHeader, id)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{
		Code:      adminapi.CodeForbidden,
		Message:   "Cross-origin or non-loopback-host browser requests are refused by the admin listener.",
		RequestID: id,
	}})
}
