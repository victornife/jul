// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/upstream"
	"jul/internal/versionid"
)

// A rejected managed reload must report the candidate's keyed desired version
// and the immutable serving version, including when a fence changes while
// preparing resources. It must abort those resources without publishing them.
func TestEffectiveVersionsAtManagedReloadFences(t *testing.T) {
	for _, fence := range []string{"initial auth", "prepared auth", "prepared runtime"} {
		t.Run(fence, func(t *testing.T) {
			addr := freePort(t)
			initial := normalizedCandidate(t, cfgWithReturn(addr, http.StatusOK))
			candidate := normalizedCandidate(t, cfgWithReturn(addr, http.StatusTeapot))
			raw, err := config.Marshal(candidate.Raw)
			if err != nil {
				t.Fatal(err)
			}
			src := &stubSource{}
			src.set(candidate.Raw, nil)
			src.setRaw(raw)
			versions, err := versionid.New(make([]byte, 32))
			if err != nil {
				t.Fatal(err)
			}
			var calls, aborted atomic.Int64
			var authChanged atomic.Bool
			var srv *Server
			factory := func(_ context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
				generation := uint64(calls.Add(1))
				if generation > 1 {
					if fence == "prepared auth" {
						authChanged.Store(true)
					}
					if fence == "prepared runtime" {
						snapshot := *srv.runtimeState.Load()
						snapshot.Generation++
						srv.runtimeState.Store(&snapshot)
					}
				}
				code := cfg.Servers[0].Locations[0].Return
				return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })}, generation,
					func() (upstream.SnapshotMap, func()) { return nil, nil }, func() { aborted.Add(1) }, nil
			}
			srv = New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, src, nil, versions)
			ctx, cancel := context.WithCancel(context.Background())
			reload := make(chan ReloadRequest, 1)
			done := make(chan error, 1)
			go func() { done <- srv.Run(ctx, reload, initial.Redaction) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("server: %v", err)
				}
			})
			before := waitForInitialGeneration(t, srv)
			resultCh := make(chan ReloadResult, 1)
			reload <- ReloadRequest{ID: fence, Source: ReloadSourceAdmin, Candidate: candidate,
				ExpectedGeneration: before.Generation, RawDigest: sha256Digest(raw), AuthGeneration: "authorized",
				ValidateAuthGeneration: func(string) bool { return fence != "initial auth" && !authChanged.Load() }, Result: resultCh}
			result := <-resultCh
			phase := "auth_cas"
			if fence == "prepared runtime" {
				phase = "runtime_cas"
			}
			if result.Outcome != ReloadNotApplied || result.FailedPhase != phase || result.Published {
				t.Fatalf("fence result: %+v", result)
			}
			if result.DesiredVersion != versions.Config(candidate.Effective) || result.ServingVersion != versions.Config(initial.Effective) || result.ServingVersion != before.EffectiveVersion {
				t.Fatal("rejected reload lost keyed desired/serving identity")
			}
			if srv.LiveSnapshot().EffectiveConfig != before.EffectiveConfig || srv.LiveSnapshot().EffectiveVersion != before.EffectiveVersion {
				t.Fatal("rejected candidate was published")
			}
			wantCalls, wantAborts := int64(2), int64(1)
			if fence == "initial auth" {
				wantCalls, wantAborts = 1, 0
			}
			if calls.Load() != wantCalls || aborted.Load() != wantAborts {
				t.Fatalf("factory calls/aborts: %d/%d, want %d/%d", calls.Load(), aborted.Load(), wantCalls, wantAborts)
			}
			client := http.Client{Transport: testTransport}
			response, err := client.Get("http://" + addr)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("serving status: %d", response.StatusCode)
			}
		})
	}
}
