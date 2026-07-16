// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubSource lets a test swap the config returned by Load and the validation
// outcome to exercise the reload pipeline.
type stubSource struct {
	mu      sync.Mutex
	cfg     *config.Config
	loadErr error
}

func (s *stubSource) set(cfg *config.Config, loadErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.loadErr = cfg, loadErr
}

func (s *stubSource) Load() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.loadErr
}

func (s *stubSource) Name() string { return "stub" }

func cfgWith(addr string) *config.Config {
	return &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{{
			Listen:    addr,
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
}

func bodyHandlerFactory(tag *atomic.Pointer[string]) HandlerFactory {
	return func(c *config.Config) (map[string]http.Handler, func(), error) {
		current := *tag.Load()
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, current)
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		return m, nil, nil
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// testTransport disables keep-alives so no client-side connection is pooled
// after a request completes. A pooled keep-alive connection would keep a
// net/http persistConn readLoop goroutine parked in an I/O wait until an idle
// timeout elapsed, which under parallel load leaked goroutines and could hang
// the package's goroutine-leak checks (see Finding CQ-1).
var testTransport = &http.Transport{DisableKeepAlives: true}

// fetch returns the body served at url, or ("", err) on failure.
func fetch(url string) (string, error) {
	client := &http.Client{Transport: testTransport}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// reachable reports whether url responds to a GET.
func reachable(url string) bool {
	client := http.Client{Transport: testTransport, Timeout: 200 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// waitForServe blocks until url serves want, failing the test on timeout.
func waitForServe(t *testing.T, url, want string) {
	t.Helper()
	if !eventually(t, url, want) {
		t.Fatalf("server never served %q at %s", want, url)
	}
}

// eventually polls url until it serves want or a timeout elapses.
// Each attempt uses a short client timeout so an in-flight request that
// happens to reach the old (blocking) generation does not hang the poll
// loop forever (see Finding CQ-1).
func eventually(t *testing.T, url, want string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	client := &http.Client{Transport: testTransport, Timeout: 200 * time.Millisecond}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if string(b) == want {
				return true
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	return false
}

func TestReloadSwapsHandler(t *testing.T) {
	addr := freePort(t)
	tag := &atomic.Pointer[string]{}
	v1 := "v1"
	tag.Store(&v1)

	src := &stubSource{}
	src.set(cfgWith(addr), nil)

	srv := New(cfgWith(addr), quietLogger(), bodyHandlerFactory(tag), src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload) }()
	waitForServe(t, "http://"+addr+"/", "v1")

	// Change what the factory produces, then trigger reload.
	v2 := "v2"
	tag.Store(&v2)
	src.set(cfgWith(addr), nil)
	reload <- struct{}{}

	if !eventually(t, "http://"+addr+"/", "v2") {
		t.Fatal("handler did not swap to v2 after reload")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// dialable reports whether a TCP connection to addr can be established. Unlike
// reachable it does not issue a request, so it returns true even while the only
// handler is blocking a request mid-flight.
func dialable(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// waitDialable blocks until addr accepts TCP connections, failing on timeout.
func waitDialable(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if dialable(addr) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server never listened on %s", addr)
}

// TestReloadDrainsBeforeRetiringClosers asserts the generational drain contract:
// a request in flight on the old generation when a reload runs keeps that
// generation's resources alive — the factory's retire callback for the
// superseded generation must not run until the in-flight request finishes —
// while new requests immediately observe the new generation. Run with -race to
// also catch any handler-map data race during the swap.
func TestReloadDrainsBeforeRetiringClosers(t *testing.T) {
	addr := freePort(t)

	release := make(chan struct{}) // closed to let the gen-1 request finish
	entered := make(chan struct{}) // closed when the gen-1 request is in flight
	retired := make(chan struct{}) // closed when gen-1's retire callback runs
	var enterOnce, retireOnce sync.Once
	var builds atomic.Int32

	factory := func(c *config.Config) (map[string]http.Handler, func(), error) {
		n := builds.Add(1)
		var h http.Handler
		switch n {
		case 1:
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// /ready is a non-blocking probe path so the test can wait for the
				// server to be HTTP-ready without consuming the blocking slot.
				if r.URL.Path == "/ready" {
					_, _ = io.WriteString(w, "ready")
					return
				}
				enterOnce.Do(func() { close(entered) })
				<-release
				_, _ = io.WriteString(w, "v1")
			})
		default:
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "v2")
			})
		}
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		// The retire callback a build returns closes the resources of the
		// generation it supersedes. Only the reload (n == 2) has a predecessor to
		// retire; record when the server actually runs it.
		var retire func()
		if n == 2 {
			retire = func() { retireOnce.Do(func() { close(retired) }) }
		}
		return m, retire, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload) }()

	// Wait until the server is actually serving HTTP responses, not just TCP
	// connections. Under the race detector the HTTP stack initialises slowly, so
	// a bare TCP-dial check is not sufficient. We probe /ready, which responds
	// immediately, rather than / which blocks in the gen-1 handler.
	waitForServe(t, "http://"+addr+"/ready", "ready")

	// Start a request that blocks inside the gen-1 handler, then wait until it
	// is actually executing so the reload genuinely races an in-flight request.
	reqBody := make(chan string, 1)
	go func() {
		body, _ := fetch("http://" + addr + "/")
		reqBody <- body
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("gen-1 request never entered the handler")
	}

	// Trigger the reload while the gen-1 request is in flight.
	src.set(cfgWith(addr), nil)
	reload <- struct{}{}

	// New requests must immediately observe the new generation.
	if !eventually(t, "http://"+addr+"/", "v2") {
		t.Fatal("new requests should use v2 after reload")
	}

	// The superseded generation must NOT be retired while its request is in
	// flight: closing gen-1's resources now would tear them out from under the
	// blocked request.
	select {
	case <-retired:
		t.Fatal("retire ran before the in-flight gen-1 request drained")
	case <-time.After(300 * time.Millisecond):
	}

	// Let the in-flight request finish; it must still succeed on gen-1.
	close(release)
	select {
	case body := <-reqBody:
		if body != "v1" {
			t.Fatalf("in-flight request body = %q, want v1", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight gen-1 request never completed")
	}

	// Once drained, the generation's resources are retired.
	select {
	case <-retired:
	case <-time.After(2 * time.Second):
		t.Fatal("gen-1 was never retired after its request drained")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestReloadNoGoroutineLeak drives many reloads through the generational retire
// path and asserts the server's goroutine count does not grow unbounded: each
// retired generation's resources are closed and any drain goroutine it spawned
// exits, so steady-state reloads must not accumulate goroutines.
func TestReloadNoGoroutineLeak(t *testing.T) {
	addr := freePort(t)

	factory := func(c *config.Config) (map[string]http.Handler, func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		// Non-nil retire so every reload exercises the retire path.
		return m, func() {}, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload) }()
	waitForServe(t, "http://"+addr+"/", "ok")

	// A no-keep-alive client so each request's server-side connection goroutine
	// exits promptly and does not inflate the goroutine sample.
	client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
	get := func() {
		resp, err := client.Get("http://" + addr + "/")
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		_, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}

	settle := func() int {
		runtime.GC()
		runtime.GC()
		return runtime.NumGoroutine()
	}

	get()
	base := settle()

	const reloads = 50
	for i := 0; i < reloads; i++ {
		src.set(cfgWith(addr), nil)
		reload <- struct{}{}
		get()
	}

	// Give retire goroutines time to exit, then sample. Retry to absorb the brief
	// window where a just-spawned drain goroutine has not yet returned.
	deadline := time.Now().Add(3 * time.Second)
	end := settle()
	for time.Now().Before(deadline) && end > base+8 {
		time.Sleep(50 * time.Millisecond)
		end = settle()
	}
	if end > base+8 {
		t.Errorf("goroutine leak after %d reloads: %d -> %d", reloads, base, end)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestReloadRejectsInvalidConfig(t *testing.T) {
	addr := freePort(t)
	tag := &atomic.Pointer[string]{}
	v1 := "v1"
	tag.Store(&v1)

	src := &stubSource{}
	src.set(cfgWith(addr), nil)

	validateErr := atomic.Bool{}
	validate := func(*config.Config) error {
		if validateErr.Load() {
			return fmt.Errorf("invalid config")
		}
		return nil
	}
	srv := New(cfgWith(addr), quietLogger(), bodyHandlerFactory(tag), src, validate)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan struct{}, 1)
	go func() { _ = srv.Run(ctx, reload) }()
	waitForServe(t, "http://"+addr+"/", "v1")

	// Make the next config invalid and bump the factory output.
	v2 := "v2"
	tag.Store(&v2)
	validateErr.Store(true)
	reload <- struct{}{}
	time.Sleep(200 * time.Millisecond)

	// Server must keep serving the old handler (v1) after a rejected reload.
	if !eventually(t, "http://"+addr+"/", "v1") {
		t.Fatal("server should keep old config after rejected reload")
	}
}

func TestReloadAddsAndRemovesListener(t *testing.T) {
	addr1 := freePort(t)
	addr2 := freePort(t)
	tag := &atomic.Pointer[string]{}
	v := "ok"
	tag.Store(&v)

	twoAddr := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{
			{Listen: addr1, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
			{Listen: addr2, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
		},
	}
	oneAddr := cfgWith(addr1)

	src := &stubSource{}
	srv := New(oneAddr, quietLogger(), bodyHandlerFactory(tag), src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan struct{}, 1)
	go func() { _ = srv.Run(ctx, reload) }()
	waitForServe(t, "http://"+addr1+"/", "ok")

	// Reload to a config that adds addr2.
	src.set(twoAddr, nil)
	reload <- struct{}{}
	if !eventually(t, "http://"+addr2+"/", "ok") {
		t.Fatal("new listener addr2 not serving after reload")
	}

	// Reload back to one address; addr2 should stop accepting.
	src.set(oneAddr, nil)
	reload <- struct{}{}
	time.Sleep(300 * time.Millisecond)
	if reachable("http://" + addr2 + "/") {
		t.Fatal("addr2 should be removed after reload")
	}
	if !eventually(t, "http://"+addr1+"/", "ok") {
		t.Fatal("addr1 should still serve")
	}
}

// TestPreflightListeners pins the apply-time listener probe: an apply that adds
// an unbindable address fails fast, while unchanged addresses (still held by the
// running server) and removals are never probed — closing the gap where
// doReload binds new listeners best-effort and only logs a bind failure.
func TestPreflightListeners(t *testing.T) {
	servers := func(addrs ...string) []config.ServerConfig {
		out := make([]config.ServerConfig, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, config.ServerConfig{Listen: a})
		}
		return out
	}

	t.Run("adding a bindable address passes", func(t *testing.T) {
		a, b := freePort(t), freePort(t)
		if err := PreflightListeners(servers(a), servers(a, b)); err != nil {
			t.Fatalf("a free new address should pass: %v", err)
		}
	})

	t.Run("unchanged held address is not probed", func(t *testing.T) {
		// Bind and HOLD an address, then keep it in both old and next. Because it
		// is not new, it must be skipped — probing it would false-fail with
		// "address already in use".
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		held := ln.Addr().String()
		if err := PreflightListeners(servers(held), servers(held)); err != nil {
			t.Fatalf("an unchanged held address must not be probed: %v", err)
		}
	})

	t.Run("new address already in use fails", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		busy := ln.Addr().String()
		// busy is NEW relative to old, so it is probed and the bind fails.
		if err := PreflightListeners(servers(freePort(t)), servers(busy)); err == nil {
			t.Fatal("adding an in-use address should fail preflight")
		}
	})

	t.Run("invalid address fails", func(t *testing.T) {
		if err := PreflightListeners(nil, servers("127.0.0.1:999999")); err == nil {
			t.Fatal("an invalid port should fail preflight")
		}
	})

	t.Run("removing an address passes", func(t *testing.T) {
		a, b := freePort(t), freePort(t)
		if err := PreflightListeners(servers(a, b), servers(a)); err != nil {
			t.Fatalf("removing an address introduces nothing to probe: %v", err)
		}
	})
}

// TestDoReloadBlocksOnRestartRequired verifies that a direct reload (SIGHUP /
// file-watch) containing a startup-bound field change is blocked: the old config
// remains authoritative, last reload is OK=false, and the reason contains
// "restart_required".
func TestDoReloadBlocksOnRestartRequired(t *testing.T) {
	addr := freePort(t)
	base := cfgWith(addr)

	// Build a new config that enables the cache — a startup-bound change that
	// Preflight.Apply would reject on the admin write path.
	withCache := cfgWith(addr)
	withCache.Cache = config.CacheConfig{Enabled: true, MemoryMaxSize: config.Size(64 << 20)}

	src := &stubSource{}
	src.set(base, nil)

	tag := &atomic.Pointer[string]{}
	initial := "v1"
	tag.Store(&initial)
	srv := New(base, quietLogger(), bodyHandlerFactory(tag), src,
		func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload) }()
	waitDialable(t, addr)

	// Push a restart-required change via the direct reload path.
	src.set(withCache, nil)
	reload <- struct{}{}

	// Wait for the reload to be processed.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if li := srv.LastReload(); li != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	li := srv.LastReload()
	if li == nil {
		t.Fatal("LastReload is nil after reload attempt")
	}
	if li.OK {
		t.Error("LastReload.OK = true; want false for a restart-required change")
	}
	if !strings.Contains(li.Error, "restart_required") {
		t.Errorf("LastReload.Error = %q; want it to contain 'restart_required'", li.Error)
	}

	cancel()
	<-done
}

// TestDoReloadDegradedOnBindFailure verifies that when a direct reload adds a
// new listen address that is already occupied, the reload is recorded as
// degraded (OK=false with a bind error), while existing listeners continue to
// serve the new handler generation.
func TestDoReloadDegradedOnBindFailure(t *testing.T) {
	addr := freePort(t)
	tag := &atomic.Pointer[string]{}
	v1 := "v1"
	tag.Store(&v1)

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), quietLogger(), bodyHandlerFactory(tag), src,
		func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload) }()
	waitForServe(t, "http://"+addr+"/", "v1")

	// Occupy a second port so the reload's bind will fail.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	busyAddr := busy.Addr().String()

	// New config adds the busy port — and changes the handler body.
	v2 := "v2"
	tag.Store(&v2)
	newCfg := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{
			{Listen: addr, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
			{Listen: busyAddr, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
		},
	}
	src.set(newCfg, nil)
	reload <- struct{}{}

	// Wait for the reload result.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if li := srv.LastReload(); li != nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	li := srv.LastReload()
	if li == nil {
		t.Fatal("LastReload is nil after reload attempt")
	}
	if li.OK {
		t.Error("LastReload.OK = true; want false when a new listener failed to bind")
	}
	if !strings.Contains(li.Error, "degraded") {
		t.Errorf("LastReload.Error = %q; want it to contain 'degraded'", li.Error)
	}

	// The existing listener must still serve (with the new handler — the swap
	// completes for the addresses that did bind).
	if !eventually(t, "http://"+addr+"/", "v2") {
		t.Error("existing listener should serve new handler even after degraded reload")
	}

	cancel()
	<-done
}
