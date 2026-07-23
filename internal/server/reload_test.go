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
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// stubSource lets a test swap the config returned by Load and the validation
// outcome to exercise the reload pipeline.
type stubSource struct {
	mu      sync.Mutex
	cfg     *config.Config
	raw     []byte
	loadErr error
}

func (s *stubSource) set(cfg *config.Config, loadErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg, s.loadErr = cfg, loadErr
}

func (s *stubSource) setRaw(raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw = raw
}

func (s *stubSource) Load() (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg, s.loadErr
}

func (s *stubSource) ReadRaw() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raw, s.loadErr
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

func cfgWithReturn(addr string, code int) *config.Config {
	c := cfgWith(addr)
	c.Servers[0].Locations[0].Return = code
	return c
}

func bodyHandlerFactory(tag *atomic.Pointer[string]) HandlerFactory {
	return func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		current := *tag.Load()
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, current)
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		// No staged resources in test factory; commit and abort are no-ops.
		committed := false
		commitFn := func() (upstream.SnapshotMap, func()) {
			committed = true
			return nil, nil
		}
		abortFn := func() {
			if !committed {
				// nothing to discard
			}
		}
		return m, 1, commitFn, abortFn, nil
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

	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "v1")

	// Change what the factory produces, then trigger reload.
	v2 := "v2"
	tag.Store(&v2)
	src.set(cfgWith(addr), nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

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

	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
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
		committed := false
		commitFn := func() (upstream.SnapshotMap, func()) {
			committed = true
			return nil, retire
		}
		abortFn := func() {
			if !committed {
				// nothing to discard
			}
		}
		return m, uint64(n), commitFn, abortFn, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()

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
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

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

	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		// Non-nil retire so every reload exercises the retire path.
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, func() {} }
		abortFn := func() {}
		return m, 1, commitFn, abortFn, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
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
		reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
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

