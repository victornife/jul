// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestPrincipalOptionsRequireVerifiedAuthority(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.AuthConfig
		opts PrincipalOptions
	}{
		{"empty domain", config.AuthConfig{Basic: &config.BasicAuthConfig{}}, PrincipalOptions{}},
		{"no credential method", config.AuthConfig{}, PrincipalOptions{Domain: "accounts"}},
		{"JWT without issuer", config.AuthConfig{JWT: &config.JWTAuthConfig{Audience: "api"}}, PrincipalOptions{Domain: "accounts"}},
		{"JWT without audience", config.AuthConfig{JWT: &config.JWTAuthConfig{Issuer: "issuer"}}, PrincipalOptions{Domain: "accounts"}},
		{"basic with response header", config.AuthConfig{Basic: &config.BasicAuthConfig{}}, PrincipalOptions{Domain: "accounts", ForwardResponseHeader: "X-User"}},
		{"forward without response header", config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{}}, PrincipalOptions{Domain: "accounts"}},
		{"forward with invalid response header", config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{}}, PrincipalOptions{Domain: "accounts", ForwardResponseHeader: "not a header"}},
		{"forward with hop-by-hop response header", config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{}}, PrincipalOptions{Domain: "accounts", ForwardResponseHeader: "Connection"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(context.Background(), tt.cfg, Options{Principal: &tt.opts}); err == nil {
				t.Fatal("invalid principal authority accepted")
			}
		})
	}
}

func TestBasicPrincipalRejectsForwardResponseHeader(t *testing.T) {
	err := validatePrincipalOptions(
		config.AuthConfig{Basic: &config.BasicAuthConfig{}},
		&PrincipalOptions{Domain: "accounts", ForwardResponseHeader: "X-User"},
	)
	if err == nil || !strings.Contains(err.Error(), "requires forward authentication") {
		t.Fatalf("basic principal response-header validation error = %v", err)
	}

	err = validatePrincipalOptions(
		config.AuthConfig{JWT: &config.JWTAuthConfig{Issuer: "issuer", Audience: "api"}},
		&PrincipalOptions{Domain: "accounts", ForwardResponseHeader: "X-User"},
	)
	if err == nil || !strings.Contains(err.Error(), "requires forward authentication") {
		t.Fatalf("JWT principal response-header validation error = %v", err)
	}
}

func TestBasicPrincipalRequiresSuccessfulAuthentication(t *testing.T) {
	path := writeHtpasswd(t, map[string]string{"alice": "secret", strings.Repeat("a", maxPrincipalBytes+1): "secret"})
	opts := PrincipalOptions{Domain: "staff"}
	a, err := New(context.Background(), config.AuthConfig{
		Basic: &config.BasicAuthConfig{File: path, Realm: "display only"},
	}, Options{Principal: &opts})
	if err != nil {
		t.Fatal(err)
	}
	opts.Domain = "changed"

	var principal Principal
	var present, dispatched bool
	h := a.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		dispatched = true
		principal, present = PrincipalFromContext(r.Context())
	}))
	for _, tt := range []struct {
		name, account, password string
		status                  int
		want                    Principal
		present                 bool
	}{
		{"verified", "alice", "secret", http.StatusOK, Principal{Method: "basic", Domain: "staff", Subject: "alice"}, true},
		{"wrong password", "alice", "wrong", http.StatusUnauthorized, Principal{}, false},
		{"oversized account falls back", strings.Repeat("a", maxPrincipalBytes+1), "secret", http.StatusOK, Principal{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			principal, present, dispatched = Principal{}, false, false
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.SetBasicAuth(tt.account, tt.password)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.status || dispatched != (tt.status == http.StatusOK) || present != tt.present || principal != tt.want {
				t.Fatalf("status=%d dispatched=%v principal=%+v present=%v", rec.Code, dispatched, principal, present)
			}
		})
	}
}

