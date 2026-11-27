// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"jul/internal/auth"
	"jul/internal/config"
)

func quotaServingPolicy(rate, burst int, classes map[string]config.QuotaClassConfig) config.QuotaPolicyConfig {
	return config.QuotaPolicyConfig{
		Mode:       "enforce",
		Rate:       rate,
		Burst:      burst,
		MaxCallers: 16,
		Classes:    classes,
	}
}

func quotaServingClass(rate, burst, callerRate, callerBurst int) config.QuotaClassConfig {
	return config.QuotaClassConfig{
		Shared:    config.QuotaLimitConfig{Rate: rate, Burst: burst},
		PerCaller: config.QuotaLimitConfig{Rate: callerRate, Burst: callerBurst},
	}
}

func startQuotaHTTPServer(t *testing.T, cfg *config.Config) *httptest.Server {
	t.Helper()
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate quota serving config: %v", err)
	}
	factory, cleanup := minimalFactory(t)
	factory.QuotaRuntime = newQuotaRuntime()
	handlers, retire, err := factory.Build(context.Background(), cfg, true)
	if err != nil {
		cleanup()
		t.Fatalf("build quota serving handler: %v", err)
	}
	handler := handlers[cfg.Servers[0].Listen]
	if handler == nil {
		cleanup()
		t.Fatalf("handler for %q was not built", cfg.Servers[0].Listen)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		if retire != nil {
			retire()
		}
		cleanup()
	})
	return server
}

func quotaHTTPRequest(t *testing.T, server *httptest.Server, method, path, host, user, password string) *http.Response {
	t.Helper()
	return quotaHTTPDo(t, server.Client(), server, method, path, host, user, password)
}

