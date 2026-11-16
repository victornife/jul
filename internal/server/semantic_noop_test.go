// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/router"
	"jul/internal/upstream"
)

type sequencedRawSource struct {
	cfg      *config.Config
	firstRaw []byte
	laterRaw []byte
	reads    atomic.Int64
}

func (s *sequencedRawSource) Load() (*config.Config, error) { return s.cfg, nil }
func (s *sequencedRawSource) Name() string                  { return "sequenced" }
func (s *sequencedRawSource) ReadRaw() ([]byte, error) {
	if s.reads.Add(1) == 1 {
		return s.firstRaw, nil
	}
	return s.laterRaw, nil
}

func normalizedCandidate(t *testing.T, cfg *config.Config) *config.Candidate {
	t.Helper()
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatalf("candidate: %v", err)
	}
	return candidate
}

// waitForInitialGeneration closes the small startup window between the first
// accept loop becoming reachable and Run publishing its coherent runtime
// snapshot. Tests that assert generation identity must not sample that window.
func waitForInitialGeneration(t *testing.T, srv *Server) LiveSnapshot {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := srv.LiveSnapshot()
		if snapshot.Generation != 0 && len(snapshot.Listeners) > 0 {
			return snapshot
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("initial serving generation was not published: %+v", srv.LiveSnapshot())
	return LiveSnapshot{}
}

func TestSemanticNoopPreservesGenerationAndResources(t *testing.T) {
	addr := freePort(t)
	t.Setenv("JUL_NOOP_SECRET_A", "initial-noop-secret")
	t.Setenv("JUL_NOOP_SECRET_B", "accepted-noop-secret")
	initialConfig := cfgWithReturn(addr, http.StatusOK)
	initialConfig.Global.AccessLog = "${env:JUL_NOOP_SECRET_A}"
	initial := normalizedCandidate(t, initialConfig)
	src := &stubSource{}
	src.set(initial.Raw, nil)
	initialRaw, err := config.Marshal(initial.Raw)
	if err != nil {
		t.Fatalf("marshal initial: %v", err)
	}
	src.setRaw(initialRaw)

	var factoryCalls, commits, aborts, retirements, reloadStarts, reloadCompletes atomic.Int64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := uint64(factoryCalls.Add(1))
		code := c.Servers[0].Locations[0].Return
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, "return-%d", code)
		})
		return map[string]http.Handler{addr: h}, genID,
			func() (upstream.SnapshotMap, func()) {
				commits.Add(1)
				return nil, func() { retirements.Add(1) }
			},
			func() { aborts.Add(1) }, nil
	}

	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	srv.OnReloadStart = func() { reloadStarts.Add(1) }
	srv.OnReloadComplete = func(_, outcome string, _ int64) {
		if outcome == "" {
			t.Error("reload completion omitted outcome")
		}
		reloadCompletes.Add(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 8)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, initial.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	initialSnapshot := waitForInitialGeneration(t, srv)
	initialHandler := srv.handlers.Load()
	srv.redactMu.Lock()
	initialRedactionGenerations := len(srv.redactGens)
	initialRetiredSecrets := srv.retiredRedaction.Count()
	srv.redactMu.Unlock()
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{ID: "same", Source: ReloadSourceSIGHUP, Result: resultCh}
	same := <-resultCh
	if same.Outcome != ReloadNoChange || same.Published || same.Error != "" || same.FailedPhase != "" {
		t.Fatalf("same reload = %+v, want clean no_change", same)
	}
	if same.HTTP.Status != ReloadSubsystemSkipped || same.Stream.Status != ReloadSubsystemSkipped || same.Admin.Status != ReloadSubsystemSkipped {
		t.Fatalf("same reload subsystems = http:%s stream:%s admin:%s, want skipped", same.HTTP.Status, same.Stream.Status, same.Admin.Status)
	}
	if _, ok := same.PhaseDurations["prepare"]; ok {
		t.Fatalf("no-op phases = %v, prepare must not run", same.PhaseDurations)
	}
	if _, ok := same.PhaseDurations["change_assessment"]; !ok {
		t.Fatalf("no-op phases = %v, change_assessment missing", same.PhaseDurations)
	}
	if last := srv.LastReload(); last == nil || last.ID != same.ID || last.Outcome != ReloadNoChange || last.Published {
		t.Fatalf("LastReload = %+v, want terminal unpublished no_change", last)
	}
	if got := srv.LiveSnapshot().Generation; got != initialSnapshot.Generation {
		t.Fatalf("generation after same reload = %d, want %d", got, initialSnapshot.Generation)
	}
	if srv.handlers.Load() != initialHandler {
		t.Fatal("same reload replaced the live handler generation")
	}
	if factoryCalls.Load() != 1 || commits.Load() != 1 || aborts.Load() != 0 || retirements.Load() != 0 {
		t.Fatalf("same reload factory/commit/abort/retire = %d/%d/%d/%d, want 1/1/0/0", factoryCalls.Load(), commits.Load(), aborts.Load(), retirements.Load())
	}

	// An ignored/deprecated-only edit advances accepted configuration metadata,
	// including its canonical version, without replacing serving resources.
	ignoredRaw, err := initial.Raw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	ignoredRaw.Global.AccessLog = "${env:JUL_NOOP_SECRET_B}"
	ignoredRaw.Servers[0].AccessLog = "ignored-server.log"
	src.set(ignoredRaw, nil)
	reload <- ReloadRequest{ID: "ignored", Source: ReloadSourceFileWatch, Result: resultCh}
	ignored := <-resultCh
	if ignored.Outcome != ReloadNoChange || ignored.DesiredVersion != ignored.ServingVersion {
		t.Fatalf("ignored reload = %+v, want adopted no_change", ignored)
	}
	ignoredSnapshot := srv.LiveSnapshot()
	if ignoredSnapshot.Generation != initialSnapshot.Generation {
		t.Fatalf("ignored-only generation = %d, want %d", ignoredSnapshot.Generation, initialSnapshot.Generation)
	}
	if ignoredSnapshot.EffectiveConfig.Global.AccessLog != "accepted-noop-secret" {
		t.Fatal("ignored-only metadata was not adopted")
	}
	if ignoredSnapshot.EffectiveConfig.Servers[0].AccessLog != "ignored-server.log" {
		t.Fatal("ignored server metadata was not adopted")
	}
	if CanonicalVersion(ignoredSnapshot.EffectiveConfig) == CanonicalVersion(initialSnapshot.EffectiveConfig) {
		t.Fatal("ignored-only canonical version did not advance")
	}
	if redact.Apply("accepted-noop-secret") != redact.Mask {
		t.Fatal("accepted no-op secret metadata was not installed for redaction")
	}

	// Rewriting a secret reference as the same effective literal must not
	// unmask the value held by the unchanged handler generation.
	literalRaw, err := ignoredRaw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	literalRaw.Global.AccessLog = "accepted-noop-secret"
	src.set(literalRaw, nil)
	reload <- ReloadRequest{ID: "same-effective-secret", Source: ReloadSourceSIGHUP, Result: resultCh}
	literal := <-resultCh
	if literal.Outcome != ReloadNoChange || literal.Published {
		t.Fatalf("same-effective secret reload = %+v, want no_change", literal)
	}
	if redact.Apply("accepted-noop-secret") != redact.Mask {
		t.Fatal("same-effective reference-to-literal rewrite unmasked a live-generation secret")
	}
	srv.redactMu.Lock()
	redactionGenerations := len(srv.redactGens)
	retiredSecrets := srv.retiredRedaction.Count()
	srv.redactMu.Unlock()
	if redactionGenerations != initialRedactionGenerations || retiredSecrets != initialRetiredSecrets {
		t.Fatalf("no-op redaction generations/retired secrets = %d/%d, want %d/%d",
			redactionGenerations, retiredSecrets, initialRedactionGenerations, initialRetiredSecrets)
	}

	// Repeated ignored-only secret metadata replaces the current overlay instead
	// of growing an unbounded history. The original handler-generation secret
	// remains masked, as does only the latest accepted metadata secret.
	const secretMetadataEvents = 25
	latestMetadataSecret := ""
	for i := 0; i < secretMetadataEvents; i++ {
		name := fmt.Sprintf("JUL_NOOP_IGNORED_%02d", i)
		latestMetadataSecret = fmt.Sprintf("ignored-secret-%02d", i)
		t.Setenv(name, latestMetadataSecret)
		raw, cloneErr := ignoredRaw.Clone()
		if cloneErr != nil {
			t.Fatal(cloneErr)
		}
		raw.Global.AccessLog = "${env:" + name + "}"
		src.set(raw, nil)
		reload <- ReloadRequest{ID: fmt.Sprintf("ignored-secret-%d", i), Source: ReloadSourceFileWatch, Result: resultCh}
		if result := <-resultCh; result.Outcome != ReloadNoChange {
			t.Fatalf("ignored secret %d = %+v, want no_change", i, result)
		}
	}
	srv.redactMu.Lock()
	activeSecrets := srv.redactGens[initialHandler.genID].Count()
	baseGenerations := len(srv.redactGenBases)
	srv.redactMu.Unlock()
	if activeSecrets != 2 || baseGenerations != 1 {
		t.Fatalf("no-op secret overlay count/base generations = %d/%d, want 2/1", activeSecrets, baseGenerations)
	}
	if redact.Apply("initial-noop-secret") != redact.Mask || redact.Apply(latestMetadataSecret) != redact.Mask {
		t.Fatal("handler-base or latest metadata secret was not masked")
	}
	if redact.Apply("accepted-noop-secret") == redact.Mask {
		t.Fatal("superseded ignored-only metadata secret remained accumulated")
	}

	// A real hot change publishes exactly once; repeated equivalent events do
	// not consume another factory generation or create a gap.
	hotRaw, err := ignoredRaw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	hotRaw.Servers[0].Locations[0].Return = http.StatusCreated
	src.set(hotRaw, nil)
	reload <- ReloadRequest{ID: "hot", Source: ReloadSourceSIGHUP, Result: resultCh}
	hot := <-resultCh
	if hot.Outcome != ReloadAppliedLive || !hot.Published {
		t.Fatalf("hot reload = %+v, want applied_live", hot)
	}
	if got := srv.LiveSnapshot().Generation; got != initialSnapshot.Generation+1 {
		t.Fatalf("generation after hot reload = %d, want %d", got, initialSnapshot.Generation+1)
	}
	hotHandler := srv.handlers.Load()
	hotListeners := len(srv.LiveSnapshot().Listeners)
	const duplicateEvents = 100
	for i := 0; i < duplicateEvents; i++ {
		reload <- ReloadRequest{ID: fmt.Sprintf("duplicate-%d", i), Source: ReloadSourceFileWatch, Result: resultCh}
		if duplicate := <-resultCh; duplicate.Outcome != ReloadNoChange {
			t.Fatalf("duplicate %d = %+v, want no_change", i, duplicate)
		}
	}
	if factoryCalls.Load() != 2 || commits.Load() != 2 || aborts.Load() != 0 {
		t.Fatalf("after duplicates factory/commit/abort = %d/%d/%d, want 2/2/0", factoryCalls.Load(), commits.Load(), aborts.Load())
	}
	if srv.handlers.Load() != hotHandler || srv.LiveSnapshot().Generation != initialSnapshot.Generation+1 || len(srv.LiveSnapshot().Listeners) != hotListeners {
		t.Fatalf("duplicates changed serving resources: handler_same=%t generation=%d listeners=%d", srv.handlers.Load() == hotHandler, srv.LiveSnapshot().Generation, len(srv.LiveSnapshot().Listeners))
	}

	// Returning to a previous configuration is a real transition, not a
	// process-lifetime seen-digest dedupe.
	src.set(initial.Raw, nil)
	reload <- ReloadRequest{ID: "back-to-a", Source: ReloadSourceSIGHUP, Result: resultCh}
	back := <-resultCh
	if back.Outcome != ReloadAppliedLive || srv.LiveSnapshot().Generation != initialSnapshot.Generation+2 {
		t.Fatalf("A -> B -> A result/snapshot = %+v/%+v", back, srv.LiveSnapshot())
	}
	wantEvents := int64(5 + secretMetadataEvents + duplicateEvents)
	if reloadStarts.Load() != wantEvents || reloadCompletes.Load() != wantEvents {
		t.Fatalf("reload start/complete = %d/%d, want %d/%d", reloadStarts.Load(), reloadCompletes.Load(), wantEvents, wantEvents)
	}
}

