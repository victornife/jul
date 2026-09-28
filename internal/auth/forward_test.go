// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"jul/internal/clientaddr"
)

func TestForwardAuthDecide(t *testing.T) {
	var gotMethod, gotURI, gotHost, gotConnection, gotCustom string
	var methodValues, uriValues, hostValues []string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Header.Get("X-Forwarded-Method")
		gotURI = r.Header.Get("X-Forwarded-Uri")
		gotHost = r.Header.Get("X-Forwarded-Host")
		methodValues = r.Header.Values("X-Forwarded-Method")
		uriValues = r.Header.Values("X-Forwarded-Uri")
		hostValues = r.Header.Values("X-Forwarded-Host")
		gotConnection = r.Header.Get("Connection")
		gotCustom = r.Header.Get("X-Custom")
		switch r.URL.Query().Get("decision") {
		case "deny":
			w.Header().Set("Location", "/login")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("denied"))
		default:
			w.Header().Set("X-Auth-User", "alice")
			w.Header().Set("X-Auth-Role", "admin")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer auth.Close()

	t.Run("allow copies response headers", func(t *testing.T) {
		fa := newForwardAuth(auth.URL+"?decision=allow", []string{"X-Auth-User", "X-Auth-Role"}, auth.Client(), nil)
		orig := httptest.NewRequest(http.MethodPost, "http://app.example/orders?q=1", nil)
		orig.Header.Set("Connection", "keep-alive")
		orig.Header.Set("X-Custom", "abc")
		orig.Header.Add("X-Forwarded-Method", "GET")
		orig.Header.Add("X-Forwarded-Method", "DELETE")
		orig.Header.Set("X-Forwarded-Uri", "/spoofed")
		orig.Header.Set("X-Forwarded-Host", "attacker.example")
		res, err := fa.decide(context.Background(), orig)
		if err != nil {
			t.Fatalf("decide: %v", err)
		}
		if !res.ok {
			t.Fatalf("expected ok decision, got status %d", res.statusCode)
		}
		if res.copyHeaders.Get("X-Auth-User") != "alice" || res.copyHeaders.Get("X-Auth-Role") != "admin" {
			t.Errorf("copyHeaders = %v, want X-Auth-User/X-Auth-Role", res.copyHeaders)
		}
		if gotMethod != http.MethodPost {
			t.Errorf("X-Forwarded-Method = %q, want POST", gotMethod)
		}
		if gotURI != "/orders?q=1" {
			t.Errorf("X-Forwarded-Uri = %q, want /orders?q=1", gotURI)
		}
		if gotHost != "app.example" {
			t.Errorf("X-Forwarded-Host = %q, want app.example", gotHost)
		}
		if len(methodValues) != 1 || methodValues[0] != http.MethodPost || len(uriValues) != 1 || uriValues[0] != "/orders?q=1" || len(hostValues) != 1 || hostValues[0] != "app.example" {
			t.Errorf("forwarded context contains client-supplied values: method=%q uri=%q host=%q", methodValues, uriValues, hostValues)
		}
		if gotConnection != "" {
			t.Errorf("hop-by-hop Connection header should not be forwarded, got %q", gotConnection)
		}
		if gotCustom != "abc" {
			t.Errorf("X-Custom = %q, want abc", gotCustom)
		}
	})

	t.Run("deny propagates status and body", func(t *testing.T) {
		fa := newForwardAuth(auth.URL+"?decision=deny", nil, auth.Client(), nil)
		orig := httptest.NewRequest(http.MethodGet, "http://app.example/secret", nil)
		res, err := fa.decide(context.Background(), orig)
		if err != nil {
			t.Fatalf("decide: %v", err)
		}
		if res.ok {
			t.Fatal("expected deny decision")
		}
		if res.statusCode != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", res.statusCode)
		}
		if string(res.body) != "denied" {
			t.Errorf("body = %q, want denied", res.body)
		}
		if res.header.Get("Location") != "/login" {
			t.Errorf("Location = %q, want /login", res.header.Get("Location"))
		}
	})

	t.Run("missing original host does not forward client host", func(t *testing.T) {
		fa := newForwardAuth(auth.URL, nil, auth.Client(), nil)
		orig := httptest.NewRequest(http.MethodGet, "http://app.example/", nil)
		orig.Host = ""
		orig.Header.Set("X-Forwarded-Host", "attacker.example")
		if _, err := fa.decide(context.Background(), orig); err != nil {
			t.Fatalf("decide: %v", err)
		}
		if len(hostValues) != 0 {
			t.Errorf("X-Forwarded-Host = %q, want no client-supplied host", hostValues)
		}
	})
}

