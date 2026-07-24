// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// TestManagedApplyGet_ResponseRules exercises the GET /api/config/applies/{id}
// response contract: 200 for terminal, 202 for pending, 404 for unknown, 400
// for a malformed ID, and Cache-Control: no-store on every response (AC-02).
func TestManagedApplyGet_ResponseRules(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.BeginPending(ManagedApplyRecord{ID: "rl_1", Operation: ApplyOperationConfigApply})
	_ = reg.Complete(ManagedApplyRecord{
		ID:        "rl_2",
		Operation: ApplyOperationPatchApply,
		Result:    ConfigApplyResult{ApplyID: "rl_2"},
	})

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	h := s.routes()

	cases := []struct {
		name     string
		id       string
		wantCode int
	}{
		{"terminal", "rl_2", http.StatusOK},
		{"pending", "rl_1", http.StatusAccepted},
		{"unknown", "rl_9999", http.StatusNotFound},
		{"invalid", "rl_bad", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/config/applies/"+tc.id, nil)
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("code = %d, want %d (body %s)", rr.Code, tc.wantCode, rr.Body.String())
			}
			if got := rr.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

// TestManagedApplyGet_TerminalBody proves the terminal record is serialized with
// its state and result and omits actor/source IP (AC-02 public projection).
func TestManagedApplyGet_TerminalBody(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	_ = reg.Complete(ManagedApplyRecord{
		ID:                "rl_5",
		Operation:         ApplyOperationRollback,
		Result:            ConfigApplyResult{ApplyID: "rl_5"},
		HistorySnapshotID: "snap-1",
	})

	s := newTestServer(t, config.AdminConfig{}, Deps{ManagedApplies: reg})
	h := s.routes()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_5", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["state"] != string(ManagedApplyTerminal) {
		t.Errorf("state = %v, want terminal", got["state"])
	}
	if got["operation"] != string(ApplyOperationRollback) {
		t.Errorf("operation = %v, want %s", got["operation"], ApplyOperationRollback)
	}
	if got["history_snapshot_id"] != "snap-1" {
		t.Errorf("history_snapshot_id = %v, want snap-1", got["history_snapshot_id"])
	}
	// The public projection must never leak actor or source IP.
	for _, forbidden := range []string{"actor", "source_ip", "token_digest"} {
		if _, present := got[forbidden]; present {
			t.Errorf("public record leaked %q", forbidden)
		}
	}
}

// TestManagedApplyGet_NilRegistry proves a nil ledger yields 404 for all IDs
// rather than panicking (AC-02).
func TestManagedApplyGet_NilRegistry(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/rl_1", nil)
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("code = %d, want 404", rr.Code)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
