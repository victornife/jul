// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func issue158Factory(addr string) HandlerFactory {
	return func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "issue158")
		})
		return map[string]http.Handler{addr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
}

func TestIssue158SourceReloadPublishesAdminLimits(t *testing.T) {
	for _, source := range []ReloadSource{ReloadSourceSIGHUP, ReloadSourceFileWatch} {
		t.Run(source.String(), func(t *testing.T) {
			trafficAddr := freePort(t)
			initial := cfgWith(trafficAddr)
			initial.Admin = config.AdminConfig{
				Enabled:              true,
				Listen:               "127.0.0.1:19090",
				Token:                "issue158-token",
				RateLimitReadPerMin:  240,
				RateLimitWritePerMin: 60,
				RateLimitApplyPerMin: 30,
				MaxEventConns:        4,
			}
			candidate := cfgWith(trafficAddr)
			candidate.Admin = initial.Admin
			candidate.Admin.RateLimitReadPerMin = 17
			candidate.Admin.RateLimitWritePerMin = -1
			candidate.Admin.RateLimitApplyPerMin = 9
			candidate.Admin.MaxEventConns = 2

			src := &stubSource{}
			src.set(initial, nil)
			srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue158Factory(trafficAddr), src, func(context.Context, *config.Config) error { return nil })
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
			waitForServe(t, "http://"+trafficAddr+"/", "issue158")

			src.set(candidate, nil)
			resultCh := make(chan ReloadResult, 1)
			reload <- ReloadRequest{ID: "issue158-source-parity", Source: source, Result: resultCh}
			result := <-resultCh
			if result.Outcome != ReloadAppliedLive || !result.Published {
				t.Fatalf("limit-only reload result = %+v, want applied_live/published", result)
			}
			if prepared.Load() != 1 || committed.Load() != 1 {
				t.Fatalf("prepared=%d committed=%d, want one prepare and one commit", prepared.Load(), committed.Load())
			}
			if seen.RateLimitReadPerMin != 17 || seen.RateLimitWritePerMin != -1 || seen.RateLimitApplyPerMin != 9 || seen.MaxEventConns != 2 {
				t.Fatalf("PrepareAdmin saw wrong limit candidate: %+v", seen)
			}
			live := srv.LiveSnapshot().EffectiveConfig
			if live == nil {
				t.Fatal("live snapshot has no effective config")
			}
			if live.Admin.RateLimitReadPerMin != 17 || live.Admin.RateLimitWritePerMin != -1 || live.Admin.RateLimitApplyPerMin != 9 || live.Admin.MaxEventConns != 2 {
				t.Fatalf("published admin limits = %+v", live.Admin)
			}

			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		})
	}
}

func TestIssue158MixedAdminStructuralAndLimitsIsAtomic(t *testing.T) {
	for _, source := range []ReloadSource{ReloadSourceSIGHUP, ReloadSourceFileWatch} {
		t.Run(source.String(), func(t *testing.T) {
			trafficAddr := freePort(t)
			initial := cfgWith(trafficAddr)
			initial.Admin = config.AdminConfig{
				Enabled:              true,
				Listen:               "127.0.0.1:19090",
				Token:                "issue158-token",
				RateLimitReadPerMin:  240,
				RateLimitWritePerMin: 60,
				RateLimitApplyPerMin: 30,
				MaxEventConns:        4,
			}
			candidate := cfgWith(trafficAddr)
			candidate.Admin = initial.Admin
			candidate.Admin.Listen = "127.0.0.1:19091" // structural / restart-required (#97)
			candidate.Admin.RateLimitWritePerMin = 3
			candidate.Admin.MaxEventConns = 1

			src := &stubSource{}
			src.set(initial, nil)
			srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue158Factory(trafficAddr), src, func(context.Context, *config.Config) error { return nil })
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
			waitForServe(t, "http://"+trafficAddr+"/", "issue158")

			src.set(candidate, nil)
			resultCh := make(chan ReloadResult, 1)
			reload <- ReloadRequest{ID: "issue158-mixed", Source: source, Result: resultCh}
			result := <-resultCh
			if result.Outcome != ReloadNotApplied || result.Published {
				t.Fatalf("mixed candidate result = %+v, want not_applied and unpublished", result)
			}
			if committed.Load() != 0 {
				t.Fatalf("prepared admin commits = %d, want 0", committed.Load())
			}
			if prepared.Load() != aborted.Load() {
				t.Fatalf("prepared=%d aborted=%d, want every prepared artifact aborted", prepared.Load(), aborted.Load())
			}
			live := srv.LiveSnapshot().EffectiveConfig
			if live == nil {
				t.Fatal("live snapshot has no effective config")
			}
			if live.Admin.Listen != initial.Admin.Listen || live.Admin.RateLimitWritePerMin != initial.Admin.RateLimitWritePerMin || live.Admin.MaxEventConns != initial.Admin.MaxEventConns {
				t.Fatalf("mixed candidate partially changed live admin config: got listen=%q write=%d conns=%d", live.Admin.Listen, live.Admin.RateLimitWritePerMin, live.Admin.MaxEventConns)
			}

			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		})
	}
}
