// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// expiredRequest builds a request whose context deadline has already elapsed, so
// handler-side candidate resolution observes context.DeadlineExceeded without any
// sleep or slow provider.
func expiredRequest(method, path string, body []byte) *http.Request {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx)
}

func seedRollbackBody(t *testing.T, s *Server, cfgPath string) []byte {
	t.Helper()
	seed, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seed: %v", err)
	}
	entryID, err := s.hist.snapshot(seed)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"id": entryID})
	return body
}

// TestCandidateResolutionDeadlineIsTimeout proves that when handler-side
// candidate resolution exceeds the absolute managed-apply deadline BEFORE the
// coordinator is entered, the structured-patch, settings, and both rollback
// routes classify it as a 504 resolve-phase timeout — not a 400 validation
// error — persist nothing, and record exactly one resolve-phase failure audit
// (AC-08). Unlike the existing coordinator-mock timeout test this exercises the
// deadline expiring in the candidate-preparation stage itself.
func TestCandidateResolutionDeadlineIsTimeout(t *testing.T) {
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
		name string
		path string
		op   ApplyOperation
		body func(s *Server, cfgPath string) []byte
	}{
		{"patch", "/api/config/patch/apply", ApplyOperationPatchApply, func(*Server, string) []byte { return patchBody }},
		{"settings", "/api/config/settings", ApplyOperationSettings, func(*Server, string) []byte { return settingsBody }},
		{"rollback_v2", "/api/config/rollback", ApplyOperationRollback, func(s *Server, cfgPath string) []byte { return seedRollbackBody(t, s, cfgPath) }},
		{"rollback_v1", "/api/history/rollback", ApplyOperationRollback, func(s *Server, cfgPath string) []byte { return seedRollbackBody(t, s, cfgPath) }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, cfgPath := v2WriteServer(t)
			// A resolution deadline must be classified before persistence: the
			// coordinator must never be entered.
			s.deps.ApplyConfigRaw = func(ApplyRequestContext, []byte, string) (ConfigApplyResult, error) {
				t.Error("coordinator (ApplyConfigRaw) entered despite a resolution deadline")
				return ConfigApplyResult{}, nil
			}
			s.deps.ApplyConfig = func(ApplyRequestContext, *config.Config, string) (ConfigApplyResult, error) {
				t.Error("coordinator (ApplyConfig) entered despite a resolution deadline")
				return ConfigApplyResult{}, nil
			}

			body := tc.body(s, cfgPath)
			before, _ := os.ReadFile(cfgPath)

			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, expiredRequest(http.MethodPost, tc.path, body))

			if rr.Code != http.StatusGatewayTimeout {
				t.Fatalf("status = %d, want 504; body: %s", rr.Code, rr.Body.String())
			}
			var result ConfigApplyResult
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatalf("decode: %v; body %s", err, rr.Body.String())
			}
			if result.TimedOutPhase != "resolve" {
				t.Errorf("timed_out_phase = %q, want resolve", result.TimedOutPhase)
			}
			if result.OK {
				t.Error("result.OK = true, want false")
			}
			if result.Persisted {
				t.Error("result.Persisted = true, want false (nothing may be written)")
			}
			if result.ApplyID != "" {
				t.Errorf("apply_id = %q, want empty (coordinator allocated none)", result.ApplyID)
			}

			after, _ := os.ReadFile(cfgPath)
			if !bytes.Equal(before, after) {
				t.Error("on-disk configuration changed despite a resolution timeout")
			}

			var timeoutAudits int
			for _, ev := range s.audit.snapshot(string(tc.op), "failure", 0) {
				if strings.Contains(ev.Detail, "phase=resolve") {
					timeoutAudits++
				}
			}
			if timeoutAudits != 1 {
				t.Fatalf("resolve-phase failure audits = %d, want exactly 1", timeoutAudits)
			}
			if success := s.audit.snapshot(string(tc.op), "success", 0); len(success) != 0 {
				t.Errorf("unexpected success audit on timeout: %+v", success)
			}
		})
	}
}

// TestCandidateResolutionCancellationIsNotValidation proves an already-canceled
// request is classified as a 408 client abort — never a 400 validation failure —
// and persists nothing (AC-08).
func TestCandidateResolutionCancellationIsNotValidation(t *testing.T) {
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

	s, cfgPath := v2WriteServer(t)
	s.deps.ApplyConfig = func(ApplyRequestContext, *config.Config, string) (ConfigApplyResult, error) {
		t.Error("coordinator entered despite a canceled request")
		return ConfigApplyResult{}, nil
	}

	before, _ := os.ReadFile(cfgPath)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(patchBody)).WithContext(ctx)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusRequestTimeout {
		t.Fatalf("status = %d, want 408; body: %s", rr.Code, rr.Body.String())
	}
	var result ConfigApplyResult
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v; body %s", err, rr.Body.String())
	}
	if result.OK {
		t.Error("result.OK = true, want false")
	}
	if result.TimedOutPhase != "" {
		t.Errorf("timed_out_phase = %q, want empty (cancellation is not a timeout)", result.TimedOutPhase)
	}
	if len(result.ValidationErrors) != 0 {
		t.Errorf("validation_errors = %v, want none (cancellation is not a validation failure)", result.ValidationErrors)
	}
	if after, _ := os.ReadFile(cfgPath); !bytes.Equal(before, after) {
		t.Error("on-disk configuration changed despite a canceled request")
	}
}
