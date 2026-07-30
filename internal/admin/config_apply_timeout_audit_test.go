// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"jul/internal/config"
)

// TestManagedWriteRoutesRecordTimeoutAudit asserts that every managed
// configuration write route records a dedicated failure audit event naming the
// timed-out preflight phase when the coordinator returns a pre-persistence
// timeout result (AC-08 / defect 9). The audit operation is the per-handler
// managed operation so the trail distinguishes which mutation path timed out,
// and the apply ID is included only when the coordinator allocated one before
// the timeout. The 504 status is the shared status mapping's responsibility and
// is asserted alongside so the audit is proven on the real production path.
func TestManagedWriteRoutesRecordTimeoutAudit(t *testing.T) {
	const phase = "preflight_handlers"

	valid := validTOML(t, "./public", ":8080")
	patchBody, err := json.Marshal(patchApplyRequest{Ops: []patchRequest{{
		Op:        "route_set_target",
		Listen:    ":8080",
		MatchType: "prefix",
		Path:      "/",
		Target:    "http://127.0.0.1:9999",
	}}})
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	settingsBody := []byte(`{"log_level":"debug"}`)

	cases := []struct {
		name    string
		path    string
		want    ApplyOperation
		applyID string // non-empty proves apply_id inclusion; empty proves omission
		body    func(s *Server, cfgPath string) []byte
	}{
		{
			name:    "apply",
			path:    "/api/config/apply",
			want:    ApplyOperationConfigApply,
			applyID: "rl_timeout_apply",
			body:    func(_ *Server, _ string) []byte { return valid },
		},
		{
			name:    "legacy_raw",
			path:    "/api/config/raw",
			want:    ApplyOperationLegacyRaw,
			applyID: "rl_timeout_raw",
			body:    func(_ *Server, _ string) []byte { return valid },
		},
		{
			name:    "settings",
			path:    "/api/config/settings",
			want:    ApplyOperationSettings,
			applyID: "", // realistic pre-persistence timeout carries no apply ID
			body:    func(_ *Server, _ string) []byte { return settingsBody },
		},
		{
			name:    "patch",
			path:    "/api/config/patch/apply",
			want:    ApplyOperationPatchApply,
			applyID: "", // realistic pre-persistence timeout carries no apply ID
			body:    func(_ *Server, _ string) []byte { return patchBody },
		},
		{
			name:    "rollback",
			path:    "/api/config/rollback",
			want:    ApplyOperationRollback,
			applyID: "rl_timeout_rollback",
			body: func(s *Server, cfgPath string) []byte {
				seed, readErr := os.ReadFile(cfgPath)
				if readErr != nil {
					t.Fatalf("read seed: %v", readErr)
				}
				entryID, snapErr := s.hist.snapshot(seed)
				if snapErr != nil {
					t.Fatalf("snapshot: %v", snapErr)
				}
				body, _ := json.Marshal(map[string]string{"id": entryID})
				return body
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cfgPath := v2WriteServer(t)

			timeout := func(mode string) ConfigApplyResult {
				return ConfigApplyResult{
					OK:            false,
					Mode:          mode,
					ApplyID:       tc.applyID,
					TimedOutPhase: phase,
					Message:       "exceeded reload_timeout during the " + phase + " phase",
				}
			}
			s.deps.ApplyConfigRaw = func(_ ApplyRequestContext, _ []byte, mode string) (ConfigApplyResult, error) {
				return timeout(mode), nil
			}
			s.deps.ApplyConfig = func(_ ApplyRequestContext, _ *config.Config, mode string) (ConfigApplyResult, error) {
				return timeout(mode), nil
			}

			body := tc.body(s, cfgPath)
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body)))

			if rr.Code != http.StatusGatewayTimeout {
				t.Fatalf("status for %s = %d, want 504; body: %s", tc.path, rr.Code, rr.Body.String())
			}

			events := s.audit.snapshot(string(tc.want), "failure", 0)
			var found *AuditEvent
			for i := range events {
				if strings.Contains(events[i].Detail, "timed out before persistence") {
					found = &events[i]
					break
				}
			}
			if found == nil {
				t.Fatalf("no timeout audit for %s; failure events: %+v", tc.path, events)
			}
			if !strings.Contains(found.Detail, "phase="+phase) {
				t.Fatalf("audit detail %q does not name the timed-out phase %q", found.Detail, phase)
			}
			if tc.applyID != "" {
				if !strings.Contains(found.Detail, "apply_id="+tc.applyID) {
					t.Fatalf("audit detail %q omits apply_id=%s", found.Detail, tc.applyID)
				}
			} else if strings.Contains(found.Detail, "apply_id=") {
				t.Fatalf("audit detail %q includes an apply_id when the coordinator allocated none", found.Detail)
			}
		})
	}
}