func TestCanonicalEquivalentSourceReloadsAreNoChange(t *testing.T) {
	addr := freePort(t)
	firstRaw := []byte(fmt.Sprintf(`
[[servers]]
listen = %q

[[servers.locations]]
return = 200
match = { path = "/", type = "prefix" }
`, addr))
	formattedRaw := []byte(fmt.Sprintf(`# formatting and key order are not serving inputs
[[servers]]
  listen=%q

[[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`, addr))
	firstConfig, err := config.Parse(firstRaw)
	if err != nil {
		t.Fatalf("parse first config: %v", err)
	}
	formattedConfig, err := config.Parse(formattedRaw)
	if err != nil {
		t.Fatalf("parse formatted config: %v", err)
	}
	first := normalizedCandidate(t, firstConfig)
	formatted := normalizedCandidate(t, formattedConfig)
	if CanonicalVersion(first.Effective) != CanonicalVersion(formatted.Effective) {
		t.Fatal("formatting/key-order variants did not canonicalize equally")
	}

	src := &stubSource{}
	src.set(formatted.Raw, nil)
	var factoryCalls atomic.Int64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		generation := uint64(factoryCalls.Add(1))
		code := c.Servers[0].Locations[0].Return
		return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })}, generation,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(first.Effective, first.Raw, lifecycle.ComputeFingerprint(first.Effective), quietLogger(), factory, src, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan ReloadRequest, 2)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, first.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	initial := waitForInitialGeneration(t, srv)
	initialHandler := srv.handlers.Load()
	for _, source := range []ReloadSource{ReloadSourceFileWatch, ReloadSourceSIGHUP} {
		resultCh := make(chan ReloadResult, 1)
		reloads <- ReloadRequest{ID: source.String(), Source: source, Result: resultCh}
		result := <-resultCh
		if result.Outcome != ReloadNoChange || result.Published {
			t.Fatalf("%s result = %+v, want no_change", source, result)
		}
	}
	if got := srv.LiveSnapshot().Generation; got != initial.Generation {
		t.Fatalf("generation = %d, want %d", got, initial.Generation)
	}
	if srv.handlers.Load() != initialHandler || factoryCalls.Load() != 1 {
		t.Fatalf("equivalent source reload replaced resources; handler_same=%t factory_calls=%d", srv.handlers.Load() == initialHandler, factoryCalls.Load())
	}
}