func quotaHTTPDo(t *testing.T, client *http.Client, server *httptest.Server, method, path, host, user, password string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, server.URL+path, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if host != "" {
		req.Host = host
	}
	if user != "" {
		req.SetBasicAuth(user, password)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("HTTP %s %s: %v", method, path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func quotaHTTPForwardedFor(t *testing.T, server *httptest.Server, address string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatalf("new forwarded request: %v", err)
	}
	req.Header.Set("X-Forwarded-For", address)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("forwarded request for %s: %v", address, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func quotaHTTPBearerRequest(t *testing.T, server *httptest.Server, path, token string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatalf("new bearer request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatalf("bearer request for %s: %v", path, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

func quotaBasicFile(t *testing.T, users map[string]string) string {
	t.Helper()
	userFile := filepath.Join(t.TempDir(), "users")
	if err := os.WriteFile(userFile, quotaBasicFileContents(t, users), 0o600); err != nil {
		t.Fatalf("write test htpasswd: %v", err)
	}
	return userFile
}

func quotaBasicFileContents(t *testing.T, users map[string]string) []byte {
	t.Helper()
	var contents strings.Builder
	for user, password := range users {
		passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
		if err != nil {
			t.Fatalf("generate test password hash: %v", err)
		}
		fmt.Fprintf(&contents, "%s:%s\n", user, passwordHash)
	}
	return []byte(contents.String())
}

func TestQuotaServingHTTPBudgetsAggregateAcrossHostsAndRoutes(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"shared": quotaServingPolicy(1, 1, map[string]config.QuotaClassConfig{
			"public": quotaServingClass(1, 1, 1, 1),
		}),
	}
	cfg.Servers[0].ServerNames = []string{"a.example"}
	cfg.Servers[0].Locations[0].Match.Path = "/one"
	cfg.Servers[0].Locations[0].QuotaPolicy = "shared"
	second := cfg.Servers[0]
	second.ServerNames = []string{"b.example"}
	second.Locations = append([]config.LocationConfig(nil), cfg.Servers[0].Locations...)
	second.Locations[0].Match.Path = "/two"
	cfg.Servers = append(cfg.Servers, second)

	server := startQuotaHTTPServer(t, cfg)
	first := quotaHTTPRequest(t, server, http.MethodGet, "/one", "a.example", "", "")
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first host/route status = %d, want 200", first.StatusCode)
	}
	secondResponse := quotaHTTPRequest(t, server, http.MethodGet, "/two", "b.example", "", "")
	if secondResponse.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second host/route status = %d, want shared-policy 429", secondResponse.StatusCode)
	}
}

func TestQuotaServingHTTPClassifiesTrustedIPv4AndIPv6Pools(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"network": {
			Mode:       "enforce",
			Rate:       5,
			Burst:      5,
			MaxCallers: 16,
			Classes: map[string]config.QuotaClassConfig{
				"public":   quotaServingClass(1, 1, 1, 1),
				"broad":    quotaServingClass(2, 2, 2, 2),
				"narrow":   quotaServingClass(1, 1, 2, 2),
				"network6": quotaServingClass(1, 1, 2, 2),
			},
			Pools: map[string]config.QuotaPoolConfig{
				"broad":    {Class: "broad", CIDRs: []string{"198.51.100.0/24"}},
				"narrow":   {Class: "narrow", CIDRs: []string{"198.51.100.128/25"}},
				"network6": {Class: "network6", CIDRs: []string{"2001:db8:1::/48"}},
			},
		},
	}
	cfg.Servers[0].ClientAddress = &config.ClientAddressConfig{
		TrustedProxies:   []string{"127.0.0.1/32"},
		ForwardedHeaders: []string{"x-forwarded-for"},
	}
	cfg.Servers[0].Locations[0].QuotaPolicy = "network"
	server := startQuotaHTTPServer(t, cfg)

	for _, address := range []string{"198.51.100.10", "198.51.100.11", "::ffff:198.51.100.130"} {
		if resp := quotaHTTPForwardedFor(t, server, address); resp.StatusCode != http.StatusOK {
			t.Fatalf("request for %s = %d, want broad or longest-prefix class admission", address, resp.StatusCode)
		}
	}
	if resp := quotaHTTPForwardedFor(t, server, "198.51.100.131"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second narrow-pool caller = %d, want shared narrow-class 429", resp.StatusCode)
	}
	if resp := quotaHTTPForwardedFor(t, server, "2001:db8:1::1"); resp.StatusCode != http.StatusOK {
		t.Fatalf("IPv6 pool request = %d, want 200", resp.StatusCode)
	}
}

func TestQuotaServingHTTPRetryAfterRoundsUpFractionalRefill(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"shared": quotaServingPolicy(1, 1, map[string]config.QuotaClassConfig{
			"public": quotaServingClass(1, 1, 1, 1),
		}),
	}
	cfg.Servers[0].Locations[0].QuotaPolicy = "shared"
	server := startQuotaHTTPServer(t, cfg)

	if resp := quotaHTTPRequest(t, server, http.MethodGet, "/", "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("initial request status = %d, want 200", resp.StatusCode)
	}
	time.Sleep(10 * time.Millisecond)
	firstDenied := quotaHTTPRequest(t, server, http.MethodGet, "/", "", "", "")
	if firstDenied.StatusCode != http.StatusTooManyRequests || firstDenied.Header.Get("Retry-After") != "1" {
		t.Fatalf("fractional retry response = %d Retry-After=%q, want 429 and 1", firstDenied.StatusCode, firstDenied.Header.Get("Retry-After"))
	}
	time.Sleep(410 * time.Millisecond)
	secondDenied := quotaHTTPRequest(t, server, http.MethodGet, "/", "", "", "")
	if secondDenied.StatusCode != http.StatusTooManyRequests || secondDenied.Header.Get("Retry-After") != "1" {
		t.Fatalf("second fractional retry response = %d Retry-After=%q, want 429 and 1", secondDenied.StatusCode, secondDenied.Header.Get("Retry-After"))
	}
	time.Sleep(610 * time.Millisecond)
	if resp := quotaHTTPRequest(t, server, http.MethodGet, "/", "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("request after one refill interval status = %d, want 200 after non-mutating denials", resp.StatusCode)
	}
}

func TestQuotaServingHTTPPreflightUsesPublicQuotaBeforeAuthentication(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	userFile := quotaBasicFile(t, map[string]string{"alice": "quota-test-password"})

	cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"shared": quotaServingPolicy(1, 1, map[string]config.QuotaClassConfig{
			"public": quotaServingClass(1, 1, 1, 1),
		}),
	}
	location := &cfg.Servers[0].Locations[0]
	location.QuotaPolicy = "shared"
	location.Auth = &config.AuthConfig{
		Basic: &config.BasicAuthConfig{File: userFile},
	}
	location.CORS = &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://app.example"},
		AllowedMethods: []string{"GET"},
	}
	server := startQuotaHTTPServer(t, cfg)

	preflight, err := http.NewRequest(http.MethodOptions, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	preflight.Header.Set("Origin", "https://app.example")
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	preflightResponse, err := server.Client().Do(preflight)
	if err != nil {
		t.Fatalf("CORS preflight: %v", err)
	}
	_, _ = io.Copy(io.Discard, preflightResponse.Body)
	_ = preflightResponse.Body.Close()
	if preflightResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204 without credentials", preflightResponse.StatusCode)
	}

	ordinary := quotaHTTPRequest(t, server, http.MethodGet, "/", "", "alice", "quota-test-password")
	if ordinary.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("request after preflight status = %d, want quota 429", ordinary.StatusCode)
	}
}

