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
	"jul/internal/lifecycletest"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// TestShutdownDuringPrepareAbortsCandidateAndNeverPublishes pins the #428
// shutdown/reload race: a process shutdown that arrives while a candidate is
// being prepared — even by a builder that ignores cancellation — aborts the
// candidate exactly once, never publishes it, and still lets Run drain.
func TestShutdownDuringPrepareAbortsCandidateAndNeverPublishes(t *testing.T) {
	addr := freePort(t)
	live := lifecycletest.NewResource("live")
	candidate := lifecycletest.NewResource("candidate")
	entered := make(chan struct{})
	var calls, published atomic.Int32

	factory := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		n := calls.Add(1)
		res := live
		body := "live"
		if n > 1 {
			res, body = candidate, "candidate"
			close(entered)
			<-ctx.Done()
		}
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) })
		}
		commit := func() (upstream.SnapshotMap, func()) {
			if n > 1 {
				published.Add(1)
			}
			return nil, func() { _ = live.Close() }
		}
		abort := func() { _ = res.Close() }
		return m, uint64(n), commit, abort, nil
	}

	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "live")

	result := make(chan ReloadResult, 1)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP, Result: result}
	<-entered
	cancel()

	select {
	case rr := <-result:
		if rr.Published || rr.Outcome == ReloadAppliedLive {
			t.Fatalf("candidate published during shutdown: %+v", rr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload result never delivered after shutdown")
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not drain after shutdown during reload")
	}
	if published.Load() != 0 {
		t.Fatal("candidate generation was committed")
	}
	lifecycletest.AssertClosedOnce(t, candidate)
	lifecycletest.AssertOpen(t, live)
}
