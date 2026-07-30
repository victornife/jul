// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// TestManagedApplyFinalizationAdvisoryNonReadiness is the WS02 §3.9 contract
// test for the advisory managed-apply finalization health. It proves the
// finalization advisory is surfaced in the runtime overview as
// managed_apply_finalization AND that it is deliberately INDEPENDENT of
// readiness: a Healthy=false finalization advisory NEVER makes /readyz fail and
// NEVER populates the readiness-gating AdminHealth projection. It drives the
// real route stack (s.routes()) over httptest — no seam bypass — reading the
// advisory through the production Deps.ManagedApplyFinalizationHealth hook the
// composition root wires.
func TestManagedApplyFinalizationAdvisoryNonReadiness(t *testing.T) {
	var ready atomic.Bool
	ready.Store(true)
	// advisory is the single source the production overview reads through
	// Deps.ManagedApplyFinalizationHealth. It starts unset (no apply finalized).
	var advisory atomic.Pointer[ManagedApplyAdvisory]

	cfg := &config.Config{
		Servers: []config.ServerConfig{{
			Locations: []config.LocationConfig{{ProxyPass: "http://localhost:9000"}},
		}},
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		Product:    "Jul.IA",
		Version:    "9.9.9",
		Ready:      ready.Load,
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
		// AdminHealth is healthy: the finalization advisory MUST NOT flow through
		// this readiness-gating hook (§3.9). If a future regression routed the
		// finalization degradation here, /readyz would fail and this test would
		// catch it.
		AdminHealth: func() error { return nil },
		ManagedApplyFinalizationHealth: func() *ManagedApplyAdvisory {
			return advisory.Load()
		},
	})
	h := s.routes()

	overview := func(t *testing.T) RuntimeOverview {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("overview status = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		var out RuntimeOverview
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode overview: %v", err)
		}
		return out
	}
	readyzCode := func(t *testing.T) int {
		t.Helper()
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		return rr.Code
	}

	// 1. No managed apply has finalized: the advisory field is omitted, readiness
	//    is 200, and admin_health is absent.
	if out := overview(t); out.ManagedApplyFinalization != nil {
		t.Errorf("managed_apply_finalization present before any apply finalized: %+v", out.ManagedApplyFinalization)
	}
	if code := readyzCode(t); code != http.StatusOK {
		t.Fatalf("readyz before any apply = %d, want 200", code)
	}

	// 2. A DEGRADED finalization advisory is surfaced in the overview but MUST
	//    NOT gate readiness and MUST NOT appear as an admin_health degradation.
	advisory.Store(&ManagedApplyAdvisory{
		Healthy: false,
		At:      time.Now().UTC(),
		ApplyID: "rl_boot_42",
		Detail:  "complete terminal ledger: disk full",
	})
	out := overview(t)
	if out.ManagedApplyFinalization == nil {
		t.Fatal("degraded finalization advisory not surfaced in runtime overview")
	}
	if out.ManagedApplyFinalization.Healthy {
		t.Errorf("advisory Healthy = true, want false")
	}
	if out.ManagedApplyFinalization.ApplyID != "rl_boot_42" {
		t.Errorf("advisory apply_id = %q, want rl_boot_42", out.ManagedApplyFinalization.ApplyID)
	}
	if out.ManagedApplyFinalization.Detail != "complete terminal ledger: disk full" {
		t.Errorf("advisory detail = %q, want the finalization detail", out.ManagedApplyFinalization.Detail)
	}
	if out.AdminHealth != nil {
		t.Errorf("admin_health present for a finalization advisory (§3.9 forbids readiness coupling): %+v", out.AdminHealth)
	}
	// The core §3.9 invariant: a degraded finalization advisory does NOT fail
	// readiness.
	if code := readyzCode(t); code != http.StatusOK {
		t.Fatalf("readyz with degraded finalization advisory = %d, want 200 (advisory is non-readiness)", code)
	}

	// 3. A subsequent clean finalize publishes a HEALTHY advisory, which the
	//    overview reflects; readiness stays 200 throughout.
	advisory.Store(&ManagedApplyAdvisory{
		Healthy: true,
		At:      time.Now().UTC(),
		ApplyID: "rl_boot_43",
	})
	out = overview(t)
	if out.ManagedApplyFinalization == nil || !out.ManagedApplyFinalization.Healthy {
		t.Fatalf("healthy advisory not surfaced after clean finalize: %+v", out.ManagedApplyFinalization)
	}
	if out.ManagedApplyFinalization.Detail != "" {
		t.Errorf("healthy advisory carried a detail = %q, want empty", out.ManagedApplyFinalization.Detail)
	}
	if code := readyzCode(t); code != http.StatusOK {
		t.Fatalf("readyz after clean finalize = %d, want 200", code)
	}
}