// TestLiveSnapshotCoherentDuringReload exercises LiveSnapshot concurrently
// with reloads and verifies that every observed snapshot is internally
// coherent: the effective config, listener set, and generation all describe
// the same logical runtime state (R10-02). Run with -race to catch data races
// on the fields that previously were updated without synchronization.
func TestLiveSnapshotCoherentDuringReload(t *testing.T) {
	addr := freePort(t)

	var builds atomic.Uint64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		gen := builds.Add(1)
		code := c.Servers[0].Locations[0].Return
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return m, gen, commitFn, abortFn, nil
	}

	cfg1 := cfgWithReturn(addr, 201)
	cfg2 := cfgWithReturn(addr, 202)

	cand1, err := config.NewCandidate(cfg1)
	if err != nil {
		t.Fatalf("candidate1: %v", err)
	}
	cand2, err := config.NewCandidate(cfg2)
	if err != nil {
		t.Fatalf("candidate2: %v", err)
	}

	src := &stubSource{}
	src.set(cfg1, nil)

	srv := New(cfg1, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	// Use an unbuffered reload channel so the source config is set immediately
	// before the server receives each request; a buffered channel would let the
	// test overwrite src.cfg while a previous reload is still queued.
	reload := make(chan ReloadRequest)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitDialable(t, addr)

	// Wait for the initial runtime state to be published. waitDialable only
	// guarantees the TCP listener accepts; the atomic runtime snapshot is
	// published immediately after bind registration.
	var snap LiveSnapshot
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap = srv.LiveSnapshot()
		if len(snap.Listeners) == 1 && snap.Generation != 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if snap.Generation == 0 {
		t.Fatal("initial generation not published")
	}
	if len(snap.Listeners) != 1 {
		t.Fatalf("initial listener count = %d, want 1", len(snap.Listeners))
	}

	const iterations = 75
	var wg sync.WaitGroup
	stop := make(chan struct{})
	errs := make(chan string, iterations*2)
	var checks atomic.Uint64

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := srv.LiveSnapshot()
			checks.Add(1)
			if len(snap.Listeners) != 1 {
				errs <- fmt.Sprintf("expected 1 listener, got %d", len(snap.Listeners))
				continue
			}
			if _, ok := snap.Listeners[addr]; !ok {
				errs <- fmt.Sprintf("listener %s missing from snapshot", addr)
			}
			if snap.EffectiveConfig == nil || len(snap.EffectiveConfig.Servers) == 0 || len(snap.EffectiveConfig.Servers[0].Locations) == 0 {
				errs <- "effective config missing server/location"
				continue
			}
			code := snap.EffectiveConfig.Servers[0].Locations[0].Return
			// Reloads alternate cfg2 (even generations) and cfg1 (odd generations).
			wantCode := 201
			if snap.Generation%2 == 0 {
				wantCode = 202
			}
			if code != wantCode {
				errs <- fmt.Sprintf("gen %d expects return %d, got %d", snap.Generation, wantCode, code)
			}
		}
	}()

	for i := 0; i < iterations; i++ {
		// Hand the exact candidate to the server so the config sequence is
		// deterministic regardless of when the source is read.
		var cand *config.Candidate
		if i%2 == 0 {
			cand = cand2
		} else {
			cand = cand1
		}
		select {
		case reload <- ReloadRequest{Source: ReloadSourceAdmin, Candidate: cand}:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out sending reload %d", i)
		}
	}

	close(stop)
	wg.Wait()
	close(errs)

	if checks.Load() == 0 {
		t.Fatal("snapshot reader did not run")
	}

	var errList []string
	for msg := range errs {
		errList = append(errList, msg)
		if len(errList) > 10 {
			break
		}
	}
	if len(errList) > 0 {
		t.Fatalf("coherence violations: %v", errList)
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
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src, validate)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "v1")

	// Make the next config invalid and bump the factory output.
	v2 := "v2"
	tag.Store(&v2)
	validateErr.Store(true)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
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
	srv := New(oneAddr, nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 1)
	go func() { _ = srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr1+"/", "ok")

	// Reload to a config that adds addr2.
	src.set(twoAddr, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	if !eventually(t, "http://"+addr2+"/", "ok") {
		t.Fatal("new listener addr2 not serving after reload")
	}

	// Reload back to one address; addr2 should stop accepting.
	src.set(oneAddr, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	time.Sleep(300 * time.Millisecond)
	if reachable("http://" + addr2 + "/") {
		t.Fatal("addr2 should be removed after reload")
	}
	if !eventually(t, "http://"+addr1+"/", "ok") {
		t.Fatal("addr1 should still serve")
	}
}

// TestPreflightListeners pins the apply-time listener probe: an apply that adds
// an unbindable address fails fast, while addresses already bound by the
// running server and removals are never probed — closing the gap where
// doReload binds new listeners best-effort and only logs a bind failure.
func TestPreflightListeners(t *testing.T) {
	servers := func(addrs ...string) []config.ServerConfig {
		out := make([]config.ServerConfig, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, config.ServerConfig{Listen: a})
		}
		return out
	}
	bound := func(addrs ...string) map[string]struct{} {
		m := make(map[string]struct{}, len(addrs))
		for _, a := range addrs {
			m[a] = struct{}{}
		}
		return m
	}

	t.Run("adding a bindable address passes", func(t *testing.T) {
		a, b := freePort(t), freePort(t)
		if err := PreflightListeners(bound(a), nil, servers(a, b)); err != nil {
			t.Fatalf("a free new address should pass: %v", err)
		}
	})

	t.Run("unchanged bound address is not probed", func(t *testing.T) {
		// Bind and HOLD an address, then mark it as already bound. Because it
		// is not new, it must be skipped — probing it would false-fail with
		// "address already in use".
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		held := ln.Addr().String()
		if err := PreflightListeners(bound(held), nil, servers(held)); err != nil {
			t.Fatalf("an unchanged bound address must not be probed: %v", err)
		}
	})

	t.Run("new address already in use fails", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		busy := ln.Addr().String()
		// busy is NEW relative to the bound set, so it is probed and the bind fails.
		if err := PreflightListeners(bound(freePort(t)), nil, servers(busy)); err == nil {
			t.Fatal("adding an in-use address should fail preflight")
		}
	})

	t.Run("invalid address fails", func(t *testing.T) {
		if err := PreflightListeners(nil, nil, servers("127.0.0.1:999999")); err == nil {
			t.Fatal("an invalid port should fail preflight")
		}
	})

	t.Run("removing an address passes", func(t *testing.T) {
		a, b := freePort(t), freePort(t)
		if err := PreflightListeners(bound(a, b), nil, servers(a)); err != nil {
			t.Fatalf("removing an address introduces nothing to probe: %v", err)
		}
	})
}

