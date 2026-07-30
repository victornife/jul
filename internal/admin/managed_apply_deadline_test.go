// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/server"
)

// TestManagedApplyBindsAbsoluteDeadlineFromServingReloadTimeout proves the full
// HTTP admission path derives one absolute managed-apply deadline from the
// currently serving reload_timeout at admission (AC-08), carries the request
// context through to the coordinator, and resolves the candidate under that
// bounded context. The candidate carries a different reload_timeout to prove the
// serving value governs the deadline, never the submitted candidate's (R15-01).
func TestManagedApplyBindsAbsoluteDeadlineFromServingReloadTimeout(t *testing.T) {
	const servingTimeout = 7 * time.Second

	live := config.ServeDir("./public", ":8080")
	live.Global.ReloadTimeout = config.Duration(servingTimeout)

	var captured ApplyRequestContext
	var reachedCoordinator bool
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: live}
		},
		ReadConfigRaw: func() ([]byte, error) { return config.Marshal(live) },
		LoadConfig:    func() (*config.Config, error) { return live, nil },
		ApplyConfigRaw: func(ctx ApplyRequestContext, _ []byte, mode string) (ConfigApplyResult, error) {
			captured = ctx
			reachedCoordinator = true
			return ConfigApplyResult{OK: true, Mode: mode, Version: "v", ServingVersion: "v"}, nil
		},
	})

	candidate := config.ServeDir("./public", ":8080")
	candidate.Global.ReloadTimeout = config.Duration(time.Hour)
	candidateRaw, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	before := time.Now().UTC()
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(candidateRaw)))
	after := time.Now().UTC()

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !reachedCoordinator {
		t.Fatal("coordinator was not reached; deadline wiring is unproven")
	}
	if captured.StartedAt.IsZero() {
		t.Fatal("StartedAt was not bound at admission")
	}
	if captured.StartedAt.Before(before) || captured.StartedAt.After(after) {
		t.Fatalf("StartedAt %v outside admission window [%v, %v]", captured.StartedAt, before, after)
	}
	wantDeadline := captured.StartedAt.Add(servingTimeout)
	if !captured.Deadline.Equal(wantDeadline) {
		t.Fatalf("Deadline = %v, want StartedAt + serving reload_timeout (%v)", captured.Deadline, wantDeadline)
	}
	if captured.RequestContext == nil {
		t.Fatal("RequestContext was not carried to the coordinator")
	}
	if captured.Candidate == nil {
		t.Fatal("candidate was not resolved under the bounded context")
	}
	if got := captured.Candidate.Effective.Global.ReloadTimeout.Std(); got != time.Hour {
		t.Fatalf("resolved candidate reload_timeout = %v, want 1h (candidate value preserved)", got)
	}
}

// TestManagedApplyDeadlineOmittedWithoutServingTimeout proves the handler binds
// StartedAt but no bounded deadline when the serving reload_timeout is not
// positive, so callers without a configured timeout keep unbounded behaviour.
func TestManagedApplyDeadlineOmittedWithoutServingTimeout(t *testing.T) {
	live := config.ServeDir("./public", ":8080")
	live.Global.ReloadTimeout = 0

	var captured ApplyRequestContext
	var reachedCoordinator bool
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: live}
		},
		ReadConfigRaw: func() ([]byte, error) { return config.Marshal(live) },
		LoadConfig:    func() (*config.Config, error) { return live, nil },
		ApplyConfigRaw: func(ctx ApplyRequestContext, _ []byte, mode string) (ConfigApplyResult, error) {
			captured = ctx
			reachedCoordinator = true
			return ConfigApplyResult{OK: true, Mode: mode, Version: "v", ServingVersion: "v"}, nil
		},
	})

	candidate := config.ServeDir("./public", ":8080")
	candidate.Global.LogLevel = "debug"
	candidateRaw, err := config.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(candidateRaw)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !reachedCoordinator {
		t.Fatal("coordinator was not reached")
	}
	if captured.StartedAt.IsZero() {
		t.Fatal("StartedAt was not bound at admission")
	}
	if !captured.Deadline.IsZero() {
		t.Fatalf("Deadline = %v, want zero when serving reload_timeout is not positive", captured.Deadline)
	}
	if captured.RequestContext == nil {
		t.Fatal("RequestContext was not carried to the coordinator")
	}
}
