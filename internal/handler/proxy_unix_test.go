// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"

	"golang.org/x/net/websocket"
)

func startUnixHTTPBackend(t *testing.T, h http.Handler) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("AF_UNIX runtime fixture is not portable on Windows CI")
	}
	dir, err := os.MkdirTemp("/tmp", "jul407-")
	if err != nil {
		t.Fatalf("create short Unix fixture dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "backend.sock")
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

func TestUnixHTTPPoolKeyIsolation(t *testing.T) {
	a := upstream.BackendIdentity{Scheme: "http", Network: upstream.NetworkUnix, Address: "/tmp/a.sock"}
	b := upstream.BackendIdentity{Scheme: "http", Network: upstream.NetworkUnix, Address: "/tmp/b.sock"}
	ka, kb := unixHTTPPoolKey(a), unixHTTPPoolKey(b)
	if ka == kb {
		t.Fatalf("distinct sockets share pool key %q", ka)
	}
	if ka != unixHTTPPoolKey(a) {
		t.Fatal("pool key is not stable")
	}
	if strings.Contains(ka, a.Address) || strings.Contains(kb, b.Address) {
		t.Fatalf("pool key leaks filesystem path: %q / %q", ka, kb)
	}
}

func TestProxyUnixHTTPRetryUnixToUnix(t *testing.T) {
	live := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "unix-b")
	}))
	missing := filepath.Join(t.TempDir(), "missing.sock")
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "round_robin", MaxFails: 1, Servers: []config.UpstreamServer{{Address: "unix:" + missing, Weight: 1}, {Address: "unix:" + live, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "unix-b" {
		t.Fatalf("Unix->Unix retry = %d %q", rec.Code, rec.Body.String())
	}
}

func TestProxyUnixHTTPServerSentEventsStreaming(t *testing.T) {
	releaseSecond := make(chan struct{})
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: first\n\n")
		fl.Flush()
		<-releaseSecond
		_, _ = io.WriteString(w, "data: second\n\n")
		fl.Flush()
	}))
	front := httptest.NewServer(unixProxy(t, "events", path, config.LocationConfig{}))
	defer front.Close()
	resp, err := http.Get(front.URL + "/events")
	if err != nil {
		t.Fatalf("GET SSE through Unix proxy: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	if got := readSSEDataWithin(t, br, 5*time.Second); got != "first" {
		t.Fatalf("first SSE event = %q", got)
	}
	close(releaseSecond)
	if got := readSSEDataWithin(t, br, 5*time.Second); got != "second" {
		t.Fatalf("second SSE event = %q", got)
	}
}

func TestProxyUnixHTTPWebSocketPassthrough(t *testing.T) {
	path := startUnixHTTPBackend(t, websocket.Handler(func(ws *websocket.Conn) {
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			return
		}
		_ = websocket.Message.Send(ws, msg)
	}))
	front := httptest.NewServer(unixProxy(t, "ws", path, config.LocationConfig{}))
	defer front.Close()
	wsURL := "ws" + strings.TrimPrefix(front.URL, "http")
	ws, err := websocket.Dial(wsURL, "", front.URL)
	if err != nil {
		t.Fatalf("WebSocket dial through Unix proxy: %v", err)
	}
	defer ws.Close()
	want := []byte("unix-websocket")
	if err := websocket.Message.Send(ws, want); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := websocket.Message.Receive(ws, &got); err != nil {
		t.Fatalf("receive: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("echo = %q, want %q", got, want)
	}
}
