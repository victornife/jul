// Copyright 2026 Victor Niharra <vniharre@gmail.com>
// SPDX-License-Identifier: agpl

// Package oidctest provides a synthetic, local-only identity provider for tests.
// It is never imported by the shipped gateway.
package oidctest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Provider owns synthetic keys/codes and bounded test-controlled failures.
type Provider struct {
	Server *httptest.Server
	mu     sync.Mutex
	key    *rsa.PrivateKey
	kid    string
	codes  map[string]url.Values
	claims map[string]any
	mode   string
}

// New starts a deterministic-protocol provider with synthetic credentials only.
func New() (*Provider, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	p := &Provider{key: key, kid: "key-1", codes: map[string]url.Values{}, claims: map[string]any{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	return p, nil
}
func (p *Provider) Close()      { p.Server.Close() }
func (p *Provider) URL() string { return p.Server.URL }

// Configure replaces bounded test claims and a failure mode between exchanges.
func (p *Provider) Configure(mode string, claims map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mode = mode
	p.claims = claims
}

// Rotate publishes a genuinely different asymmetric signing key.
func (p *Provider) Rotate() error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = key
	p.kid += "x"
	return nil
}
func (p *Provider) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if p.mode == "outage" {
		w.WriteHeader(503)
		return
	}
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		if p.mode == "oversized" {
			_, _ = w.Write(make([]byte, 65537))
			return
		}
		if p.mode == "redirect" {
			http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
			return
		}
		m := map[string]any{"issuer": p.URL(), "authorization_endpoint": p.URL() + "/authorize", "token_endpoint": p.URL() + "/token", "jwks_uri": p.URL() + "/jwks", "response_types_supported": []string{"code"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none", "client_secret_basic", "client_secret_post"}}
		if p.mode == "wrong_discovery_issuer" {
			m["issuer"] = "https://other.invalid"
		}
		if p.mode == "unsafe_endpoint" {
			m["token_endpoint"] = "http://remote.invalid/token"
		}
		if p.mode == "plain_pkce" {
			m["code_challenge_methods_supported"] = []string{"plain"}
		}
		_ = json.NewEncoder(w).Encode(m)
	case "/authorize":
		q := r.URL.Query()
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("nonce") == "" {
			w.WriteHeader(400)
			return
		}
		code := rand.Text()
		p.codes[code] = q
		callback, _ := url.Parse(q.Get("redirect_uri"))
		v := callback.Query()
		v.Set("code", code)
		v.Set("state", q.Get("state"))
		v.Set("iss", p.URL())
		callback.RawQuery = v.Encode()
		http.Redirect(w, r, callback.String(), http.StatusSeeOther)
	case "/token":
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(400)
			return
		}
		q, ok := p.codes[r.Form.Get("code")]
		delete(p.codes, r.Form.Get("code"))
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if !ok || q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("redirect_uri") != r.Form.Get("redirect_uri") || q.Get("client_id") != r.Form.Get("client_id") {
			w.WriteHeader(400)
			return
		}
		now := time.Now()
		claims := jwt.MapClaims{"iss": p.URL(), "sub": "operator-1", "aud": r.Form.Get("client_id"), "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": q.Get("nonce")}
		for k, v := range p.claims {
			claims[k] = v
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		token.Header["kid"] = p.kid
		raw, err := token.SignedString(p.key)
		if err != nil {
			w.WriteHeader(500)
			return
		}
		if p.mode == "bad_signature" {
			raw = raw[:len(raw)-20] + "AAAAAAAAAAAAAAAAAAAA"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": raw, "access_token": "synthetic-not-for-Jul", "token_type": "Bearer"})
	case "/jwks":
		n := base64.RawURLEncoding.EncodeToString(p.key.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(p.key.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": p.kid, "alg": "RS256", "use": "sig", "n": n, "e": e}}})
	default:
		w.WriteHeader(404)
	}
}