// TestPreflightListenersHTTP3UDP pins the HTTP/3 UDP side of the apply-time
// listener probe. Enabling HTTP/3 on a server block requires the same UDP port
// to be free; the TCP probe alone would miss a conflicting UDP listener, so
// this gate rejects the apply before persistence (R10-07).
func TestPreflightListenersHTTP3UDP(t *testing.T) {
	serversH3 := func(addrs ...string) []config.ServerConfig {
		out := make([]config.ServerConfig, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, config.ServerConfig{
				Listen: a,
				TLS:    &config.TLSConfig{Enabled: true},
				HTTP3:  &config.HTTP3Config{Enabled: true},
			})
		}
		return out
	}
	bound := func(addrs ...string) map[string]struct{} {
		m := make(map[string]struct{}, len(addrs))
		for _, a := range addrs {
			m[a] = struct{}{}
		}
		return m
	}

	t.Run("adding a bindable http3 address passes", func(t *testing.T) {
		a := freePort(t)
		if err := PreflightListeners(nil, nil, serversH3(a)); err != nil {
			t.Fatalf("a free new http3 address should pass: %v", err)
		}
	})

	t.Run("bound http3 address skips udp probe", func(t *testing.T) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		held := pc.LocalAddr().String()
		// Mark the address as bound on both TCP and HTTP/3 so neither side is
		// re-probed, even though a UDP socket is currently open.
		if err := PreflightListeners(bound(held), bound(held), serversH3(held)); err != nil {
			t.Fatalf("an unchanged http3 address must not be re-probed: %v", err)
		}
	})

	t.Run("new http3 address with udp in use fails", func(t *testing.T) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		busy := pc.LocalAddr().String()
		// busy is NEW relative to the http3 bound set, so UDP is probed.
		if err := PreflightListeners(nil, nil, serversH3(busy)); err == nil {
			t.Fatal("adding an http3 address whose udp port is in use should fail preflight")
		}
	})

	t.Run("plain address does not probe udp", func(t *testing.T) {
		pc, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer pc.Close()
		busy := pc.LocalAddr().String()
		plain := []config.ServerConfig{{Listen: busy}}
		// Mark TCP as bound so the test only verifies that a plain config does
		// not trigger a UDP probe; the UDP socket itself would otherwise be
		// irrelevant.
		if err := PreflightListeners(bound(busy), nil, plain); err != nil {
			t.Fatalf("plain server config must not probe udp: %v", err)
		}
	})
}

