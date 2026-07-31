// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/rbac"
)

// The four credentials exercised by the role-matrix journey: three named
// principals plus the legacy shared token folded into the policy (compatibility
// mode 2 — RBAC enabled with the legacy token still honoured).
const (
	rbacFlowViewerTok = "viewer-flow-token-32-chars-padded"
	rbacFlowOpTok     = "operator-flow-token-32-chars-pad-"
	rbacFlowAdminTok  = "admin-flow-token-32-chars-padded-"
	rbacFlowLegacyTok = "legacy-flow-token-32-chars-padded"
)

// rbacFlowServer builds a file-backed admin server (real atomic write path plus
// history) with RBAC enabled and the three predefined principals, plus the
// legacy shared token folded into the policy. Because apply and rollback
// genuinely persist, the role journey below is a true end-to-end exercise of
// the server the Console talks to rather than an isolated authz probe.
func rbacFlowServer(t *testing.T) *Server {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(cfgPath, seed, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		WriteConfigRaw: func(data []byte) error {
			c, err := config.Parse(data)
			if err != nil {
				return err
			}
			if err := config.Validate(c); err != nil {
				return err
			}
			return atomicfile.Write(cfgPath, data, 0o600)
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	cfg := config.AdminConfig{
		HistoryDir:          t.TempDir(),
		HistoryKeep:         50,
		Token:               rbacFlowLegacyTok,
		PluginUploadMaxSize: 4, // enable upload so a denied role's 403 is authz, not a disabled handler
	}
	s := newTestServer(t, cfg, deps)
	pol, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{
		{Name: "viewer", Role: rbac.RoleViewer, Token: rbacFlowViewerTok},
		{Name: "op", Role: rbac.RoleOperator, Token: rbacFlowOpTok},
		{Name: "root", Role: rbac.RoleAdmin, Token: rbacFlowAdminTok},
	}, rbacFlowLegacyTok, time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s.UpdatePolicy(pol)
	return s
}

// assertAuthz asserts the RBAC gate outcome for a request. A denied role must
// receive exactly 403; an allowed role must not be rejected by authn/authz
// (401/403) — the handler's own status for a benign or empty body is irrelevant
// to the authorization contract under test.
func assertAuthz(t *testing.T, label string, rr *httptest.ResponseRecorder, allowed bool) {
	t.Helper()
	if allowed {
		if rr.Code == http.StatusUnauthorized || rr.Code == http.StatusForbidden {
			t.Errorf("%s (allowed) = %d, want authorized (not 401/403)", label, rr.Code)
		}
		return
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("%s (denied) = %d, want 403", label, rr.Code)
	}
}

// TestConsoleRBACRoleMatrixE2E is the real-server counterpart to the Console's
// role-aware gating (P3-04 §37 "E2E"). It drives the primary operator journey —
// discover identity, read status, read raw config, apply, roll back, upload a
// plugin, and touch an admin-only surface — end-to-end over the live admin
// router for viewer, operator, admin, and the legacy shared token, asserting the
// server enforces exactly the matrix the Console gates on. The Console hides or
// disables these same controls proactively, but the server remains the single
// source of truth, which is what this test pins down.
func TestConsoleRBACRoleMatrixE2E(t *testing.T) {
	cases := []struct {
		name        string
		token       string
		principal   string
		role        string
		legacy      bool
		canRaw      bool
		canApply    bool
		canRollback bool
		canUpload   bool
		canAdmin    bool
	}{
		{"viewer", rbacFlowViewerTok, "viewer", rbac.RoleViewer, false, false, false, false, false, false},
		{"operator", rbacFlowOpTok, "op", rbac.RoleOperator, false, false, true, true, true, false},
		{"admin", rbacFlowAdminTok, "root", rbac.RoleAdmin, false, true, true, true, true, true},
		{"legacy-shared", rbacFlowLegacyTok, "shared", rbac.RoleAdmin, true, true, true, true, true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := rbacFlowServer(t)
			h := s.routes()

			do := func(method, path string, body []byte) *httptest.ResponseRecorder {
				t.Helper()
				var r io.Reader
				if body != nil {
					r = bytes.NewReader(body)
				}
				req := httptest.NewRequest(method, path, r)
				req.Header.Set("Authorization", "Bearer "+tc.token)
				if body != nil {
					req.Header.Set("Content-Type", "application/json")
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				return rr
			}

			// 1. Identity — GET /api/admin/me reflects the caller's own role and
			// the exact permission bits the Console gates on.
			meRR := do(http.MethodGet, "/api/admin/me", nil)
			if meRR.Code != http.StatusOK {
				t.Fatalf("GET /api/admin/me = %d, want 200", meRR.Code)
			}
			var me struct {
				Principal   string   `json:"principal"`
				Role        string   `json:"role"`
				Permissions []string `json:"permissions"`
				Legacy      bool     `json:"legacy"`
			}
			if err := json.Unmarshal(meRR.Body.Bytes(), &me); err != nil {
				t.Fatalf("decode identity: %v", err)
			}
			if me.Principal != tc.principal || me.Role != tc.role || me.Legacy != tc.legacy {
				t.Errorf("identity = {principal:%q role:%q legacy:%v}, want {principal:%q role:%q legacy:%v}",
					me.Principal, me.Role, me.Legacy, tc.principal, tc.role, tc.legacy)
			}
			perms := make(map[string]bool, len(me.Permissions))
			for _, p := range me.Permissions {
				perms[p] = true
			}
			assertPerm := func(perm string, want bool) {
				t.Helper()
				if perms[perm] != want {
					t.Errorf("identity permission %q = %v, want %v", perm, perms[perm], want)
				}
			}
			assertPerm("config:apply", tc.canApply)
			assertPerm("history:rollback", tc.canRollback)
			assertPerm("plugins:upload", tc.canUpload)
			assertPerm("admin:manage", tc.canAdmin)

			// 2. Read status — every authenticated role holds status:read.
			if rr := do(http.MethodGet, "/api/status", nil); rr.Code != http.StatusOK {
				t.Errorf("GET /api/status = %d, want 200", rr.Code)
			}

			// 3. Raw config (config:raw) — only admin-tier roles may read secrets.
			assertAuthz(t, "GET /api/config", do(http.MethodGet, "/api/config", nil), tc.canRaw)

			// 4. Apply a structured edit (config:apply). Allowed roles persist a
			// real change and produce a rollback snapshot.
			applyBody, _ := json.Marshal(patchApplyRequest{
				Ops: []patchRequest{{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9999"}},
			})
			applyRR := do(http.MethodPost, "/api/config/patch/apply", applyBody)
			if tc.canApply {
				if applyRR.Code != http.StatusOK {
					t.Fatalf("apply (allowed) = %d, want 200; body: %s", applyRR.Code, applyRR.Body.String())
				}
			} else if applyRR.Code != http.StatusForbidden {
				t.Fatalf("apply (denied) = %d, want 403", applyRR.Code)
			}

			// 5. Roll back (history:rollback). Denied roles are rejected before
			// the handler, so no snapshot is required to prove the gate.
			if tc.canRollback {
				histRR := do(http.MethodGet, "/api/history", nil)
				if histRR.Code != http.StatusOK {
					t.Fatalf("GET /api/history = %d, want 200", histRR.Code)
				}
				var entries []struct {
					ID string `json:"id"`
				}
				if err := json.Unmarshal(histRR.Body.Bytes(), &entries); err != nil {
					t.Fatalf("decode history: %v", err)
				}
				if len(entries) == 0 {
					t.Fatal("expected a history snapshot after apply")
				}
				rbBody, _ := json.Marshal(map[string]string{"id": entries[0].ID})
				if rr := do(http.MethodPost, "/api/history/rollback", rbBody); rr.Code != http.StatusOK {
					t.Fatalf("rollback (allowed) = %d, want 200; body: %s", rr.Code, rr.Body.String())
				}
			} else {
				rbBody, _ := json.Marshal(map[string]string{"id": "any"})
				if rr := do(http.MethodPost, "/api/history/rollback", rbBody); rr.Code != http.StatusForbidden {
					t.Fatalf("rollback (denied) = %d, want 403", rr.Code)
				}
			}

			// 6. Plugin upload (plugins:upload). Allowed roles pass the gate and
			// reach the handler (which rejects the empty body); the point is that
			// authorization does not turn them away.
			assertAuthz(t, "POST /api/plugins/upload", do(http.MethodPost, "/api/plugins/upload", []byte("{}")), tc.canUpload)

			// 7. Admin-only surface (admin:manage). An operator cannot administer
			// admin-scoped settings while an admin (and the legacy wildcard) can.
			assertAuthz(t, "GET /debug/pprof/", do(http.MethodGet, "/debug/pprof/", nil), tc.canAdmin)
		})
	}
}
