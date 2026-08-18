// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"crypto"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/resilience"
	"jul/internal/upstream"
)

// TestForwardAuthFailsClosedWhenAdmissionRejects is the most important test in
// this slice. A resilience control may never become an authentication bypass:
// when the auth dependency cannot be called, the request is denied, not let
// through unauthenticated.
//
// "Fail open on dependency failure" is a defensible-sounding default in other
// systems and is a critical vulnerability here, so it is asserted rather than
// assumed — and asserted through the Authenticator, which is what a request
// actually meets.
func TestForwardAuthFailsClosedWhenAdmissionRejects(t *testing.T) {
	var reached atomic.Int64
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK) // would allow, if it were ever consulted
	}))
	defer authSrv.Close()

	pool := saturatedPool(t, strings.TrimPrefix(authSrv.URL, "http://"))

	a, err := New(context.Background(), config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: authSrv.URL},
	}, Options{ForwardPool: pool})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.forward.dep.client = authSrv.Client()

	var served atomic.Int64
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example/secret", nil))

	if served.Load() != 0 {
		t.Fatal("the request reached the protected handler while the auth dependency was unavailable: a resilience control became an authentication bypass")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: an unavailable auth dependency denies", rec.Code)
	}
	if reached.Load() != 0 {
		t.Fatalf("the auth service was called %d times; admission should have rejected before the subrequest", reached.Load())
	}
}

// TestForwardAuthFailsClosedOnTransportError pins the same rule for the case
// that exists today: the auth service is simply unreachable.
func TestForwardAuthFailsClosedOnTransportError(t *testing.T) {
	a, err := New(context.Background(), config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: "http://127.0.0.1:1/verify"},
	}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var served atomic.Int64
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
	}))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example/secret", nil))

	if served.Load() != 0 {
		t.Fatal("an unreachable auth service let the request through")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestJWTFailsClosedWhenAdmissionRejects is the JWKS half. A key that cannot be
// fetched must deny the token, never accept it unverified.
func TestJWTFailsClosedWhenAdmissionRejects(t *testing.T) {
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("JWKS was fetched despite admission rejecting")
	}))
	defer jwks.Close()

	pool := saturatedPool(t, strings.TrimPrefix(jwks.URL, "http://"))
	c := newJWKSCache(jwks.URL, jwks.Client(), pool)

	if _, err := c.keyByID("unknown-kid"); err == nil {
		t.Fatal("keyByID returned a key when the JWKS endpoint could not be reached")
	}
}

// TestForwardAuthApplicationStatusesAreUnchanged pins the scope limit: this
// slice brings transport resilience only. A 2xx still allows, a 4xx still
// denies with the service's own response, and a persistent 500 is a received
// answer that must not be retried, must not trip passive health, and must not
// change the client's outcome.
func TestForwardAuthApplicationStatusesAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		wantStatus int
		wantAllow  bool
	}{
		{"2xx allows", http.StatusOK, http.StatusOK, true},
		{"401 is relayed", http.StatusUnauthorized, http.StatusUnauthorized, false},
		{"403 is relayed", http.StatusForbidden, http.StatusForbidden, false},
		{"500 is the answer, not a transport failure", http.StatusInternalServerError, http.StatusInternalServerError, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int64
			authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer authSrv.Close()

			addr := strings.TrimPrefix(authSrv.URL, "http://")
			pool, err := upstream.NewPool(config.UpstreamConfig{
				Name:     "authpool",
				Strategy: "round_robin",
				Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}, {Address: addr, Weight: 1}},
				MaxFails: 1,
			}, "http")
			if err != nil {
				t.Fatalf("pool: %v", err)
			}
			t.Cleanup(pool.Close)

			a, err := New(context.Background(), config.AuthConfig{
				ForwardAuth: &config.ForwardAuthConfig{URL: authSrv.URL},
			}, Options{ForwardPool: pool})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			a.forward.dep.client = authSrv.Client()

			var served atomic.Int64
			h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				served.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example/x", nil))

			if got := served.Load() == 1; got != tc.wantAllow {
				t.Fatalf("allowed = %v, want %v", got, tc.wantAllow)
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("the auth service was called %d times, want 1: an application status is an answer, not a retryable failure", got)
			}
			for _, b := range pool.Backends() {
				if !b.Available() {
					t.Fatal("an application status tripped passive health; one misbehaving auth service would take a healthy replica out of rotation")
				}
			}
		})
	}
}

// TestForwardAuthTransportFailureIsRetriedAndTripsHealth is the other side of
// that line: a transport failure is a failure to reach the service, so it fails
// over to another replica and counts against passive health.
func TestForwardAuthTransportFailureIsRetriedAndTripsHealth(t *testing.T) {
	var calls atomic.Int64
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer authSrv.Close()

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "authpool",
		Strategy: "round_robin",
		Servers: []config.UpstreamServer{
			{Address: "127.0.0.1:1", Weight: 1},
			{Address: strings.TrimPrefix(authSrv.URL, "http://"), Weight: 1},
		},
		MaxFails:    1,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	a, err := New(context.Background(), config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: authSrv.URL},
	}, Options{ForwardPool: pool})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	a.forward.dep.client = authSrv.Client()

	var served atomic.Int64
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served.Add(1) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example/x", nil))

	if served.Load() != 1 {
		t.Fatalf("request was not allowed after failing over to the healthy auth replica (status %d)", rec.Code)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("healthy replica was called %d times, want 1", got)
	}
	down := 0
	for _, b := range pool.Backends() {
		if !b.Available() {
			down++
		}
	}
	if down != 1 {
		t.Fatalf("%d backends tripped, want the unreachable one", down)
	}
}

