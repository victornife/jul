// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package adminoidc is the bounded, single-provider Console relying party.
// It authenticates identities; Jul's existing immutable RBAC policy authorizes.
package adminoidc

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/auth"
	"jul/internal/config"
	"jul/internal/rbac"
)

var ErrUnauthenticated = errors.New("OIDC session unavailable")

const transactionLifetime = 5 * time.Minute

// Runtime belongs to exactly one immutable admin authentication generation.
// No background workers are needed: every store operation expires dead entries.
type Runtime struct {
	cfg           config.AdminOIDCConfig
	policy        *rbac.Policy
	client        *http.Client
	origin        string
	secure        bool
	now           func() time.Time
	providerMu    sync.Mutex
	provider      *provider
	providerUntil time.Time
	providerRetry time.Time
	mu            sync.Mutex
	transactions  map[[32]byte]transaction
	sessions      [32]sessionShard
	sessionCount  atomic.Int64
}

type transaction struct {
	browser         [32]byte
	nonce, verifier string
	expires         time.Time
	provider        *provider
}

type session struct {
	identity      rbac.Identity
	last, expires time.Time
}

type provider struct {
	authorization, token, jwks string
	authMethod                 string
	verifier                   *auth.IDTokenVerifier
}

// New constructs local bounded state only; provider outages never make a
// validated config candidate mutate or discard the current generation.
func New(cfg config.AdminOIDCConfig, policy *rbac.Policy, client *http.Client) (*Runtime, error) {
	config.DefaultAdminOIDC(&cfg)
	callback, err := config.OIDCURL(cfg.CallbackURL)
	if err != nil || callback.Path != "/auth/oidc/callback" || policy == nil || !policy.Enabled() || cfg.MaxSessions < 1 || cfg.MaxSessions > 65536 || cfg.MaxTransactions < 1 || cfg.MaxTransactions > 1024 {
		return nil, ErrUnauthenticated
	}
	if _, err := config.OIDCURL(cfg.Issuer); err != nil {
		return nil, ErrUnauthenticated
	}
	if client == nil {
		return nil, ErrUnauthenticated
	}
	// Copy the caller-owned egress client; never mutate its policy or pooling.
	bounded := *client
	bounded.Timeout = 10 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Runtime{cfg: cfg, policy: policy, client: &bounded, origin: callback.Scheme + "://" + canonicalHost(callback.Scheme, callback.Host), secure: callback.Scheme == "https", now: time.Now, transactions: map[[32]byte]transaction{}}, nil
}

