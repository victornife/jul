// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// TestReportManagedApplyErrorPendingRegistration proves the coordinator routes a
// post-persistence pending-registration write failure to the composition root's
// ReportManagedApplyError hook with the bounded phase "pending" (WS06 §7.6). The
// pending notify runs synchronously inside ApplyRaw before the provisional
// saved_not_live 202 is returned, so the report is observable immediately after
// ApplyRaw returns without racing the finalizer goroutine.
func TestReportManagedApplyErrorPendingRegistration(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	type report struct {
		id    string
		phase string
		err   error
	}
	reports := make(chan report, 4)

	reqCh := make(chan server.ReloadRequest, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			// Withhold the terminal result so ApplyRaw returns the provisional
			// saved_not_live 202 after its bounded wait expires.
			reqCh <- req
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(30 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
		// The pending-registration write fails after persistence.
		OnManagedApplyStarted: func(admin.ManagedApplyStart) error {
			return errors.New("ledger unavailable")
		},
		ReportManagedApplyError: func(id, phase string, err error) {
			reports <- report{id: id, phase: phase, err: err}
		},
	}

	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: time.Now().UTC(),
		Deadline:  time.Now().Add(time.Minute).UTC(),
	}
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.ApplyID == "" {
		t.Fatal("apply result carried no apply id")
	}

	select {
	case got := <-reports:
		if got.phase != "pending" {
			t.Fatalf("report phase = %q, want pending", got.phase)
		}
		if got.id != res.ApplyID {
			t.Fatalf("report id = %q, want %q", got.id, res.ApplyID)
		}
		if got.err == nil {
			t.Fatal("report err = nil, want the pending-registration error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending-registration failure was not reported")
	}

	// Drain the withheld reload request so the finalizer goroutine can exit.
	req := <-reqCh
	req.Result <- server.ReloadResult{
		ID:        req.ID,
		Source:    server.ReloadSourceAdmin,
		Outcome:   server.ReloadAppliedLive,
		Published: true,
	}
}

// TestReportManagedApplyErrorRestoration proves the coordinator routes a
// saved_not_live terminal restoration write failure to the composition root's
// ReportManagedApplyError hook with the bounded phase "restoration" (WS06 §7.6).
// The failure is forced deterministically: the beforeRestore barrier rewrites
// the on-disk file so restorePreviousLocked observes a digest that no longer
// matches the expected candidate and returns an error.
func TestReportManagedApplyErrorRestoration(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	type report struct {
		id    string
		phase string
		err   error
	}
	reports := make(chan report, 4)

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
			cfg.Global.ReloadTimeout = config.Duration(30 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
		// Corrupt the on-disk candidate just before restoration so the digest
		// check fails deterministically and restorePreviousLocked returns an
		// error routed to logRestorationFailure.
		beforeRestore: func() {
			_ = os.WriteFile(path, []byte("# superseded by an unrelated write\n"), 0o600)
		},
		ReportManagedApplyError: func(id, phase string, err error) {
			reports <- report{id: id, phase: phase, err: err}
		},
	}

	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: time.Now().UTC(),
		Deadline:  time.Now().Add(time.Minute).UTC(),
	}
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.ApplyID == "" {
		t.Fatal("apply result carried no apply id")
	}

	// Release a not-applied terminal reload result so the finalizer takes the
	// restoration branch.
	req := <-reqCh
	req.Result <- server.ReloadResult{
		ID:          req.ID,
		Source:      server.ReloadSourceAdmin,
		Outcome:     server.ReloadNotApplied,
		FailedPhase: "prepare",
		Error:       "bind failed",
		Published:   false,
	}

	select {
	case got := <-reports:
		if got.phase != "restoration" {
			t.Fatalf("report phase = %q, want restoration", got.phase)
		}
		if got.id != res.ApplyID {
			t.Fatalf("report id = %q, want %q", got.id, res.ApplyID)
		}
		if got.err == nil {
			t.Fatal("report err = nil, want the restoration error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restoration failure was not reported")
	}
}
