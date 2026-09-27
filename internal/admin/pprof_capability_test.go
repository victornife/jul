// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// pprof_enabled follows the admin runtime generation, and the profiler route
// keeps its own gate: disabled means 404 even for an authorized caller (#445).
func TestPprofCapabilityFollowsTheAdminGeneration(t *testing.T) {
	cfg := config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"}
	srv := New(cfg, testLogger(t), Deps{})
	if st := srv.adminRuntimeStatus(nil); st == nil || !st.PprofEnabled {
		t.Fatalf("omitted admin.pprof defaults to enabled: %#v", st)
	}

	cfg.PprofEnabled = config.Bool(false)
	srv.UpdateLiveAdminConfig(cfg)
	if st := srv.adminRuntimeStatus(nil); st == nil || st.PprofEnabled {
		t.Fatalf("admin.pprof = false reported as enabled: %#v", st)
	}
	req := httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rr := httptest.NewRecorder()
	srv.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled profiler status = %d, want 404", rr.Code)
	}
}
