// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func issue160Factory(addr string) HandlerFactory {
	return func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "issue160")
		})
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
}

func TestIssue160SourceReloadPublishesAuditSink(t *testing.T) {
	for _, source := range []ReloadSource{ReloadSourceSIGHUP, ReloadSourceFileWatch} {
		t.Run(source.String(), func(t *testing.T) {
			trafficAddr := freePort(t)
			initial := cfgWith(trafficAddr)
			initial.Admin = config.AdminConfig{
				Enabled:             true,
				Listen:              "127.0.0.1:19090",
				Token:               "issue160-token",
				AuditLogFile:        filepath.Join(t.TempDir(), "a.jsonl"),
				AuditLogRotateMaxMB: 100,
				AuditLogRotateKeep:  14,
			}
			candidate := cfgWith(trafficAddr)
			candidate.Admin = initial.Admin
			candidate.Admin.AuditLogFile = filepath.Join(t.TempDir(), "b.jsonl")
			candidate.Admin.AuditLogRotateMaxMB = 64
			candidate.Admin.AuditLogRotateKeep = 7

			src := &stubSource{}
			src.set(initial, nil)
			srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue160Factory(trafficAddr), src, func(context.Context, *config.Config) error { return nil })
			var prepared atomic.Int64
			var committed atomic.Int64
			var seen config.AdminConfig
			srv.PrepareAdmin = func(cfg config.AdminConfig) (*PreparedCommit, error) {
				seen = cfg
				prepared.Add(1)
				return NewPreparedCommit(func() { committed.Add(1) }, nil), nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			reload := make(chan ReloadRequest, 1)
			done := make(chan error, 1)
			go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
			waitForServe(t, "http://"+trafficAddr+"/", "issue160")

			src.set(candidate, nil)
			resultCh := make(chan ReloadResult, 1)
			reload <- ReloadRequest{ID: "issue160-source-parity", Source: source, Result: resultCh}
			result := <-resultCh
			if result.Outcome != ReloadAppliedLive || !result.Published {
				t.Fatalf("audit-only reload result = %+v, want applied_live/published", result)
			}
			if prepared.Load() != 1 || committed.Load() != 1 {
				t.Fatalf("prepared=%d committed=%d, want one prepare and one commit", prepared.Load(), committed.Load())
			}
			if seen.AuditLogFile != candidate.Admin.AuditLogFile || seen.AuditLogRotateMaxMB != 64 || seen.AuditLogRotateKeep != 7 {
				t.Fatalf("PrepareAdmin saw wrong audit candidate: %+v", seen)
			}
			live := srv.LiveSnapshot().EffectiveConfig
			if live == nil {
				t.Fatal("live snapshot has no effective config")
			}
			if live.Admin.AuditLogFile != candidate.Admin.AuditLogFile || live.Admin.AuditLogRotateMaxMB != 64 || live.Admin.AuditLogRotateKeep != 7 {
				t.Fatalf("published audit settings = %+v", live.Admin)
			}

			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		})
	}
}

