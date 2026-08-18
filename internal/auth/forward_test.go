// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForwardAuthDecide(t *testing.T) {
	var gotMethod, gotURI, gotHost, gotConnection, gotCustom string
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Header.Get("X-Forwarded-Method")
		gotURI = r.Header.Get("X-Forwarded-Uri")
		gotHost = r.Header.Get("X-Forwarded-Host")
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