func TestReloadDoesNotHideRouteChanges(t *testing.T) {
	tests := []struct {
		name      string
		locations []config.LocationConfig
		change    func([]config.LocationConfig)
	}{
		{
			name: "overlapping regex order",
			locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "regex", Path: "^/"}, Return: http.StatusOK},
				{Match: config.MatchConfig{Type: "regex", Path: "^/private/"}, Return: http.StatusForbidden},
			},
			change: func(locations []config.LocationConfig) {
				locations[0], locations[1] = locations[1], locations[0]
			},
		},
		{
			name: "duplicate location identity",
			locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "regex", Path: "^/private/"}, Return: http.StatusOK},
				{Match: config.MatchConfig{Type: "regex", Path: "^/private/"}, Return: http.StatusForbidden},
			},
			change: func(locations []config.LocationConfig) {
				locations[0].Return = http.StatusForbidden
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := freePort(t)
			raw := cfgWith(addr)
			raw.Servers[0].Locations = tt.locations
			initial := normalizedCandidate(t, raw)
			var factoryCalls atomic.Int64
			factory := func(_ context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
				r, err := router.New(cfg, nil, nil, nil, quietLogger())
				if err != nil {
					return nil, 0, nil, nil, err
				}
				generation := uint64(factoryCalls.Add(1))
				return map[string]http.Handler{addr: r.For(addr)}, generation,
					func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
			}
			source := &stubSource{cfg: initial.Raw, raw: []byte("persisted")}
			srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, source, nil)
			ctx, cancel := context.WithCancel(context.Background())
			reloads := make(chan ReloadRequest, 1)
			done := make(chan error, 1)
			go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
			t.Cleanup(func() {
				cancel()
				if err := <-done; err != nil {
					t.Errorf("Run returned error: %v", err)
				}
			})
			waitForInitialGeneration(t, srv)
			client := &http.Client{Timeout: 5 * time.Second}
			status := func() int {
				t.Helper()
				resp, err := client.Get("http://" + addr + "/private/data")
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				return resp.StatusCode
			}
			if got := status(); got != http.StatusOK {
				t.Fatalf("initial private route = %d, want 200", got)
			}
			nextCfg, err := initial.Effective.Clone()
			if err != nil {
				t.Fatal(err)
			}
			tt.change(nextCfg.Servers[0].Locations)
			next := normalizedCandidate(t, nextCfg)
			resultCh := make(chan ReloadResult, 1)
			reloads <- ReloadRequest{
				ID:                 "route-change",
				Source:             ReloadSourceAdmin,
				Candidate:          next,
				ExpectedGeneration: srv.LiveSnapshot().Generation,
				Result:             resultCh,
			}
			if result := <-resultCh; result.Outcome != ReloadAppliedLive || !result.Published {
				t.Fatalf("reload = %+v, want published route change", result)
			}
			if got := status(); got != http.StatusForbidden {
				t.Fatalf("private route after published change = %d, want 403", got)
			}
			if got := factoryCalls.Load(); got != 2 {
				t.Fatalf("handler builds = %d, want initial and replacement", got)
			}
		})
	}
}

