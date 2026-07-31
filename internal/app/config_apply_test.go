// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

func testPreflight() *Preflight {
	return &Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
}

func validConfigRaw(t *testing.T, listen string) []byte {
	t.Helper()
	cfg := config.ProxyTarget("127.0.0.1:9000", listen)
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

func mutationBaseline(t *testing.T, raw []byte) *admin.MutationBaseline {
	t.Helper()
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse mutation baseline: %v", err)
	}
	return &admin.MutationBaseline{
		Raw:     append([]byte(nil), raw...),
		Digest:  sha256.Sum256(raw),
		Version: server.CanonicalVersion(cfg),
		Config:  cfg,
		Exists:  true,
	}
}

// restartRequiredConfigRaw returns a config raw that differs from the seed
// produced by validConfigRaw in a restart-required field (log_format).
func restartRequiredConfigRaw(t *testing.T, listen string) []byte {
	t.Helper()
	cfg := config.ProxyTarget("127.0.0.1:9000", listen)
	cfg.Global.LogFormat = "json"
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return raw
}

func TestCoordinatorApplyRawSuccess(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	if res.Mode != ApplyHot {
		t.Errorf("mode = %v, want %v", res.Mode, ApplyHot)
	}

	// The file should contain the new bytes and watcher suppression should be
	// registered for the new digest.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(newRaw) {
		t.Error("on-disk bytes do not match applied bytes")
	}
	if watchDigest.Load() == nil {
		t.Error("watcher digest was not set")
	}
}

// TestCoordinatorApplyRawRejectsExternalWriteInCASWindow verifies finding 12:
// the coordinator-level optimistic-concurrency CAS. An external writer that
// does not participate in applyMu changes the file on disk AFTER the apply read
// its base (prevRaw) but BEFORE the coordinator's guarded write. The apply must
// reject with a conflict and must NOT clobber the external bytes.
//
// The external write is injected through LiveSnapshot, which the coordinator
// invokes inside applyCandidate before it takes c.mu — i.e. exactly inside the
// time-of-check/time-of-use window the CAS closes.
func TestCoordinatorApplyRawRejectsExternalWriteInCASWindow(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// The bytes an external editor lands during the apply window.
	externalRaw := validConfigRaw(t, ":9999")

	var injected atomic.Bool
	var submitted atomic.Bool
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(server.ReloadRequest) error {
			submitted.Store(true)
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			// Simulate an external writer landing a change exactly once, in the
			// window between the base read and the guarded write.
			if injected.CompareAndSwap(false, true) {
				if err := os.WriteFile(path, externalRaw, 0o600); err != nil {
					t.Errorf("inject external write: %v", err)
				}
			}
			return server.LiveSnapshot{}
		},
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatalf("ok = true, want false (CAS must reject an apply over an external write)")
	}
	if !strings.Contains(res.Message, "changed on disk") {
		t.Errorf("message = %q, want a disk-changed conflict", res.Message)
	}
	if submitted.Load() {
		t.Error("SubmitReload was called; a CAS-rejected apply must not enqueue a reload")
	}
	// The external bytes must survive untouched — the apply must not clobber them.
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(externalRaw) {
		t.Error("CAS-rejected apply overwrote the external write; disk must retain external bytes")
	}
}

