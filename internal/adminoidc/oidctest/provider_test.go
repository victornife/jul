// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package oidctest_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"jul/internal/adminoidc/oidctest"
	"jul/internal/auth"
)

// These assertions keep the fake provider an independent PKCE/replay oracle:
// a permissive test double would conceal relying-party protocol regressions.
func TestProviderEnforcesPKCEBindingReplayAndRotation(t *testing.T) {
	p, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	c := p.Server.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	get := func(path string) *http.Response {
		t.Helper()
		r, e := c.Get(p.URL() + path)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { r.Body.Close() })
		return r
	}
	if get("/missing").StatusCode != 404 || get("/authorize").StatusCode != 400 {
		t.Fatal("invalid provider requests admitted")
	}
	verifier := strings.Repeat("v", 64)
	sum := sha256.Sum256([]byte(verifier))
	authorize := func() string {
		t.Helper()
		q := url.Values{"client_id": {"jul-console"}, "redirect_uri": {"http://127.0.0.1/callback"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "nonce": {"expected-nonce"}, "state": {"expected-state"}}
		r := get("/authorize?" + q.Encode())
		u, e := url.Parse(r.Header.Get("Location"))
		if e != nil || u.Query().Get("state") != "expected-state" {
			t.Fatal("state not bound")
		}
		return u.Query().Get("code")
	}
	exchange := func(code, proof, redirect string) *http.Response {
		t.Helper()
		r, e := c.PostForm(p.URL()+"/token", url.Values{"code": {code}, "client_id": {"jul-console"}, "redirect_uri": {redirect}, "code_verifier": {proof}})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { r.Body.Close() })
		return r
	}
	for _, tt := range []struct{ proof, redirect string }{{"wrong", "http://127.0.0.1/callback"}, {verifier, "http://127.0.0.1/other"}} {
		code := authorize()
		if exchange(code, tt.proof, tt.redirect).StatusCode != 400 {
			t.Fatal("PKCE or redirect bypass")
		}
		if exchange(code, verifier, "http://127.0.0.1/callback").StatusCode != 400 {
			t.Fatal("failed exchange code replay")
		}
	}
	keys := get("/jwks")
	before, _ := io.ReadAll(keys.Body)
	if err := p.Rotate(); err != nil {
		t.Fatal(err)
	}
	keys = get("/jwks")
	after, _ := io.ReadAll(keys.Body)
	if string(before) == string(after) {
		t.Fatal("rotation reused signing key")
	}
	code := authorize()
	r := exchange(code, verifier, "http://127.0.0.1/callback")
	var tokens map[string]string
	if json.NewDecoder(r.Body).Decode(&tokens) != nil {
		t.Fatal("missing token")
	}
	v := auth.NewIDTokenVerifier(p.URL()+"/jwks", p.URL(), "jul-console", c, time.Minute)
	claims, err := v.Verify(context.Background(), tokens["id_token"])
	if err != nil || claims["nonce"] != "expected-nonce" {
		t.Fatal("rotated token not verifiable", err)
	}
	if exchange(code, verifier, "http://127.0.0.1/callback").StatusCode != 400 {
		t.Fatal("successful code replay")
	}
	for _, mode := range []string{"outage", "oversized", "redirect", "wrong_discovery_issuer", "unsafe_endpoint", "plain_pkce"} {
		p.Configure(mode, nil)
		r := get("/.well-known/openid-configuration")
		body, _ := io.ReadAll(r.Body)
		switch mode {
		case "outage":
			if r.StatusCode != 503 {
				t.Fatal(mode)
			}
		case "oversized":
			if len(body) <= 65536 {
				t.Fatal(mode)
			}
		case "redirect":
			if r.StatusCode != 302 {
				t.Fatal(mode)
			}
		default:
			if !json.Valid(body) {
				t.Fatal(mode)
			}
		}
	}
	p.Configure("bad_signature", nil)
	r = exchange(authorize(), verifier, "http://127.0.0.1/callback")
	if json.NewDecoder(r.Body).Decode(&tokens) != nil {
		t.Fatal("bad signature fixture")
	}
	if _, err := v.Verify(context.Background(), tokens["id_token"]); err == nil {
		t.Fatal("bad-signature fixture was valid")
	}
}
