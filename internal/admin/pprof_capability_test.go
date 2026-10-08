// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
	"jul/internal/rbac"
)

func TestPprofPolicyUsesCapturedRequestGeneration(t *testing.T) {
	for _, initial := range []bool{false, true} {
		name := "disabled_request_then_enabled"
		if initial {
			name = "enabled_request_then_disabled"
		}
		t.Run(name, func(t *testing.T) {
			cfg := config.AdminConfig{Listen: "127.0.0.1:0", Token: snapLegacyTok, PprofEnabled: config.Bool(initial)}
			server := newTestServer(t, cfg, Deps{})
			var profiler http.Handler
			for _, spec := range Catalog {
				if spec.Pattern == "/debug/pprof/" {
					profiler = spec.Handler(server)
					break
				}
			}
			if profiler == nil {
				t.Fatal("profiler route missing from Catalog")
			}
			handler := server.captureAdminRuntimeSnapshot(server.requirePermission(rbac.AdminManage, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				updated := server.currentAdminConfig()
				updated.PprofEnabled = config.Bool(!initial)
				server.UpdateLiveAdminConfig(updated)
				profiler.ServeHTTP(w, r)
			})))
			request := httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil)
			request.Header.Set("Authorization", "Bearer "+snapLegacyTok)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			want := http.StatusNotFound
			if initial {
				want = http.StatusOK
			}
			if recorder.Code != want {
				t.Fatalf("captured profiler policy: status = %d, want %d", recorder.Code, want)
			}
			recorder = httptest.NewRecorder()
			server.routes().ServeHTTP(recorder, request)
			if initial {
				want = http.StatusNotFound
			} else {
				want = http.StatusOK
			}
			if recorder.Code != want {
				t.Fatalf("new request profiler policy: status = %d, want %d", recorder.Code, want)
			}
		})
	}
}

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