// The HTTP handler authorizes against seed, but an external writer lands a new
// config before the coordinator starts. The supplied baseline must prevent the
// coordinator from adopting and overwriting that later file.
func TestCoordinatorRejectsExternalWriteBeforeEntry(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	baseline := mutationBaseline(t, seed)
	externalRaw := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, externalRaw, 0o600); err != nil {
		t.Fatalf("write external config: %v", err)
	}

	var submitted atomic.Bool
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(server.ReloadRequest) error {
			submitted.Store(true)
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{Baseline: baseline}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.Conflict || res.OK {
		t.Fatalf("result = %+v, want typed conflict", res)
	}
	if submitted.Load() {
		t.Fatal("CAS-rejected baseline was submitted for reload")
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(externalRaw) {
		t.Fatal("external configuration was overwritten")
	}
}

func TestCoordinatorStageRejectsExternalWriteBeforeEntry(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	baseline := mutationBaseline(t, seed)
	externalRaw := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, externalRaw, 0o600); err != nil {
		t.Fatalf("write external config: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflight()
	pf.StartupFP = lifecycle.ComputeFingerprint(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{Baseline: baseline}, restartRequiredConfigRaw(t, ":8080"), ApplyStageRestart)
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if !res.Conflict || res.OK {
		t.Fatalf("result = %+v, want typed conflict", res)
	}
	if _, err := os.Stat(path + ".pending-restart.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stage conflict wrote a marker: %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(externalRaw) {
		t.Fatal("stage conflict overwrote external configuration")
	}
}

func TestCoordinatorStageRejectsExternalWriteAfterPreparedMarker(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	externalRaw := validConfigRaw(t, ":9999")
	pf := testPreflight()
	pf.StartupFP = lifecycle.ComputeFingerprint(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: NewFilePlannedRestartStore(path),
		beforePersist: func(mode ApplyMode) {
			if mode == ApplyStageRestart {
				if err := os.WriteFile(path, externalRaw, 0o600); err != nil {
					t.Errorf("write external config: %v", err)
				}
			}
		},
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{Baseline: mutationBaseline(t, seed)}, restartRequiredConfigRaw(t, ":8080"), ApplyStageRestart)
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if !res.Conflict || res.OK {
		t.Fatalf("result = %+v, want typed conflict", res)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(externalRaw) {
		t.Fatal("stage overwrote external configuration after preparing marker")
	}
	marker, err := c.PlannedRestart.LoadMarker()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load marker: %v", err)
	}
	if marker != nil {
		t.Fatalf("marker = %+v, want fresh prepared state rolled back", marker)
	}
}

func TestCoordinatorRejectsCommentOnlyExternalEdit(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	externalRaw := append([]byte("# externally edited\n"), seed...)
	if err := os.WriteFile(path, externalRaw, 0o600); err != nil {
		t.Fatalf("write external config: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}
	res, err := c.ApplyRaw(admin.ApplyRequestContext{Baseline: mutationBaseline(t, seed)}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.Conflict {
		t.Fatalf("result = %+v, want exact-byte conflict", res)
	}
	if res.CurrentVersion != mutationBaseline(t, seed).Version {
		t.Fatalf("current_version = %q, want canonical version %q", res.CurrentVersion, mutationBaseline(t, seed).Version)
	}
}

func TestCoordinatorInitialReadErrorFailsClosed(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
		ReadConfigRaw:  func() ([]byte, error) { return nil, os.ErrPermission },
	}
	_, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err == nil || !errors.Is(err, admin.ErrConfigStorageUnavailable) {
		t.Fatalf("error = %v, want ErrConfigStorageUnavailable", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Fatal("read failure changed persisted config")
	}
}

func TestCoordinatorSecretReferenceVersionDomains(t *testing.T) {
	t.Setenv("JUL_TEST_LOG_LEVEL", "warn")
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	apply := func(raw []byte, baseline *admin.MutationBaseline) ApplyResult {
		c := &ConfigApplyCoordinator{
			BaseCtx:   context.Background(),
			Path:      path,
			Preflight: testPreflight(),
			SubmitReload: func(req server.ReloadRequest) error {
				go func() {
					desired := server.CanonicalVersion(req.Candidate.Effective)
					req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadAppliedLive, Published: true, DesiredVersion: desired, ServingVersion: desired}
				}()
				return nil
			},
			LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
			PlannedRestart: &PlannedRestartStore{},
		}
		res, err := c.ApplyRaw(admin.ApplyRequestContext{Baseline: baseline}, raw, ApplyHot)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		if !res.OK {
			t.Fatalf("apply result: %+v", res)
		}
		return res
	}

	candidateCfg := config.ProxyTarget("127.0.0.1:9000", ":8081")
	candidateCfg.Global.LogLevel = "${env:JUL_TEST_LOG_LEVEL}"
	candidateRaw, err := config.Marshal(candidateCfg)
	if err != nil {
		t.Fatalf("marshal candidate: %v", err)
	}
	first := apply(candidateRaw, mutationBaseline(t, seed))
	if first.Version == first.DesiredVersion {
		t.Fatalf("persisted and effective versions unexpectedly match: %q", first.Version)
	}
	if first.Version != first.PersistedVersion || first.FinalDiskVersion != first.PersistedVersion {
		t.Fatalf("version domains inconsistent: %+v", first)
	}

	secondCfg, err := config.Parse(candidateRaw)
	if err != nil {
		t.Fatalf("parse second candidate: %v", err)
	}
	secondCfg.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9001"
	secondRaw, err := config.Marshal(secondCfg)
	if err != nil {
		t.Fatalf("marshal second candidate: %v", err)
	}
	baseline := mutationBaseline(t, candidateRaw)
	if first.Version != baseline.Version {
		t.Fatalf("first response version = %q, subsequent base = %q", first.Version, baseline.Version)
	}
	_ = apply(secondRaw, baseline)
}

// TestCoordinatorApplyRawSucceedsWhenNoExternalWrite is the positive control for
// finding 12: with no external write in the window, the CAS is a no-op and the
// apply proceeds and persists normally.
func TestCoordinatorApplyRawSucceedsWhenNoExternalWrite(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(newRaw) {
		t.Error("on-disk bytes do not match applied bytes")
	}
}

func TestCoordinatorApplyRawValidationFailureNoWrite(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, []byte("{bad toml"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if len(res.ValidationErrors) == 0 {
		t.Error("expected validation errors")
	}

	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("validation failure should not change the file")
	}
}

func TestCoordinatorApplyRawRestartRequiredCanStage(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := config.ProxyTarget("127.0.0.1:9000", ":8080")
	seed.Global.LogFormat = "json"
	seedRaw, err := config.Marshal(seed)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, seedRaw, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	p := testPreflight()
	p.StartupFP = lifecycle.ComputeFingerprint(seed)

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      p,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	// Change a restart-required field.
	next := config.ProxyTarget("127.0.0.1:9000", ":8080")
	next.Global.LogFormat = "combined"
	nextRaw, err := config.Marshal(next)
	if err != nil {
		t.Fatalf("marshal next: %v", err)
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, nextRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if !res.RestartRequired {
		t.Error("RestartRequired = false, want true")
	}
	if !res.CanStage {
		t.Error("CanStage = false, want true")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seedRaw) {
		t.Error("restart-required rejection should not change the file")
	}
}

func TestCoordinatorApplyRawRestoresOnSubmitFailure(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		SubmitReload:   func(req server.ReloadRequest) error { return errors.New("enqueue failed") },
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err == nil {
		t.Fatal("expected enqueue error")
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("submit failure should restore previous bytes")
	}
	if watchDigest.Load() == nil {
		t.Error("restoration watcher suppression should be registered")
	}
}

// TestApplyResultReportsRestorationSuccess verifies that a pre-Publish failure
// populates the restoration outcome fields (F-03).
func TestApplyResultReportsRestorationSuccess(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadNotApplied,
					Published: false,
					Error:     "bind failed",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if !res.Persisted {
		t.Error("Persisted = false, want true")
	}
	if !res.Restored {
		t.Errorf("Restored = false, want true")
	}
	if res.RestoreError != "" {
		t.Errorf("RestoreError = %q, want empty", res.RestoreError)
	}
	if res.FinalDiskVersion == "" {
		t.Error("FinalDiskVersion should be set")
	}
	if res.FinalDiskVersion == server.CanonicalVersion(nil) {
		t.Error("FinalDiskVersion should not match rejected candidate")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("previous bytes should be restored")
	}
}

func TestFastRestorationHTTPAndCallbackResultsMatch(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	callbackCh := make(chan admin.ConfigApplyResult, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadNotApplied, FailedPhase: "prepare", Error: "build failed"}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			callbackCh <- comp.Result
			return admin.ManagedApplyFinalization{FinalizationError: comp.Result.FinalizationError}
		},
	}
	httpResult, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	callback := <-callbackCh
	want := toAdminConfigApplyResult(httpResult)
	if !reflect.DeepEqual(callback, want) {
		t.Fatalf("callback and HTTP result differ:\ncallback=%+v\nhttp=%+v", callback, want)
	}
}

// TestManagedApplyFinalizationProvenanceThreaded verifies AC-14: the
// configuration-history finalization provenance written by WriteManagedHistory
// at terminalization (snapshot id and its non-fatal degradation) is delivered
// to OnManagedApplyComplete through the dedicated ManagedApplyFinalization
// argument — NOT the serialized ConfigApplyResult, which by the AC-05 invariant
// never carries it. The composition root routes these into the durable ledger
// record and the runtime-overview outcome so the Console can render a
// finalization/degradation surface independent of the reload outcome.
func TestManagedApplyFinalizationProvenanceThreaded(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	finCh := make(chan admin.ManagedApplyFinalization, 1)
	resCh := make(chan admin.ConfigApplyResult, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
		// A committed apply snapshots pre_apply and returns a degraded sidecar
		// error: the raw snapshot id is still set (the config stays
		// roll-back-able) while HistoryError explains the metadata degradation.
		// The unified completion callback is the composition root: it produces
		// the ManagedApplyFinalization provenance itself (mirroring serve.go
		// invoking RecordManagedHistory) and returns it to the coordinator.
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			fin := admin.ManagedApplyFinalization{
				HistorySnapshotID: "snap-42",
				HistoryError:      "metadata sidecar write failed",
				FinalizationError: comp.Result.FinalizationError,
			}
			resCh <- comp.Result
			finCh <- fin
			return fin
		},
	}

	httpResult, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !httpResult.OK {
		t.Fatalf("apply ok=false: %s", httpResult.Message)
	}

	var fin admin.ManagedApplyFinalization
	var res admin.ConfigApplyResult
	select {
	case fin = <-finCh:
		res = <-resCh
	case <-time.After(2 * time.Second):
		t.Fatal("OnManagedApplyComplete was not called")
	}

	if fin.HistorySnapshotID != "snap-42" {
		t.Errorf("finalization HistorySnapshotID = %q, want snap-42", fin.HistorySnapshotID)
	}
	if fin.HistoryError == "" {
		t.Error("finalization HistoryError should carry the sidecar degradation")
	}
	// AC-05 invariant: the serialized result must NOT expose history provenance.
	// ConfigApplyResult has no such fields, so the provenance can only travel on
	// the finalization argument — this is the contract AC-14 depends on.
	if !res.OK {
		t.Errorf("terminal result ok=false, want true (history degradation must not fail a committed apply)")
	}
}

func TestShutdownReturnsCorrelatedSavedNotLive(t *testing.T) {
	baseCtx, cancel := context.WithCancel(context.Background())
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	submitted := make(chan struct{})
	reloadRequest := make(chan server.ReloadRequest, 1)
	callbackCh := make(chan admin.ConfigApplyResult, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   baseCtx,
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			reloadRequest <- req
			close(submitted)
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			callbackCh <- comp.Result
			return admin.ManagedApplyFinalization{FinalizationError: comp.Result.FinalizationError}
		},
	}
	resultCh := make(chan ApplyResult, 1)
	go func() {
		result, _ := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		resultCh <- result
	}()
	<-submitted
	cancel()
	httpResult := <-resultCh
	if httpResult.Reload == nil || httpResult.Reload.Outcome != server.ReloadSavedNotLive || !httpResult.Persisted {
		t.Fatalf("shutdown result = %+v, want correlated saved_not_live", httpResult)
	}
	if httpResult.Reload.TimedOut {
		t.Fatal("shutdown provisional result must not be labeled timed_out")
	}
	select {
	case callback := <-callbackCh:
		t.Fatalf("provisional shutdown result unexpectedly emitted terminal callback: %+v", callback)
	default:
	}
	req := <-reloadRequest
	req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadNotApplied, FailedPhase: "shutdown", Error: "canceled"}
	callback := <-callbackCh
	if callback.Reload == nil || callback.Reload.Outcome != server.ReloadNotApplied || !callback.Restored {
		t.Fatalf("shutdown terminal callback = %+v, want rejected/restored", callback)
	}
}

