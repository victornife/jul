// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"jul/internal/config"
)

// TestManagedWriteRoutesCarryOperationIdentity asserts that every configuration
// write route stamps the terminal finalizer with the exact initiating
// operation (AC-01). The operation must be assigned by the handler before the
// coordinator is invoked and must never be inferred from URL strings, request
// mode, or response shape.
func TestManagedWriteRoutesCarryOperationIdentity(t *testing.T) {
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
		name   string
		method string
		path   string
		want   ApplyOperation
		body   func(s *Server, cfgPath string) []byte
	}{
		{
			name:   "apply",
			method: http.MethodPost,
			path:   "/api/config/apply",
			want:   ApplyOperationConfigApply,
			body:   func(_ *Server, _ string) []byte { return valid },
		},
		{
			name:   "legacy_raw",
			method: http.MethodPost,
			path:   "/api/config/raw",
			want:   ApplyOperationLegacyRaw,
			body:   func(_ *Server, _ string) []byte { return valid },
		},
		{
			name:   "settings",
			method: http.MethodPost,
			path:   "/api/config/settings",
			want:   ApplyOperationSettings,
			body:   func(_ *Server, _ string) []byte { return settingsBody },
		},
		{
			name:   "patch",
			method: http.MethodPost,
			path:   "/api/config/patch/apply",
			want:   ApplyOperationPatchApply,
			body:   func(_ *Server, _ string) []byte { return patchBody },
		},
		{
			name:   "rollback",
			method: http.MethodPost,
			path:   "/api/config/rollback",
			want:   ApplyOperationRollback,
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

			var captured ApplyOperation
			var seen bool
			s.deps.ApplyConfigRaw = func(ctx ApplyRequestContext, data []byte, mode string) (ConfigApplyResult, error) {
				captured = ctx.Operation
				seen = true
				return ConfigApplyResult{
					OK:             true,
					Mode:           mode,
					Version:        configVersion(data),
					ServingVersion: configVersion(data),
				}, nil
			}
			s.deps.ApplyConfig = func(ctx ApplyRequestContext, c *config.Config, mode string) (ConfigApplyResult, error) {
				captured = ctx.Operation
				seen = true
				data, marshalErr := config.Marshal(c)
				if marshalErr != nil {
					t.Fatalf("marshal candidate: %v", marshalErr)
				}
				return ConfigApplyResult{
					OK:             true,
					Mode:           mode,
					Version:        configVersion(data),
					ServingVersion: configVersion(data),
				}, nil
			}

			body := tc.body(s, cfgPath)
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, httptest.NewRequest(tc.method, tc.path, bytes.NewReader(body)))

			if !seen {
				t.Fatalf("coordinator was not invoked for %s (status=%d body=%s)", tc.path, rr.Code, rr.Body.String())
			}
			if captured != tc.want {
				t.Fatalf("operation for %s = %q, want %q", tc.path, captured, tc.want)
			}
		})
	}
}