// errReadCloser simulates a response body that fails on read.
type errReadCloser struct{ err error }

func (e *errReadCloser) Read([]byte) (int, error) { return 0, e.err }
func (e *errReadCloser) Close() error             { return nil }

// roundTripperFn lets us inject a raw *http.Response into the client.
type roundTripperFn func(*http.Request) (*http.Response, error)

func (f roundTripperFn) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestForwardAuthBodyReadError(t *testing.T) {
	fa := newForwardAuth("http://auth.example", nil, &http.Client{
		Transport: roundTripperFn(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Header:     http.Header{"Location": []string{"/login"}},
				Body:       &errReadCloser{err: errors.New("connection reset")},
				Request:    req,
			}, nil
		}),
	}, nil)
	orig := httptest.NewRequest(http.MethodGet, "http://app.example/secret", nil)
	res, err := fa.decide(context.Background(), orig)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if res.statusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.statusCode)
	}
	if res.body != nil {
		t.Errorf("body should be nil on read error, got %q", res.body)
	}
}

func TestForwardAuthDropsConnectionNominatedHeaders(t *testing.T) {
	var received http.Header
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = r.Header.Clone()
		w.WriteHeader(http.StatusForbidden)
	}))
	defer auth.Close()
	fa := newForwardAuth(auth.URL, nil, auth.Client(), nil)
	req := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	req.Header.Add("Connection", "X-Internal-Identity, keep-alive")
	req.Header.Add("Connection", "X-Other-Hop")
	req.Header.Set("X-Internal-Identity", "admin")
	req.Header.Set("X-Other-Hop", "spoofed")
	req.Header.Set("X-End-To-End", "retained")
	if _, err := fa.decide(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if received.Get("X-Internal-Identity") != "" || received.Get("X-Other-Hop") != "" ||
		received.Get("X-End-To-End") != "retained" {
		t.Fatalf("forward-auth headers: %v", received)
	}

	w := httptest.NewRecorder()
	denialHeaders := make(http.Header)
	denialHeaders.Set("Connection", "X-Internal-Identity")
	denialHeaders.Set("X-Internal-Identity", "admin")
	denialHeaders.Set("Location", "/login")
	writeForwardDenied(w, forwardResult{statusCode: http.StatusForbidden, header: denialHeaders})
	if w.Header().Get("X-Internal-Identity") != "" || w.Header().Get("Location") != "/login" {
		t.Fatalf("denial headers: %v", w.Header())
	}
}

func TestForwardAuthRejectsDuplicateAuthorizationBeforeSubrequest(t *testing.T) {
	called := false
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer authServer.Close()
	fa := newForwardAuth(authServer.URL, nil, authServer.Client(), nil)
	r := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	r.Header.Add("Authorization", "Bearer first")
	r.Header.Add("Authorization", "Bearer second")
	res, err := fa.decide(context.Background(), r)
	if err != nil || res.ok || res.statusCode != http.StatusUnauthorized || called {
		t.Fatalf("result=%+v err=%v authCalled=%v, want local 401", res, err, called)
	}
}