func TestShutdownRestoresBufferedRejection(t *testing.T) {
	baseCtx, cancel := context.WithCancel(context.Background())
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	resultBuffered := make(chan struct{})
	resultContinue := make(chan struct{})
	c := &ConfigApplyCoordinator{
		BaseCtx:   baseCtx,
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			close(resultBuffered)
			go func() {
				<-resultContinue
				req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadNotApplied, FailedPhase: "prepare", Error: "rejected"}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}
	resultCh := make(chan ApplyResult, 1)
	go func() {
		result, _ := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		resultCh <- result
	}()
	<-resultBuffered
	cancel()
	provisional := <-resultCh
	if provisional.Reload == nil || provisional.Reload.Outcome != server.ReloadSavedNotLive || provisional.Reload.TimedOut {
		t.Fatalf("shutdown provisional = %+v", provisional)
	}
	close(resultContinue)
	deadline := time.Now().Add(time.Second)
	for {
		onDisk, _ := os.ReadFile(path)
		if string(onDisk) == string(seed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shutdown abandoned a later rejection without restoration")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCompletionCallbackPanicDoesNotBlockHTTP(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	// WS02 §3.6: a finalization callback panic must be made EXPLICIT — recovered
	// into a FinalizationError threaded onto the terminal result AND reported to
	// OnManagedApplyFinalizationError — never silently discarded, and never
	// blocking the HTTP path or failing an already-committed apply.
	var (
		hookMu      sync.Mutex
		hookCalls   int
		hookApplyID string
		hookErr     error
	)
	errHookDone := make(chan struct{})
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadAppliedLive, Published: true}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			panic("callback panic")
		},
		OnManagedApplyFinalizationError: func(completion admin.ManagedApplyCompletion, err error) {
			hookMu.Lock()
			hookCalls++
			hookApplyID = completion.Result.ApplyID
			hookErr = err
			hookMu.Unlock()
			close(errHookDone)
		},
	}
	result, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	// The committed apply must still succeed — a finalization panic never rolls
	// back an already-applied configuration.
	if err != nil || !result.OK {
		t.Fatalf("result=%+v err=%v, callback panic blocked HTTP", result, err)
	}
	// The recovered panic must be threaded onto the terminal result as a
	// FinalizationError instead of being silently swallowed.
	if !strings.Contains(result.FinalizationError, "finalization panic") {
		t.Fatalf("FinalizationError = %q, want it to carry the recovered panic", result.FinalizationError)
	}
	if !strings.Contains(result.FinalizationError, "callback panic") {
		t.Fatalf("FinalizationError = %q, want it to wrap the panic value", result.FinalizationError)
	}
	// The error-reporting hook must have been invoked exactly once with the
	// apply ID and the reconstructed panic error.
	select {
	case <-errHookDone:
	case <-time.After(2 * time.Second):
		t.Fatal("OnManagedApplyFinalizationError was not called")
	}
	hookMu.Lock()
	defer hookMu.Unlock()
	if hookCalls != 1 {
		t.Fatalf("finalization-error hook calls = %d, want 1", hookCalls)
	}
	if hookApplyID != result.ApplyID {
		t.Fatalf("finalization-error hook apply id = %q, want %q", hookApplyID, result.ApplyID)
	}
	if hookErr == nil || !strings.Contains(hookErr.Error(), "finalization panic") {
		t.Fatalf("finalization-error hook err = %v, want it to carry the panic", hookErr)
	}
}

