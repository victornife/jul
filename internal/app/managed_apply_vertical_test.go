// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// TestManagedApplyLifecycle_202PendingThen200Terminal is the WS01 vertical
// lifecycle test (§2.9). It drives a REAL managed apply through the actual
// ConfigApplyCoordinator and the actual admin HTTP handler — not a hand-seeded
// registry — and proves the exact production sequence the workstream exists to
// guarantee (AC-02):
//
//	persist candidate
//	→ enqueue reload
//	→ register exact-ID pending record
//	→ synchronous HTTP path returns a real 202 saved_not_live
//	→ GET /api/config/applies/<id> observes 202 + state=pending (never a 404)
//	→ terminal reload completion is released
//	→ GET the same endpoint observes 200 + state=terminal with matching result
//
// The terminal reload result is held behind a channel so the coordinator's
// bounded synchronous wait expires and returns the provisional saved_not_live
// exactly as it does in production when a reload outlives reload_timeout. No
// sleeps approximate the interleaving: the 202 is observed while the reload is
// demonstrably still in flight (its result is still buffered in the test), and
// the 200 is observed only after the terminal result is delivered and the
// finalizer signals completion.
func TestManagedApplyLifecycle_202PendingThen200Terminal(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// reqCh captures the enqueued reload so the test controls exactly when the
	// terminal result is delivered. Withholding it forces the synchronous
	// ApplyRaw wait to expire and return the provisional saved_not_live 202,
	// while the exact-ID pending ledger record is already registered.
	reqCh := make(chan server.ReloadRequest, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			reqCh <- req
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			// A short reload_timeout bounds the synchronous wait so the 202 is
			// returned promptly without any test sleep.
			cfg.Global.ReloadTimeout = config.Duration(30 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
	}

	// Wire the coordinator to a real admin ManagedApplyRegistry using the EXACT
	// field mapping serve.go installs in production, and observe the terminal
	// finalization through a channel so the test never races the finalizer.
	registry, _, completedCh := wireProductionLedger(c)

	// Build the REAL admin HTTP handler over that same registry. The GET path is
	// served through admin.Server.Handler(), the exact handler the listener
	// serves in production, including route authorization.
	adminSrv := admin.New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"}, nil, admin.Deps{
		ManagedApplies: registry,
	})
	if adminSrv == nil {
		t.Fatal("admin.New returned nil")
	}
	h := adminSrv.Handler()

	// 1. Submit a managed apply. The reload is withheld, so ApplyRaw returns the
	//    provisional saved_not_live 202 after its bounded wait expires.
	startedAt := time.Now().UTC()
	deadline := startedAt.Add(30 * time.Millisecond)
	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: startedAt,
		Deadline:  deadline,
		TokenID:   "tok-owner-vertical",
	}
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.Reload == nil || res.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("result = %+v, want provisional saved_not_live", res.Reload)
	}
	applyID := res.ApplyID
	if applyID == "" {
		t.Fatal("saved_not_live result carried no apply id")
	}
	// The generated ID must be boot-scoped (rl_<12-hex>_<seq>), not a legacy
	// rl_<seq>, so it cannot collide across process restarts.
	if !bootScopedIDPattern.MatchString(applyID) {
		t.Fatalf("apply id %q is not boot-scoped (rl_<12-hex>_<seq>)", applyID)
	}

	// 2-5. GET /api/config/applies/<id> through the real admin handler MUST see
	//      202 + state=pending: a real 202 is never followed by a 404. This is
	//      the exact window the workstream closes.
	pendingCode, pendingBody := getApply(t, h, applyID)
	if pendingCode != http.StatusAccepted {
		t.Fatalf("pending GET code = %d, want 202 (body %+v)", pendingCode, pendingBody)
	}
	if pendingBody["state"] != string(admin.ManagedApplyPending) {
		t.Errorf("pending state = %v, want pending", pendingBody["state"])
	}
	if pendingBody["id"] != applyID {
		t.Errorf("pending id = %v, want %q", pendingBody["id"], applyID)
	}
	if pendingBody["operation"] != string(admin.ApplyOperationConfigApply) {
		t.Errorf("pending operation = %v, want %s", pendingBody["operation"], admin.ApplyOperationConfigApply)
	}
	// The pending response exposes the absolute deadline for deadline-aware
	// polling (AC-08), and never leaks actor/source IP/token digest.
	if _, ok := pendingBody["deadline"]; !ok {
		t.Error("pending response missing deadline for deadline-aware polling")
	}
	for _, forbidden := range []string{"actor", "source_ip", "token_digest", "owner_token_id"} {
		if _, present := pendingBody[forbidden]; present {
			t.Errorf("pending response leaked %q", forbidden)
		}
	}

	// 6. Release the terminal reload completion.
	req := <-reqCh
	req.Result <- server.ReloadResult{
		ID:             applyID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedLive,
		Published:      true,
		DesiredVersion: server.CanonicalVersion(mustParse(t, validConfigRaw(t, ":8081"))),
		ServingVersion: "v2",
	}
	// Wait for the finalizer to durably terminalize the ledger record before
	// polling (no sleep-based interleaving).
	select {
	case fin := <-completedCh:
		if fin.FinalizationError != "" {
			t.Fatalf("unexpected finalization error: %q", fin.FinalizationError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal finalization callback was not called")
	}

	// 7-9. Poll the same endpoint: it MUST now be 200 + state=terminal with the
	//      IDs, operation, and result matching the accepted apply.
	termCode, termBody := getApply(t, h, applyID)
	if termCode != http.StatusOK {
		t.Fatalf("terminal GET code = %d, want 200 (body %+v)", termCode, termBody)
	}
	if termBody["state"] != string(admin.ManagedApplyTerminal) {
		t.Errorf("terminal state = %v, want terminal", termBody["state"])
	}
	if termBody["id"] != applyID {
		t.Errorf("terminal id = %v, want %q", termBody["id"], applyID)
	}
	if termBody["operation"] != string(admin.ApplyOperationConfigApply) {
		t.Errorf("terminal operation = %v, want %s", termBody["operation"], admin.ApplyOperationConfigApply)
	}
	result, ok := termBody["result"].(map[string]any)
	if !ok {
		t.Fatalf("terminal result missing or not an object: %v", termBody["result"])
	}
	if result["apply_id"] != applyID {
		t.Errorf("terminal result apply_id = %v, want %q", result["apply_id"], applyID)
	}
	if result["ok"] != true {
		t.Errorf("terminal result ok = %v, want true", result["ok"])
	}
	// The terminal projection still never leaks actor/source IP/token digest.
	for _, forbidden := range []string{"actor", "source_ip", "token_digest", "owner_token_id"} {
		if _, present := termBody[forbidden]; present {
			t.Errorf("terminal response leaked %q", forbidden)
		}
	}
}

// getApply issues GET /api/config/applies/<id> through the admin handler and
// returns the status code and decoded JSON body. It asserts the Cache-Control:
// no-store contract on every response so a lifecycle poll is never cached.
func getApply(t *testing.T, h http.Handler, id string) (int, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/config/applies/"+id, nil)
	h.ServeHTTP(rr, req)
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var body map[string]any
	if rr.Body.Len() > 0 {
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("unmarshal body: %v (raw %s)", err, rr.Body.String())
		}
	}
	return rr.Code, body
}

// mustParse parses config raw for a canonical-version comparison in the test.
func mustParse(t *testing.T, raw []byte) *config.Config {
	t.Helper()
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}