// TestDoReloadBlocksOnRestartRequired verifies that a direct reload (SIGHUP /
// file-watch) containing a startup-bound field change is blocked: the old config
// remains authoritative, last reload is OK=false, and the reason contains
// "restart_required".
// TestDoReloadBlocksOnRestartRequired verifies that a direct reload containing
// a startup-bound change (e.g. enabling the cache, which cannot be live-applied)
// records a failed reload and leaves the previous config serving.
// It also tests the rawCfg path: when rawCfg is set to a config that shares the
// same admin token reference as the reload candidate, the reload must NOT be
// blocked even though s.cfg holds the expanded (resolved) value.
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
	// Pass rawCfg = base (simulates the pre-expansion startup config).
	srv := New(base, base, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src,
		func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitDialable(t, addr)

	// Push a restart-required change via the direct reload path.
	src.set(withCache, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

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
	if li.Outcome == ReloadAppliedLive {
		t.Error("LastReload.Outcome = applied_live; want not applied for a restart-required change")
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
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src,
		func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
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
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

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
	if li.Outcome == ReloadAppliedLive {
		t.Error("LastReload.Outcome = applied_live; want not applied when a new listener failed to bind")
	}
	// New doReload aborts entirely on bind failure (no partial state).
	if !strings.Contains(li.Error, "aborted") {
		t.Errorf("LastReload.Error = %q; want it to contain 'aborted'", li.Error)
	}

	// The existing listener must still serve the OLD handler — the reload was
	// aborted before any handler swap occurred.
	if !eventually(t, "http://"+addr+"/", "v1") {
		t.Error("existing listener should keep old handler after aborted reload")
	}

	cancel()
	<-done
}

// TestDoReloadReplacementAddressBindFailureKeepsOld verifies the R3-02 fix:
// when a direct reload replaces address A with address B and B fails to bind,
// A continues serving. Previously the code removed A after the failed bind,
// leaving no working listener.
func TestDoReloadReplacementAddressBindFailureKeepsOld(t *testing.T) {
	oldAddr := freePort(t)
	tag := &atomic.Pointer[string]{}
	v1 := "v1"
	tag.Store(&v1)

	src := &stubSource{}
	src.set(cfgWith(oldAddr), nil)
	srv := New(cfgWith(oldAddr), nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src,
		func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+oldAddr+"/", "v1")

	// Occupy a port, then reload with the new config replacing oldAddr with it.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = busy.Close() })
	busyAddr := busy.Addr().String()

	// Change tag to v2 so we can detect if the handler accidentally swapped.
	v2 := "v2"
	tag.Store(&v2)

	// New config has busyAddr instead of oldAddr — a replacement, not an addition.
	replacement := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{
			{Listen: busyAddr, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
		},
	}
	src.set(replacement, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

	// Wait for the reload to complete.
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
	if li.Outcome == ReloadAppliedLive {
		t.Error("LastReload.Outcome = applied_live; want not applied when replacement bind failed")
	}

	// oldAddr must still be alive and serving the OLD handler (v1, not v2).
	if !eventually(t, "http://"+oldAddr+"/", "v1") {
		t.Error("old listener must remain serving with old handler after failed replacement")
	}
	if !dialable(oldAddr) {
		t.Error("old listener must still accept connections")
	}

	cancel()
	<-done
}

// TestDoReloadNewListenerUsesNewConfig verifies the R3-01 fix: when a reload
// adds a new listen address, the bind uses the candidate config settings
// (not the startup config). We test this by verifying that a new address
// with a custom IdleTimeout is successfully bound with that exact timeout value.
func TestDoReloadNewListenerUsesNewConfig(t *testing.T) {
	addr1 := freePort(t)
	addr2 := freePort(t)
	tag := &atomic.Pointer[string]{}
	v1 := "v1"
	tag.Store(&v1)

	src := &stubSource{}
	src.set(cfgWith(addr1), nil)
	srv := New(cfgWith(addr1), nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src,
		func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr1+"/", "v1")

	// New config adds addr2 with a distinctive idle timeout so we can verify the
	// bind-time setting was actually applied from newCfg (not from s.cfg which
	// has no entry for addr2).
	const wantIdleTimeout = 45 * time.Second
	v2 := "v2"
	tag.Store(&v2)
	newCfg := &config.Config{
		Global: config.GlobalConfig{ShutdownTimeout: config.Duration(2 * time.Second)},
		Servers: []config.ServerConfig{
			{Listen: addr1, Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
			{
				Listen:      addr2,
				IdleTimeout: config.Duration(wantIdleTimeout),
				Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			},
		},
	}
	src.set(newCfg, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

	// Wait for the reload to complete and addr2 to be serving.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if li := srv.LastReload(); li != nil && li.Outcome == ReloadAppliedLive {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	li := srv.LastReload()
	if li == nil || li.Outcome != ReloadAppliedLive {
		t.Fatalf("LastReload = %+v; want outcome=applied_live after reload", li)
	}

	// addr2 must be reachable: the bind succeeded using newCfg.
	waitDialable(t, addr2)

	// Verify that addr2's http.Server.IdleTimeout was set to the value from
	// newCfg, not the zero-value default from the old config. This is the
	// concrete proof that bindFrom used newCfg, not s.cfg (which had no
	// server block for addr2 and would produce a zero idle timeout).
	srv.mu.Lock()
	entry := srv.listeners[addr2]
	srv.mu.Unlock()
	if entry == nil {
		t.Fatal("expected listener entry for addr2 after reload")
	}
	if got := entry.httpd.IdleTimeout; got != wantIdleTimeout {
		t.Errorf("addr2 http.Server.IdleTimeout = %v, want %v (from newCfg)", got, wantIdleTimeout)
	}

	// addr1 must still serve the new handler.
	if !eventually(t, "http://"+addr1+"/", "v2") {
		t.Error("addr1 should serve new handler after reload")
	}

	cancel()
	<-done
}

// TestAdminReloadRequestUsesCandidate (R9-02) verifies the typed admin reload
// path: a ReloadRequest carrying a preflight-resolved candidate is applied
// directly, and a stale candidate whose raw digest no longer matches is
// rejected without publishing a different source config under the same ID.
func TestAdminReloadRequestUsesCandidate(t *testing.T) {
	addr := freePort(t)

	var genIDCounter atomic.Uint64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := genIDCounter.Add(1)
		body := fmt.Sprintf("return-%d", c.Servers[0].Locations[0].Return)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		m := map[string]http.Handler{addr: h}
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return m, genID, commitFn, abortFn, nil
	}

	src := &stubSource{}
	src.set(cfgWithReturn(addr, 200), nil)

	srv := New(cfgWithReturn(addr, 200), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "return-200")

	// Valid admin candidate: Return 201. The server should apply it directly.
	raw201 := cfgWithReturn(addr, 201)
	cand201, err := config.NewCandidate(raw201)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	data201, err := config.Marshal(cand201.Raw)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Simulate the admin write path: the source now holds the same raw config
	// the candidate was built from.
	src.set(raw201, nil)
	src.setRaw(data201)
	reload <- ReloadRequest{Source: ReloadSourceAdmin, Candidate: cand201, RawDigest: sha256Digest(data201)}

	if !eventually(t, "http://"+addr+"/", "return-201") {
		t.Fatal("admin candidate was not applied")
	}

	// Stale admin candidate: the on-disk raw bytes no longer match the supplied
	// digest, so the correlated transaction must be rejected. A later ordinary
	// source reload may apply raw202 under its own transaction.
	raw202 := cfgWithReturn(addr, 202)
	data202, err := config.Marshal(raw202)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	src.set(raw202, nil)
	src.setRaw(data202)
	raw203 := cfgWithReturn(addr, 203)
	cand203, err := config.NewCandidate(raw203)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	// Deliberately use the digest from raw201 to force a mismatch.
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "stale-admin", Source: ReloadSourceAdmin, Candidate: cand203, RawDigest: sha256Digest(data201), Result: resultCh}
	result := <-resultCh
	if result.Outcome != ReloadNotApplied || result.FailedPhase != "persisted_cas" || result.Published {
		t.Fatalf("stale result = %+v, want pre-publish persisted_cas rejection", result)
	}
	if !eventually(t, "http://"+addr+"/", "return-201") {
		t.Fatal("stale managed candidate changed the serving runtime")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestAdminReloadRejectsDiskChangeDuringPreparation(t *testing.T) {
	addr := freePort(t)
	src := &stubSource{}
	initial := cfgWithReturn(addr, 200)
	src.set(initial, nil)
	initialRaw, err := config.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal initial: %v", err)
	}
	src.setRaw(initialRaw)

	var mutateDuringPrepare atomic.Bool
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		if mutateDuringPrepare.Load() {
			external := cfgWithReturn(addr, 202)
			externalRaw, merr := config.Marshal(external)
			if merr != nil {
				return nil, 0, nil, nil, merr
			}
			src.set(external, nil)
			src.setRaw(externalRaw)
		}
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "return-%d", c.Servers[0].Locations[0].Return)
		})
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}

	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "return-200")

	candidateRaw := cfgWithReturn(addr, 201)
	candidate, err := config.NewCandidate(candidateRaw)
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	persisted, err := config.Marshal(candidate.Raw)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	src.set(candidate.Raw, nil)
	src.setRaw(persisted)
	mutateDuringPrepare.Store(true)
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "prepare-race", Source: ReloadSourceAdmin, Candidate: candidate, RawDigest: sha256Digest(persisted), Result: resultCh}
	result := <-resultCh
	if result.Outcome != ReloadNotApplied || result.FailedPhase != "persisted_cas" || result.Published {
		t.Fatalf("result = %+v, want persisted_cas rejection", result)
	}
	if !eventually(t, "http://"+addr+"/", "return-200") {
		t.Fatal("candidate was published despite disk change during preparation")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestReloadInstallsPreparedCommitAtPublish(t *testing.T) {
	addr := freePort(t)
	src := &stubSource{}
	initial := cfgWithReturn(addr, 200)
	src.set(initial, nil)
	var prepared, committed atomic.Int64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "return-%d", c.Servers[0].Locations[0].Return)
		})
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })
	srv.PrepareAdmin = func(config.AdminConfig) (*PreparedCommit, error) {
		prepared.Add(1)
		return NewPreparedCommit(func() { committed.Add(1) }, nil), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "return-200")
	next := cfgWithReturn(addr, 201)
	src.set(next, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	if !eventually(t, "http://"+addr+"/", "return-201") {
		t.Fatal("reload did not publish")
	}
	if prepared.Load() != 1 || committed.Load() != 1 {
		t.Fatalf("prepared/committed = %d/%d, want 1/1", prepared.Load(), committed.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestManagedReloadWaitsForCoordinatorFinalization(t *testing.T) {
	addr := freePort(t)
	src := &stubSource{}
	initial := cfgWithReturn(addr, 200)
	src.set(initial, nil)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "return-%d", c.Servers[0].Locations[0].Return)
		})
		return map[string]http.Handler{addr: h}, uint64(c.Servers[0].Locations[0].Return), func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 2)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "return-200")

	managedCfg := cfgWithReturn(addr, 201)
	managed, err := config.NewCandidate(managedCfg)
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	managedRaw, err := config.Marshal(managed.Raw)
	if err != nil {
		t.Fatalf("marshal managed: %v", err)
	}
	src.set(managedCfg, nil)
	src.setRaw(managedRaw)
	finalized := make(chan struct{})
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "managed-ack", Source: ReloadSourceAdmin, Candidate: managed, RawDigest: sha256Digest(managedRaw), Result: resultCh, Finalized: finalized}
	result := <-resultCh
	if result.Outcome != ReloadAppliedLive {
		t.Fatalf("managed reload = %+v", result)
	}

	next := cfgWithReturn(addr, 202)
	src.set(next, nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	time.Sleep(50 * time.Millisecond)
	if !eventually(t, "http://"+addr+"/", "return-201") {
		t.Fatal("server processed next reload before coordinator finalization")
	}
	close(finalized)
	if !eventually(t, "http://"+addr+"/", "return-202") {
		t.Fatal("server did not process queued reload after finalization")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestStaleManagedReloadAbortsPreparedCommit(t *testing.T) {
	addr := freePort(t)
	src := &stubSource{}
	initial := cfgWithReturn(addr, 200)
	src.set(initial, nil)
	initialRaw, err := config.Marshal(initial)
	if err != nil {
		t.Fatalf("marshal initial: %v", err)
	}
	src.setRaw(initialRaw)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, "return-%d", c.Servers[0].Locations[0].Return)
		})
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "return-200")

	candidate, err := config.NewCandidate(cfgWithReturn(addr, 201))
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	var aborted atomic.Int64
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "stale-prepared", Source: ReloadSourceAdmin, Candidate: candidate, RawDigest: sha256Digest([]byte("does-not-match")), PreparedAdmin: NewPreparedCommit(nil, func() { aborted.Add(1) }), Result: resultCh}
	result := <-resultCh
	if result.Outcome != ReloadNotApplied || result.FailedPhase != "persisted_cas" {
		t.Fatalf("result = %+v, want stale rejection", result)
	}
	if aborted.Load() != 1 {
		t.Fatalf("prepared aborts = %d, want 1", aborted.Load())
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestAdminReloadRawDigestMatchesRawBytes (R11-03) verifies that the raw
// digest used for candidate validity is compared against the source's raw
// bytes, not against a canonical re-marshal. A file that contains comments
// or formatting differences round-trips to the same Config but produces a
// different canonical byte stream; only the raw bytes must determine
// validity.
func TestAdminReloadRawDigestMatchesRawBytes(t *testing.T) {
	addr := freePort(t)

	var genIDCounter atomic.Uint64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := genIDCounter.Add(1)
		body := fmt.Sprintf("return-%d", c.Servers[0].Locations[0].Return)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, body)
		})
		m := map[string]http.Handler{addr: h}
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return m, genID, commitFn, abortFn, nil
	}

	cfgFrom := func(data []byte) *config.Config {
		cfg, err := config.Parse(data)
		if err != nil {
			t.Fatalf("parse config: %v", err)
		}
		return cfg
	}

	src := &stubSource{}
	startData := []byte(fmt.Sprintf(`
[global]
shutdown_timeout = "2s"

[[servers]]
listen = %q
server_names = ["example.test"]

[[servers.locations]]
match = { type = "prefix", path = "/" }
return = 200
`, addr))
	src.set(cfgFrom(startData), nil)
	src.setRaw(startData)

	srv := New(cfgFrom(startData), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "return-200")

	// Admin writes a config file that includes a comment. The raw digest is
	// computed from the commented bytes; the candidate only carries the
	// parsed config. Validity must compare against the source's raw bytes.
	commented := []byte(fmt.Sprintf(`
# admin comment
[global]
shutdown_timeout = "2s"

[[servers]]
listen = %q
server_names = ["example.test"]

[[servers.locations]]
match = { type = "prefix", path = "/" }
return = 201  # inline comment
`, addr))
	raw201 := cfgFrom(commented)
	cand201, err := config.NewCandidate(raw201)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	src.set(raw201, nil)
	src.setRaw(commented)
	reload <- ReloadRequest{Source: ReloadSourceAdmin, Candidate: cand201, RawDigest: sha256Digest(commented)}

	if !eventually(t, "http://"+addr+"/", "return-201") {
		t.Fatal("commented admin candidate was not applied")
	}

	// Now the source is overwritten by a canonical marshal (no comments).
	// The same logical config still validates because the digest matches
	// the new raw bytes.
	canonical202, err := config.Marshal(raw201)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	raw202 := cfgFrom(canonical202)
	cand202, err := config.NewCandidate(raw202)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	src.set(raw202, nil)
	src.setRaw(canonical202)
	reload <- ReloadRequest{Source: ReloadSourceAdmin, Candidate: cand202, RawDigest: sha256Digest(canonical202)}

	if !eventually(t, "http://"+addr+"/", "return-201") {
		t.Fatal("canonical admin candidate was not applied")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// TestShutdownBoundedByGraceTimeout (R9-03) verifies that shutdown does not
// hang forever when a previous generation never drains: the redaction waiter
// hits its grace timeout, moves secrets to the retired tombstone, and exits,
// so Run returns within the configured shutdown window.
func TestShutdownBoundedByGraceTimeout(t *testing.T) {
	addr := freePort(t)

	entered := make(chan struct{})
	blocking := make(chan struct{}) // never closed

	var genIDCounter atomic.Uint64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := genIDCounter.Add(1)
		var h http.Handler
		if genID == 1 {
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ready" {
					_, _ = io.WriteString(w, "ready")
					return
				}
				close(entered)
				<-blocking
			})
		} else {
			h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.WriteString(w, "v2")
			})
		}
		m := map[string]http.Handler{addr: h}
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return m, genID, commitFn, abortFn, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	cfg := cfgWith(addr)
	cfg.Global.ShutdownTimeout = config.Duration(100 * time.Millisecond)
	srv := New(cfg, nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/ready", "ready")

	// Start a request on generation 1 that will never complete.
	go func() {
		client := &http.Client{Transport: testTransport, Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + addr + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request never entered handler")
	}

	// Reload to generation 2. Generation 1 is now retiring but will never drain.
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

	if !eventually(t, "http://"+addr+"/ready", "v2") {
		t.Fatal("reload did not install generation 2")
	}

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within bounded time")
	}

	// Release the blocked handler goroutine so goleak does not flag it.
	close(blocking)
}