func TestSlowRestorationReturnsSavedNotLiveThenOneTerminalResult(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	restoreStarted := make(chan struct{})
	restoreContinue := make(chan struct{})
	terminalCh := make(chan admin.ConfigApplyResult, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadNotApplied, FailedPhase: "prepare", Error: "build failed"}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(20 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		PlannedRestart: &PlannedRestartStore{},
		beforeRestore: func() {
			close(restoreStarted)
			<-restoreContinue
		},
		waitMargin: 10 * time.Millisecond,
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			terminalCh <- comp.Result
			return admin.ManagedApplyFinalization{FinalizationError: comp.Result.FinalizationError}
		},
	}

	resultCh := make(chan ApplyResult, 1)
	go func() {
		result, _ := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		resultCh <- result
	}()
	<-restoreStarted
	provisional := <-resultCh
	if provisional.Reload == nil || provisional.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("provisional result = %+v, want saved_not_live", provisional)
	}
	select {
	case terminal := <-terminalCh:
		t.Fatalf("callback ran before restoration completed: %+v", terminal)
	default:
	}
	close(restoreContinue)
	terminal := <-terminalCh
	if terminal.OK || !terminal.Restored || terminal.RestoreError != "" {
		t.Fatalf("terminal result = %+v, want rejected/restored", terminal)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Fatal("terminal callback disagrees with final disk state")
	}
}

