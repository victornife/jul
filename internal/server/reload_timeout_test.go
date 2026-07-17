// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func cfgWithReloadTimeout(addr string, timeout time.Duration) *config.Config {
	c := cfgWith(addr)
	c.Global.ReloadTimeout = config.Duration(timeout)
	return c
}

// factoryFor returns a HandlerFactory that serves body for every listen address.
func factoryFor(c *config.Config, body string) map[string]http.Handler {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	})
	m := make(map[string]http.Handler, len(c.Servers))
	for _, srv := range c.Servers {
		m[srv.Listen] = h
	}
	return m
}

func TestReloadTimeout(t *testing.T) {
	addr := freePort(t)

	src := &stubSource{}
	src.set(cfgWithReloadTimeout(addr, 50*time.Millisecond), nil)

	// Factory that sleeps longer than the reload timeout.
	slowFactory := func(c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		time.Sleep(200 * time.Millisecond)
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return factoryFor(c, "v1"), 1, commitFn, abortFn, nil
	}

	srv := New(cfgWithReloadTimeout(addr, 50*time.Millisecond), nil, lifecycle.Fingerprint{}, quietLogger(), slowFactory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitDialable(t, addr)

	// Trigger a reload that exceeds the timeout threshold. Poll for the result
	// rather than sleeping a fixed duration: the factory takes 200 ms, plus
	// goroutine scheduling and coverage-instrumentation overhead on a loaded
	// machine can push the total past a tight fixed sleep, making the test
	// intermittently flaky.
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	var li *lastReloadInfo
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		li = srv.LastReload()
		if li != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if li == nil {
		t.Fatal("expected LastReload to be set after slow reload")
	}
	if !li.TimedOut {
		t.Fatalf("expected TimedOut=true for slow reload, got OK=%v TimedOut=%v", li.OK, li.TimedOut)
	}
	if !li.OK {
		t.Fatal("expected OK=true because the advisory timeout still completes the swap")
	}
	// The swap must still have completed despite the timeout warning.
	prevGen := srv.handlers.Load()
	if prevGen == nil {
		t.Fatal("expected handler generation to exist after advisory timeout")
	}
	cancel()
	<-done
}

func TestReloadRecordsSuccessAndDuration(t *testing.T) {
	addr := freePort(t)

	src := &stubSource{}
	src.set(cfgWithReloadTimeout(addr, 5*time.Second), nil)

	tag := &atomic.Pointer[string]{}
	v1 := "v1"
	tag.Store(&v1)

	srv := New(cfgWithReloadTimeout(addr, 5*time.Second), nil, lifecycle.Fingerprint{}, quietLogger(), bodyHandlerFactory(tag), src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }() //nolint:errcheck
	waitDialable(t, addr)

	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}
	time.Sleep(50 * time.Millisecond)

	li := srv.LastReload()
	if li == nil {
		t.Fatal("expected LastReload after reload")
	}
	if !li.OK {
		t.Fatalf("expected OK=true, got OK=%v Error=%q", li.OK, li.Error)
	}
	if li.TimedOut {
		t.Fatal("expected TimedOut=false for a fast reload")
	}
	if li.At.IsZero() {
		t.Fatal("expected At set")
	}

	cancel()
	<-done
}
