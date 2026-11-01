// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/cachemeta"
	"jul/internal/rbac"
)

func TestCacheCutoverReplayPreservesOutcomeAndRejectsChangedChoice(t *testing.T) {
	registry := NewManagedApplyRegistry(0, 0)
	s := &Server{deps: Deps{ManagedApplies: registry, BootID: func() string { return "abcdef123456" }}}
	const applyID = "rl_abcdef123456_1"
	request := func(choice string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply?base_version=v1&cache_cutover="+choice, nil)
		r.Header.Set("Content-Type", "application/toml")
		r.Header.Set("Idempotency-Key", "cutover-choice-624")
		identity := rbac.Identity{Principal: "operator", TokenID: "token", Permissions: []rbac.Permission{rbac.ConfigApply}}
		return r.WithContext(rbac.WithIdentity(r.Context(), identity))
	}
	calls := 0
	handler := func(w http.ResponseWriter, r *http.Request) {
		calls++
		binding := v1IdempotencyFromRequest(r)
		if binding == nil {
			t.Fatal("missing mutation binding")
		}
		if err := registry.BeginPending(ManagedApplyRecord{ID: applyID, Operation: ApplyOperationConfigApply,
			IdempotencyKey: binding.Key, IdempotencyFingerprint: binding.Fingerprint,
			IdempotencyMethod: binding.Method, IdempotencyOperation: binding.Operation, IdempotencyPrincipal: binding.Principal}); err != nil {
			t.Fatal(err)
		}
		result := ConfigApplyResult{ApplyID: applyID, OK: true, Mode: "hot", AppOutcome: "applied_degraded",
			CacheCutoverState: "uncertain", CacheCutoverResult: &cachemeta.CutoverResult{State: "uncertain", InvalidationEffective: "uncertain", RecoveryGuidance: "Configuration is live. Reconcile cache scopes."}}
		if err := registry.Complete(ManagedApplyRecord{ID: applyID, Operation: ApplyOperationConfigApply, Result: result}); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, http.StatusOK, result)
	}
	s.runExternalMutationV1(httptest.NewRecorder(), request("invalidate"), "v1", []byte("same candidate"), handler)
	replay := httptest.NewRecorder()
	s.runExternalMutationV1(replay, request("invalidate"), "v1", []byte("same candidate"), handler)
	var response adminapi.ConfigApplyResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !response.IdempotentReplay || response.CacheCutoverResult == nil || response.CacheCutoverResult.RecoveryGuidance == "" {
		t.Fatalf("replay lost correlated cleanup: calls=%d response=%+v", calls, response)
	}
	changed := httptest.NewRecorder()
	s.runExternalMutationV1(changed, request("retain"), "v1", []byte("same candidate"), handler)
	if calls != 1 || changed.Code != http.StatusConflict {
		t.Fatalf("changed cleanup choice replayed or executed: calls=%d status=%d", calls, changed.Code)
	}
}
