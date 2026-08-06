// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// generationCloser stands in for a generation-owned resource — a gRPC backend
// connection, a WASM plugin runtime, a static-file directory handle — so the
// test can observe exactly when the server decides it is safe to close it.
type generationCloser struct {
	closed atomic.Bool
	done   chan struct{}
	once   sync.Once
}

func newGenerationCloser() *generationCloser {
	return &generationCloser{done: make(chan struct{})}
}

func (c *generationCloser) Close() {
	c.once.Do(func() {
		c.closed.Store(true)
		close(c.done)
	})
}

// TestReloadWaitsForCacheRevalidationHoldingGeneration is the end-to-end proof
// of the #131 lifecycle contract over a real listener, a real reload and the
// real response cache: a stale hit starts a background revalidation, the
// originating request returns, a reload publishes a new generation, and the OLD
// generation's resources must stay open until the revalidation finishes.
//
// Before the generation lease this was the confirmed defect: revalidation ran on
// context.Background() with the captured handler tree, so the reload closed the
// resources it was still using.
func TestReloadWaitsForCacheRevalidationHoldingGeneration(t *testing.T) {
	addr := freePort(t)

	responseCache, err := cache.New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        config.Size(1 << 20),
		DefaultTTL:           config.Duration(50 * time.Millisecond),
		StaleWhileRevalidate: config.Duration(10 * time.Minute),
	}, quietLogger())
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	var (
		builds        atomic.Int32
		originCalls   atomic.Int32
		enterOnce     sync.Once
		entered       = make(chan struct{})
		release       = make(chan struct{})
		gen1Closer    = newGenerationCloser()
		gen1CloserSet = make(chan struct{})
		closerOnce    sync.Once
	)

	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		n := builds.Add(1)
		first := n == 1

		origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if !first {
				// The new generation's origin never blocks.
				_, _ = io.WriteString(w, "v2")
				return
			}
			if originCalls.Add(1) == 1 {
				// Initial fill: cheap, so the entry exists and can go stale.
				_, _ = io.WriteString(w, "v1")
				return
			}
			// Background revalidation: hold it open across the reload.
			enterOnce.Do(func() { close(entered) })
			<-release
			_, _ = io.WriteString(w, "v1-refreshed")
		})

		cached := responseCache.Handler(origin)
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/ready" {
				_, _ = io.WriteString(w, "ready")
				return
			}
			cached.ServeHTTP(w, r)
		})

		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = h
		}

		var retire func()
		if n == 2 {
			// The retire callback a build returns closes the resources of the
			// generation it supersedes.
			retire = gen1Closer.Close
			closerOnce.Do(func() { close(gen1CloserSet) })
		}
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, retire }
		return m, uint64(n), commitFn, func() {}, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src,
		func(context.Context, *config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()

	releaseOrigin := sync.OnceFunc(func() { close(release) })
	stopServer := sync.OnceFunc(func() {
		cancel()
		<-done
	})
	// Guarantee the blocked origin and the server are released even if an
	// assertion fails before the happy path reaches them.
	t.Cleanup(func() {
		releaseOrigin()
		stopServer()
	})

	waitForServe(t, "http://"+addr+"/ready", "ready")

	client := &http.Client{Transport: testTransport, Timeout: 2 * time.Second}
	url := "http://" + addr + "/cached"

	// Fill the cache.
	if state := getCacheState(t, client, url); state != "MISS" {
		t.Fatalf("first request X-Cache = %q, want MISS", state)
	}
	// Poll until the entry has aged past its 50ms freshness; the stale hit that
	// observes it starts the background revalidation.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if state := getCacheState(t, client, url); state == "STALE" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cached entry never became stale")
		}
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background revalidation did not reach the origin")
	}

	// The originating request has already returned. Publish a new generation.
	src.set(cfgWith(addr), nil)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP}

	select {
	case <-gen1CloserSet:
	case <-time.After(5 * time.Second):
		t.Fatal("reload did not build a second generation")
	}

	// The old generation owns the handler tree the revalidation is using, so its
	// resources must remain open.
	select {
	case <-gen1Closer.done:
		t.Fatal("previous generation's resources were closed while its cache revalidation was still running")
	case <-time.After(300 * time.Millisecond):
	}
	if gen1Closer.closed.Load() {
		t.Fatal("previous generation was retired during an active revalidation")
	}

	// Releasing the revalidation lets the generation drain and retire.
	releaseOrigin()
	select {
	case <-gen1Closer.done:
	case <-time.After(10 * time.Second):
		t.Fatal("previous generation's resources were not closed after its revalidation finished")
	}

	stopServer()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	default:
	}
}

func getCacheState(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Header.Get("X-Cache")
}