func TestQuotaServingHTTPKeepsPublicAndVerifiedAccountBudgetsSeparate(t *testing.T) {
	var originRequests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	userFile := quotaBasicFile(t, map[string]string{"alice": "quota-test-password"})

	cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"shared": quotaServingPolicy(3, 3, map[string]config.QuotaClassConfig{
			"public":  quotaServingClass(2, 2, 2, 2),
			"account": quotaServingClass(1, 1, 1, 1),
		}),
	}
	cfg.QuotaPolicies["shared"].Classes["account"] = config.QuotaClassConfig{
		Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
		PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
		Principals: []config.QuotaPrincipalMatch{
			{Method: "basic", Domain: "staff", Subject: "alice"},
		},
	}
	public := cfg.Servers[0].Locations[0]
	public.Match.Path = "/public"
	public.QuotaPolicy = "shared"
	protected := public
	protected.Match.Path = "/protected"
	protected.Auth = &config.AuthConfig{
		Basic:          &config.BasicAuthConfig{File: userFile},
		QuotaPrincipal: &config.AuthQuotaPrincipalConfig{Domain: "staff"},
	}
	cfg.Servers[0].Locations = []config.LocationConfig{public, protected}

	server := startQuotaHTTPServer(t, cfg)
	firstClient := server.Client()
	secondTransport := &http.Transport{}
	secondClient := &http.Client{Transport: secondTransport}
	t.Cleanup(secondTransport.CloseIdleConnections)

	if resp := quotaHTTPDo(t, firstClient, server, http.MethodGet, "/public", "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("public request sharing the address = %d, want 200", resp.StatusCode)
	}
	if resp := quotaHTTPDo(t, firstClient, server, http.MethodGet, "/protected", "", "alice", "quota-test-password"); resp.StatusCode != http.StatusOK {
		t.Fatalf("first verified account session = %d, want 200", resp.StatusCode)
	}
	secondSession := quotaHTTPDo(t, secondClient, server, http.MethodGet, "/protected", "", "alice", "quota-test-password")
	if secondSession.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second verified account session = %d, want shared caller 429", secondSession.StatusCode)
	}
	if resp := quotaHTTPDo(t, secondClient, server, http.MethodGet, "/public", "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("public request after denied account attempt = %d, want uncharged 200", resp.StatusCode)
	}
	if got := originRequests.Load(); got != 3 {
		t.Fatalf("origin received %d requests, want only three admitted requests", got)
	}
}