func TestReloadDoesNotHideDuplicateUpstreamWeightChange(t *testing.T) {
	addr := freePort(t)
	raw := cfgWithReturn(addr, http.StatusOK)
	raw.Upstreams = []config.UpstreamConfig{{
		Name:     "backend",
		Strategy: "weighted_round_robin",
		Servers: []config.UpstreamServer{
			{Address: "a.example:80", Weight: 1},
			{Address: "a.example:80", Weight: 1},
			{Address: "b.example:80", Weight: 1},
		},
	}}
	initial := normalizedCandidate(t, raw)
	var factoryCalls atomic.Int64
	registry := upstream.NewRegistry(upstream.RegistryOptions{})
	t.Cleanup(registry.CloseAll)
	factory := func(ctx context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		registry.Begin()
		pool, err := registry.For(ctx, cfg.Upstreams[0], "http")
		if err != nil {
			registry.Abort()
			return nil, 0, nil, nil, err
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempt, err := pool.Pick()
			if err != nil {
				http.Error(w, "no backend", http.StatusServiceUnavailable)
				return
			}
			defer pool.Release(attempt.Backend)
			_, _ = io.WriteString(w, attempt.Address)
		})
		generation := uint64(factoryCalls.Add(1))
		return map[string]http.Handler{addr: handler}, generation,
			func() (upstream.SnapshotMap, func()) { registry.Commit(); return nil, nil }, registry.Abort, nil
	}
	source := &stubSource{cfg: initial.Raw, raw: []byte("persisted")}
	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, source, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	waitForInitialGeneration(t, srv)
	client := &http.Client{Timeout: 5 * time.Second}
	distribution := func() map[string]int {
		t.Helper()
		counts := make(map[string]int)
		for range 9 {
			resp, err := client.Get("http://" + addr + "/")
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("backend selection = %d, %v", resp.StatusCode, err)
			}
			counts[string(body)]++
		}
		return counts
	}
	if got := distribution(); got["a.example:80"] != 6 || got["b.example:80"] != 3 {
		t.Fatalf("initial distribution = %v, want 6/3", got)
	}
	reorderedRaw, err := raw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	reorderedRaw.Upstreams[0].Servers[1], reorderedRaw.Upstreams[0].Servers[2] =
		reorderedRaw.Upstreams[0].Servers[2], reorderedRaw.Upstreams[0].Servers[1]
	reordered := normalizedCandidate(t, reorderedRaw)
	resultCh := make(chan ReloadResult, 1)
	reloads <- ReloadRequest{
		ID:                 "equivalent-upstream-reorder",
		Source:             ReloadSourceAdmin,
		Candidate:          reordered,
		ExpectedGeneration: srv.LiveSnapshot().Generation,
		Result:             resultCh,
	}
	if result := <-resultCh; result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("reorder reload = %+v, want published backend order", result)
	}
	nextRaw, err := raw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	nextRaw.Upstreams[0].Servers[0].Weight = 7
	next := normalizedCandidate(t, nextRaw)
	reloads <- ReloadRequest{
		ID:                 "duplicate-upstream-weight",
		Source:             ReloadSourceAdmin,
		Candidate:          next,
		ExpectedGeneration: srv.LiveSnapshot().Generation,
		Result:             resultCh,
	}
	if result := <-resultCh; result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("reload = %+v, want published upstream change", result)
	}
	if got := distribution(); got["a.example:80"] != 8 || got["b.example:80"] != 1 {
		t.Fatalf("new distribution = %v, want 8/1", got)
	}
	if got := factoryCalls.Load(); got != 3 {
		t.Fatalf("handler builds = %d, want initial and two replacements", got)
	}
}

