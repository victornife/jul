// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

type waitLog struct {
	mu   sync.Mutex
	seen map[string]int
}

func (w *waitLog) add(_, result string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.seen == nil {
		w.seen = make(map[string]int)
	}
	w.seen[result]++
}

func (w *waitLog) count(result string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seen[result]
}

func limitManager(t *testing.T, waits *waitLog, panics *atomic.Int64) *Manager {
	t.Helper()
	m, err := NewManager(Options{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnInstanceWait: waits.add,
		OnPanic:        func(string) { panics.Add(1) },
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

// assertNoSlotLeak checks that every held slot belongs to an idle pooled
// instance once no call is in flight.
func assertNoSlotLeak(t *testing.T, p *plugin) {
	t.Helper()
	if live, idle := len(p.slots), len(p.pool); live != idle {
		t.Fatalf("live slots = %d with %d idle instances and no call in flight: slot leak", live, idle)
	}
}

// At the cap a caller waits for an instance to come back or be retired, and is
// rejected after one call timeout; live instances never exceed max_instances.
func TestInstanceCapWaitsThenRejects(t *testing.T) {
	var waits waitLog
	var panics atomic.Int64
	m := limitManager(t, &waits, &panics)
	s := buildSet(t, m, map[string]config.PluginConfig{"hi": pcfg("header-inject", func(pc *config.PluginConfig) {
		pc.MaxInstances = 2
		pc.Timeout = config.Duration(100 * time.Millisecond)
	})})
	p := s.plugins["hi"]
	ctx := context.Background()

	pm1, err := p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pm2, err := p.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.InstanceStats(); len(got) != 1 || got[0].Live != 2 {
		t.Fatalf("InstanceStats = %+v, want hi live=2", got)
	}

	start := time.Now()
	if _, err := p.acquire(ctx); err != errInstanceLimit {
		t.Fatalf("third acquire err = %v, want errInstanceLimit", err)
	}
	if waited := time.Since(start); waited < 90*time.Millisecond {
		t.Fatalf("rejected after %v, want a wait of about one call timeout", waited)
	}
	if waits.count(instanceWaitRejected) != 1 || len(p.slots) != 2 {
		t.Fatalf("rejected=%d live=%d, want 1 and 2", waits.count(instanceWaitRejected), len(p.slots))
	}

	// A returned instance goes to the waiter.
	got := make(chan *pooledModule, 1)
	go func() {
		pm, err := p.acquire(ctx)
		if err != nil {
			t.Error(err)
		}
		got <- pm
	}()
	time.Sleep(20 * time.Millisecond)
	p.release(pm1)
	if pm := <-got; pm != pm1 {
		t.Fatal("the waiter did not receive the released instance")
	}

	// A retired instance frees its slot for the waiter to instantiate into.
	go func() {
		pm, err := p.acquire(ctx)
		if err != nil {
			t.Error(err)
		}
		got <- pm
	}()
	time.Sleep(20 * time.Millisecond)
	p.discard(pm2.mod)
	pm4 := <-got
	if pm4 == nil || pm4 == pm2 {
		t.Fatal("the waiter did not get a fresh instance after a retirement")
	}
	if len(p.slots) != 2 {
		t.Fatalf("live = %d, want 2", len(p.slots))
	}
	p.release(pm1)
	p.release(pm4)
	assertNoSlotLeak(t, p)
	if panics.Load() != 0 {
		t.Fatalf("a capacity wait was counted as %d panics", panics.Load())
	}
}

// A request turned away at the cap gets 503 with Retry-After, never reaches
// next, is not counted as a panic, and a canceled waiter returns promptly.
func TestInstanceCapRejectsRequestWith503(t *testing.T) {
	var waits waitLog
	var panics atomic.Int64
	m := limitManager(t, &waits, &panics)
	s := buildSet(t, m, map[string]config.PluginConfig{"hi": pcfg("header-inject", func(pc *config.PluginConfig) {
		pc.MaxInstances = 1
		pc.Timeout = config.Duration(time.Second)
	})})
	p := s.plugins["hi"]
	held, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	next, called := okNext()
	h := s.Middleware("hi")(next)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("status=%d Retry-After=%q, want 503 and 1", rec.Code, rec.Header().Get("Retry-After"))
	}
	if *called {
		t.Fatal("next ran for a request rejected at the instance cap")
	}

	// Cancellation ends the wait early and leaks nothing.
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(10*time.Millisecond, cancel)
	start := time.Now()
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx))
	if waited := time.Since(start); waited > 500*time.Millisecond {
		t.Fatalf("a canceled waiter took %v to return; the wait bound is 1s", waited)
	}
	if waits.count(instanceWaitRejected) != 2 || panics.Load() != 0 {
		t.Fatalf("rejected=%d panics=%d, want 2 and 0", waits.count(instanceWaitRejected), panics.Load())
	}

	p.release(held)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status after release = %d, want 200", rec.Code)
	}
	assertNoSlotLeak(t, p)
}