func TestForwardAuthReplacesClientForwardingClaims(t *testing.T) {
	var got http.Header
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer authServer.Close()
	fa := newForwardAuth(authServer.URL, nil, authServer.Client(), nil)
	r := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	r.RemoteAddr = "203.0.113.7:1234"
	r.Header.Set("Forwarded", "for=127.0.0.1;proto=https")
	r.Header.Set("X-Forwarded-For", "127.0.0.1")
	r.Header.Set("X-Real-Ip", "127.0.0.1")
	r.Header.Set("X-Forwarded-Proto", "https")
	if _, err := fa.decide(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if got.Get("Forwarded") != "" || got.Get("X-Forwarded-For") != "203.0.113.7" ||
		got.Get("X-Real-Ip") != "203.0.113.7" || got.Get("X-Forwarded-Proto") != "http" {
		t.Fatalf("forward auth saw client claims: %v", got)
	}
}

func TestForwardAuthRejectsUnattributedProxyHopLocally(t *testing.T) {
	called := false
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer authServer.Close()
	fa := newForwardAuth(authServer.URL, nil, authServer.Client(), nil)
	r := httptest.NewRequest(http.MethodGet, "http://app.example/private", nil)
	id := clientaddr.Identity{Client: netip.MustParseAddr("10.0.0.1"), Result: clientaddr.ResultMalformed}
	r = r.WithContext(clientaddr.NewContext(r.Context(), id))
	res, err := fa.decide(context.Background(), r)
	if err != nil || res.ok || res.statusCode != http.StatusForbidden || called {
		t.Fatalf("result=%+v err=%v called=%v, want local 403", res, err, called)
	}
}

func TestForwardAuthSuccessDoesNotCopyConnectionScopedIdentity(t *testing.T) {
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "X-Auth-User")
		w.Header().Set("X-Auth-User", "alice")
		w.Header().Set("X-Auth-Role", "reader")
		w.WriteHeader(http.StatusOK)
	}))
	defer auth.Close()
	fa := newForwardAuth(auth.URL, []string{"X-Auth-User", "X-Auth-Role"}, auth.Client(), nil)
	res, err := fa.decide(context.Background(), httptest.NewRequest(http.MethodGet, "http://app.example/private", nil))
	if err != nil {
		t.Fatal(err)
	}
	if !res.ok || res.copyHeaders.Get("X-Auth-User") != "" || res.copyHeaders.Get("X-Auth-Role") != "reader" {
		t.Fatalf("copied success headers = %v, want only end-to-end X-Auth-Role", res.copyHeaders)
	}
}

func TestForwardAuthDenialRedirectRequiresLocation(t *testing.T) {
	for _, tc := range []struct {
		status   int
		location string
		want     int
	}{
		{http.StatusFound, "/login", http.StatusFound},
		{http.StatusTemporaryRedirect, "/login", http.StatusTemporaryRedirect},
		{http.StatusFound, "", http.StatusForbidden},
		{http.StatusNotModified, "/login", http.StatusForbidden},
		{http.StatusNoContent, "", http.StatusForbidden},
	} {
		headers := make(http.Header)
		if tc.location != "" {
			headers.Set("Location", tc.location)
		}
		w := httptest.NewRecorder()
		writeForwardDenied(w, forwardResult{statusCode: tc.status, header: headers})
		if w.Code != tc.want {
			t.Errorf("status %d, location %q: got %d, want %d", tc.status, tc.location, w.Code, tc.want)
		}
	}
}

func TestForwardAuthLoginRedirectReachesBrowserWithoutBackendAccess(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "/login")
		w.WriteHeader(http.StatusFound)
	}))
	defer authServer.Close()
	a := &Authenticator{forward: newForwardAuth(authServer.URL, nil, forwardHTTPClient(nil, 0), nil)}
	backendCalled := false
	handler := a.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://app.example/private", nil))
	if w.Code != http.StatusFound || w.Header().Get("Location") != "/login" || backendCalled {
		t.Fatalf("status=%d location=%q backendCalled=%t", w.Code, w.Header().Get("Location"), backendCalled)
	}
}
