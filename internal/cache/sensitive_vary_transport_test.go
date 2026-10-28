// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

type fixtureRoundTrip func(*http.Request) (*http.Response, error)

func (f fixtureRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type fixtureCanceledBody struct{ ctx context.Context }

func (b fixtureCanceledBody) Read([]byte) (int, error) {
	<-b.ctx.Done()
	return 0, errors.New("fixture copy failure")
}
func (fixtureCanceledBody) Close() error { return nil }

func TestSensitiveVaryProxyAbortStillReturnsFallback(t *testing.T) {
	var calls atomic.Int32
	var serverContext atomic.Bool
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.Out.URL.Scheme = "http"
		r.Out.URL.Host = "fixture.test"
	}, Transport: fixtureRoundTrip(func(r *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		h := http.Header{"Cache-Control": {"public, no-cache"}, "Etag": {`"fixture"`}}
		var body io.ReadCloser
		if r.Header.Get("If-None-Match") != "" {
			serverContext.Store(r.Context().Value(http.ServerContextKey) != nil)
			h.Set("Vary", "Cookie")
			body = fixtureCanceledBody{ctx: r.Context()}
		} else if n == 1 {
			body = io.NopCloser(strings.NewReader("old-body"))
		} else {
			body = io.NopCloser(strings.NewReader("new-body"))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: h, Body: body, Request: r}, nil
	})}
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	var abortSeen atomic.Bool
	proxied := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				abortSeen.Store(true)
				panic(recovered)
			}
		}()
		proxy.ServeHTTP(w, r)
	})
	gateway := httptest.NewServer(c.Handler(proxied))
	defer gateway.Close()
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	for i, want := range []string{"old-body", "new-body"} {
		res, err := client.Get(gateway.URL + "/item")
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		body, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil || string(body) != want {
			t.Fatalf("request %d did not return the full fallback", i)
		}
	}
	if !serverContext.Load() {
		t.Fatal("proxy validation did not carry an HTTP server context")
	}
	if !abortSeen.Load() {
		t.Fatal("reverse proxy did not abort after the fixture copy error")
	}
}

func TestSensitiveVaryProxyCancellationStillReturnsFallback(t *testing.T) {
	var calls atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Cache-Control", "public, no-cache")
		w.Header().Set("ETag", `"fixture"`)
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Vary", "Cookie")
			w.Header().Set("Content-Length", "8192")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if n == 1 {
			_, _ = io.WriteString(w, "old-body")
		} else {
			_, _ = io.WriteString(w, "new-body")
		}
	}))
	defer origin.Close()
	target, err := url.Parse(origin.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	gateway := httptest.NewServer(c.Handler(httputil.NewSingleHostReverseProxy(target)))
	defer gateway.Close()
	client := gateway.Client()
	client.Timeout = 3 * time.Second
	for i, want := range []string{"old-body", "new-body"} {
		res, err := client.Get(gateway.URL + "/item")
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		body, readErr := io.ReadAll(res.Body)
		closeErr := res.Body.Close()
		if readErr != nil || closeErr != nil || string(body) != want {
			t.Fatalf("request %d did not return the full fallback", i)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("origin calls = %d, want 3", calls.Load())
	}
}

func TestRecordedValidationPreservesResponseTrailers(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)})
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") != "" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Trailer", "Content-Digest")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "replacement")
			w.Header().Set("Content-Digest", "fixture-digest")
			w.Header().Set(http.TrailerPrefix+"X-Fixture-Trailer", "fixture-extra")
			w.Header().Set("X-Too-Late", "not-a-trailer")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"fixture"`)
		_, _ = io.WriteString(w, "original")
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	res := rec.Result()
	defer res.Body.Close()
	if rec.Body.String() != "replacement" || res.Trailer.Get("Content-Digest") != "fixture-digest" ||
		res.Trailer.Get("X-Fixture-Trailer") != "fixture-extra" || res.Header.Get("X-Too-Late") != "" {
		t.Fatal("recorded replacement response lost its declared trailer")
	}
}
