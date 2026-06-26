package server

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	return func(c *config.Config) (map[string]http.Handler, error) {
		current := *tag.Load()
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, current)
		})
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}
		return m, nil
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

// fetch returns the body served at url, or ("", err) on failure.
func fetch(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// reachable reports whether url responds to a GET.
func reachable(url string) bool {
	client := http.Client{Timeout: 200 * time.Millisecond}
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
func eventually(t *testing.T, url, want string) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := fetch(url); err == nil && body == want {
			return true
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