func TestQuotaServingHTTPSeparatesIdenticalJWTSubjectsByIssuer(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test JWT key: %v", err)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		document := map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"kid": "quota-key",
				"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.PublicKey.E)).Bytes()),
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(document)
	}))
	defer jwks.Close()

	const (
		issuerA = "https://issuer-a.example"
		issuerB = "https://issuer-b.example"
		subject = "shared-account-id"
	)
	makeJWT := func(issuer string) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": issuer,
			"aud": "quota-api",
			"sub": subject,
			"iat": time.Now().Add(-time.Minute).Unix(),
			"exp": time.Now().Add(time.Hour).Unix(),
		})
		token.Header["kid"] = "quota-key"
		signed, err := token.SignedString(key)
		if err != nil {
			t.Fatalf("sign test JWT: %v", err)
		}
		return signed
	}

	cfg := &config.Config{QuotaPolicies: map[string]config.QuotaPolicyConfig{
		"shared": {
			Mode:       "enforce",
			Rate:       3,
			Burst:      3,
			MaxCallers: 16,
			Classes: map[string]config.QuotaClassConfig{
				"public": quotaServingClass(1, 1, 1, 1),
				"account": {
					Shared:    config.QuotaLimitConfig{Rate: 2, Burst: 2},
					PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
					Principals: []config.QuotaPrincipalMatch{
						{Method: "jwt", Domain: "accounts", Issuer: issuerA, Subject: subject},
						{Method: "jwt", Domain: "accounts", Issuer: issuerB, Subject: subject},
					},
				},
			},
		},
	}}
	runtime := newQuotaRuntime()
	candidate, err := runtime.prepare(cfg, &Generation{})
	if err != nil {
		t.Fatalf("prepare quota policy: %v", err)
	}
	binding := candidate.Binding("shared")
	candidate.Commit()

	authenticator := func(issuer string) *auth.Authenticator {
		t.Helper()
		instance, err := auth.New(context.Background(), config.AuthConfig{
			JWT: &config.JWTAuthConfig{
				JWKSURL:    jwks.URL,
				Issuer:     issuer,
				Audience:   "quota-api",
				Algorithms: []string{"RS256"},
			},
		}, auth.Options{
			HTTPClient: jwks.Client(),
			Principal:  &auth.PrincipalOptions{Domain: "accounts"},
		})
		if err != nil {
			t.Fatalf("create %s authenticator: %v", issuer, err)
		}
		return instance
	}
	action := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux := http.NewServeMux()
	mux.Handle("/issuer-a", authenticator(issuerA).Wrap(quotaMiddleware(binding, "", nil, nil)(action)))
	mux.Handle("/issuer-b", authenticator(issuerB).Wrap(quotaMiddleware(binding, "", nil, nil)(action)))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	if resp := quotaHTTPBearerRequest(t, server, "/issuer-a", makeJWT(issuerA)); resp.StatusCode != http.StatusOK {
		t.Fatalf("first issuer-A session = %d, want 200", resp.StatusCode)
	}
	if resp := quotaHTTPBearerRequest(t, server, "/issuer-b", makeJWT(issuerB)); resp.StatusCode != http.StatusOK {
		t.Fatalf("same subject from issuer B = %d, want independent 200", resp.StatusCode)
	}
	if resp := quotaHTTPBearerRequest(t, server, "/issuer-a", makeJWT(issuerA)); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second issuer-A session = %d, want shared account 429", resp.StatusCode)
	}
}