func TestJWTPrincipalIsIssuerScoped(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := newJWKSServer(t, rsaJWK("key", &key.PublicKey))
	var principals []Principal
	for _, issuer := range []string{"https://issuer.example", "https://other.example"} {
		cfg := config.AuthConfig{JWT: &config.JWTAuthConfig{
			JWKSURL: srv.URL, Issuer: issuer, Audience: "my-api", Algorithms: defaultAlgs(),
		}}
		a, err := New(context.Background(), cfg, Options{
			HTTPClient: srv.Client(), Principal: &PrincipalOptions{Domain: "accounts"},
		})
		if err != nil {
			t.Fatal(err)
		}
		var got Principal
		var present bool
		h := a.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			got, present = PrincipalFromContext(r.Context())
		}))
		claims := validClaims()
		claims["iss"] = issuer
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq(signRS256(t, key, "key", claims)))
		if rec.Code != http.StatusOK || !present || got != (Principal{Method: "jwt", Domain: "accounts", Issuer: issuer, Subject: "user-123"}) {
			t.Fatalf("status=%d principal=%+v present=%v", rec.Code, got, present)
		}
		principals = append(principals, got)

		for _, subject := range []any{"", strings.Repeat("a", maxPrincipalBytes+1), 123} {
			claims["sub"] = subject
			present = true
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, bearerReq(signRS256(t, key, "key", claims)))
			if rec.Code != http.StatusOK || present {
				t.Fatalf("unusable subject: status=%d present=%v", rec.Code, present)
			}
		}
		claims["aud"] = "wrong"
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, bearerReq(signRS256(t, key, "key", claims)))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong audience status=%d", rec.Code)
		}
	}
	if principals[0] == principals[1] {
		t.Fatal("subjects from different issuers collapsed to the same principal")
	}
}

func TestForwardPrincipalComesOnlyFromSuccessfulServiceResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Forwarded-Uri") {
		case "/verified":
			w.Header().Set("X-Quota-User", "alice")
		case "/multiple":
			w.Header().Add("X-Quota-User", "alice")
			w.Header().Add("X-Quota-User", "bob")
		case "/combined":
			w.Header().Set("X-Quota-User", "alice, bob")
		case "/oversized":
			w.Header().Set("X-Quota-User", strings.Repeat("x", maxPrincipalBytes+1))
		case "/connection":
			w.Header().Set("Connection", "X-Quota-User")
			w.Header().Set("X-Quota-User", "alice")
		case "/deny":
			w.Header().Set("X-Quota-User", "alice")
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer srv.Close()
	a, err := New(context.Background(), config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{URL: srv.URL},
	}, Options{HTTPClient: srv.Client(), Principal: &PrincipalOptions{Domain: "services", ForwardResponseHeader: "X-Quota-User"}})
	if err != nil {
		t.Fatal(err)
	}
	var principal Principal
	var present, dispatched bool
	h := a.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		dispatched = true
		principal, present = PrincipalFromContext(r.Context())
	}))
	for _, tt := range []struct {
		path    string
		status  int
		present bool
	}{
		{"/verified", http.StatusOK, true},
		{"/missing", http.StatusOK, false},
		{"/multiple", http.StatusOK, false},
		{"/combined", http.StatusOK, false},
		{"/oversized", http.StatusOK, false},
		{"/connection", http.StatusOK, false},
		{"/deny", http.StatusForbidden, false},
	} {
		t.Run(tt.path, func(t *testing.T) {
			principal, present, dispatched = Principal{}, false, false
			r := httptest.NewRequest(http.MethodGet, "http://app.example"+tt.path, nil)
			r.Header.Set("X-Quota-User", "attacker")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tt.status || dispatched != (tt.status == http.StatusOK) || present != tt.present {
				t.Fatalf("status=%d dispatched=%v principal=%+v present=%v", rec.Code, dispatched, principal, present)
			}
			if present && principal != (Principal{Method: "forward", Domain: "services", Subject: "alice"}) {
				t.Fatalf("principal=%+v", principal)
			}
		})
	}
}

func TestAuthenticatorClearsPriorPrincipal(t *testing.T) {
	a, err := New(context.Background(), config.AuthConfig{}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), principalKey{}, Principal{Method: "basic", Domain: "old", Subject: "alice"}))
	h := a.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if p, ok := PrincipalFromContext(r.Context()); ok {
			t.Errorf("inherited principal: %+v", p)
		}
	}))
	h.ServeHTTP(httptest.NewRecorder(), r)
}
