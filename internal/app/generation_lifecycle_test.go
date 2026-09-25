// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycletest"
)

type recordingStager struct{ begins, commits, aborts int }

func (s *recordingStager) Begin()  { s.begins++ }
func (s *recordingStager) Commit() { s.commits++ }
func (s *recordingStager) Abort()  { s.aborts++ }

// TestGenerationOwnershipInvariants states the generation-owned closer
// contract of docs/resource-ownership.md in lifecycle vocabulary.
func TestGenerationOwnershipInvariants(t *testing.T) {
	stager := &recordingStager{}
	res := NewGenerationResources(stager)

	live := lifecycletest.NewResource("gen1")
	g1 := res.Begin()
	g1.Stage(live)
	g1.Commit()()

	// A failed candidate closes its own resources and never the live ones.
	candidate := lifecycletest.NewResource("failed-candidate")
	g2 := res.Begin()
	g2.Stage(candidate)
	g2.Abort()
	g2.Abort()
	lifecycletest.AssertClosedOnce(t, candidate)
	lifecycletest.AssertOpen(t, live)
	if err := live.Acquire(); err != nil {
		t.Fatalf("live resource unusable after candidate abort: %v", err)
	}

	// A replacement retires its predecessor only through the retire callback
	// (invoked after drain), and exactly once even if the callback repeats.
	next := lifecycletest.NewResource("gen3")
	g3 := res.Begin()
	g3.Stage(next)
	retire := g3.Commit()
	lifecycletest.AssertOpen(t, live)
	retire()
	retire()
	lifecycletest.AssertClosedOnce(t, live)
	if err := live.Acquire(); err == nil {
		t.Fatal("a retired resource was newly acquired")
	}
	lifecycletest.AssertOpen(t, next)

	// Commit after Commit and Abort after Commit never touch the live set.
	g3.Commit()()
	g3.Abort()
	lifecycletest.AssertOpen(t, next)

	res.CloseLive()
	lifecycletest.AssertClosedOnce(t, next)
	if stager.begins != 3 || stager.commits != 2 || stager.aborts != 1 {
		t.Fatalf("pool staging = %+v, want 3 begins, 2 commits, 1 abort", *stager)
	}
}

// churnConfig builds a generation with a generation-owned proxy transport, a
// static root directory handle and a health-checked upstream pool whose
// identity changes with variant, so every commit replaces the pool.
func churnConfig(t *testing.T, backend, root string, variant int) *config.Config {
	t.Helper()
	raw := fmt.Sprintf(`
[[upstreams]]
name = "churn"
[[upstreams.servers]]
address = %q
[upstreams.health_check]
enabled = true
path = "/healthz"
interval = "%dms"
timeout = "50ms"

[[servers]]
listen = "127.0.0.1:0"
[[servers.locations]]
match = { type = "prefix", path = "/api" }
proxy_pass = "http://churn"
[[servers.locations]]
match = { type = "prefix", path = "/" }
root = %q
`, backend, 1000+variant, root)
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse churn config: %v", err)
	}
	return cfg
}

// TestFactoryChurnReturnsToQuiescence drives repeated Prepare->Abort and
// Prepare->Commit->retire cycles and proves goroutines (health checkers,
// transport readers) and FDs (static roots, connections) return to baseline.
func TestFactoryChurnReturnsToQuiescence(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	addr := strings.TrimPrefix(backend.URL, "http://")

	f, cleanup := minimalFactory(t)
	base := lifecycletest.Take()
	var retireLive func()
	for i := 0; i < 20; i++ {
		_, _, _, abort, err := f.Prepare(context.Background(), churnConfig(t, addr, root, i))
		if err != nil {
			t.Fatalf("prepare %d: %v", i, err)
		}
		abort()

		handlers, _, commit, _, err := f.Prepare(context.Background(), churnConfig(t, addr, root, 100+i))
		if err != nil {
			t.Fatalf("prepare commit %d: %v", i, err)
		}
		_, retire := commit()
		if i == 0 && lifecycletest.Take().Goroutines <= base.Goroutines {
			t.Fatal("committed generation started no health checker; churn would prove nothing")
		}
		// Leave an idle keep-alive connection in this generation's proxy
		// transport; only its retirement releases the connection's goroutines
		// and FD, which the GC cannot reclaim on its own.
		for _, h := range handlers {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("proxy request status %d", rec.Code)
			}
		}
		if retireLive != nil {
			retireLive()
		}
		retireLive = retire
	}
	if retireLive != nil {
		retireLive()
	}
	cleanup()

	lifecycletest.AwaitQuiescence(t, base, 2)
}
