// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func shortUnixFixturePath(t *testing.T, name string) string {
	t.Helper()
	base := os.TempDir()
	if runtime.GOOS == "darwin" {
		base = "/tmp"
	}
	dir, err := os.MkdirTemp(base, "jul407-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, name)
}

func serveUnixHTTPAt(t *testing.T, path string, h http.Handler, state func(net.Conn, http.ConnState)) func() {
	t.Helper()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix %q: %v", path, err)
	}
	srv := &http.Server{Handler: h, ConnState: state}
	done := make(chan struct{})
	go func() {
		_ = srv.Serve(ln)
		close(done)
	}()
	return func() {
		_ = srv.Close()
		_ = ln.Close()
		<-done
	}
}

func TestProxyUnixHTTPAdmissionAndPendingCancellation(t *testing.T) {
	release := make(chan struct{})
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	h, adm := accountingProxy(t, "unix:"+path, &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: 1,
	}, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	}()
	waitActive(t, adm, 1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil).WithContext(ctx))
	}()
	waitFor(t, func() bool { return adm.Pending() == 1 })
	cancel()
	<-done
	waitFor(t, func() bool { return adm.Pending() == 0 })

	close(release)
	wg.Wait()
	waitFor(t, func() bool { return adm.Active() == 0 })
}

func TestProxyUnixHTTPCircuitRecoversWhenSocketAppears(t *testing.T) {
	path := shortUnixFixturePath(t, "recover.sock")
	ups := map[string]config.UpstreamConfig{
		"api": {
			Name:     "api",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: "unix:" + path, Weight: 1}},
			Resilience: &config.ResilienceConfig{
				MaxFails:    1,
				FailTimeout: config.Duration(50 * time.Millisecond),
			},
		},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "http://api"}, ups, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	ph := h.(*proxyHandler)
	defer ph.Close()

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if first.Code < 500 {
		t.Fatalf("first request status = %d, want upstream failure", first.Code)
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if second.Code != http.StatusServiceUnavailable && second.Code != http.StatusBadGateway {
		t.Fatalf("open-circuit status = %d", second.Code)
	}

	time.Sleep(80 * time.Millisecond)
	stop := serveUnixHTTPAt(t, path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "recovered")
	}), nil)
	defer stop()

	deadline := time.Now().Add(3 * time.Second)
	for {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
		if rec.Code == http.StatusOK && rec.Body.String() == "recovered" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Unix backend did not recover, last response = %d %q", rec.Code, rec.Body.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestProxyUnixHTTPMaxConnectionsAreIsolatedPerBackend(t *testing.T) {
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	enteredA := make(chan struct{}, 1)
	enteredB := make(chan struct{}, 1)
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enteredA <- struct{}{}
		<-releaseA
		w.WriteHeader(http.StatusNoContent)
	}))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enteredB <- struct{}{}
		<-releaseB
		w.WriteHeader(http.StatusNoContent)
	}))

	ups := map[string]config.UpstreamConfig{
		"pool": {
			Name:       "pool",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 1}},
			Resilience: &config.ResilienceConfig{MaxConnectionsPerBackend: 1, MaxActiveRequests: 4},
		},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/a", nil))
	}()
	select {
	case <-enteredA:
	case <-time.After(3 * time.Second):
		t.Fatal("first Unix backend was not entered")
	}
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/b", nil))
	}()
	select {
	case <-enteredB:
		// Two distinct Unix backends can each use one physical connection. If
		// their synthetic pool keys alias, the second request blocks here.
	case <-time.After(3 * time.Second):
		close(releaseA)
		t.Fatal("second Unix backend was blocked by another backend's MaxConnsPerHost key")
	}
	close(releaseA)
	close(releaseB)
	wg.Wait()
}

func TestProxyUnixHTTPWeightedRoundRobin(t *testing.T) {
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "B") }))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "weighted_round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 3}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)
	counts := map[string]int{}
	for i := 0; i < 40; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
		counts[rec.Body.String()]++
	}
	if counts["A"] != 10 || counts["B"] != 30 {
		t.Fatalf("weighted counts = %#v, want A=10 B=30", counts)
	}
}

