// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/respwriter"
)

type controllerOnlyWriter struct {
	header    http.Header
	committed http.Header
	status    int
	body      bytes.Buffer
	err       error
}

func (w *controllerOnlyWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *controllerOnlyWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		w.committed = w.Header().Clone()
	}
}

func (w *controllerOnlyWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.body.Write(p)
}

func (w *controllerOnlyWriter) FlushError() error {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.err
}

func TestControllerOnlyFlushSnapshotsCachePolicyThroughMiddleware(t *testing.T) {
	for _, initial := range []string{"no-store", "public, max-age=60"} {
		t.Run(initial, func(t *testing.T) {
			c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
			var calls atomic.Int32
			var flushErr error
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				if _, ok := w.(http.Flusher); ok {
					t.Error("invented http.Flusher")
				}
				w.Header().Set("Cache-Control", initial)
				flushErr = http.NewResponseController(w).Flush()
				if initial == "no-store" {
					w.Header().Set("Cache-Control", "public, max-age=60")
				} else {
					w.Header().Set("Cache-Control", "no-store")
				}
				_, _ = w.Write([]byte("body"))
			})
			compress, err := middleware.NewCompression(middleware.CompressionOptions{Encoders: []string{"gzip"}})
			if err != nil {
				t.Fatal(err)
			}
			value := "fixture"
			policy := middleware.ResponsePolicy([]config.ResponseHeaderOp{{Op: "set", Name: "X-Policy", Value: &value}}, nil)
			h := compress(policy(c.Handler(origin)))
			writer := &controllerOnlyWriter{}
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			recorder := middleware.NewRecorder(writer)
			h.ServeHTTP(recorder.Writer(), req)
			if flushErr != nil || writer.committed.Get("Cache-Control") != initial || writer.committed.Get("X-Policy") != value {
				t.Fatalf("flush=%v committed=%v", flushErr, writer.committed)
			}
			second := &controllerOnlyWriter{}
			h.ServeHTTP(middleware.NewRecorder(second).Writer(), req)
			wantCalls := int32(1)
			if initial == "no-store" {
				wantCalls = 2
			}
			if calls.Load() != wantCalls {
				t.Fatalf("origin calls = %d, want %d", calls.Load(), wantCalls)
			}
		})
	}
}

func TestControllerOnlyFlushErrorPropagatesThroughMiddleware(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	want := errors.New("flush failure")
	var got error
	compress, err := middleware.NewCompression(middleware.CompressionOptions{Encoders: []string{"gzip"}})
	if err != nil {
		t.Fatal(err)
	}

	h := compress(c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		got = http.NewResponseController(w).Flush()
	})))
	writer := &controllerOnlyWriter{err: want}
	h.ServeHTTP(middleware.NewRecorder(writer).Writer(), httptest.NewRequest(http.MethodGet, "http://example.test/item", nil))
	if !errors.Is(got, want) {
		t.Fatalf("flush = %v, want %v", got, want)
	}
}

type basicResponseWriter struct{ w *controllerOnlyWriter }

func (w basicResponseWriter) Header() http.Header         { return w.w.Header() }
func (w basicResponseWriter) WriteHeader(code int)        { w.w.WriteHeader(code) }
func (w basicResponseWriter) Write(p []byte) (int, error) { return w.w.Write(p) }

func TestUnsupportedFlushDoesNotCommitThroughMiddleware(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	compress, err := middleware.NewCompression(middleware.CompressionOptions{Encoders: []string{"gzip"}})
	if err != nil {
		t.Fatal(err)
	}
	writer := &controllerOnlyWriter{}
	h := compress(c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); !errors.Is(err, http.ErrNotSupported) {
			t.Errorf("flush = %v, want unsupported", err)
		}
		if writer.status != 0 {
			t.Errorf("unsupported flush committed status %d", writer.status)
		}
	})))
	h.ServeHTTP(middleware.NewRecorder(basicResponseWriter{w: writer}).Writer(), httptest.NewRequest(http.MethodGet, "http://example.test/item", nil))
}

type classicFlushErrorWriter struct {
	*httptest.ResponseRecorder
	err error
}

func (w *classicFlushErrorWriter) FlushError() error { return w.err }

func TestFlushErrorThroughExchangePreventsCachePublication(t *testing.T) {
	c, _ := conformanceCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	want := errors.New("transport flush failure")
	var calls atomic.Int32
	var got error
	origin := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=60")
		got = http.NewResponseController(w).Flush()
		_, _ = w.Write([]byte("body"))
	}))
	reg := respwriter.NewExchangeRegistry()
	compress, err := middleware.NewCompression(middleware.CompressionOptions{Encoders: []string{"gzip"}})
	if err != nil {
		t.Fatal(err)
	}
	h := compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchange, request, _, release := reg.Admit(w, r, respwriter.EntryCoord{GenID: 1})
		defer release()
		origin.ServeHTTP(exchange, request)
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
	for range 2 {
		writer := &classicFlushErrorWriter{ResponseRecorder: httptest.NewRecorder(), err: want}
		h.ServeHTTP(middleware.NewRecorder(writer).Writer(), req)
		if !errors.Is(got, want) {
			t.Fatalf("flush error = %v, want %v", got, want)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("failed flush response was cached: %d origin calls", calls.Load())
	}
}