func TestReloadPublishesLeastConnBackendOrder(t *testing.T) {
	addr := freePort(t)
	raw := cfgWithReturn(addr, http.StatusOK)
	raw.Upstreams = []config.UpstreamConfig{{
		Name: "backend", Strategy: "least_conn",
		Servers: []config.UpstreamServer{{Address: "a.example:80"}, {Address: "b.example:80"}},
	}}
	initial := normalizedCandidate(t, raw)
	registry := upstream.NewRegistry(upstream.RegistryOptions{})
	t.Cleanup(registry.CloseAll)
	var factoryCalls atomic.Int64
	factory := func(ctx context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		registry.Begin()
		pool, err := registry.For(ctx, cfg.Upstreams[0], "http")
		if err != nil {
			registry.Abort()
			return nil, 0, nil, nil, err
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			attempt, err := pool.Pick()
			if err != nil {
				http.Error(w, "no backend", http.StatusServiceUnavailable)
				return
			}
			defer pool.Release(attempt.Backend)
			_, _ = io.WriteString(w, attempt.Address)
		})
		generation := uint64(factoryCalls.Add(1))
		return map[string]http.Handler{addr: handler}, generation,
			func() (upstream.SnapshotMap, func()) { registry.Commit(); return nil, nil }, registry.Abort, nil
	}
	source := &stubSource{cfg: initial.Raw, raw: []byte("persisted")}
	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, source, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	waitForInitialGeneration(t, srv)
	client := &http.Client{Timeout: 5 * time.Second}
	selected := func() string {
		t.Helper()
		resp, err := client.Get("http://" + addr + "/")
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("backend selection = %d, %v", resp.StatusCode, err)
		}
		return string(body)
	}
	if got := selected(); got != "a.example:80" {
		t.Fatalf("initial selection = %q", got)
	}
	nextRaw, err := raw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	nextRaw.Upstreams[0].Servers[0], nextRaw.Upstreams[0].Servers[1] =
		nextRaw.Upstreams[0].Servers[1], nextRaw.Upstreams[0].Servers[0]
	results := make(chan ReloadResult, 1)
	reloads <- ReloadRequest{
		ID: "backend-order", Source: ReloadSourceAdmin, Candidate: normalizedCandidate(t, nextRaw),
		ExpectedGeneration: srv.LiveSnapshot().Generation, Result: results,
	}
	if result := <-results; result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("reorder reload = %+v, want published order", result)
	}
	if got := selected(); got != "b.example:80" {
		t.Fatalf("selection after reorder = %q, want b.example:80", got)
	}
}

func TestServingServerOrderPreservesGenerationCoordinates(t *testing.T) {
	first := config.ServerConfig{Listen: ":1001", ServerNames: []string{"a"}}
	second := config.ServerConfig{Listen: ":1002", ServerNames: []string{"b"}}
	third := config.ServerConfig{Listen: ":1001", ServerNames: []string{"c"}}
	before := &config.Config{Servers: []config.ServerConfig{first, second, third}}
	if servingServersEqual(before, &config.Config{Servers: []config.ServerConfig{second, first, third}}) {
		t.Fatal("reordering independent listeners changes generation coordinates")
	}
	if servingServersEqual(before, &config.Config{Servers: []config.ServerConfig{third, second, first}}) {
		t.Fatal("reordering blocks on one listener can change the default route")
	}
}

func TestReloadPublishesIndependentListenerReorder(t *testing.T) {
	firstAddr, secondAddr := freePort(t), freePort(t)
	raw := cfgWithReturn(firstAddr, http.StatusOK)
	second := raw.Servers[0]
	second.Listen = secondAddr
	raw.Servers = append(raw.Servers, second)
	initial := normalizedCandidate(t, raw)
	var factoryCalls atomic.Int64
	factory := func(_ context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		handlers := make(map[string]http.Handler)
		for _, server := range cfg.Servers {
			handlers[server.Listen] = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		}
		return handlers, uint64(factoryCalls.Add(1)), func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	source := &stubSource{cfg: initial.Raw, raw: []byte("persisted")}
	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, source, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	waitForInitialGeneration(t, srv)
	nextRaw, err := raw.Clone()
	if err != nil {
		t.Fatal(err)
	}
	nextRaw.Servers[0], nextRaw.Servers[1] = nextRaw.Servers[1], nextRaw.Servers[0]
	results := make(chan ReloadResult, 1)
	reloads <- ReloadRequest{
		ID: "listener-reorder", Source: ReloadSourceAdmin, Candidate: normalizedCandidate(t, nextRaw),
		ExpectedGeneration: srv.LiveSnapshot().Generation, Result: results,
	}
	if result := <-results; result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("reorder reload = %+v, want a generation with matching coordinates", result)
	}
	if got := factoryCalls.Load(); got != 2 {
		t.Fatalf("handler builds = %d, want initial and replacement", got)
	}
}