func TestProxyUnixHTTPLeastConnPrefersIdleBackend(t *testing.T) {
	releaseA := make(chan struct{})
	enteredA := make(chan struct{}, 1)
	enteredB := make(chan struct{}, 1)
	a := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enteredA <- struct{}{}
		<-releaseA
		_, _ = io.WriteString(w, "A")
	}))
	b := startUnixHTTPBackend(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		enteredB <- struct{}{}
		_, _ = io.WriteString(w, "B")
	}))
	ups := map[string]config.UpstreamConfig{
		"pool": {Name: "pool", Strategy: "least_conn", Servers: []config.UpstreamServer{{Address: "unix:" + a, Weight: 1}, {Address: "unix:" + b, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/first", nil))
	}()
	select {
	case <-enteredA:
	case <-time.After(3 * time.Second):
		t.Fatal("least_conn first request did not enter backend A")
	}

	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "http://edge/second", nil))
	select {
	case <-enteredB:
	case <-time.After(3 * time.Second):
		close(releaseA)
		t.Fatal("least_conn did not select idle Unix backend B")
	}
	if second.Body.String() != "B" {
		close(releaseA)
		t.Fatalf("second response = %q, want B", second.Body.String())
	}
	close(releaseA)
	<-firstDone
}

func TestProxyUnixHTTPKeepAliveAndGenerationRetirement(t *testing.T) {
	path := shortUnixFixturePath(t, "keepalive.sock")
	var opened atomic.Int64
	var closed atomic.Int64
	stop := serveUnixHTTPAt(t, path, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	})
	defer stop()

	ups := map[string]config.UpstreamConfig{
		"api": {Name: "api", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: "unix:" + path, Weight: 1}}},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "http://api"}, ups, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	ph := h.(*proxyHandler)

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
	}
	if got := opened.Load(); got != 1 {
		_ = ph.Close()
		t.Fatalf("backend connections after five sequential requests = %d, want one kept-alive connection", got)
	}

	if err := ph.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	waitFor(t, func() bool { return closed.Load() == opened.Load() })
}

func TestProxyUnixHTTPRepeatedGenerationSwitchRetiresConnections(t *testing.T) {
	pathA := shortUnixFixturePath(t, "a.sock")
	pathB := shortUnixFixturePath(t, "b.sock")
	var opened atomic.Int64
	var closed atomic.Int64
	state := func(_ net.Conn, st http.ConnState) {
		switch st {
		case http.StateNew:
			opened.Add(1)
		case http.StateClosed:
			closed.Add(1)
		}
	}
	stopA := serveUnixHTTPAt(t, pathA, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "A") }), state)
	defer stopA()
	stopB := serveUnixHTTPAt(t, pathB, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "B") }), state)
	defer stopB()
	tcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "T") }))
	defer tcp.Close()

	type generation struct {
		address string
		want    string
	}
	seq := []generation{{"unix:" + pathA, "A"}, {"unix:" + pathB, "B"}, {tcp.Listener.Addr().String(), "T"}}
	for i := 0; i < 30; i++ {
		g := seq[i%len(seq)]
		ups := map[string]config.UpstreamConfig{
			"api": {Name: "api", Strategy: "round_robin", Servers: []config.UpstreamServer{{Address: g.address, Weight: 1}}},
		}
		h, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: "http://api"}, ups, nil, nil, nil)
		if err != nil {
			t.Fatalf("generation %d NewProxy: %v", i, err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
		if rec.Code != http.StatusOK || rec.Body.String() != g.want {
			_ = h.(*proxyHandler).Close()
			t.Fatalf("generation %d response = %d %q, want %q", i, rec.Code, rec.Body.String(), g.want)
		}
		if err := h.(*proxyHandler).Close(); err != nil {
			t.Fatalf("generation %d Close: %v", i, err)
		}
	}
	waitFor(t, func() bool { return closed.Load() == opened.Load() })
}
