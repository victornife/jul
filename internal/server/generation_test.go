// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// TestRedactionGenerationRegistryUnionAndRetire verifies the per-generation
// redaction registry used by Publish: secrets are masked while any active
// generation references them and pruned only when that generation drains.
func TestRedactionGenerationRegistryUnionAndRetire(t *testing.T) {
	s := &Server{redactGens: make(map[uint64]redact.State)}

	gen1 := redact.NewState([]string{"old-secret", "shared-secret"}, redact.DefaultMinLen)
	gen2 := redact.NewState([]string{"new-secret", "shared-secret"}, redact.DefaultMinLen)

	merged1 := s.registerRedactionGen(1, gen1)
	if merged1.Apply("old-secret") != "***" || merged1.Apply("shared-secret") != "***" {
		t.Error("generation 1 secrets not masked after register")
	}

	merged2 := s.registerRedactionGen(2, gen2)
	for _, secret := range []string{"old-secret", "new-secret", "shared-secret"} {
		if merged2.Apply(secret) != "***" {
			t.Errorf("%q not masked in union of active generations", secret)
		}
	}

	remaining := s.retireRedactionGen(1)
	if remaining.Apply("old-secret") != "old-secret" {
		t.Error("old-secret still masked after generation 1 retired")
	}
	for _, secret := range []string{"new-secret", "shared-secret"} {
		if remaining.Apply(secret) != "***" {
			t.Errorf("%q not masked after generation 1 retired", secret)
		}
	}
}

// TestDynamicHandlerInstallsGenerationSnapshots verifies that dynamicHandler
// attaches the generation-scoped pool snapshots to the request context before
// dispatching to the address handler (R7-03).
func TestDynamicHandlerInstallsGenerationSnapshots(t *testing.T) {
	reg := upstream.NewRegistry(upstream.RegistryOptions{})
	reg.Begin()
	pool, err := reg.For(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:8001"}},
	}, "http")
	if err != nil {
		t.Fatalf("registry.For: %v", err)
	}
	reg.Commit()

	snap := pool.Snapshot()
	s := &Server{redactGens: make(map[uint64]redact.State)}
	s.handlers.Store(newHandlerGen(map[string]http.Handler{
		":80": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count := len(pool.BackendsCtx(r.Context()))
			_, _ = io.WriteString(w, itoa(count))
		}),
	}, map[string]*upstream.PoolSnapshot{"api": snap}, 1))

	rec := httptest.NewRecorder()
	s.dynamicHandler(":80").ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if got, want := rec.Body.String(), "1"; got != want {
		t.Errorf("got %q backends, want %q from generation snapshot", got, want)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
