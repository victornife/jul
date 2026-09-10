// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// TestIssue157MixedAdminStructuralAndHotReloadIsAtomic proves the important
// HR-06B boundary on both source-driven entry points. Console/upload policy is
// hot, but a candidate that also moves the structural admin listener must stop
// at lifecycle validation: no prepared admin state may be committed and the
// old effective AdminConfig remains the live snapshot.
func TestIssue157MixedAdminStructuralAndHotReloadIsAtomic(t *testing.T) {
	for _, source := range []ReloadSource{ReloadSourceSIGHUP, ReloadSourceFileWatch} {
		t.Run(source.String(), func(t *testing.T) {
			trafficAddr := freePort(t)
			off := false
			on := true

			initial := cfgWith(trafficAddr)
			initial.Admin = config.AdminConfig{
				Enabled: true,
				Listen:  "127.0.0.1:19090",
				Token:   "issue157-token",
				Console: &off,
			}
			candidate := cfgWith(trafficAddr)
			candidate.Admin = config.AdminConfig{
				Enabled: true,
				Listen:  "127.0.0.1:19091", // structural / restart-required (#97)
				Token:   "issue157-token",
				Console: &on, // hot under #157, but must not partially publish
			}

			src := &stubSource{}
			src.set(initial, nil)
			factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
				h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = io.WriteString(w, "issue157")
				})
				return map[string]http.Handler{trafficAddr: h}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
			}

			srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
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
			waitForServe(t, "http://"+trafficAddr+"/", "issue157")

			src.set(candidate, nil)
			resultCh := make(chan ReloadResult, 1)
			reload <- ReloadRequest{ID: "issue157-mixed", Source: source, Result: resultCh}
			result := <-resultCh

			if result.Outcome != ReloadNotApplied || result.Published {
				t.Fatalf("mixed candidate result = %+v, want not_applied and unpublished", result)
			}
			if !strings.Contains(result.Error, "restart_required") {
				t.Fatalf("mixed candidate error = %q, want restart_required lifecycle rejection", result.Error)
			}
			if committed.Load() != 0 {
				t.Fatalf("prepared admin commits = %d, want 0", committed.Load())
			}
			// Depending on phase ordering, preparation may be skipped entirely or a
			// prepared artifact may be aborted; it must never survive uncommitted.
			if prepared.Load() != aborted.Load() {
				t.Fatalf("prepared=%d aborted=%d, want every prepared artifact aborted", prepared.Load(), aborted.Load())
			}
			live := srv.LiveSnapshot().EffectiveConfig
			if live == nil {
				t.Fatal("live snapshot has no effective config")
			}
			if live.Admin.Listen != initial.Admin.Listen || live.Admin.ConsoleEnabled() != initial.Admin.ConsoleEnabled() {
				t.Fatalf("mixed candidate partially changed live admin config: got listen=%q console=%v", live.Admin.Listen, live.Admin.ConsoleEnabled())
			}

			cancel()
			if err := <-done; err != nil {
				t.Fatalf("Run returned error: %v", err)
			}
		})
	}
}