func TestQuotaServingHTTPPublicationFencesAuthenticatedOldGeneration(t *testing.T) {
	authEntered := make(chan struct{})
	releaseAuth := make(chan struct{})
	var enteredOnce sync.Once
	authService := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Uri") == "/hold" {
			enteredOnce.Do(func() { close(authEntered) })
			select {
			case <-releaseAuth:
			case <-time.After(5 * time.Second):
				http.Error(w, "test auth release timed out", http.StatusGatewayTimeout)
				return
			}
			w.Header().Set("X-Quota-User", "alice")
		} else {
			w.Header().Set("X-Quota-User", "bob")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer authService.Close()

	var originRequests atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	makeConfig := func(subject string) *config.Config {
		cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
		cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
			"shared": {
				Mode:       "enforce",
				Rate:       2,
				Burst:      2,
				MaxCallers: 16,
				Classes: map[string]config.QuotaClassConfig{
					"public": quotaServingClass(1, 1, 1, 1),
					"service": {
						Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
						PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
						Principals: []config.QuotaPrincipalMatch{
							{Method: "forward", Domain: "services", Subject: subject},
						},
					},
				},
			},
		}
		cfg.Servers[0].Locations[0].QuotaPolicy = "shared"
		cfg.Servers[0].Locations[0].Auth = &config.AuthConfig{
			ForwardAuth: &config.ForwardAuthConfig{
				URL:                 authService.URL,
				AuthResponseHeaders: []string{"X-Quota-User"},
			},
			QuotaPrincipal: &config.AuthQuotaPrincipalConfig{
				Domain:         "services",
				ResponseHeader: "X-Quota-User",
			},
		}
		return cfg
	}

	factory, cleanup := minimalFactory(t)
	factory.QuotaRuntime = newQuotaRuntime()
	oldHandlers, _, oldCommit, oldAbort, err := factory.Prepare(context.Background(), makeConfig("alice"))
	if err != nil {
		cleanup()
		t.Fatalf("prepare initial generation: %v", err)
	}
	_, oldRetire := oldCommit()
	oldServer := httptest.NewServer(oldHandlers["127.0.0.1:0"])
	t.Cleanup(func() {
		oldServer.Close()
		if oldRetire != nil {
			oldRetire()
		}
		cleanup()
	})
	oldAbort()

	oldResponse := make(chan *http.Response, 1)
	oldRequestError := make(chan error, 1)
	go func() {
		resp, err := oldServer.Client().Get(oldServer.URL + "/hold")
		if err != nil {
			oldRequestError <- err
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		oldResponse <- resp
	}()
	select {
	case <-authEntered:
	case err := <-oldRequestError:
		t.Fatalf("old authenticated request failed before publication: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not reach forward-auth service")
	}

	newHandlers, _, newCommit, newAbort, err := factory.Prepare(context.Background(), makeConfig("bob"))
	if err != nil {
		close(releaseAuth)
		t.Fatalf("prepare changed authorization: %v", err)
	}
	_, retireOld := newCommit()
	newServer := httptest.NewServer(newHandlers["127.0.0.1:0"])
	t.Cleanup(func() {
		newServer.Close()
		if retireOld != nil {
			retireOld()
		}
	})
	newAbort()
	close(releaseAuth)

	select {
	case resp := <-oldResponse:
		if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
			t.Fatalf("old authenticated generation response = %d Retry-After=%q, want fenced 503/1", resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	case err := <-oldRequestError:
		t.Fatalf("old request failed after publication: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("old request did not finish after publication")
	}
	if got := originRequests.Load(); got != 0 {
		t.Fatalf("stale authenticated request reached origin %d times, want 0", got)
	}
	if resp := quotaHTTPRequest(t, newServer, http.MethodGet, "/", "", "", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh request under new authorization = %d, want 200", resp.StatusCode)
	}
	if resp := quotaHTTPRequest(t, newServer, http.MethodGet, "/", "", "", ""); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("second fresh request = %d, want retained service-class quota 429", resp.StatusCode)
	}
	if got := originRequests.Load(); got != 1 {
		t.Fatalf("origin received %d requests, want only one fresh admission", got)
	}
}

func TestQuotaServingHTTPFencesRevokedBasicFileAndPreservesQuotaState(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	userFile := quotaBasicFile(t, map[string]string{"alice": "old-password"})

	makeConfig := func() *config.Config {
		cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
		cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
			"shared": {
				Mode:       "enforce",
				Rate:       2,
				Burst:      2,
				MaxCallers: 16,
				Classes: map[string]config.QuotaClassConfig{
					"public": quotaServingClass(1, 1, 1, 1),
					"account": {
						Shared:    config.QuotaLimitConfig{Rate: 1, Burst: 1},
						PerCaller: config.QuotaLimitConfig{Rate: 1, Burst: 1},
						Principals: []config.QuotaPrincipalMatch{
							{Method: "basic", Domain: "staff", Subject: "alice"},
						},
					},
				},
			},
		}
		cfg.Servers[0].Locations[0].QuotaPolicy = "shared"
		cfg.Servers[0].Locations[0].Auth = &config.AuthConfig{
			Basic:          &config.BasicAuthConfig{File: userFile},
			QuotaPrincipal: &config.AuthQuotaPrincipalConfig{Domain: "staff"},
		}
		return cfg
	}

	factory, cleanup := minimalFactory(t)
	factory.QuotaRuntime = newQuotaRuntime()
	oldHandlers, _, oldCommit, _, err := factory.Prepare(context.Background(), makeConfig())
	if err != nil {
		cleanup()
		t.Fatalf("prepare initial Basic generation: %v", err)
	}
	_, _ = oldCommit()
	oldServer := httptest.NewServer(oldHandlers["127.0.0.1:0"])
	t.Cleanup(func() {
		oldServer.Close()
		cleanup()
	})
	if resp := quotaHTTPRequest(t, oldServer, http.MethodGet, "/", "", "alice", "old-password"); resp.StatusCode != http.StatusOK {
		t.Fatalf("initial Basic admission = %d, want 200", resp.StatusCode)
	}

	if err := os.WriteFile(userFile, quotaBasicFileContents(t, map[string]string{"alice": "new-password"}), 0o600); err != nil {
		t.Fatalf("replace htpasswd content: %v", err)
	}
	newHandlers, _, newCommit, newAbort, err := factory.Prepare(context.Background(), makeConfig())
	if err != nil {
		t.Fatalf("prepare Basic revocation: %v", err)
	}
	_, retireOld := newCommit()
	newServer := httptest.NewServer(newHandlers["127.0.0.1:0"])
	t.Cleanup(func() {
		newServer.Close()
		if retireOld != nil {
			retireOld()
		}
	})
	newAbort()

	oldCredential := quotaHTTPRequest(t, oldServer, http.MethodGet, "/", "", "alice", "old-password")
	if oldCredential.StatusCode != http.StatusServiceUnavailable || oldCredential.Header.Get("Retry-After") != "1" {
		t.Fatalf("old generation after file revocation = %d Retry-After=%q, want fenced 503/1", oldCredential.StatusCode, oldCredential.Header.Get("Retry-After"))
	}
	if resp := quotaHTTPRequest(t, newServer, http.MethodGet, "/", "", "alice", "old-password"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("new generation accepted revoked credential: status=%d, want 401", resp.StatusCode)
	}
	if resp := quotaHTTPRequest(t, newServer, http.MethodGet, "/", "", "alice", "new-password"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("new credential after reload = %d, want retained depleted caller 429", resp.StatusCode)
	}
}

