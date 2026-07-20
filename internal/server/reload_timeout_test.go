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
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP, Deadline: time.Now().Add(50 * time.Millisecond)}
	var li *ReloadResult
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
		t.Fatalf("expected TimedOut=true for slow reload, got outcome=%v TimedOut=%v", li.Outcome, li.TimedOut)
	}
	if li.Outcome != ReloadNotApplied {
		t.Fatalf("expected outcome=%v for pre-publish timeout, got %v", ReloadNotApplied, li.Outcome)
	}
	// The swap must NOT have completed: bounded cancellation aborts before
	// Publish when the deadline expires during preparation.
	if li.Published {
		t.Fatal("expected Published=false because the reload was cancelled before Publish")
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
	if li.Outcome != ReloadAppliedLive {
		t.Fatalf("expected outcome=%v, got %v Error=%q", ReloadAppliedLive, li.Outcome, li.Error)
	}
	if li.TimedOut {
		t.Fatal("expected TimedOut=false for a fast reload")
	}
	if li.StartedAt.IsZero() {
		t.Fatal("expected StartedAt set")
	}

	cancel()
	<-done
}

// TestReloadResultCorrelation verifies that a ReloadRequest ID and source are
// echoed back in the ReloadResult and that the result is delivered on the
// supplied channel.
func TestReloadResultCorrelation(t *testing.T) {
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
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitDialable(t, addr)

	resultCh := make(chan ReloadResult, 1)
	req := ReloadRequest{
		ID:       "rl_correlation_42",
		Source:   ReloadSourceAdmin,
		Deadline: time.Now().Add(5 * time.Second),
		Result:   resultCh,
	}
	reload <- req

	var rr ReloadResult
	select {
	case rr = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload result on supplied channel")
	}

	if rr.ID != req.ID {
		t.Errorf("result.ID = %q, want %q", rr.ID, req.ID)
	}
	if rr.Source != req.Source {
		t.Errorf("result.Source = %v, want %v", rr.Source, req.Source)
	}
	if rr.Outcome != ReloadAppliedLive {
		t.Errorf("result.Outcome = %v, want %v", rr.Outcome, ReloadAppliedLive)
	}

	// LastReload must expose the same correlated result.
	if li := srv.LastReload(); li == nil || li.ID != req.ID {
		t.Errorf("LastReload did not return correlated result")
	}

	cancel()
	<-done
}

// TestReloadDeadlineBoundsCancellation verifies that a deadline earlier than
// the default reload timeout cancels the transaction when the phase runs long.
func TestReloadDeadlineBoundsCancellation(t *testing.T) {
	addr := freePort(t)
	src := &stubSource{}
	// Default reload timeout is generous; the per-request deadline is much
	// shorter and should govern cancellation.
	src.set(cfgWithReloadTimeout(addr, 5*time.Second), nil)

	slowFactory := func(c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		time.Sleep(200 * time.Millisecond)
		commitFn := func() (upstream.SnapshotMap, func()) { return nil, nil }
		abortFn := func() {}
		return factoryFor(c, "v1"), 1, commitFn, abortFn, nil
	}

	srv := New(cfgWithReloadTimeout(addr, 5*time.Second), nil, lifecycle.Fingerprint{}, quietLogger(), slowFactory, src, func(*config.Config) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitDialable(t, addr)

	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{
		Source:   ReloadSourceSIGHUP,
		Deadline: time.Now().Add(50 * time.Millisecond),
		Result:   resultCh,
	}

	var rr ReloadResult
	select {
	case rr = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload result")
	}

	if !rr.TimedOut {
		t.Errorf("TimedOut = %v, want true", rr.TimedOut)
	}
	if rr.Outcome != ReloadNotApplied {
		t.Errorf("Outcome = %v, want %v", rr.Outcome, ReloadNotApplied)
	}
	if rr.Published {
		t.Error("Published = true, want false for timed-out reload")
	}

	cancel()
	<-done
}

// TestReloadPhaseTimingRecordsDurations verifies that phase durations are
// recorded and exposed in the reload result.
func TestReloadPhaseTimingRecordsDurations(t *testing.T) {
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
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitDialable(t, addr)

	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP, Result: resultCh}

	var rr ReloadResult
	select {
	case rr = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload result")
	}

	if rr.Outcome != ReloadAppliedLive {
		t.Fatalf("Outcome = %v, want %v", rr.Outcome, ReloadAppliedLive)
	}
	if rr.DurationMS <= 0 {
		t.Errorf("DurationMS = %d, want > 0", rr.DurationMS)
	}
	if rr.CompletedAt.Before(rr.StartedAt) {
		t.Error("CompletedAt should not be before StartedAt")
	}
	if rr.HTTP.Status != ReloadSubsystemOK {
		t.Errorf("HTTP.Status = %v, want %v", rr.HTTP.Status, ReloadSubsystemOK)
	}
	if rr.Stream.Status != ReloadSubsystemOK {
		t.Errorf("Stream.Status = %v, want %v", rr.Stream.Status, ReloadSubsystemOK)
	}

	cancel()
	<-done
}