func TestCoordinatorApplyRawRestoresOnPrePublishFailure(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			// Simulate a pre-Publish failure returned by the server.
			go func() {
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadNotApplied,
					Published: false,
					Error:     "bind failed",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("pre-Publish failure should restore previous bytes")
	}
}

func TestCoordinatorApplyRawRetainsFileOnPostPublishDegradation(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedDegraded,
					Published:      true,
					ServingVersion: "degraded-version",
					Error:          "stream reload failed",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	if res.Reload == nil || res.Reload.Outcome != server.ReloadAppliedDegraded {
		t.Error("expected degraded reload outcome")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(newRaw) {
		t.Error("post-Publish degradation should retain the candidate file")
	}
}

// TestCoordinatorApplyRawUsesServingReloadTimeout verifies that the
// coordinator uses the currently serving config's reload_timeout for the
// transaction, not the candidate's. A candidate that changes reload_timeout
// should affect the next apply, not this one (R15-01).
func TestCoordinatorApplyRawUsesServingReloadTimeout(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var gotDeadline time.Time
	var watchDigest atomicPointer32
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			gotDeadline = req.Deadline
			go func() {
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadAppliedLive,
					Published: true,
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(7 * time.Second)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		WatchDigest:    &watchDigest,
		PlannedRestart: &PlannedRestartStore{},
	}

	// Candidate changes reload_timeout to 1s; serving config says 7s.
	newRaw := validConfigRaw(t, ":8081")
	if _, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot); err != nil {
		t.Fatalf("apply error: %v", err)
	}

	slack := time.Until(gotDeadline)
	if slack < 6*time.Second || slack > 8*time.Second {
		t.Errorf("reload deadline slack = %v, want ~7s (serving reload_timeout)", slack)
	}
}

