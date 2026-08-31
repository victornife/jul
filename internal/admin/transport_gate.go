// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net"
	"net/http"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// requiredTransport is the constant returned in the insecure_transport error's
// `details`. It names the condition the caller must satisfy, and it is a
// constant of the contract rather than a fact about this server — which is why
// it is safe to return before authentication, and why the listen address is
// deliberately not returned alongside it (ADR 0019 §26 rule 3).
const requiredTransport = "tls_or_loopback"

// transportExemptPaths are the only routes outside the gate: genuinely
// credential-free liveness and readiness probes.
//
// The comparison is on the exact request path, before any cleaning, so a path
// that merely begins with a probe's name (`/healthz/../api/config`) does not
// match and is gated. That is the fail-closed direction: an exemption that
// matched loosely would be a bypass.
var transportExemptPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
}

// requireSecureTransport refuses to authenticate or act on a request that
// arrived in cleartext on a listener that is not bound to loopback
// (ADR 0019 §28.1).
//
// # What this promises, and what it cannot
//
// The gate runs before route lookup and before authentication, so the server
// never *accepts* a credential presented over an exposed channel. It does not,
// and cannot, promise that the credential never crossed the network: by the
// time any handler runs the request — Authorization header included — has
// already traversed it, and server-side ordering cannot unsend it. The half of
// the control that protects the credential is client-side, where the CLI
// refuses a non-loopback http:// endpoint before loading or transmitting a
// token at all.
//
// # Why it covers reads, and every existing /api/… route
//
// An earlier design gated only mutating /api/v1 requests. That was not merely
// incomplete, it was ineffective: the legacy single-token identity
// authenticates as a wildcard principal holding both read and write, so a token
// disclosed by a permitted plaintext *read* could simply be replayed against an
// exempt legacy mutation. The gate therefore covers every route that consumes
// an admin credential, /metrics included.
//
// # There is no override
//
// A server-side bypass would be the same hole as the `--insecure` flag the CLI
// deliberately does not have. This is a breaking change for a deployment
// authenticating over a non-loopback address in cleartext today; the remedy is
// to terminate TLS in front of the listener, or to bind to loopback and tunnel.
func (s *Server) requireSecureTransport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if transportExemptPaths[r.URL.Path] || s.transportIsSecure(r) {
			next.ServeHTTP(w, r)
			return
		}
		s.writeInsecureTransport(w, r)
	})
}

// transportIsSecure implements §28.1's two-clause test: the connection is TLS,
// or the connection's local address is loopback.
func (s *Server) transportIsSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok && addr != nil {
		return addrIsLoopback(addr.String())
	}
	// No connection to inspect. net/http sets LocalAddrContextKey on every
	// connection it serves, so this is an in-process call rather than a network
	// request — but rather than assume that, fall back to the static properties
	// of the listener this server was configured to bind. Neither clause widens
	// the gate: a plaintext wildcard or non-loopback bind is still refused.
	cfg := s.currentAdminConfig()
	return adminListenerTerminatesTLS(cfg) || addrIsLoopback(cfg.Listen)
}

// adminListenerTerminatesTLS reports whether [admin.tls] terminates the
// listener itself (#336). A request arriving on such a listener carries
// r.TLS, so this is consulted only on the no-connection path above and by the
// Console status projection.
func adminListenerTerminatesTLS(cfg config.AdminConfig) bool {
	return cfg.TLS != nil && cfg.TLS.Enabled
}

// addrIsLoopback reports whether a host:port address names a loopback
// interface. A wildcard bind (0.0.0.0, ::, or an empty host) is not loopback:
// it accepts connections from anywhere, which is precisely the exposure this
// gate exists for.
func addrIsLoopback(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	case "", "0.0.0.0", "::":
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// writeInsecureTransport renders the refusal. It uses the external error
// envelope on every route, versioned or not, because a caller that is being
// refused before authentication has no way to know which shape the route it
// aimed at would otherwise have used — and because the refusal is the same
// verdict in both namespaces.
func (s *Server) writeInsecureTransport(w http.ResponseWriter, r *http.Request) {
	if s.log != nil {
		// The log carries the request id and the verdict, never the credential
		// and never the listen address the response deliberately omits.
		s.log.Warn("admin request refused: insecure transport",
			"method", r.Method, "path", r.URL.Path, "required", requiredTransport)
	}
	id := adminapi.NewRequestID()
	w.Header().Set(requestIDHeader, id)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(adminapi.Envelope{Error: adminapi.Body{
		Code: adminapi.CodeInsecureTransport,
		Message: "This admin listener is neither TLS-terminated nor bound to loopback, so it will not authenticate or act on this request. " +
			"Terminate TLS in front of the listener, or bind it to loopback and reach it through a tunnel.",
		Details:   adminapi.Details{Required: requiredTransport},
		RequestID: id,
	}})
}

// requestIDHeader is the response header carrying the server-minted
// correlation id. The value is always minted here: a client-supplied
// X-Request-ID is never reflected, so the header cannot be used to forge a log
// correlation or to smuggle attacker-chosen bytes into an operator's terminal.
const requestIDHeader = "X-Request-ID"