// Under concurrency with a guest that runs until its timeout, live instances
// never exceed max_instances, and trapped instances return their slots.
func TestInstanceCapBoundsSlowGuestConcurrency(t *testing.T) {
	var waits waitLog
	var panics atomic.Int64
	m := limitManager(t, &waits, &panics)
	const maxInstances = 3
	s := buildSet(t, m, map[string]config.PluginConfig{"loop": pcfg("testguest-loop", func(pc *config.PluginConfig) {
		pc.MaxInstances = maxInstances
		pc.Timeout = config.Duration(100 * time.Millisecond)
	})})
	p := s.plugins["loop"]
	h := s.Middleware("loop")(http.NotFoundHandler())

	stop := make(chan struct{})
	var peak atomic.Int64
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		for {
			if n := int64(len(p.slots)); n > peak.Load() {
				peak.Store(n)
			}
			select {
			case <-stop:
				return
			default:
				time.Sleep(time.Millisecond)
			}
		}
	}()

	var wg sync.WaitGroup
	var trapped, rejected atomic.Int64
	for range 12 {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			switch rec.Code {
			case http.StatusInternalServerError:
				trapped.Add(1)
			case http.StatusServiceUnavailable:
				rejected.Add(1)
			default:
				t.Errorf("status = %d, want 500 (guest timeout) or 503 (cap)", rec.Code)
			}
		})
	}
	wg.Wait()
	close(stop)
	<-sampled

	if peak.Load() > maxInstances {
		t.Fatalf("peak live instances = %d, want <= %d", peak.Load(), maxInstances)
	}
	if trapped.Load() < maxInstances {
		t.Fatalf("trapped = %d, want at least %d guests to have run", trapped.Load(), maxInstances)
	}
	if int(rejected.Load()) != waits.count(instanceWaitRejected) {
		t.Fatalf("503s = %d but rejected waits = %d", rejected.Load(), waits.count(instanceWaitRejected))
	}
	assertNoSlotLeak(t, p)
}

// A guest panic discards its instance and returns the slot.
func TestInstanceSlotReturnedAfterPanic(t *testing.T) {
	var waits waitLog
	var panics atomic.Int64
	m := limitManager(t, &waits, &panics)
	s := buildSet(t, m, map[string]config.PluginConfig{"p": pcfg("testguest-panic", func(pc *config.PluginConfig) {
		pc.MaxInstances = 1
	})})
	p := s.plugins["p"]
	next, _ := okNext()
	h := s.Middleware("p")(next)
	for range 3 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500 from the guest panic, not a cap rejection", rec.Code)
		}
	}
	assertNoSlotLeak(t, p)
	if waits.count(instanceWaitRejected) != 0 {
		t.Fatal("a panicking guest leaked its slot into a cap rejection")
	}
}

// Closing a Set drops its plugins from the live-instance report.
func TestInstanceStatsForgetClosedSet(t *testing.T) {
	var waits waitLog
	var panics atomic.Int64
	m := limitManager(t, &waits, &panics)
	s, err := m.Build(context.Background(), map[string]config.PluginConfig{"hi": pcfg("header-inject")})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.InstanceStats(); len(got) != 1 || got[0].Plugin != "hi" || got[0].Live != 1 {
		t.Fatalf("InstanceStats = %+v, want hi live=1 (the eager instance)", got)
	}
	_ = s.Close()
	if got := m.InstanceStats(); len(got) != 0 {
		t.Fatalf("InstanceStats after Close = %+v, want none", got)
	}
}
