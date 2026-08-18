// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// admissionProxy builds a proxy handler for a single backend under the supplied
// pool resilience policy, returning the handler and its admission owner so a
// test can inspect the live counters.
func admissionProxy(t *testing.T, backend string, r *config.ResilienceConfig) (*proxyHandler, *upstream.Admission) {
	t.Helper()
	upstreams := map[string]config.UpstreamConfig{
		"api": {
			Name:       "api",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: backend, Weight: 1}},
			MaxFails:   3,
			Resilience: r,
		},
	}
	loc := config.LocationConfig{
		Match:     config.MatchConfig{Type: "prefix", Path: "/"},
		ProxyPass: "http://api",
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{}, loc, upstreams, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	ph, ok := h.(*proxyHandler)
	if !ok {
		t.Fatalf("NewProxy returned %T, want *proxyHandler", h)
	}
	t.Cleanup(func() { _ = ph.Close() })
	return ph, ph.admission
}

// TestProxyAdmissionRejectsOverLimit proves the admission limit is enforced on
// the request path and that a rejected request is a 503 with Retry-After.
//
// Overload is deliberately not 429: 429 says the client sent too many requests,
// but a saturated pool is not the client's fault, and Retry-After is defined
// for 503.
func TestProxyAdmissionRejectsOverLimit(t *testing.T) {
	block := make(chan struct{})
	var unblock sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	defer unblock.Do(func() { close(block) })

	h, adm := admissionProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 1})

	// Occupy the only slot.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()
	waitActive(t, adm, 1)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("overload response carries no Retry-After")
	}

	unblock.Do(func() { close(block) })
	wg.Wait()
}

// TestProxyAdmissionReleasesOnEveryPath proves the slot is returned whether the
// backend succeeds or fails. A release that only runs on the happy path leaks
// capacity precisely when the backend is unhealthy, which is when the limit
// matters most.
func TestProxyAdmissionReleasesOnEveryPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	t.Run("success", func(t *testing.T) {
		h, adm := admissionProxy(t, backend.Listener.Addr().String(),
			&config.ResilienceConfig{MaxActiveRequests: 4})
		for i := 0; i < 10; i++ {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("request %d: status = %d", i, rec.Code)
			}
		}
		if got := adm.Active(); got != 0 {
			t.Fatalf("active after 10 successes = %d, want 0", got)
		}
	})

	t.Run("backend unreachable", func(t *testing.T) {
		// A closed port: every attempt fails at dial, exercising the error path
		// through ErrorHandler rather than the response-body release.
		h, adm := admissionProxy(t, "127.0.0.1:1", &config.ResilienceConfig{MaxActiveRequests: 4})
		for i := 0; i < 10; i++ {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			if rec.Code == http.StatusOK {
				t.Fatalf("request %d unexpectedly succeeded", i)
			}
		}
		if got := adm.Active(); got != 0 {
			t.Fatalf("active after 10 failures = %d, want 0", got)
		}
	})
}

// TestProxyAdmissionQueuesThenServes proves a request over the limit waits in
// the bounded queue and is served once a slot frees, rather than being rejected
// outright when a queue is configured.
func TestProxyAdmissionQueuesThenServes(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	defer once.Do(func() { close(release) })

	h, adm := admissionProxy(t, backend.Listener.Addr().String(), &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: 4,
		PendingTimeout:     config.Duration(5 * time.Second),
	})

	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
			codes <- rec.Code
		}()
	}
	waitActive(t, adm, 1)
	waitPendingAdmission(t, adm, 1)

	once.Do(func() { close(release) })
	for i := 0; i < 2; i++ {
		select {
		case code := <-codes:
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200: a queued request must be served, not rejected", code)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("queued request never completed")
		}
	}
	if got := adm.Active(); got != 0 {
		t.Fatalf("active at quiesce = %d, want 0", got)
	}
}

// TestProxyCloseWakesQueuedRequests is the forced-retirement case. A parked
// request keeps its generation in-flight, but retirement is bounded by a grace
// after which Close runs anyway and shuts the transport. Without this wakeup the
// request would be granted a slot onto a transport that no longer exists.
func TestProxyCloseWakesQueuedRequests(t *testing.T) {
	block := make(chan struct{})
	var unblock sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	defer unblock.Do(func() { close(block) })

	h, adm := admissionProxy(t, backend.Listener.Addr().String(), &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: 4,
	})

	go h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	waitActive(t, adm, 1)

	queued := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		queued <- rec.Code
	}()
	waitPendingAdmission(t, adm, 1)

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case code := <-queued:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 after forced retirement", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request parked in the pending queue was not woken by generation retirement")
	}
}

// TestProxyCloseIsIdempotent pins that the retirement wakeup can run twice: the
// server closes a generation from both the drain path and the grace timeout.
func TestProxyCloseIsIdempotent(t *testing.T) {
	h, _ := admissionProxy(t, "127.0.0.1:1", nil)
	if err := h.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func waitActive(t *testing.T, a *upstream.Admission, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Active() != n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for active=%d (have %d)", n, a.Active())
		}
		time.Sleep(time.Millisecond)
	}
}

func waitPendingAdmission(t *testing.T, a *upstream.Admission, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Pending() != n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for pending=%d (have %d)", n, a.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}