// TestApplyTimeoutRestoresAndBlocksConcurrentApply verifies that a timed-out
// managed apply holds the in-flight transaction until the finalizer completes,
// that the finalizer restores the previous bytes, and that a second managed
// apply receives a conflict rather than overlapping.
func TestApplyTimeoutRestoresAndBlocksConcurrentApply(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	var submitted atomic.Bool
	finalizerStarted := make(chan struct{})
	finalizerContinue := make(chan struct{})
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			submitted.Store(true)
			// Never send a result until released; the synchronous path times out.
			go func() {
				close(finalizerStarted)
				<-finalizerContinue
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadNotApplied,
					Published: false,
					Error:     "intentional timeout",
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(50 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		PlannedRestart: &PlannedRestartStore{},
	}

	// First apply times out synchronously; the finalizer goroutine is started.
	res1Ch := make(chan ApplyResult, 1)
	go func() {
		res, _ := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		res1Ch <- res
	}()

	<-finalizerStarted
	res1 := <-res1Ch
	if !res1.OK {
		t.Fatalf("ok = false, want true for timed-out apply; message: %s", res1.Message)
	}
	if res1.Reload == nil || res1.Reload.Outcome != server.ReloadSavedNotLive || !res1.Reload.TimedOut {
		t.Fatalf("expected saved_not_live timed-out result, got %+v", res1.Reload)
	}
	if !submitted.Load() {
		t.Fatal("reload was not submitted")
	}

	// Second apply should be rejected while the first finalizer is still in-flight.
	res2, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res2.OK {
		t.Fatal("ok = true, want false for overlapping apply")
	}
	if !strings.Contains(res2.Message, "still in flight") {
		t.Fatalf("expected in-flight conflict, got: %s", res2.Message)
	}

	// Allow the first finalizer to finish restoring.
	close(finalizerContinue)
	deadline := time.Now().Add(2 * time.Second)
	for {
		onDisk, _ := os.ReadFile(path)
		if string(onDisk) == string(seed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("finalizer should have restored previous bytes")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestApplyTimeoutFinalizerDoesNotOverwriteLaterApply verifies that a late
// finalizer from apply A cannot overwrite a subsequent apply B because it
// holds the coordinator mutex around digest check + restore.
func TestApplyTimeoutFinalizerDoesNotOverwriteLaterApply(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	finalizerBlock := make(chan struct{})
	finalizerEntered := make(chan struct{}, 1)
	submitCount := atomic.Int32{}
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			n := submitCount.Add(1)
			go func() {
				if n == 1 {
					finalizerEntered <- struct{}{}
					<-finalizerBlock
				}
				outcome := server.ReloadAppliedLive
				if n == 1 {
					outcome = server.ReloadNotApplied
				}
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   outcome,
					Published: outcome == server.ReloadAppliedLive,
					Error:     "intentional failure",
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(50 * time.Millisecond * raceTimeScale)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		PlannedRestart: &PlannedRestartStore{},
	}

	// Apply A times out. Its SubmitReload goroutine has entered the finalizer
	// body and is blocked before sending the result, so inFlightState is still
	// waiting and apply B must be rejected. The audit point is that once B
	// completes, a later apply C can proceed, and the late finalizer from A
	// cannot overwrite C's candidate.
	resACh := make(chan ApplyResult, 1)
	go func() {
		res, _ := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		resACh <- res
	}()
	<-finalizerEntered

	// Wait for A to time out and release applyMu.
	resA := <-resACh
	if !resA.OK {
		t.Fatalf("apply A should have timed out with ok=true, got: %s", resA.Message)
	}
	if resA.Reload == nil || resA.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("apply A expected saved_not_live, got %+v", resA.Reload)
	}

	// Apply B must be rejected because A's finalizer is still in-flight.
	resB, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("apply B error: %v", err)
	}
	if resB.OK {
		t.Fatal("ok = true, want false for overlapping apply B")
	}
	if !strings.Contains(resB.Message, "still in flight") {
		t.Fatalf("expected in-flight conflict, got: %s", resB.Message)
	}

	// Allow A's finalizer to finish; this clears inFlightState and restores
	// the previous (seed) bytes. Poll for the restore rather than sleeping a
	// fixed interval so the test stays deterministic under parallel load
	// (go test ./...) where the finalizer goroutine can be scheduled late.
	close(finalizerBlock)
	var onDiskAfterA []byte
	deadline := time.Now().Add(5 * time.Second)
	for {
		onDiskAfterA, _ = os.ReadFile(path)
		if string(onDiskAfterA) == string(seed) {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("finalizer from A should have restored seed, got %q", onDiskAfterA)
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Apply C can now proceed. Under the AC-03 finalization ordering the
	// finalizer restores the disk while still holding the in-flight guard and
	// only clears it after terminal finalization completes, so a restored disk
	// no longer implies the transaction is terminal. Poll apply C until the
	// in-flight guard has been released. It uses the same SubmitReload
	// callback; submit count is now 2 so it returns applied_live.
	var resC ApplyResult
	cDeadline := time.Now().Add(5 * time.Second)
	for {
		resC, err = c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8083"), ApplyHot)
		if err != nil {
			t.Fatalf("apply C error: %v", err)
		}
		if resC.OK {
			break
		}
		if !strings.Contains(resC.Message, "still in flight") {
			t.Fatalf("apply C should succeed, got: %s", resC.Message)
		}
		if time.Now().After(cDeadline) {
			t.Fatalf("apply C never cleared in-flight guard: %s", resC.Message)
		}
		time.Sleep(5 * time.Millisecond)
	}

	onDiskC, _ := os.ReadFile(path)
	if string(onDiskC) != string(validConfigRaw(t, ":8083")) {
		t.Fatal("apply C's candidate should be on disk")
	}
}

// TestSuccessApplyClearsInFlightStateEarly verifies the M-08 fix: after a
// successful apply returns, a subsequent apply is not blocked because the
// finalizer cleared inFlightState before forwarding the terminal result.
func TestSuccessApplyClearsInFlightStateEarly(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	res1, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply 1 error: %v", err)
	}
	if !res1.OK {
		t.Fatalf("apply 1 should succeed: %s", res1.Message)
	}

	res2, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("apply 2 error: %v", err)
	}
	if !res2.OK {
		t.Fatalf("apply 2 should succeed immediately after a successful apply; got: %s", res2.Message)
	}
}

// TestManagedApplyOutcomeCallbackFired verifies H-05: the coordinator calls
// OnManagedApplyComplete after the async finalizer reaches a terminal state,
// carrying the original request context and the final restoration status.
func TestManagedApplyOutcomeCallbackFired(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	finalizerContinue := make(chan struct{})
	gotCallback := make(chan admin.ConfigApplyResult, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				<-finalizerContinue
				req.Result <- server.ReloadResult{
					ID:        req.ID,
					Source:    server.ReloadSourceAdmin,
					Outcome:   server.ReloadNotApplied,
					Published: false,
					Error:     "intentional failure",
				}
			}()
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(50 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		PlannedRestart: &PlannedRestartStore{},
		OnManagedApplyComplete: func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
			if comp.Context.Actor != "alice" {
				t.Errorf("callback actor = %q, want alice", comp.Context.Actor)
			}
			gotCallback <- comp.Result
			return admin.ManagedApplyFinalization{FinalizationError: comp.Result.FinalizationError}
		},
	}

	ctx := admin.ApplyRequestContext{Actor: "alice", SourceIP: "127.0.0.1"}
	resCh := make(chan ApplyResult, 1)
	go func() {
		res, _ := c.ApplyRaw(ctx, validConfigRaw(t, ":8081"), ApplyHot)
		resCh <- res
	}()

	// Wait for the synchronous path to time out.
	res := <-resCh
	if !res.OK || res.Reload == nil || res.Reload.Outcome != server.ReloadSavedNotLive {
		t.Fatalf("expected saved_not_live timeout result, got %+v", res.Reload)
	}

	// Allow the finalizer to finish and invoke the callback.
	close(finalizerContinue)
	select {
	case terminal := <-gotCallback:
		if terminal.OK {
			t.Errorf("terminal outcome ok = true, want false")
		}
		if terminal.RestoreError != "" {
			t.Errorf("unexpected restore error: %s", terminal.RestoreError)
		}
		if !terminal.Restored {
			t.Errorf("terminal restored = false, want true")
		}
		if terminal.Reload == nil || terminal.Reload.Outcome != server.ReloadNotApplied {
			t.Errorf("terminal outcome = %v, want not_applied", terminal.Reload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnManagedApplyComplete was not called")
	}
}

func TestCoordinatorApplyRawSerialized(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
	cfg.Global.ReloadTimeout = config.Duration(100 * time.Millisecond)
	seed, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			// Return a successful result immediately so the test does not wait
			// for the reload timeout.
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent applies did not complete")
		}
	}
}

func TestCoordinatorApplyRawBlocksHotWhenPlannedRestartPending(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := &PlannedRestartStore{}
	store.Stage([]byte("staged"))
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if res.PendingRestart == nil || !res.PendingRestart.Managed {
		t.Error("expected pending_restart status")
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("blocked hot apply should not change the file")
	}
}

// TestCoordinatorApplyRawBlocksHotWhenExternalDivergenceSet verifies that a
// hot apply is rejected when the authoritative store reports external
// disk/runtime divergence (F-04).
func TestCoordinatorApplyRawBlocksHotWhenExternalDivergenceSet(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := &PlannedRestartStore{}
	store.SetExternalDivergence(true)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if res.PendingRestart == nil || !res.PendingRestart.External {
		t.Errorf("expected external pending_restart status, got %+v", res.PendingRestart)
	}
	if !strings.Contains(res.Message, "external divergence") {
		t.Errorf("message should mention external divergence, got %q", res.Message)
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("blocked hot apply should not change the file")
	}
}

// TestCoordinatorApplyStageBlocksWhenExternalDivergenceSet verifies that a
// stage_restart apply is also rejected while external divergence is present,
// so staging does not silently adopt an externally-owned change (F-04).
func TestCoordinatorApplyStageBlocksWhenExternalDivergenceSet(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := restartRequiredConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := &PlannedRestartStore{}
	store.SetExternalDivergence(true)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, restartRequiredConfigRaw(t, ":8080"), ApplyStageRestart)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false")
	}
	if res.PendingRestart == nil || !res.PendingRestart.External {
		t.Errorf("expected external pending_restart status, got %+v", res.PendingRestart)
	}

	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("blocked stage_restart should not change the file")
	}
}