func TestIssue160MixedHotAdminFieldsPublishAsOneGeneration(t *testing.T) {
	trafficAddr := freePort(t)
	uploadDisabled := false
	uploadEnabled := true
	consoleEnabled := true
	consoleDisabled := false
	initial := cfgWith(trafficAddr)
	initial.Admin = config.AdminConfig{
		Enabled:              true,
		Listen:               "127.0.0.1:19090",
		Token:                "issue160-token",
		Console:              &consoleEnabled,
		PluginUploadEnabled:  &uploadDisabled,
		PluginUploadDir:      t.TempDir(),
		RateLimitReadPerMin:  240,
		RateLimitWritePerMin: 60,
		RateLimitApplyPerMin: 30,
		MaxEventConns:        4,
		AuditLogFile:         filepath.Join(t.TempDir(), "a.jsonl"),
		AuditLogRotateMaxMB:  100,
		AuditLogRotateKeep:   14,
	}
	candidate := cfgWith(trafficAddr)
	candidate.Admin = initial.Admin
	candidate.Admin.Console = &consoleDisabled
	candidate.Admin.PluginUploadEnabled = &uploadEnabled
	candidate.Admin.PluginUploadDir = t.TempDir()
	candidate.Admin.RateLimitReadPerMin = 17
	candidate.Admin.MaxEventConns = 2
	candidate.Admin.AuditLogFile = filepath.Join(t.TempDir(), "b.jsonl")
	candidate.Admin.AuditLogRotateMaxMB = 32
	candidate.Admin.AuditLogRotateKeep = 5

	src := &stubSource{}
	src.set(initial, nil)
	srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue160Factory(trafficAddr), src, func(context.Context, *config.Config) error { return nil })
	var prepared atomic.Int64
	var committed atomic.Int64
	var seen config.AdminConfig
	srv.PrepareAdmin = func(cfg config.AdminConfig) (*PreparedCommit, error) {
		seen = cfg
		prepared.Add(1)
		return NewPreparedCommit(func() { committed.Add(1) }, nil), nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+trafficAddr+"/", "issue160")

	src.set(candidate, nil)
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "issue160-mixed-hot", Source: ReloadSourceFileWatch, Result: resultCh}
	result := <-resultCh
	if result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("mixed-hot result = %+v", result)
	}
	if prepared.Load() != 1 || committed.Load() != 1 {
		t.Fatalf("prepared=%d committed=%d, want exactly one admin generation", prepared.Load(), committed.Load())
	}
	if seen.Console == nil || *seen.Console || seen.PluginUploadEnabled == nil || !*seen.PluginUploadEnabled || seen.RateLimitReadPerMin != 17 || seen.MaxEventConns != 2 || seen.AuditLogFile != candidate.Admin.AuditLogFile || seen.AuditLogRotateMaxMB != 32 || seen.AuditLogRotateKeep != 5 {
		t.Fatalf("prepared mixed-hot snapshot mismatch: %+v", seen)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestIssue160MixedRestartBoundAndAuditCandidateIsAtomic(t *testing.T) {
	for _, source := range []ReloadSource{ReloadSourceSIGHUP, ReloadSourceFileWatch} {
		t.Run(source.String(), func(t *testing.T) {
			trafficAddr := freePort(t)
			initial := cfgWith(trafficAddr)
			initial.Admin = config.AdminConfig{
				Enabled:             true,
				Listen:              "127.0.0.1:19090",
				Token:               "issue160-token",
				AuditLogFile:        filepath.Join(t.TempDir(), "a.jsonl"),
				AuditLogRotateMaxMB: 100,
				AuditLogRotateKeep:  14,
			}
			candidate := cfgWith(trafficAddr)
			candidate.Admin = initial.Admin
			candidate.Admin.Listen = "127.0.0.1:19091"
			candidate.Admin.AuditLogFile = filepath.Join(t.TempDir(), "b.jsonl")
			candidate.Admin.AuditLogRotateKeep = 3

			src := &stubSource{}
			src.set(initial, nil)
			srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue160Factory(trafficAddr), src, func(context.Context, *config.Config) error { return nil })
			var prepared atomic.Int64
			var committed atomic.Int64
			var aborted atomic.Int64
			srv.PrepareAdmin = func(config.AdminConfig) (*PreparedCommit, error) {
				prepared.Add(1)
				return NewPreparedCommit(func() { committed.Add(1) }, func() { aborted.Add(1) }), nil
			}

			ctx, cancel := context.WithCancel(context.Background())
			reload := make(chan ReloadRequest, 1)
			done := make(chan error, 1)
			go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
			waitForServe(t, "http://"+trafficAddr+"/", "issue160")

			src.set(candidate, nil)
			resultCh := make(chan ReloadResult, 1)
			reload <- ReloadRequest{ID: "issue160-mixed-restart", Source: source, Result: resultCh}
			result := <-resultCh
			if result.Outcome != ReloadNotApplied || result.Published {
				t.Fatalf("mixed restart/audit result = %+v, want not_applied/unpublished", result)
			}
			if committed.Load() != 0 {
				t.Fatalf("prepared commits=%d, want 0", committed.Load())
			}
			if prepared.Load() != aborted.Load() {
				t.Fatalf("prepared=%d aborted=%d, want every prepared artifact aborted", prepared.Load(), aborted.Load())
			}
			live := srv.LiveSnapshot().EffectiveConfig
			if live == nil || live.Admin.Listen != initial.Admin.Listen || live.Admin.AuditLogFile != initial.Admin.AuditLogFile || live.Admin.AuditLogRotateKeep != initial.Admin.AuditLogRotateKeep {
				t.Fatalf("mixed candidate partially published: %+v", live)
			}

			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		})
	}
}
