// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// skipLog collects capture-skip reasons from any goroutine.
type skipLog struct {
	mu      sync.Mutex
	reasons []string
}

func (s *skipLog) add(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reasons = append(s.reasons, reason)
}

func (s *skipLog) get() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reasons...)
}

func sameReasons(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// A response that declares a Content-Length above the capture limit is never
// buffered: the capture is abandoned at WriteHeader, before the first byte
// (#505).
func TestCaptureDeclaredOversizeNeverBuffers(t *testing.T) {
	var budget captureBudget
	var skips skipLog
	rec := httptest.NewRecorder()
	cw := &cacheWriter{ResponseWriter: rec, limit: 1024, acct: captureAccount{budget: &budget, onSkip: skips.add}}
	cw.Header().Set("Content-Length", "4096")
	cw.WriteHeader(http.StatusOK)
	chunk := bytes.Repeat([]byte("x"), 512)
	for range 8 {
		if _, err := cw.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if cw.storable() {
		t.Fatal("oversize response is storable")
	}
	if c := cw.buf.Cap(); c != 0 {
		t.Fatalf("capture buffer grew to %d bytes for a declared-oversize response", c)
	}
	if got := budget.inflight.Load(); got != 0 {
		t.Fatalf("in-flight budget = %d, want 0", got)
	}
	if got := skips.get(); !sameReasons(got, captureSkipOversize) {
		t.Fatalf("skip reasons = %q, want [oversize]", got)
	}
	if rec.Body.Len() != 4096 {
		t.Fatalf("client received %d bytes, want 4096", rec.Body.Len())
	}

	var rskips skipLog
	r := &recorder{header: http.Header{}, limit: 1024, acct: captureAccount{budget: &budget, onSkip: rskips.add}}
	r.Header().Set("Content-Length", "4096")
	r.WriteHeader(http.StatusOK)
	for range 8 {
		_, _ = r.Write(chunk)
	}
	if r.storable() || r.body.Cap() != 0 {
		t.Fatalf("recorder storable=%v cap=%d, want unstorable with no buffer", r.storable(), r.body.Cap())
	}
	if got := rskips.get(); !sameReasons(got, captureSkipOversize) {
		t.Fatalf("recorder skip reasons = %q, want [oversize]", got)
	}
	if got := budget.inflight.Load(); got != 0 {
		t.Fatalf("in-flight budget after recorder = %d, want 0", got)
	}
}

// A body without Content-Length that crosses the limit frees its capture
// buffer at the overflow point rather than holding it until the response ends.
func TestCaptureOverflowReleasesBuffer(t *testing.T) {
	var budget captureBudget
	var skips skipLog
	cw := &cacheWriter{ResponseWriter: httptest.NewRecorder(), limit: 1024, acct: captureAccount{budget: &budget, onSkip: skips.add}}
	cw.WriteHeader(http.StatusOK)
	_, _ = cw.Write(bytes.Repeat([]byte("a"), 900))
	if cw.buf.Cap() < 900 || budget.inflight.Load() != 900 {
		t.Fatalf("before overflow: cap=%d inflight=%d, want >=900 and 900", cw.buf.Cap(), budget.inflight.Load())
	}
	_, _ = cw.Write(bytes.Repeat([]byte("b"), 200))
	if cw.buf.Cap() != 0 {
		t.Fatalf("capture buffer still holds %d bytes after overflow", cw.buf.Cap())
	}
	if got := budget.inflight.Load(); got != 0 {
		t.Fatalf("in-flight budget after overflow = %d, want 0", got)
	}
	if got := skips.get(); !sameReasons(got, captureSkipOversize) {
		t.Fatalf("skip reasons = %q, want [oversize]", got)
	}

	var rskips skipLog
	r := &recorder{header: http.Header{}, limit: 1024, acct: captureAccount{budget: &budget, onSkip: rskips.add}}
	_, _ = r.Write(bytes.Repeat([]byte("a"), 900))
	_, _ = r.Write(bytes.Repeat([]byte("b"), 200))
	if r.body.Cap() != 0 || budget.inflight.Load() != 0 {
		t.Fatalf("recorder after overflow: cap=%d inflight=%d, want 0 and 0", r.body.Cap(), budget.inflight.Load())
	}
	if got := rskips.get(); !sameReasons(got, captureSkipOversize) {
		t.Fatalf("recorder skip reasons = %q, want [oversize]", got)
	}
}

// With the in-flight budget held by one capture, a concurrent miss streams to
// its client uncaptured and is counted; the budget is returned when the
// captures finish and when a request is canceled.
func TestCaptureBudgetExhaustedStreamsUncaptured(t *testing.T) {
	const limit = 1000
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(limit), DefaultTTL: config.Duration(time.Hour)})
	var skips skipLog
	c.SetCaptureSkipObserver(skips.add)

	started := make(chan struct{})
	release := make(chan struct{})
	body := bytes.Repeat([]byte("z"), 600)
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		if r.URL.Path == "/hold" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}
	})
	h := c.Handler(origin)

	var wg sync.WaitGroup
	holder := httptest.NewRecorder()
	wg.Go(func() {
		h.ServeHTTP(holder, httptest.NewRequest(http.MethodGet, "http://example.com/hold", nil))
	})
	<-started
	if got := c.capture.inflight.Load(); got != int64(len(body)) {
		t.Fatalf("in-flight capture while held = %d, want %d", got, len(body))
	}

	// 600 + 600 > 1000: the second miss cannot reserve and streams uncaptured.
	second := httptest.NewRecorder()
	h.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "http://example.com/other", nil))
	if !bytes.Equal(second.Body.Bytes(), body) {
		t.Fatalf("uncaptured miss delivered %d bytes, want %d", second.Body.Len(), len(body))
	}
	if got := skips.get(); !sameReasons(got, captureSkipBudget) {
		t.Fatalf("skip reasons = %q, want [budget]", got)
	}
	if _, ok := c.get(key(httptest.NewRequest(http.MethodGet, "http://example.com/other", nil))); ok {
		t.Fatal("a miss streamed over budget was stored")
	}

	close(release)
	wg.Wait()
	if got := c.capture.inflight.Load(); got != 0 {
		t.Fatalf("in-flight capture after completion = %d, want 0", got)
	}
	if _, ok := c.get(key(httptest.NewRequest(http.MethodGet, "http://example.com/hold", nil))); !ok {
		t.Fatal("the in-budget capture was not stored")
	}

	// Cancellation returns the reservation too.
	started = make(chan struct{})
	release = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://example.com/hold?cancel="+strconv.Itoa(1), nil).WithContext(ctx)
	wg.Go(func() { h.ServeHTTP(httptest.NewRecorder(), req) })
	<-started
	if got := c.capture.inflight.Load(); got != int64(len(body)) {
		t.Fatalf("in-flight capture before cancel = %d, want %d", got, len(body))
	}
	cancel()
	wg.Wait()
	if got := c.capture.inflight.Load(); got != 0 {
		t.Fatalf("in-flight capture after cancel = %d, want 0", got)
	}
}