func TestChangedFileSecretPublishes(t *testing.T) {
	addr := freePort(t)
	secretPath := filepath.Join(t.TempDir(), "header-secret")
	if err := os.WriteFile(secretPath, []byte("first-header-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := cfgWithReturn(addr, http.StatusOK)
	raw.Servers[0].Locations[0].Headers = map[string]string{
		"X-Backend-Token": "Bearer ${file:" + secretPath + "}",
	}
	initial := normalizedCandidate(t, raw)
	src := &stubSource{}
	src.set(initial.Raw, nil)

	var factoryCalls atomic.Int64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		generation := uint64(factoryCalls.Add(1))
		return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}, generation,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, src, nil)
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	initialGeneration := waitForInitialGeneration(t, srv).Generation

	if err := os.WriteFile(secretPath, []byte("second-header-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan ReloadResult, 1)
	reloads <- ReloadRequest{ID: "file-secret-rotation", Source: ReloadSourceFileWatch, Result: resultCh}
	result := <-resultCh
	if result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("secret rotation result = %+v, want applied_live", result)
	}
	if srv.LiveSnapshot().Generation != initialGeneration+1 || factoryCalls.Load() != 2 {
		t.Fatalf("secret rotation generation/factory = %d/%d, want %d/2", srv.LiveSnapshot().Generation, factoryCalls.Load(), initialGeneration+1)
	}
	if redact.Apply("second-header-secret") != redact.Mask {
		t.Fatal("rotated secret was not installed for redaction")
	}
}

func TestManagedNoopRunsFinalCASAndAbortsPreparedAdmin(t *testing.T) {
	addr := freePort(t)
	initial := normalizedCandidate(t, cfgWithReturn(addr, http.StatusOK))
	src := &stubSource{}
	src.set(initial.Raw, nil)
	raw, err := config.Marshal(initial.Raw)
	if err != nil {
		t.Fatal(err)
	}
	src.setRaw(raw)

	var factoryCalls, preparedAborts atomic.Int64
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := uint64(factoryCalls.Add(1))
		return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}, genID,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 2)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	waitForInitialGeneration(t, srv)

	prepared := NewPreparedCommit(nil, func() { preparedAborts.Add(1) })
	resultCh := make(chan ReloadResult, 1)
	snap := srv.LiveSnapshot()
	reload <- ReloadRequest{
		ID:                 "managed-noop",
		Source:             ReloadSourceAdmin,
		Candidate:          initial,
		PreparedAdmin:      prepared,
		ExpectedGeneration: snap.Generation,
		AuthGeneration:     "auth-1",
		ValidateAuthGeneration: func(got string) bool {
			return got == "auth-1"
		},
		RawDigest: sha256Digest(raw),
		Result:    resultCh,
	}
	result := <-resultCh
	if result.Outcome != ReloadNoChange || result.Published {
		t.Fatalf("managed result = %+v, want no_change", result)
	}
	if preparedAborts.Load() != 1 {
		t.Fatalf("prepared admin aborts = %d, want 1", preparedAborts.Load())
	}
	if factoryCalls.Load() != 1 || srv.LiveSnapshot().Generation != snap.Generation {
		t.Fatalf("factory/generation = %d/%d, want 1/%d", factoryCalls.Load(), srv.LiveSnapshot().Generation, snap.Generation)
	}

	// The generation CAS remains content-unaware and conservative. #408 avoids
	// false bumps upstream; it never weakens a genuinely stale mutation.
	staleResult := make(chan ReloadResult, 1)
	reload <- ReloadRequest{
		ID:                 "managed-stale",
		Source:             ReloadSourceAdmin,
		Candidate:          initial,
		ExpectedGeneration: snap.Generation + 1,
		RawDigest:          sha256Digest(raw),
		Result:             staleResult,
	}
	stale := <-staleResult
	if stale.Outcome != ReloadNotApplied || stale.FailedPhase != "runtime_cas" {
		t.Fatalf("stale result = %+v, want runtime_cas not_applied", stale)
	}
}

func TestManagedNoopFinalFencesRejectConcurrentChanges(t *testing.T) {
	t.Run("persisted bytes", func(t *testing.T) {
		addr := freePort(t)
		initial := normalizedCandidate(t, cfgWithReturn(addr, http.StatusOK))
		raw, err := config.Marshal(initial.Raw)
		if err != nil {
			t.Fatal(err)
		}
		src := &sequencedRawSource{cfg: initial.Raw, firstRaw: raw, laterRaw: append(append([]byte(nil), raw...), '\n')}
		srv, reload := startNoopFenceServer(t, initial, src, nil)
		resultCh := make(chan ReloadResult, 1)
		reload <- ReloadRequest{
			ID:                 "persisted-fence",
			Source:             ReloadSourceAdmin,
			Candidate:          initial,
			ExpectedGeneration: srv.LiveSnapshot().Generation,
			RawDigest:          sha256Digest(raw),
			Result:             resultCh,
		}
		result := <-resultCh
		if result.Outcome != ReloadNotApplied || result.FailedPhase != "persisted_cas" || result.Published {
			t.Fatalf("result = %+v, want final persisted_cas rejection", result)
		}
	})

	t.Run("runtime generation", func(t *testing.T) {
		addr := freePort(t)
		initial := normalizedCandidate(t, cfgWithReturn(addr, http.StatusOK))
		src := &stubSource{}
		src.set(initial.Raw, nil)
		raw, err := config.Marshal(initial.Raw)
		if err != nil {
			t.Fatal(err)
		}
		src.setRaw(raw)
		var srv *Server
		var reload chan ReloadRequest
		validate := func(context.Context, *config.Config) error {
			snap := srv.LiveSnapshot()
			srv.runtimeState.Store(&runtimeState{
				EffectiveConfig: snap.EffectiveConfig,
				RawConfig:       snap.RawConfig,
				Listeners:       snap.Listeners,
				Generation:      snap.Generation + 1,
			})
			return nil
		}
		srv, reload = startNoopFenceServer(t, initial, src, validate)
		generation := srv.LiveSnapshot().Generation
		resultCh := make(chan ReloadResult, 1)
		reload <- ReloadRequest{
			ID:                 "runtime-fence",
			Source:             ReloadSourceAdmin,
			Candidate:          initial,
			ExpectedGeneration: generation,
			RawDigest:          sha256Digest(raw),
			Result:             resultCh,
		}
		result := <-resultCh
		if result.Outcome != ReloadNotApplied || result.FailedPhase != "runtime_cas" || result.Published {
			t.Fatalf("result = %+v, want final runtime_cas rejection", result)
		}
	})

	t.Run("admin authorization", func(t *testing.T) {
		addr := freePort(t)
		initial := normalizedCandidate(t, cfgWithReturn(addr, http.StatusOK))
		src := &stubSource{}
		src.set(initial.Raw, nil)
		raw, err := config.Marshal(initial.Raw)
		if err != nil {
			t.Fatal(err)
		}
		src.setRaw(raw)
		srv, reload := startNoopFenceServer(t, initial, src, nil)
		var checks atomic.Int64
		resultCh := make(chan ReloadResult, 1)
		reload <- ReloadRequest{
			ID:                 "auth-fence",
			Source:             ReloadSourceAdmin,
			Candidate:          initial,
			ExpectedGeneration: srv.LiveSnapshot().Generation,
			AuthGeneration:     "auth-1",
			ValidateAuthGeneration: func(string) bool {
				return checks.Add(1) == 1
			},
			RawDigest: sha256Digest(raw),
			Result:    resultCh,
		}
		result := <-resultCh
		if result.Outcome != ReloadNotApplied || result.FailedPhase != "auth_cas" || result.Published {
			t.Fatalf("result = %+v, want final auth_cas rejection", result)
		}
	})
}

