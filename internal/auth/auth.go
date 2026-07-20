// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"

	"jul/internal/config"
	"jul/internal/middleware"
)

// DialFunc matches net.Dialer.DialContext. When set on Options.DialContext it is
// installed on the transport of the default JWKS and forward-auth clients so an
// egress allow-list is enforced at connect time. It is an alias so a value from
// internal/egress passes through without this package importing it.
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// Options configures an Authenticator. All fields are optional: a nil HTTPClient
// uses sensible per-method defaults, a nil Logger silences diagnostics, and a nil
// OnDecision disables metrics accounting.
type Options struct {
	// HTTPClient is used for JWKS fetches and forward-auth subrequests. When set
	// it takes precedence over DialContext (the caller owns the whole client).
	HTTPClient *http.Client
	// DialContext, when non-nil and HTTPClient is nil, guards the default JWKS and
	// forward-auth clients' outbound connections (the [egress] allow-list).
	DialContext DialFunc
	// Logger records non-fatal diagnostics (for example, a forward-auth service
	// that is unreachable).
	Logger *slog.Logger
	// OnDecision, when non-nil, is invoked for each access-control decision with
	// the method ("cidr"/"basic"/"jwt"/"forward") and result ("allow"/"deny").
	OnDecision func(method, result string)
}

// Authenticator enforces a location's access-control policy: a CIDR allow/deny
// gate followed by at most one credential method. It is built once from config
// and its Wrap method is installed as a per-location middleware that composes
// around the location's action.
type Authenticator struct {
	cidr    cidrGate
	basic   *basicAuth
	jwt     *jwtAuth
	forward *forwardAuth

	logger     *slog.Logger
	onDecision func(method, result string)
}

// New builds an Authenticator from a validated AuthConfig. It returns an error
// only when a credential method cannot be initialized (for example, an htpasswd
// file that cannot be read); CIDR-only policies never fail. ctx bounds any
// I/O done during construction (currently file reads; future JWKS fetches).
func New(ctx context.Context, cfg config.AuthConfig, opts Options) (*Authenticator, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a := &Authenticator{
		cidr:       newCIDRGate(cfg.Allow, cfg.Deny),
		logger:     opts.Logger,
		onDecision: opts.OnDecision,
	}
	switch {
	case cfg.Basic != nil:
		b, err := newBasicAuth(cfg.Basic.File, cfg.Basic.Realm)
		if err != nil {
			return nil, err
		}
		a.basic = b
	case cfg.JWT != nil:
		client := opts.HTTPClient
		if client == nil {
			client = jwksHTTPClient(opts.DialContext)
		}
		a.jwt = newJWTAuth(cfg.JWT.JWKSURL, cfg.JWT.Issuer, cfg.JWT.Audience, cfg.JWT.Algorithms, client)
	case cfg.ForwardAuth != nil:
		client := opts.HTTPClient
		if client == nil {
			client = forwardHTTPClient(opts.DialContext)
		}
		a.forward = newForwardAuth(cfg.ForwardAuth.URL, cfg.ForwardAuth.AuthResponseHeaders, client)
	}
	return a, nil
}

// Wrap returns middleware that enforces the policy ahead of next. The CIDR gate
// runs first (deny wins), then the configured credential method. On success the
// request proceeds to next, with JWT claims attached to the request context and
// forward-auth response headers merged into the request headers.
func (a *Authenticator) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.cidr.empty() && !a.cidr.allowed(clientAddr(r)) {
			a.decide("cidr", "deny")
			http.Error(w, "403 Forbidden", http.StatusForbidden)
			return
		}

		switch {
		case a.basic != nil:
			if !a.basic.check(r) {
				a.decide("basic", "deny")
				a.basic.challenge(w)
				return
			}
			a.decide("basic", "allow")

		case a.jwt != nil:
			claims, err := a.jwt.validate(r)
			if err != nil {
				a.decide("jwt", "deny")
				a.logError("jwt validation failed", err)
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				http.Error(w, "401 Unauthorized", http.StatusUnauthorized)
				return
			}
			a.decide("jwt", "allow")
			r = r.WithContext(middleware.WithClaims(r.Context(), claims))

		case a.forward != nil:
			res, err := a.forward.decide(r.Context(), r)
			if err != nil {
				a.decide("forward", "deny")
				a.logError("forward-auth request failed", err)
				http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
				return
			}
			if !res.ok {
				a.decide("forward", "deny")
				writeForwardDenied(w, res)
				return
			}
			a.decide("forward", "allow")
			// Strip any client-supplied copies of the auth headers first, then
			// apply the values the auth service vouched for, so a client cannot
			// spoof an identity header the service did not return.
			for _, name := range a.forward.headers {
				r.Header.Del(name)
			}
			for name, vals := range res.copyHeaders {
				r.Header[http.CanonicalHeaderKey(name)] = vals
			}

		default:
			// CIDR-only policy (or no rules at all): the gate above already
			// authorized the request.
			if !a.cidr.empty() {
				a.decide("cidr", "allow")
			}
		}

		next.ServeHTTP(w, r)
	})
}

func (a *Authenticator) decide(method, result string) {
	if a.onDecision != nil {
		a.onDecision(method, result)
	}
}

func (a *Authenticator) logError(msg string, err error) {
	if a.logger != nil {
		a.logger.Warn(msg, "error", err)
	}
}

// writeForwardDenied relays the auth service's denial response to the client so
// that flows such as redirect-to-login work transparently. Hop-by-hop headers
// are stripped and a non-error status is normalized to 403.
func writeForwardDenied(w http.ResponseWriter, res forwardResult) {
	for name, vals := range res.header {
		if hopByHopHeaders[http.CanonicalHeaderKey(name)] {
			continue
		}
		w.Header()[http.CanonicalHeaderKey(name)] = vals
	}
	status := res.statusCode
	if status < 400 {
		status = http.StatusForbidden
	}
	w.WriteHeader(status)
	_, _ = w.Write(res.body)
}

// readLimited reads at most max bytes from r, guarding against an unbounded
// response body from an auth service.
func readLimited(r io.Reader, max int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, max))
}

// guardedTransport clones the default transport and installs dial, so egress is
// enforced at connect time while proxy, idle-pool, and TLS defaults are kept.
func guardedTransport(dial DialFunc) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = dial
	return t
}
