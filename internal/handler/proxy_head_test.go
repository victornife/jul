// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

// HEAD through the proxy returns the backend's status and headers with no
// body, and never counts against the backend's health: max_fails HEAD
// requests used to take a healthy backend out of rotation (#534).
func TestProxyHeadDoesNotFailBackend(t *testing.T) {
	var gets atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("Content-Length", "21")
		if r.Method == http.MethodGet {
			gets.Add(1)
			_, _ = io.WriteString(w, "<h1>conformance</h1>\n")
		}
	}))
	defer backend.Close()

	h := newProxy(t, config.LocationConfig{ProxyPass: "http://app"}, map[string]config.UpstreamConfig{
		"app": {Name: "app", Servers: []config.UpstreamServer{{Address: backend.Listener.Addr().String(), Weight: 1}}, MaxFails: 1},
	})

	for i := range 5 {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "http://edge.example/page", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("HEAD %d: status = %d, want 200", i, rec.Code)
		}
		if got := rec.Header().Get("Content-Length"); got != "21" {
			t.Fatalf("HEAD %d: Content-Length = %q, want the backend's 21", i, got)
		}
		if rec.Body.Len() != 0 {
			t.Fatalf("HEAD %d: body = %q, want none", i, rec.Body.String())
		}
	}

	// With max_fails = 1, a single failure recorded by those HEADs would have
	// taken the only backend out of rotation.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/page", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "<h1>conformance</h1>\n" || gets.Load() != 1 {
		t.Fatalf("GET after HEADs: status %d body %q backend GETs %d; the backend was marked down", rec.Code, rec.Body.String(), gets.Load())
	}
}
