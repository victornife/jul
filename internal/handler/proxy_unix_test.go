// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
)

func startUnixHTTPBackend(t *testing.T, h http.Handler) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("AF_UNIX runtime fixture is not portable on Windows CI")
	}
	path := filepath.Join(t.TempDir(), "backend.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := &http.Server{Handler: h}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
		<-done
	})
	return path
}

func unixProxy(t *testing.T, name, path string, loc config.LocationConfig) http.Handler {
	t.Helper()
	ups := map[string]config.UpstreamConfig{
		name: {Name: name, Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + path, Weight: 1}}},
	}
	if loc.ProxyPass == "" {
		loc.ProxyPass = "http://" + name
	}
	return newProxy(t, loc, ups)
}

func TestProxyUnixHTTPPreservesHostAndForwardsBody(t *testing.T) {
	var gotHost, gotBody, gotQuery string
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotQuery = r.URL.RawQuery
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, "unix-ok")
	}))
	h := unixProxy(t, "local-app", path, config.LocationConfig{})

	req := httptest.NewRequest(http.MethodPost, "http://edge.example/items?q=one", strings.NewReader("payload"))
	req.Host = "public.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || rec.Body.String() != "unix-ok" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if gotHost != "public.example" {
		t.Fatalf("Host = %q, want incoming Host", gotHost)
	}
	if gotBody != "payload" || gotQuery != "q=one" {
		t.Fatalf("body/query = %q/%q", gotBody, gotQuery)
	}
}

func TestProxyUnixHTTPExplicitHostOverride(t *testing.T) {
	var gotHost string
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	h := unixProxy(t, "local-app", path, config.LocationConfig{Headers: map[string]string{"Host": "app.internal"}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/", nil))
	if gotHost != "app.internal" {
		t.Fatalf("Host = %q, want explicit override", gotHost)
	}
}

func TestProxyUnixHTTPMultipleSocketsAreIsolated(t *testing.T) {
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "B") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	counts := map[string]int{}
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
		counts[rec.Body.String()]++
	}
	if counts["A"] != 10 || counts["B"] != 10 {
		t.Fatalf("socket isolation/round robin counts = %#v, want 10/10", counts)
	}
}

func TestProxyUnixHTTPConcurrentSocketIsolation(t *testing.T) {
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "B") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	var mu sync.Mutex
	counts := map[string]int{}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
			mu.Lock()
			counts[rec.Body.String()]++
			mu.Unlock()
		}()
	}
	wg.Wait()
	if counts["A"]+counts["B"] != 40 || counts["A"] == 0 || counts["B"] == 0 {
		t.Fatalf("concurrent socket results = %#v", counts)
	}
}

func TestProxyUnixHTTPMixedTCPRetry(t *testing.T) {
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "unix-live") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", MaxFails: 1, Servers: []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}, {Address: "unix:" + path, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "unix-live" {
		t.Fatalf("mixed retry = %d %q", rec.Code, rec.Body.String())
	}
}

func TestProxyUnixHTTPRejectsDirectSyntax(t *testing.T) {
	_, _, _, err := resolvePool(context.Background(), config.LocationConfig{ProxyPass: "http://unix:/tmp/app.sock"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "[[upstreams]]") {
		t.Fatalf("direct unix proxy_pass error = %v", err)
	}
}

func TestProxyUnixHTTPRejectsHTTPSBeforeTraffic(t *testing.T) {
	ups := map[string]config.UpstreamConfig{
		"local-app": {Name: "local-app", Servers: []config.UpstreamServer{{Address: "unix:/tmp/not-required-to-exist.sock", Weight: 1}}},
	}
	_, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "https://local-app"}, ups, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "plaintext http only") {
		t.Fatalf("https unix error = %v", err)
	}
}

func TestProxyUnixHTTPGatewayErrorDoesNotExposePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret-sentinel.sock")
	h := unixProxy(t, "local-app", path, config.LocationConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code < 500 {
		t.Fatalf("status = %d, want gateway failure", rec.Code)
	}
	if strings.Contains(rec.Body.String(), path) || strings.Contains(rec.Body.String(), "secret-sentinel") {
		t.Fatalf("client error leaked unix path: %q", rec.Body.String())
	}
}