func TestNoopAssessmentDeadlineIsAttributed(t *testing.T) {
	addr := freePort(t)
	initial := normalizedCandidate(t, cfgWithReturn(addr, http.StatusOK))
	src := &stubSource{}
	src.set(initial.Raw, nil)
	srv, reload := startNoopFenceServer(t, initial, src, nil)
	resultCh := make(chan ReloadResult, 1)
	reload <- ReloadRequest{
		ID:       "expired",
		Source:   ReloadSourceSIGHUP,
		Deadline: time.Now().Add(-time.Second),
		Result:   resultCh,
	}
	result := <-resultCh
	if result.Outcome != ReloadNotApplied || !result.TimedOut || result.TimedOutPhase != "resolve" || result.Published {
		t.Fatalf("result = %+v, want timed-out resolve rejection", result)
	}
	if srv.LiveSnapshot().Generation == 0 {
		t.Fatal("startup generation was not serving")
	}
}

func startNoopFenceServer(t *testing.T, initial *config.Candidate, src config.Source, validate func(context.Context, *config.Config) error) (*Server, chan ReloadRequest) {
	t.Helper()
	addr := initial.Effective.Servers[0].Listen
	factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}, 1,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, src, validate)
	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, initial.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	waitForInitialGeneration(t, srv)
	return srv, reload
}

func TestOpaqueReloadInputsFailClosed(t *testing.T) {
	cases := map[string]func(*config.Config){
		"static root": func(c *config.Config) { c.Servers[0].Locations[0].Root = "/srv/www" },
		"descriptor": func(c *config.Config) {
			c.Servers[0].Locations[0].GRPCTranscode = &config.GRPCTranscodeConfig{DescriptorSet: "api.pb"}
		},
		"reflection": func(c *config.Config) {
			c.Servers[0].Locations[0].GRPCTranscode = &config.GRPCTranscodeConfig{UseReflection: true}
		},
		"htpasswd": func(c *config.Config) {
			c.Servers[0].Locations[0].Auth = &config.AuthConfig{Basic: &config.BasicAuthConfig{File: "users.htpasswd"}}
		},
		"waf directives": func(c *config.Config) { c.WAF.DirectivesFiles = []string{"rules.conf"} },
		"location waf directives": func(c *config.Config) {
			c.Servers[0].Locations[0].WAF = &config.WAFConfig{DirectivesFiles: []string{"location-rules.conf"}}
		},
		"waf inline include": func(c *config.Config) { c.WAF.InlineRules = "Include /etc/jul/extra.conf" },
		"waf inline data file": func(c *config.Config) {
			c.WAF.InlineRules = `SecRule ARGS "@pmFromFile /etc/jul/words.data" "id:1,deny"`
		},
		"waf inline pmf alias": func(c *config.Config) {
			c.Servers[0].Locations[0].WAF = &config.WAFConfig{InlineRules: `SecRule ARGS "@pmf words.data" "id:1,deny"`}
		},
		"waf inline ip file": func(c *config.Config) {
			c.WAF.InlineRules = `SecRule REMOTE_ADDR "@ipMatchF /etc/jul/ips.txt" "id:1,deny"`
		},
		"waf inline schema": func(c *config.Config) {
			c.WAF.InlineRules = `SecRule REQUEST_BODY "@validateSchema /etc/jul/s.json" "id:1,deny"`
		},
		"backend CA": func(c *config.Config) {
			c.Upstreams = []config.UpstreamConfig{{BackendTLS: &config.BackendTLSConfig{CAFile: "ca.pem"}}}
		},
		"backend system roots": func(c *config.Config) {
			c.Upstreams = []config.UpstreamConfig{{BackendTLS: &config.BackendTLSConfig{CAMode: "system"}}}
		},
		"location backend TLS": func(c *config.Config) {
			c.Servers[0].Locations[0].BackendTLS = &config.BackendTLSConfig{CAMode: "system"}
		},
		"consul TLS": func(c *config.Config) {
			c.Upstreams = []config.UpstreamConfig{{Discovery: &config.DiscoveryConfig{
				Consul: &config.ConsulDiscovery{TLS: &config.BackendTLSConfig{CAMode: "system"}},
			}}}
		},
		"implicit consul HTTPS trust": func(c *config.Config) {
			c.Upstreams = []config.UpstreamConfig{{Discovery: &config.DiscoveryConfig{
				Consul: &config.ConsulDiscovery{Address: "https://consul.example"},
			}}}
		},
		"implicit HTTPS trust": func(c *config.Config) {
			c.Servers[0].Locations[0].Return = 0
			c.Servers[0].Locations[0].ProxyPass = "https://backend.example"
		},
		"kubernetes in-cluster identity": func(c *config.Config) {
			c.Upstreams = []config.UpstreamConfig{{Discovery: &config.DiscoveryConfig{
				Kubernetes: &config.KubernetesDiscovery{Namespace: "default", Service: "api"},
			}}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := cfgWithReturn("127.0.0.1:1", http.StatusOK)
			mutate(cfg)
			if !hasOpaqueReloadInputs(cfg) {
				t.Fatal("external input was incorrectly considered provably unchanged")
			}
		})
	}
}

