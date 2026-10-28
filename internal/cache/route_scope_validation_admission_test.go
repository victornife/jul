// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/tracing"
)

type admissionBarrierTracer struct {
	entered chan struct{}
	release chan struct{}
}

func (t admissionBarrierTracer) Start(ctx context.Context, _ string) (context.Context, tracing.Span) {
	return ctx, &admissionBarrierSpan{tracer: t}
}
func (admissionBarrierTracer) Inject(context.Context, http.Header) {}

type admissionBarrierSpan struct {
	tracer   admissionBarrierTracer
	validate bool
}

func (s *admissionBarrierSpan) End() {
	if s.validate {
		close(s.tracer.entered)
		<-s.tracer.release
	}
}
func (s *admissionBarrierSpan) SetString(key, value string) {
	if key == "cache.status" && value == "VALIDATE" {
		s.validate = true
	}
}
func (*admissionBarrierSpan) SetInt(string, int64) {}
func (*admissionBarrierSpan) SetStatus(int)        {}
func (*admissionBarrierSpan) RecordError(error)    {}

func TestPurgeBetweenScopedLookupAndValidationAdmission(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	group := testLease(t, 31)
	var fullFetches atomic.Int32
	h, scope := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if fullFetches.Add(1) > 1 {
			_, _ = w.Write([]byte("post-purge"))
			return
		}
		_, _ = w.Write([]byte("old"))
	}))
	req := func(validate bool) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
		if validate {
			r.Header.Set("Cache-Control", "no-cache")
		}
		return leased(r, group)
	}
	h.ServeHTTP(httptest.NewRecorder(), req(false))
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	previous := tracing.Active()
	tracing.Set(admissionBarrierTracer{entered: entered, release: release})
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		tracing.Set(previous)
	})
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), req(true)) }()
	awaitSignal(t, entered)
	sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{}); err != nil || result.State != "succeeded" {
		t.Fatalf("purge state %q: %v", result.State, err)
	}
	releaseOnce.Do(func() { close(release) })
	awaitSignal(t, done)
	if e, _, found := c.lookupWithOwner(key(req(false)), req(false), &scope); found && string(e.Body) == "old" {
		t.Fatal("validation published an entry invalidated before admission")
	}
}
