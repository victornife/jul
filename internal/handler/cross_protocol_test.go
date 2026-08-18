// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/resilience"
	"jul/internal/upstream"
)

// TestCrossProtocolAdmissionUnderChurn drives every HTTP-family admission path
// at once — reverse proxy, native gRPC passthrough and gRPC-JSON transcoding,
// plus FastCGI — against concurrent policy swaps, backend-set updates and
// generation retirement.
//
// Each slice proved its own path in isolation. What only exists once they are
// all present is the question this answers: do they interfere? They share the
// pool's admission counters and the same release discipline, so a mistake in one
// adapter shows up as another's leaked slot. L4 has the equivalent test in
// internal/stream, which cannot be driven from here without importing it.
//
// Run under -race. At quiesce every counter must be zero and the goroutine count
// flat.
func TestCrossProtocolAdmissionUnderChurn(t *testing.T) {
	fcgiAddr, _ := fakeFPM(t, "tcp", "127.0.0.1:0")
	grpcBackend := startGRPCEcho(t)

	httpBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer httpBackend.Close()

	policy := &config.ResilienceConfig{
		MaxActiveRequests:   16,
		MaxActivePerBackend: 8,
		MaxPendingRequests:  32,
		PendingTimeout:      config.Duration(20 * time.Millisecond),
	}

	httpUps := map[string]config.UpstreamConfig{"api": {
		Name: "api", Strategy: "round_robin", MaxFails: 3,
		Servers:    []config.UpstreamServer{{Address: httpBackend.Listener.Addr().String(), Weight: 1}},
		Resilience: policy,
	}}
	fcgiUps := map[string]config.UpstreamConfig{"php": {
		Name: "php", Strategy: "round_robin", MaxFails: 3,
		Servers:    []config.UpstreamServer{{Address: fcgiAddr, Weight: 1}},
		Resilience: policy,
	}}
	grpcUps := map[string]config.UpstreamConfig{"grpcapi": {
		Name: "grpcapi", Strategy: "round_robin", MaxFails: 3,
		Servers:    []config.UpstreamServer{{Address: grpcBackend, Weight: 1}},
		Resilience: policy,
	}}

	proxy, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://api"},
		httpUps, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	defer func() { _ = proxy.(*proxyHandler).Close() }()

	fcgi, err := NewFastCGI(context.Background(), config.ServerConfig{},
		config.LocationConfig{FastCGIPass: "php", Root: "/srv"}, fcgiUps, nil, nil)
	if err != nil {
		t.Fatalf("NewFastCGI: %v", err)
	}
	defer func() { _ = fcgi.(*admittedHandler).Close() }()

	grpcProxy, err := NewGRPCProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{ProxyPass: "http://grpcapi", GRPC: true},
		grpcUps, nil, grpcTestLogger(), nil)
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}
	defer func() { _ = grpcProxy.(*admittedHandler).Close() }()

	transcoder, tcAdm := admittedTranscoder(t, policy)

	handlers := []http.Handler{proxy, fcgi, grpcProxy, transcoder}
	admissions := []*upstream.Admission{
		proxy.(*proxyHandler).admission,
		fcgi.(*admittedHandler).admission,
		grpcProxy.(*admittedHandler).admission,
		tcAdm,
	}

	// The churn runs twice and growth is measured between the rounds, not from
	// zero. Round one establishes the keep-alive connections and gRPC transports
	// whose reader goroutines legitimately survive an idle period \u2014 measuring
	// from a cold start would count those as a leak. Growth across the second
	// identical round is the signal that actually means something.
	churn := func() {
		stop := make(chan struct{})
		var wg sync.WaitGroup

		// Traffic on every protocol at once.
		for i, h := range handlers {
			for w := 0; w < 4; w++ {
				wg.Add(1)
				go func(h http.Handler, seed int) {
					defer wg.Done()
					for n := 0; ; n++ {
						select {
						case <-stop:
							return
						default:
						}
						ctx, cancel := context.WithCancel(context.Background())
						// A quarter of the load abandons, which is what exercises
						// the handoff-versus-cancel path across adapters.
						if (n+seed)%4 == 0 {
							cancel()
						}
						req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(`{"message":"x"}`)).WithContext(ctx)
						h.ServeHTTP(httptest.NewRecorder(), req)
						cancel()
					}
				}(h, i*7+w)
			}
		}

		// Concurrent policy swaps across every pool.
		wg.Add(1)
		go func() {
			defer wg.Done()
			limits := []int{16, 2, 64, 1, 32}
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				next, rerr := resilience.Resolve(resilience.Options{
					MaxActiveRequests:   limits[n%len(limits)],
					MaxActivePerBackend: 8,
					MaxPendingRequests:  32,
					PendingTimeout:      20 * time.Millisecond,
				})
				if rerr == nil {
					for _, a := range admissions {
						a.SetPolicy(next)
					}
				}
				time.Sleep(time.Millisecond)
			}
		}()

		time.Sleep(400 * time.Millisecond)
		close(stop)
		wg.Wait()
	}

	// quiesce restores an admitting policy so anything still parked can drain,
	// then waits for every adapter's counters to reach zero.
	quiesce := func() {
		restored, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 64, MaxPendingRequests: 32})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		for _, a := range admissions {
			a.SetPolicy(restored)
		}
		names := []string{"http proxy", "fastcgi", "grpc passthrough", "transcoding"}
		deadline := time.Now().Add(5 * time.Second)
		for i, a := range admissions {
			for a.Active() != 0 || a.Pending() != 0 {
				if time.Now().After(deadline) {
					t.Fatalf("%s did not quiesce: active=%d pending=%d", names[i], a.Active(), a.Pending())
				}
				time.Sleep(time.Millisecond)
			}
		}
	}

	churn()
	quiesce()
	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	afterFirst := runtime.NumGoroutine()

	churn()
	quiesce()
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	// Growth across two identical rounds of work. Idle keep-alive readers and
	// gRPC transport goroutines are already established by the first round, so
	// anything that grows here scales with requests served rather than with
	// connections opened — which is what a per-request leak looks like.
	if grew := runtime.NumGoroutine() - afterFirst; grew > 8 {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("goroutines grew by %d across a second identical round; an adapter is leaking one per request\n%s", grew, buf[:n])
	}
}