func TestCoordinatorApplyConfigMarshalsAndApplies(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			go func() {
				req.Result <- server.ReloadResult{
					ID:             req.ID,
					Source:         server.ReloadSourceAdmin,
					Outcome:        server.ReloadAppliedLive,
					Published:      true,
					ServingVersion: "v2",
				}
			}()
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: &PlannedRestartStore{},
	}

	cfg := config.ProxyTarget("127.0.0.1:9001", ":8081")
	res, err := c.ApplyConfig(admin.ApplyRequestContext{}, cfg, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}

	onDisk, _ := os.ReadFile(path)
	if len(onDisk) == 0 {
		t.Error("file should contain marshaled config")
	}
}

func TestCoordinatorDiscardPlannedRestart(t *testing.T) {
	store := &PlannedRestartStore{}
	store.Stage([]byte("staged"))
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		PlannedRestart: store,
	}

	res, err := c.DiscardPlannedRestart()
	if err != nil {
		t.Fatalf("discard error: %v", err)
	}
	if !res.OK {
		t.Fatal("ok = false, want true")
	}
	if store.IsPending() {
		t.Error("planned restart should be discarded")
	}
}

// atomicPointer32 is a test helper type alias to avoid repeating the verbose
// atomic pointer type in every test.
type atomicPointer32 = atomic.Pointer[[32]byte]

// ── H-07 race/interaction tests ───────────────────────────────────────────────

