// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

// Deterministic fault injection for the resilience scenarios.
//
// This lives in a _test.go file on purpose. ADR 0017 requires the injection to
// be test-only and never reachable from configuration: a production knob that
// makes a backend fail, stall or truncate mid-body is a way to degrade a proxy
// from the config file, which is a worse problem than the one it would help
// diagnose. Keeping it in the test binary makes that structural rather than a
// promise.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// faultMode is what a fault backend does with the next request.
type faultMode int32

const (
	faultNone faultMode = iota
	// faultSlow holds the request until released, standing in for a backend
	// that is up and answering but far slower than its peers.
	faultSlow
	// faultMidBody writes a status and part of a body, then kills the
	// connection. This is the case that must never be retried: a byte has
	// already reached the client.
	faultMidBody
	// faultError answers 502 without touching the body.
	faultError
)

// faultBackend is an HTTP backend whose behaviour can be changed between
// requests, and which counts what it received.
type faultBackend struct {
	*httptest.Server
	mode     atomic.Int32
	requests atomic.Int64
	release  chan struct{}
	closed   atomic.Bool
}

func newFaultBackend(t *testing.T) *faultBackend {
	t.Helper()
	fb := &faultBackend{release: make(chan struct{})}
	fb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb.requests.Add(1)
		switch faultMode(fb.mode.Load()) {
		case faultSlow:
			select {
			case <-fb.release:
			case <-r.Context().Done():
			case <-time.After(10 * time.Second):
			}
			w.WriteHeader(http.StatusOK)
		case faultMidBody:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("partial"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			// Hijack and close: the client has bytes, and then the response
			// stops. A Content-Length was never sent, so this is a truncated
			// chunked body rather than a clean end.
			if hj, ok := w.(http.Hijacker); ok {
				if c, _, err := hj.Hijack(); err == nil {
					_ = c.Close()
				}
			}
		case faultError:
			w.WriteHeader(http.StatusBadGateway)
		case faultNone:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(fb.stop)
	return fb
}

func (f *faultBackend) set(m faultMode) { f.mode.Store(int32(m)) }

// releaseSlow unblocks every request parked by faultSlow.
func (f *faultBackend) releaseSlow() {
	select {
	case <-f.release:
	default:
		close(f.release)
	}
}

// stop makes the backend disappear: the listener goes away and subsequent dials
// are refused rather than hanging.
func (f *faultBackend) stop() {
	if f.closed.CompareAndSwap(false, true) {
		f.releaseSlow()
		f.Close()
	}
}

func (f *faultBackend) addr() string { return f.Listener.Addr().String() }

// refusedAddr is an address nothing listens on, so every dial is refused
// immediately rather than timing out. Port 1 is privileged and unused, which is
// the same convention the rest of the handler tests use.
const refusedAddr = "127.0.0.1:1"

// scenarioProxy builds a proxy over the given backend addresses with the given
// pool resilience policy. It is accountingProxy generalised to more than one
// backend, which every failover scenario needs.
func scenarioProxy(t *testing.T, addrs []string, pool *config.ResilienceConfig, loc *config.LocationResilienceConfig) (http.Handler, *upstream.Admission) {
	t.Helper()
	servers := make([]config.UpstreamServer, 0, len(addrs))
	for _, a := range addrs {
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
	}
	ups := map[string]config.UpstreamConfig{
		"api": {Name: "api", Strategy: "round_robin", Servers: servers, Resilience: pool},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{
			Match:      config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass:  "http://api",
			Resilience: loc,
		}, ups, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	ph := h.(*proxyHandler)
	t.Cleanup(func() { _ = ph.Close() })
	return ph, ph.admission
}