func randomSecret() string       { return rand.Text() + rand.Text() }
func digest(raw string) [32]byte { return sha256.Sum256([]byte(raw)) }
func challenge(verifier string) string {
	sum := digest(verifier)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func (r *Runtime) cookieName(login bool) string {
	name := "jul_oidc"
	if login {
		name += "_login"
	}
	if r.secure {
		return "__Host-" + name
	}
	return name + "_dev"
}
func (r *Runtime) setCookie(w http.ResponseWriter, login bool, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: r.cookieName(login), Value: value, Path: "/", HttpOnly: true, Secure: r.secure, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (r *Runtime) cookie(req *http.Request, login bool) (string, bool) {
	found := ""
	count := 0
	for _, c := range req.Cookies() {
		if c.Name == r.cookieName(login) {
			count++
			if count > 1 {
				return "", false
			}
			found = c.Value
		}
	}
	return found, len(found) >= 32 && len(found) <= 128
}
func (r *Runtime) cleanup(now time.Time) {
	for key, t := range r.transactions {
		if !now.Before(t.expires) {
			delete(r.transactions, key)
		}
	}
	for i := range r.sessions {
		shard := &r.sessions[i]
		shard.mu.Lock()
		for key, s := range shard.entries {
			if !r.active(s, now) {
				delete(shard.entries, key)
				r.sessionCount.Add(-1)
			}
		}
		shard.mu.Unlock()
	}
}

// CookiePresented recognizes both secure/development cookie names even when
// OIDC was disabled by reload, preventing fallback to an unrelated credential.
func CookiePresented(req *http.Request) bool {
	for _, c := range req.Cookies() {
		if c.Name == "__Host-jul_oidc" || c.Name == "jul_oidc_dev" {
			return true
		}
	}
	return false
}

// SameOrigin requires the exact configured origin and fetch metadata when sent.
// Missing Origin is denied for cookie-authenticated unsafe requests. The custom
// header is not a secret; it prevents simple forms from becoming API mutations.
func (r *Runtime) SameOrigin(req *http.Request, header bool) bool {
	origin := req.Header.Values("Origin")
	site := req.Header.Get("Sec-Fetch-Site")
	return canonicalHost(strings.SplitN(r.origin, ":", 2)[0], req.Host) == mustHost(r.origin) && len(origin) == 1 && origin[0] == r.origin && (site == "" || site == "same-origin" || site == "none") && (!header || req.Header.Get("X-Jul-Console") == "1")
}
func mustHost(origin string) string { u, _ := url.Parse(origin); return u.Host }

// Authenticate resolves an opaque session and enforces CSRF on unsafe methods.
// Raw cookies, provider tokens and claim sets never leave this package.
func (r *Runtime) Authenticate(req *http.Request) (rbac.Identity, time.Time, error) {
	raw, ok := r.cookie(req, false)
	if !ok || sessionCookieCount(req) != 1 || len(req.Header.Values("Authorization")) > 0 {
		return rbac.Identity{}, time.Time{}, ErrUnauthenticated
	}
	if req.Method != "GET" && req.Method != "HEAD" && req.Method != "OPTIONS" && !r.SameOrigin(req, true) {
		return rbac.Identity{}, time.Time{}, ErrUnauthenticated
	}
	now := r.now()
	key := digest(raw)
	shard := &r.sessions[key[0]%32]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	s, ok := shard.entries[key]
	if !ok {
		return rbac.Identity{}, time.Time{}, ErrUnauthenticated
	}
	if !r.active(s, now) {
		delete(shard.entries, key)
		r.sessionCount.Add(-1)
		return rbac.Identity{}, time.Time{}, ErrUnauthenticated
	}
	s.last = now
	expiry := s.expires
	if idle := now.Add(r.cfg.SessionIdle.Std()); idle.Before(expiry) {
		expiry = idle
	}
	return s.identity, expiry, nil
}

// Login creates single-use state/nonce/PKCE bound to a second browser secret.
func (r *Runtime) Login(w http.ResponseWriter, req *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if req.Method != "POST" || !r.SameOrigin(req, false) || len(req.Header.Values("Authorization")) > 0 {
		http.Error(w, "Sign-in request refused", http.StatusForbidden)
		return
	}
	p, err := r.discover(req.Context())
	if err != nil {
		http.Redirect(w, req, "/?oidc_error=unavailable", http.StatusSeeOther)
		return
	}
	state, nonce, verifier, browser := randomSecret(), randomSecret(), randomSecret()+randomSecret(), randomSecret()
	now := r.now()
	r.mu.Lock()
	r.cleanup(now)
	if len(r.transactions) >= r.cfg.MaxTransactions {
		r.mu.Unlock()
		http.Redirect(w, req, "/?oidc_error=capacity", http.StatusSeeOther)
		return
	}
	r.transactions[digest(state)] = transaction{browser: digest(browser), nonce: nonce, verifier: verifier, expires: now.Add(transactionLifetime), provider: p}
	r.mu.Unlock()
	r.setCookie(w, true, browser, 300)
	u, _ := url.Parse(p.authorization)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", r.cfg.ClientID)
	q.Set("redirect_uri", r.cfg.CallbackURL)
	q.Set("scope", joinScopes(r.cfg.Scopes))
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("code_challenge", challenge(verifier))
	q.Set("code_challenge_method", "S256")
	u.RawQuery = q.Encode()
	http.Redirect(w, req, u.String(), http.StatusSeeOther)
}

// Callback consumes the transaction before exchanging a code; all outcomes
// navigate away from the callback so codes/state never remain in Console URLs.
func (r *Runtime) Callback(w http.ResponseWriter, req *http.Request) (identity rbac.Identity, accepted bool) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	r.setCookie(w, true, "", -1)
	fail := func() { http.Redirect(w, req, "/?oidc_error=login_failed", http.StatusSeeOther) }
	if req.Method != "GET" || len(req.Header.Values("Authorization")) > 0 || len(req.URL.RawQuery) > 8192 || canonicalHost(strings.SplitN(r.origin, ":", 2)[0], req.Host) != mustHost(r.origin) {
		fail()
		return
	}
	q, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || len(q["state"]) != 1 || len(q["code"]) != 1 || q.Get("code") == "" || len(q.Get("code")) > 4096 || q.Get("error") != "" {
		fail()
		return
	}
	if iss := q.Get("iss"); len(q["iss"]) > 1 || (iss != "" && iss != r.cfg.Issuer) {
		fail()
		return
	}
	browser, ok := r.cookie(req, true)
	if !ok || len(q.Get("state")) > 128 {
		fail()
		return
	}
	now := r.now()
	key := digest(q.Get("state"))
	r.mu.Lock()
	r.cleanup(now)
	t, ok := r.transactions[key]
	delete(r.transactions, key)
	r.mu.Unlock()
	if !ok || t.browser != digest(browser) {
		fail()
		return
	}
	claims, err := r.exchange(req.Context(), t, q.Get("code"))
	if err != nil {
		fail()
		return
	}
	id, err := r.mapIdentity(claims)
	if err != nil {
		fail()
		return
	}
	id.ProviderName = r.cfg.ProviderName
	raw := randomSecret()
	now = r.now()
	r.mu.Lock()
	r.cleanup(now)
	if old, ok := r.cookie(req, false); ok {
		r.deleteSession(digest(old))
	}
	if r.sessionCount.Load() >= int64(r.cfg.MaxSessions) {
		r.mu.Unlock()
		fail()
		return
	}
	key = digest(raw)
	shard := &r.sessions[key[0]%32]
	shard.mu.Lock()
	if shard.entries == nil {
		shard.entries = map[[32]byte]*session{}
	}
	shard.entries[key] = &session{identity: id, last: now, expires: now.Add(r.cfg.SessionAbsolute.Std())}
	r.sessionCount.Add(1)
	shard.mu.Unlock()
	r.mu.Unlock()
	ClearSessionCookies(w)
	r.setCookie(w, false, raw, int(r.cfg.SessionAbsolute.Std()/time.Second))
	http.Redirect(w, req, "/", http.StatusSeeOther)
	return id, true
}

// Logout revokes the Jul session immediately; upstream IdP sessions are separate.
func (r *Runtime) Logout(w http.ResponseWriter, req *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if req.Method != "POST" || !r.SameOrigin(req, true) || len(req.Header.Values("Authorization")) > 0 {
		http.Error(w, "Logout request refused", http.StatusForbidden)
		return false
	}
	r.mu.Lock()
	for _, cookie := range req.Cookies() {
		if cookie.Name == r.cookieName(false) && len(cookie.Value) <= 128 {
			r.deleteSession(digest(cookie.Value))
		}
	}
	r.cleanup(r.now())
	r.mu.Unlock()
	ClearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
	return true
}

// ProviderName is a configured display label, not an untrusted provider claim.
func (r *Runtime) ProviderName() string { return r.cfg.ProviderName }

func subjectFingerprint(issuer, subject string) string {
	sum := digest(issuer + "\x00" + subject)
	return hex.EncodeToString(sum[:16])
}

type sessionShard struct {
	mu      sync.Mutex
	entries map[[32]byte]*session
}

func (r *Runtime) active(s *session, now time.Time) bool {
	return now.Before(s.expires) && now.Before(s.last.Add(r.cfg.SessionIdle.Std()))
}
func (r *Runtime) deleteSession(key [32]byte) {
	shard := &r.sessions[key[0]%32]
	shard.mu.Lock()
	defer shard.mu.Unlock()
	if _, ok := shard.entries[key]; ok {
		delete(shard.entries, key)
		r.sessionCount.Add(-1)
	}
}

func canonicalHost(scheme, host string) string {
	u, err := url.Parse(scheme + "://" + host)
	if err != nil {
		return ""
	}
	hostname := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" || (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		if strings.Contains(hostname, ":") {
			return "[" + hostname + "]"
		}
		return hostname
	}
	return net.JoinHostPort(hostname, port)
}

func sessionCookieCount(req *http.Request) int {
	count := 0
	for _, c := range req.Cookies() {
		if c.Name == "__Host-jul_oidc" || c.Name == "jul_oidc_dev" {
			count++
		}
	}
	return count
}

// ClearSessionCookies supports recovery even after OIDC has been disabled.
// This removes browser credentials only and confers no authentication.
func ClearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{"__Host-jul_oidc", "__Host-jul_oidc_login", "jul_oidc_dev", "jul_oidc_login_dev"} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: strings.HasPrefix(name, "__Host-"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	}
}