func TestQuotaServingHTTPWithoutAttachmentPreservesLegacyDisabledBehavior(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := config.ProxyTarget(backend.URL, "127.0.0.1:0")
	cfg.QuotaPolicies = map[string]config.QuotaPolicyConfig{
		"unused": quotaServingPolicy(1, 1, map[string]config.QuotaClassConfig{
			"public": quotaServingClass(1, 1, 1, 1),
		}),
	}
	server := startQuotaHTTPServer(t, cfg)
	for attempt := 0; attempt < 3; attempt++ {
		resp := quotaHTTPRequest(t, server, http.MethodGet, "/", "", "", "")
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Retry-After") != "" {
			t.Fatalf("unattached quota attempt %d = %d Retry-After=%q, want unchanged 200", attempt+1, resp.StatusCode, resp.Header.Get("Retry-After"))
		}
	}
}

func TestProviderFreeQuotaExampleParsesValidatesAndBuilds(t *testing.T) {
	raw, err := os.ReadFile("../../examples/application-quotas/jul.toml")
	if err != nil {
		t.Fatalf("read provider-free quota example: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse provider-free quota example: %v", err)
	}
	userFile := quotaBasicFile(t, map[string]string{
		"alice":       "quota-test-password",
		"integration": "quota-test-password",
	})
	for i := range cfg.Servers[0].Locations {
		location := &cfg.Servers[0].Locations[i]
		if location.Auth != nil && location.Auth.Basic != nil {
			location.Auth.Basic.File = userFile
		}
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate provider-free quota example: %v", err)
	}
	factory, cleanup := minimalFactory(t)
	defer cleanup()
	factory.QuotaRuntime = newQuotaRuntime()
	handlers, _, err := factory.Build(context.Background(), cfg, false)
	if err != nil {
		t.Fatalf("build provider-free quota example: %v", err)
	}
	if handlers[cfg.Servers[0].Listen] == nil {
		t.Fatal("provider-free quota example did not build its HTTP handler")
	}
}