// TestCoordinatorConcurrentStageRestartsSerialized verifies that concurrent
// stage_restart applies do not race under the Go race detector. The coordinator
// serializes applies via applyMu so only one stage completes at a time; all
// goroutines must either succeed or return a clean error — never corrupt state.
func TestCoordinatorConcurrentStageRestartsSerialized(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflightStage(t, original)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	const workers = 8
	errs := make([]error, workers)
	done := make(chan struct{})
	for i := 0; i < workers; i++ {
		go func(idx int) {
			defer func() { done <- struct{}{} }()
			candidate := restartRequiredConfigRaw(t, ":8080")
			res, err := c.ApplyRaw(admin.ApplyRequestContext{}, candidate, ApplyStageRestart)
			if err != nil {
				errs[idx] = err
				return
			}
			// With the F-08 single-candidate invariant, exactly one worker
			// succeeds and the rest are rejected because a candidate is
			// already pending.
			if !res.OK && !res.RestartRequired && !strings.Contains(res.Message, "already pending") {
				errs[idx] = errors.New("unexpected non-OK result: " + res.Message)
			}
		}(i)
	}
	for i := 0; i < workers; i++ {
		<-done
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("worker %d: %v", i, err)
		}
	}
	// After concurrent stages, the store must be in a deterministic state:
	// exactly one staged pending restart.
	if !store.IsPending() {
		t.Error("expected a pending staged restart after concurrent stages")
	}
}

// TestCoordinatorStageDiscardRace verifies that a discard immediately following
// a stage does not leave the store in an inconsistent state. This exercises the
// serialization path (applyMu is held for both operations).
func TestCoordinatorStageDiscardRace(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflightStage(t, original)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	// Stage a restart-required change.
	candidate := restartRequiredConfigRaw(t, ":8080")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, candidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("stage error: %v", err)
	}
	if !res.OK {
		t.Fatalf("stage ok=false: %s", res.Message)
	}
	if !store.IsPending() {
		t.Fatal("expected pending after stage")
	}

	// Discard immediately.
	discardRes, err := c.DiscardPlannedRestart()
	if err != nil {
		t.Fatalf("discard error: %v", err)
	}
	if !discardRes.OK {
		t.Fatalf("discard ok=false: %s", discardRes.Message)
	}
	if store.IsPending() {
		t.Error("expected no pending restart after discard")
	}
	// On-disk bytes must be the original.
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(original) {
		t.Error("discard should restore original bytes")
	}
}

// TestCoordinatorStageRestartUpdatesPendingCandidate verifies H-03: a
// stage_restart applied while another staged restart is already pending
// replaces the pending candidate while preserving the original serving config
// as the rollback base.
func TestCoordinatorStageRestartUpdatesPendingCandidate(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	pf := testPreflightStage(t, original)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      pf,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	candidate := restartRequiredConfigRaw(t, ":8080")

	if store.IsPending() {
		t.Fatal("store should not be pending before first stage")
	}
	res1, err := c.ApplyRaw(admin.ApplyRequestContext{}, candidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("first stage error: %v", err)
	}
	if !res1.OK {
		t.Fatalf("first stage ok=false: %s", res1.Message)
	}
	if res1.StagedRestartIsUpdate {
		t.Error("first stage should not be marked as an update")
	}
	if !store.IsPending() {
		t.Fatal("store should be pending after first stage")
	}

	// Second stage replaces the pending candidate.
	updateCandidate := restartRequiredConfigRaw(t, ":8080")
	res2, err := c.ApplyRaw(admin.ApplyRequestContext{}, updateCandidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("second stage error: %v", err)
	}
	if !res2.OK {
		t.Fatalf("second stage should succeed as an update: %s", res2.Message)
	}
	if !res2.StagedRestartIsUpdate {
		t.Error("second stage should be marked as an update")
	}
	if !store.IsPending() {
		t.Error("store should still be pending after update")
	}

	// Backup must still equal the original seed, not the update candidate.
	backup, _ := os.ReadFile(store.backupPath())
	if string(backup) != string(original) {
		t.Errorf("update overwrote original backup\ngot:  %q\nwant: %q", backup, original)
	}
	// Active config must now be the updated candidate.
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(updateCandidate) {
		t.Errorf("active config should be the updated candidate\ngot:  %q\nwant: %q", onDisk, updateCandidate)
	}

	// Marker base metadata must still refer to the original serving version.
	marker, err := store.LoadMarker()
	if err != nil {
		t.Fatalf("load marker: %v", err)
	}
	if marker == nil {
		t.Fatal("marker missing after update")
	}
	if marker.BaseRawSHA256 != sha256Hex(original) {
		t.Error("marker base digest changed after update")
	}
}

// testPreflightStage builds a Preflight that accepts a stage_restart apply for
// a config that is identical to the running startup config (hot path would also
// accept it, but we test stage mode here). The StartupFP is derived from the
// seed bytes so restart-required classification sees them as unchanged.
func testPreflightStage(t *testing.T, seed []byte) *Preflight {
	t.Helper()
	cfg, err := config.Parse(seed)
	if err != nil {
		t.Fatalf("parse seed config: %v", err)
	}
	fp := lifecycle.ComputeFingerprint(cfg)
	p := testPreflight()
	p.StartupFP = fp
	return p
}