// TestDependencyTimeoutComesFromConfig pins that the 10s bound is no longer
// hardcoded, and that leaving it unset keeps exactly that value.
func TestDependencyTimeoutComesFromConfig(t *testing.T) {
	if got := forwardHTTPClient(nil, 0).Timeout; got != DefaultDependencyTimeout {
		t.Fatalf("unset forward-auth timeout = %s, want %s", got, DefaultDependencyTimeout)
	}
	if got := jwksHTTPClient(nil, 0).Timeout; got != DefaultDependencyTimeout {
		t.Fatalf("unset JWKS timeout = %s, want %s", got, DefaultDependencyTimeout)
	}
	if got := forwardHTTPClient(nil, 2*time.Second).Timeout; got != 2*time.Second {
		t.Fatalf("configured forward-auth timeout = %s, want 2s", got)
	}
	if got := jwksHTTPClient(nil, 3*time.Second).Timeout; got != 3*time.Second {
		t.Fatalf("configured JWKS timeout = %s, want 3s", got)
	}

	a, err := New(context.Background(), config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth.example", Timeout: config.Duration(1500 * time.Millisecond)},
	}, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := a.forward.dep.client.Timeout; got != 1500*time.Millisecond {
		t.Fatalf("configured timeout did not reach the client: %s", got)
	}
}

// TestJWKSStaleGraceStillApplies pins that the refresh path is unchanged: a
// fetch failure still serves a cached key inside the grace window, so a brief
// JWKS outage does not reject every token.
func TestJWKSStaleGraceStillApplies(t *testing.T) {
	c := newJWKSCache("http://127.0.0.1:1/jwks.json", &http.Client{Timeout: time.Second}, nil)
	c.keys = map[string]crypto.PublicKey{"kid-1": struct{}{}}
	c.fetchedAt = time.Now().Add(-30 * time.Minute) // stale, inside the 1h grace

	if _, err := c.keyByID("kid-1"); err != nil {
		t.Fatalf("a cached key inside the stale grace window was rejected: %v", err)
	}

	c.fetchedAt = time.Now().Add(-2 * time.Hour) // past the grace window
	if _, err := c.keyByID("kid-1"); err == nil {
		t.Fatal("a key past the stale grace window was served: an outage must eventually deny, not accept forever")
	}
}

// saturatedPool returns a pool whose admission limit is already fully consumed,
// so the next Admit rejects. It is how "the dependency cannot be called" is
// produced deterministically, without depending on a timeout.
func saturatedPool(t *testing.T, addr string) *upstream.Pool {
	t.Helper()
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "authdep",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails: 1000,
	}, "http")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	policy, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 1})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	pool.SetPolicy(policy)
	if _, err := pool.Admission().Admit(context.Background(), nil); err != nil {
		t.Fatalf("priming admission: %v", err)
	}
	return pool
}

// TestForwardAuthFailsClosedWhenEveryReplicaIsUnreachable pins the fail-closed
// rule for the case a pool adds: the retry sequence runs out of backends. It is
// a different code path from a single transport error, and it is the one an
// operator meets during a real auth-service outage.
func TestForwardAuthFailsClosedWhenEveryReplicaIsUnreachable(t *testing.T) {
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "authpool",
		Strategy: "round_robin",
		Servers: []config.UpstreamServer{
			{Address: "127.0.0.1:1", Weight: 1},
			{Address: "127.0.0.1:2", Weight: 1},
		},
		MaxFails:    1000,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	a, err := New(context.Background(), config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: "http://auth.example/verify"},
	}, Options{ForwardPool: pool})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var served atomic.Int64
	h := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { served.Add(1) }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://app.example/secret", nil))

	if served.Load() != 0 {
		t.Fatal("the request was allowed through with every auth replica unreachable")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

// TestAuthDependencyDefaultClients pins that omitting a client still yields a
// bounded one, so a caller cannot accidentally build a dependency with no
// timeout at all.
func TestAuthDependencyDefaultClients(t *testing.T) {
	fa := newForwardAuth("http://auth.example", nil, nil, nil)
	if fa.dep.client == nil || fa.dep.client.Timeout != DefaultDependencyTimeout {
		t.Fatalf("forward-auth default client = %+v, want a %s timeout", fa.dep.client, DefaultDependencyTimeout)
	}
	c := newJWKSCache("https://issuer.example/jwks.json", nil, nil)
	if c.dep.client == nil || c.dep.client.Timeout != DefaultDependencyTimeout {
		t.Fatalf("JWKS default client = %+v, want a %s timeout", c.dep.client, DefaultDependencyTimeout)
	}
}
