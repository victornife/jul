// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
	"jul/internal/tracing"
)

type panicValidationTracer struct{}

func (panicValidationTracer) Start(ctx context.Context, _ string) (context.Context, tracing.Span) {
	return ctx, &panicValidationSpan{}
}
func (panicValidationTracer) Inject(context.Context, http.Header) {}

type panicValidationSpan struct{ validate bool }

func (s *panicValidationSpan) End() {
	if s.validate {
		panic("fixture")
	}
}
func (s *panicValidationSpan) SetString(key, value string) {
	if key == "cache.status" && value == "VALIDATE" {
		s.validate = true
	}
}
func (*panicValidationSpan) SetInt(string, int64) {}
func (*panicValidationSpan) SetStatus(int)        {}
func (*panicValidationSpan) RecordError(error)    {}

func TestValidationAdmissionPanicReleasesCall(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("ETag", `"fixture"`)
		_, _ = w.Write([]byte("body"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	req.Header.Set("Cache-Control", "no-cache")
	previous := tracing.Active()
	tracing.Set(panicValidationTracer{})
	defer tracing.Set(previous)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("validation span did not panic")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), req)
	}()
	if c.inflightRevalidations() != 0 || c.IndexStats().InflightOriginOps != 0 {
		t.Fatal("panic stranded admitted validation state")
	}
}