func TestServingChangeAssessmentFailsClosed(t *testing.T) {
	base := normalizedCandidate(t, cfgWithReturn("127.0.0.1:1", http.StatusOK))
	newPlan := func(candidate *config.Candidate, withHandler bool) (*Server, *ReloadPlan) {
		srv := &Server{
			cfg:       base.Effective,
			rawCfg:    base.Raw,
			listeners: make(map[string]*listenerEntry),
		}
		srv.runtimeState.Store(&runtimeState{
			EffectiveConfig: base.Effective,
			RawConfig:       base.Raw,
			Listeners:       map[string]BoundListenerInfo{},
		})
		if withHandler {
			srv.handlers.Store(&handlerGen{})
		}
		return srv, srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
	}

	t.Run("missing live configuration", func(t *testing.T) {
		srv, plan := newPlan(base, true)
		srv.runtimeState.Store(&runtimeState{Listeners: map[string]BoundListenerInfo{}})
		if err := plan.AssessServingChange(); err == nil {
			t.Fatal("assessment succeeded without a live effective configuration")
		}
	})

	t.Run("serving field changed", func(t *testing.T) {
		changed := normalizedCandidate(t, cfgWithReturn("127.0.0.1:1", http.StatusCreated))
		_, plan := newPlan(changed, true)
		if err := plan.AssessServingChange(); err != nil {
			t.Fatalf("AssessServingChange: %v", err)
		}
		if plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingConfigChanged {
			t.Fatalf("assessment = %+v, want config_changed", plan.ServingChange)
		}
	})

	t.Run("missing handler generation", func(t *testing.T) {
		_, plan := newPlan(base, false)
		if err := plan.AssessServingChange(); err != nil {
			t.Fatalf("AssessServingChange: %v", err)
		}
		if plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingUnknownExternalInput {
			t.Fatalf("assessment = %+v, want unknown_external_input", plan.ServingChange)
		}
	})

	t.Run("missing static certificate provider", func(t *testing.T) {
		cfg := cfgWithReturn("127.0.0.1:1", http.StatusOK)
		cfg.Servers[0].TLS = &config.TLSConfig{Enabled: true, Cert: "cert.pem", Key: "key.pem"}
		candidate := normalizedCandidate(t, cfg)
		srv := &Server{cfg: candidate.Effective, rawCfg: candidate.Raw, listeners: make(map[string]*listenerEntry)}
		srv.runtimeState.Store(&runtimeState{
			EffectiveConfig: candidate.Effective,
			RawConfig:       candidate.Raw,
			Listeners:       map[string]BoundListenerInfo{},
		})
		srv.handlers.Store(&handlerGen{})
		plan := srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
		if err := plan.AssessServingChange(); err != nil {
			t.Fatalf("AssessServingChange: %v", err)
		}
		if plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingRuntimeInputChanged {
			t.Fatalf("assessment = %+v, want runtime_resource_changed", plan.ServingChange)
		}
	})

	t.Run("unproved admin certificate identity", func(t *testing.T) {
		cfg := cfgWithReturn("127.0.0.1:1", http.StatusOK)
		cfg.Admin.Enabled = true
		cfg.Admin.TLS = &config.AdminTLSConfig{Enabled: true, Cert: "admin-cert.pem", Key: "admin-key.pem"}
		candidate := normalizedCandidate(t, cfg)
		srv := &Server{cfg: candidate.Effective, rawCfg: candidate.Raw, listeners: make(map[string]*listenerEntry)}
		srv.runtimeState.Store(&runtimeState{
			EffectiveConfig: candidate.Effective,
			RawConfig:       candidate.Raw,
			Listeners:       map[string]BoundListenerInfo{},
		})
		srv.handlers.Store(&handlerGen{})
		plan := srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
		if err := plan.AssessServingChange(); err != nil {
			t.Fatalf("AssessServingChange: %v", err)
		}
		if plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingRuntimeInputChanged {
			t.Fatalf("assessment = %+v, want runtime_resource_changed", plan.ServingChange)
		}
	})

	t.Run("opaque resource identity", func(t *testing.T) {
		cfg := cfgWithReturn("127.0.0.1:1", http.StatusOK)
		cfg.Plugins = map[string]config.PluginConfig{"p": {Path: "plugin.wasm"}}
		candidate := normalizedCandidate(t, cfg)
		srv := &Server{cfg: candidate.Effective, rawCfg: candidate.Raw, listeners: make(map[string]*listenerEntry)}
		srv.runtimeState.Store(&runtimeState{
			EffectiveConfig: candidate.Effective,
			RawConfig:       candidate.Raw,
			Listeners:       map[string]BoundListenerInfo{},
		})
		srv.handlers.Store(&handlerGen{})
		plan := srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
		if err := plan.AssessServingChange(); err != nil {
			t.Fatalf("AssessServingChange: %v", err)
		}
		if plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingUnknownExternalInput {
			t.Fatalf("assessment = %+v, want unknown_external_input", plan.ServingChange)
		}
	})
}

// Self-contained inline rules stay provable, so the no-op optimisation keeps
// working for them (#440).
func TestSelfContainedInlineWAFIsNotOpaque(t *testing.T) {
	cfg := cfgWithReturn("127.0.0.1:1", http.StatusOK)
	cfg.WAF.InlineRules = `SecRule ARGS:x "@streq y" "id:1,phase:1,deny"`
	cfg.Servers[0].Locations[0].WAF = &config.WAFConfig{Enabled: true, CRSEnabled: true, InlineRules: `SecAction "id:2,pass"`}
	if hasOpaqueReloadInputs(cfg) {
		t.Fatal("inline rules without file references were treated as opaque")
	}
}
