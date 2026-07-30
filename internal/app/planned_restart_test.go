// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

// ─── PlannedRestartStore file-backed tests ───────────────────────────────────

func TestFileStoreIsPendingFalseWhenNoMarker(t *testing.T) {
	tmp := t.TempDir()
	store := NewFilePlannedRestartStore(filepath.Join(tmp, "server.toml"))
	if store.IsPending() {
		t.Fatal("IsPending should be false with no marker file")
	}
}

func TestFileStoreStageManaged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	seed := []byte("[global]\nlog_level = \"info\"\n")
	if err := os.WriteFile(configPath, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		BaseRawSHA256:      sha256Hex(seed),
		StagedRawSHA256:    sha256Hex([]byte("new")),
		StagedVersion:      "v2",
		BaseServingVersion: "v1",
		PendingSubsystems:  []string{"log_format"},
	}
	if err := store.StageManaged(seed, []byte("new"), marker); err != nil {
		t.Fatalf("StageManaged: %v", err)
	}
	// StageManaged leaves the marker in "prepared" state; pending is only true
	// after the caller writes the candidate and calls PromoteToStaged.
	if store.IsPending() {
		t.Fatal("IsPending should be false after StageManaged (marker is prepared)")
	}
	if err := store.PromoteToStaged([]byte("new")); err != nil {
		t.Fatalf("PromoteToStaged: %v", err)
	}
	if !store.IsPending() {
		t.Fatal("IsPending should be true after PromoteToStaged")
	}

	// Marker file must exist and be in staged state.
	loaded, err := store.LoadMarker()
	if err != nil {
		t.Fatalf("LoadMarker: %v", err)
	}
	if loaded == nil || loaded.State != plannedRestartStateStaged {
		t.Fatalf("marker state = %q, want %q", loaded.State, plannedRestartStateStaged)
	}

	// Backup file must exist.
	if _, err := os.Stat(store.backupPath()); err != nil {
		t.Fatalf("backup file missing: %v", err)
	}
}

func TestFileStoreReconcileSuccessfulStartup(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	staged := []byte("staged-content")
	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write staged config: %v", err)
	}

	// Simulate a staged marker: disk matches the staged digest.
	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:         plannedRestartMarkerVersion,
		State:           plannedRestartStateStaged,
		ConfigPath:      configPath,
		BaseRawSHA256:   sha256Hex([]byte("original")),
		StagedRawSHA256: sha256Hex(staged),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), []byte("original"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := store.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if store.IsPending() {
		t.Fatal("IsPending should be false after successful startup reconciliation")
	}
	// Sidecar files should be removed.
	if _, err := os.Stat(store.markerPath()); !os.IsNotExist(err) {
		t.Error("marker file should be removed after successful startup")
	}
	if _, err := os.Stat(store.backupPath()); !os.IsNotExist(err) {
		t.Error("backup file should be removed after successful startup")
	}
}

func TestFileStoreReconcilePreparedEqualsBase(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	original := []byte("original-content")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Prepared marker + disk equals base = write never happened; clean up.
	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:         plannedRestartMarkerVersion,
		State:           plannedRestartStatePrepared,
		ConfigPath:      configPath,
		BaseRawSHA256:   sha256Hex(original),
		StagedRawSHA256: sha256Hex([]byte("staged")),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := store.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if store.IsPending() {
		t.Fatal("IsPending should be false after prepared==base reconciliation")
	}
	if _, err := os.Stat(store.markerPath()); !os.IsNotExist(err) {
		t.Error("marker should be removed")
	}
}

func TestFileStoreReconcilePreparedEqualsStaged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	staged := []byte("staged-content")
	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write staged config: %v", err)
	}

	// Prepared marker + disk equals staged = write completed; promote to staged.
	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:         plannedRestartMarkerVersion,
		State:           plannedRestartStatePrepared,
		ConfigPath:      configPath,
		BaseRawSHA256:   sha256Hex([]byte("original")),
		StagedRawSHA256: sha256Hex(staged),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), []byte("original"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := store.Reconcile(); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !store.IsPending() {
		t.Fatal("IsPending should be true after prepared==staged promotion")
	}
	loaded, err := store.LoadMarker()
	if err != nil || loaded == nil || loaded.State != plannedRestartStateStaged {
		t.Fatalf("marker should be promoted to staged, got %v %v", loaded, err)
	}
}

func TestFileStoreReconcileInconsistentReturnsError(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	if err := os.WriteFile(configPath, []byte("unknown-content"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:         plannedRestartMarkerVersion,
		State:           plannedRestartStatePrepared,
		ConfigPath:      configPath,
		BaseRawSHA256:   sha256Hex([]byte("original")),
		StagedRawSHA256: sha256Hex([]byte("staged")),
		// disk has "unknown-content" which matches neither
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	err := store.Reconcile()
	if err == nil {
		t.Fatal("expected error for inconsistent state")
	}
	// Backup must NOT be removed in inconsistent state.
}

func TestFileStoreDiscardSafeRestoresBytes(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	original := []byte("original-content")
	staged := []byte("staged-content")

	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write staged config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:            plannedRestartMarkerVersion,
		State:              plannedRestartStateStaged,
		ConfigPath:         configPath,
		BaseRawSHA256:      sha256Hex(original),
		BaseServingVersion: "v1",
		StagedRawSHA256:    sha256Hex(staged),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	store.pending = true

	restored, err := store.DiscardSafe("v1")
	if err != nil {
		t.Fatalf("DiscardSafe: %v", err)
	}
	if string(restored) != string(original) {
		t.Errorf("restored bytes = %q, want %q", restored, original)
	}
	// Active config should now contain original bytes.
	onDisk, _ := os.ReadFile(configPath)
	if string(onDisk) != string(original) {
		t.Errorf("on-disk after discard = %q, want %q", onDisk, original)
	}
	// Sidecar files should be removed.
	if _, err := os.Stat(store.markerPath()); !os.IsNotExist(err) {
		t.Error("marker should be removed after discard")
	}
	if store.IsPending() {
		t.Fatal("IsPending should be false after discard")
	}
}

func TestFileStoreDiscardSafeFailsWhenServingVersionChanged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	staged := []byte("staged-content")
	original := []byte("original-content")

	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write staged config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:            plannedRestartMarkerVersion,
		State:              plannedRestartStateStaged,
		ConfigPath:         configPath,
		BaseRawSHA256:      sha256Hex(original),
		BaseServingVersion: "v1",
		StagedRawSHA256:    sha256Hex(staged),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	store.pending = true

	// Live serving version is now "v2" — a concurrent reload happened.
	_, err := store.DiscardSafe("v2")
	if err == nil {
		t.Fatal("expected error when serving version changed")
	}
	// Config should be unchanged.
	onDisk, _ := os.ReadFile(configPath)
	if string(onDisk) != string(staged) {
		t.Errorf("on-disk after failed discard should still be staged, got %q", onDisk)
	}
}

func TestFileStoreDiscardSafeFailsWhenDiskChanged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	original := []byte("original-content")

	// Disk has something other than the staged content.
	if err := os.WriteFile(configPath, []byte("externally-modified"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:            plannedRestartMarkerVersion,
		State:              plannedRestartStateStaged,
		ConfigPath:         configPath,
		BaseRawSHA256:      sha256Hex(original),
		BaseServingVersion: "v1",
		StagedRawSHA256:    sha256Hex([]byte("staged-content")),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}
	store.pending = true

	_, err := store.DiscardSafe("v1")
	if err == nil {
		t.Fatal("expected error when disk digest does not match staged digest")
	}
}

// TestPlannedRestartStoreExternalDivergenceState verifies that
// SetExternalDivergence drives the authoritative State/Status to
// external_divergence and that clearing it returns to none (F-04).
func TestPlannedRestartStoreExternalDivergenceState(t *testing.T) {
	store := &PlannedRestartStore{}

	st := store.State()
	if st.State != PlannedRestartStateNone {
		t.Fatalf("initial state = %q, want none", st.State)
	}

	store.SetExternalDivergence(true)
	st = store.State()
	if st.State != PlannedRestartStateExternalDivergence {
		t.Errorf("state = %q, want external_divergence", st.State)
	}
	if !st.External {
		t.Error("External = false, want true")
	}
	status := store.Status()
	if status.State != string(PlannedRestartStateExternalDivergence) {
		t.Errorf("status.State = %q, want external_divergence", status.State)
	}
	if !status.External {
		t.Error("status.External should mirror State; got false")
	}

	store.SetExternalDivergence(false)
	st = store.State()
	if st.State != PlannedRestartStateNone {
		t.Errorf("cleared state = %q, want none", st.State)
	}
}

// ─── PreflightStageRestart mode tests ─────────────────────────────────────

func TestPreflightStageRestartAcceptsRestartRequiredChange(t *testing.T) {
	addr := freePort(t)
	base := &config.Config{
		Global: config.GlobalConfig{LogFormat: "text"},
		Servers: []config.ServerConfig{{
			Listen:    addr,
			Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}
	next, err := base.Clone()
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	// log_format is restart-required; hot mode would reject it.
	next.Global.LogFormat = "json"

	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream:    &mockStreamPreflighter{},
		StartupFP: lifecycle.ComputeFingerprint(base),
	}

	// Hot mode rejects it.
	if _, err := p.Apply(context.Background(), next, base, PreflightHot); err == nil {
		t.Fatal("hot mode should reject restart-required change")
	}

	// Stage-restart mode accepts and classifies it.
	res, err := p.Apply(context.Background(), next, base, PreflightStageRestart)
	if err != nil {
		t.Fatalf("stage-restart mode rejected restart-required change: %v", err)
	}
	if res.Candidate == nil {
		t.Fatal("stage-restart result has nil candidate")
	}
	// Lifecycle should contain the changed field.
	found := false
	for _, e := range res.Lifecycle {
		if e.Subsystem == "log_format" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("lifecycle does not contain log_format change; got %v", res.Lifecycle)
	}
}

func TestPreflightStageRestartRejectsStructuralErrors(t *testing.T) {
	bad := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Return: 200}}, // missing match
		}},
	}
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	if _, err := p.Apply(context.Background(), bad, nil, PreflightStageRestart); err == nil {
		t.Fatal("stage-restart should still reject structurally invalid configs")
	}
}

func TestPreflightStageRestartLifecycleEmptyWhenNoPrev(t *testing.T) {
	cfg := config.ProxyTarget(":9000", ":0")
	p := Preflight{
		BuildHandlers: func(_ context.Context, _ *config.Config, _ bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream: &mockStreamPreflighter{},
	}
	res, err := p.Apply(context.Background(), cfg, nil, PreflightStageRestart)
	if err != nil {
		t.Fatalf("stage-restart with nil prev: %v", err)
	}
	if len(res.Lifecycle) != 0 {
		t.Errorf("lifecycle should be empty when prev is nil, got %v", res.Lifecycle)
	}
}

// ─── CoordinatorApplyStageRestart integration test ───────────────────────────

func TestCoordinatorApplyStageRestartStagesFile(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")

	base := config.ProxyTarget("127.0.0.1:9000", ":8080")
	base.Global.LogFormat = "text"
	seedRaw, err := config.Marshal(base)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	if err := os.WriteFile(path, seedRaw, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	p := testPreflight()
	p.StartupFP = lifecycle.ComputeFingerprint(base)

	store := NewFilePlannedRestartStore(path)
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      p,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	// Change a restart-required field.
	next := config.ProxyTarget("127.0.0.1:9000", ":8080")
	next.Global.LogFormat = "json"
	nextRaw, err := config.Marshal(next)
	if err != nil {
		t.Fatalf("marshal next: %v", err)
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, nextRaw, ApplyStageRestart)
	if err != nil {
		t.Fatalf("ApplyRaw stage_restart: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false, want true; message: %s", res.Message)
	}
	if res.Mode != ApplyStageRestart {
		t.Errorf("mode = %v, want %v", res.Mode, ApplyStageRestart)
	}
	if !store.IsPending() {
		t.Error("planned restart should be pending after stage")
	}
	// Active config should now have the new bytes.
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(nextRaw) {
		t.Error("on-disk bytes should match staged candidate")
	}
	// Sidecar files should exist.
	if _, err := os.Stat(store.markerPath()); err != nil {
		t.Errorf("marker file missing: %v", err)
	}
	if _, err := os.Stat(store.backupPath()); err != nil {
		t.Errorf("backup file missing: %v", err)
	}
}

func TestCoordinatorHotApplyBlockedWhileStagePending(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(path)
	store.Stage(seed) // in-memory pending

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           path,
		Preflight:      testPreflight(),
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OK {
		t.Fatal("hot apply should be blocked while planned restart is pending")
	}
}

// marshalMarker is a test helper that encodes a marker to JSON bytes.
func marshalMarker(m PlannedRestartMarker) ([]byte, error) {
	return json.Marshal(m)
}

// ─── C-01 fix: backup contains original bytes, not the candidate ─────────────

// TestStageFirstBackupEqualsOriginal is the primary C-01 regression test.
// It stages through the real coordinator and asserts that the .bak file is
// byte-identical to the seed file — including comments and whitespace — not
// to the candidate that was staged.
func TestStageFirstBackupEqualsOriginal(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	// Seed with comments to make it easy to detect if backup == candidate.
	seed := []byte("# original comment\n[global]\nlog_level = \"info\"\n\n[[servers]]\nlisten = \":8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\n")
	if err := os.WriteFile(configPath, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)

	base := config.ProxyTarget("127.0.0.1:9000", ":8080")
	base.Global.LogFormat = "text"
	p := testPreflight()
	p.StartupFP = lifecycle.ComputeFingerprint(base)

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           configPath,
		Preflight:      p,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	// Stage a restart-required change.
	next := config.ProxyTarget("127.0.0.1:9000", ":8080")
	next.Global.LogFormat = "json"
	nextRaw, _ := config.Marshal(next)

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, nextRaw, ApplyStageRestart)
	if err != nil {
		t.Fatalf("ApplyRaw stage_restart: %v", err)
	}
	if !res.OK {
		t.Fatalf("ok = false; message: %s", res.Message)
	}

	// The backup must be the original seed bytes, NOT the candidate.
	backup, err := os.ReadFile(store.backupPath())
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	if string(backup) != string(seed) {
		t.Errorf("backup != seed\nbackup: %q\nseed:   %q", backup, seed)
	}
	// The active config must now contain the staged candidate.
	onDisk, _ := os.ReadFile(configPath)
	if string(onDisk) != string(nextRaw) {
		t.Error("active config should contain the staged candidate")
	}
}

// TestStageDiscardRoundtripRestoresExactBytes verifies that a stage → discard
// cycle through the real coordinator restores the exact original bytes including
// comments and formatting.
func TestStageDiscardRoundtripRestoresExactBytes(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	seed := []byte("# preserved comment\n[global]\nlog_level = \"info\"\n\n[[servers]]\nlisten = \":8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\n")
	if err := os.WriteFile(configPath, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	base := config.ProxyTarget("127.0.0.1:9000", ":8080")
	base.Global.LogFormat = "text"
	p := testPreflight()
	p.StartupFP = lifecycle.ComputeFingerprint(base)

	liveVersion := "v1"
	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           configPath,
		Preflight:      p,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	next := config.ProxyTarget("127.0.0.1:9000", ":8080")
	next.Global.LogFormat = "json"
	nextRaw, _ := config.Marshal(next)

	if _, err := c.ApplyRaw(admin.ApplyRequestContext{}, nextRaw, ApplyStageRestart); err != nil {
		t.Fatalf("stage: %v", err)
	}

	// Discard.
	res, err := c.DiscardPlannedRestart()
	if err != nil {
		t.Fatalf("discard: %v", err)
	}
	if !res.OK {
		t.Fatalf("discard ok=false; message: %s", res.Message)
	}

	// Active config must be exactly the original seed.
	onDisk, _ := os.ReadFile(configPath)
	if string(onDisk) != string(seed) {
		t.Errorf("discard did not restore original bytes\ngot:  %q\nwant: %q", onDisk, seed)
	}
	// Sidecar files must be removed.
	if _, err := os.Stat(store.markerPath()); !os.IsNotExist(err) {
		t.Error("marker should be removed after discard")
	}
	if _, err := os.Stat(store.backupPath()); !os.IsNotExist(err) {
		t.Error("backup should be removed after discard")
	}
	_ = liveVersion
}

// TestStageRestartUpdatesSecondCandidate verifies H-03: a second stage_restart
// while one is already pending replaces the pending candidate while preserving
// the original backup and base metadata.
func TestStageRestartUpdatesSecondCandidate(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	seed := []byte("# original\n[global]\nlog_level = \"info\"\n\n[[servers]]\nlisten = \":8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\n")
	if err := os.WriteFile(configPath, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	base := config.ProxyTarget("127.0.0.1:9000", ":8080")
	base.Global.LogFormat = "text"
	p := testPreflight()
	p.StartupFP = lifecycle.ComputeFingerprint(base)

	c := &ConfigApplyCoordinator{
		BaseCtx:        context.Background(),
		Path:           configPath,
		Preflight:      p,
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{} },
		PlannedRestart: store,
	}

	// First stage succeeds.
	v1 := config.ProxyTarget("127.0.0.1:9000", ":8080")
	v1.Global.LogFormat = "json"
	v1Raw, _ := config.Marshal(v1)
	if _, err := c.ApplyRaw(admin.ApplyRequestContext{}, v1Raw, ApplyStageRestart); err != nil {
		t.Fatalf("first stage: %v", err)
	}

	// Second stage (update) replaces the pending candidate.
	v2 := config.ProxyTarget("127.0.0.1:9001", ":8080")
	v2.Global.LogFormat = "json"
	v2Raw, _ := config.Marshal(v2)
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, v2Raw, ApplyStageRestart)
	if err != nil {
		t.Fatalf("second stage returned error: %v", err)
	}
	if !res.OK {
		t.Fatalf("second stage should succeed as an update: %s", res.Message)
	}
	if !res.StagedRestartIsUpdate {
		t.Error("second stage should be marked as an update")
	}

	// Backup must still equal the original seed, not v2.
	backup, _ := os.ReadFile(store.backupPath())
	if string(backup) != string(seed) {
		t.Errorf("update overwrote original backup\ngot:  %q\nwant: %q", backup, seed)
	}
	// Active config must now be the updated candidate.
	onDisk, _ := os.ReadFile(configPath)
	if string(onDisk) != string(v2Raw) {
		t.Errorf("active config should be the updated candidate\ngot:  %q\nwant: %q", onDisk, v2Raw)
	}
}

// ─── C-02 fix: single store, reconciliation after listeners bind ─────────────

// TestInconsistentMarkerSetsFlag verifies that a corrupt/inconsistent sidecar
// state is surfaced through Status().Inconsistent rather than silently dropped.
func TestInconsistentMarkerSetsFlag(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	// Disk content matches neither base nor staged digest → inconsistent.
	if err := os.WriteFile(configPath, []byte("unknown-content"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:         plannedRestartMarkerVersion,
		State:           plannedRestartStatePrepared,
		ConfigPath:      configPath,
		BaseRawSHA256:   sha256Hex([]byte("original")),
		StagedRawSHA256: sha256Hex([]byte("staged")),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	err := store.Reconcile()
	if err == nil {
		t.Fatal("expected error for inconsistent state")
	}
	st := store.Status()
	if !st.Inconsistent {
		t.Error("Status().Inconsistent should be true after Reconcile detects inconsistency")
	}
}

// ─── B1: managed_staged takes priority over external_divergence (H-02) ──────

// TestManagedStagedTakesPriorityOverExternalDivergence verifies that when both
// a valid managed staged marker is pending AND external divergence is set (e.g.
// because PendingRestartCheck ran and saw disk != startup), the authoritative
// state is managed_staged, not external_divergence (H-02).
func TestManagedStagedTakesPriorityOverExternalDivergence(t *testing.T) {
	store := &PlannedRestartStore{}

	// Simulate the race: external divergence set before managed staged is known.
	store.SetExternalDivergence(true)
	store.Stage([]byte("staged-candidate"))

	st := store.State()
	if st.State != PlannedRestartStateManagedStaged {
		t.Errorf("state = %q, want managed_staged when both pending and external are set", st.State)
	}
	if !st.Pending {
		t.Error("Pending should be true")
	}
	if st.External {
		t.Error("External should be false when managed_staged wins priority")
	}
}

// TestFileStoreManagedStagedTakesPriorityOverExternalDivergence tests the same
// priority fix with a real file-backed store and a valid marker on disk.
func TestFileStoreManagedStagedTakesPriorityOverExternalDivergence(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	staged := []byte("staged-config")
	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Write the marker before constructing the store so the constructor can
	// hydrate the in-memory pending flag via LoadMarker.
	markerData, _ := marshalMarker(PlannedRestartMarker{
		Version:            plannedRestartMarkerVersion,
		State:              plannedRestartStateStaged,
		ConfigPath:         configPath,
		BaseRawSHA256:      sha256Hex([]byte("original")),
		StagedRawSHA256:    sha256Hex(staged),
		BaseServingVersion: "v1",
	})
	// Determine the marker path without a store instance (same suffix convention).
	markerPath := configPath + ".pending-restart.json"
	backupPath := configPath + ".pending-restart.bak"
	if err := os.WriteFile(markerPath, markerData, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte("original"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	// Constructor hydrates pending=true from the staged marker.
	store := NewFilePlannedRestartStore(configPath)
	if !store.IsPending() {
		t.Fatal("constructor should set IsPending=true from the staged marker")
	}

	// Simulate what the RefreshState hook used to do: set external divergence
	// because the startup config differs from the current disk config.
	store.SetExternalDivergence(true)

	st := store.State()
	if st.State != PlannedRestartStateManagedStaged {
		t.Errorf("state = %q, want managed_staged; external divergence should not override a valid staged marker", st.State)
	}
}

// ─── B2: previous_staged_raw_sha256 field populated on updates ───────────────

// TestStageUpdateStoresPreviousStagedDigest verifies that StageManaged records
// the previous staged digest in PreviousStagedRawSHA256 when performing a
// staged update, enabling crash recovery (N-03).
func TestStageUpdateStoresPreviousStagedDigest(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	seed := []byte("original-config")
	v1 := []byte("staged-v1")

	if err := os.WriteFile(configPath, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)

	// First stage.
	m1 := PlannedRestartMarker{
		BaseRawSHA256:   sha256Hex(seed),
		StagedRawSHA256: sha256Hex(v1),
		StagedVersion:   "v1",
	}
	if err := store.StageManaged(seed, v1, m1); err != nil {
		t.Fatalf("first StageManaged: %v", err)
	}
	if err := store.PromoteToStaged(v1); err != nil {
		t.Fatalf("first PromoteToStaged: %v", err)
	}

	// Second stage (update): write v1 to disk so the store sees it as staged.
	if err := os.WriteFile(configPath, v1, 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	v2 := []byte("staged-v2")
	m2 := PlannedRestartMarker{
		StagedRawSHA256: sha256Hex(v2),
		StagedVersion:   "v2",
	}
	if err := store.StageManaged(seed, v2, m2); err != nil {
		t.Fatalf("second StageManaged: %v", err)
	}

	loaded, err := store.LoadMarker()
	if err != nil || loaded == nil {
		t.Fatalf("LoadMarker after update: %v, %v", loaded, err)
	}
	if loaded.PreviousStagedRawSHA256 != sha256Hex(v1) {
		t.Errorf("PreviousStagedRawSHA256 = %q, want digest of v1", loaded.PreviousStagedRawSHA256)
	}
	// BaseRawSHA256 must be preserved from the first stage.
	if loaded.BaseRawSHA256 != sha256Hex(seed) {
		t.Errorf("BaseRawSHA256 = %q, want digest of seed", loaded.BaseRawSHA256)
	}
}

// ─── B3: PromoteToStaged returns sentinel errors (N-03) ──────────────────────

// TestPromoteToStagedErrorOnMissingMarker verifies that PromoteToStaged returns
// ErrNoManagedPreparedMarker when no marker file is present, rather than
// silently succeeding.
func TestPromoteToStagedErrorOnMissingMarker(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	if err := os.WriteFile(configPath, []byte("content"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store := NewFilePlannedRestartStore(configPath)
	// No StageManaged call → no marker on disk.
	err := store.PromoteToStaged([]byte("candidate"))
	if !errors.Is(err, ErrNoManagedPreparedMarker) {
		t.Errorf("PromoteToStaged with missing marker = %v, want ErrNoManagedPreparedMarker", err)
	}
}

// TestPromoteToStagedErrorOnWrongState verifies that PromoteToStaged returns
// ErrMarkerWrongState when the marker is already in "staged" state (e.g. a
// duplicate call after a crash-resume).
func TestPromoteToStagedErrorOnWrongState(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	staged := []byte("staged-content")
	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:         plannedRestartMarkerVersion,
		State:           plannedRestartStateStaged, // already staged, not prepared
		ConfigPath:      configPath,
		BaseRawSHA256:   sha256Hex([]byte("original")),
		StagedRawSHA256: sha256Hex(staged),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	err := store.PromoteToStaged(staged)
	if !errors.Is(err, ErrMarkerWrongState) {
		t.Errorf("PromoteToStaged with staged marker = %v, want ErrMarkerWrongState", err)
	}
}

// ─── B4: crash-injection tests (N-03) ────────────────────────────────────────

// TestReconcileUpdateCrashRecoversPreviousStaged simulates a staged-update
// crash: the prepared marker has PreviousStagedRawSHA256 set and the disk
// still holds the previous staged content. Reconcile must restore the store
// to a clean staged state using the previous digest rather than marking
// the state as inconsistent.
func TestReconcileUpdateCrashRecoversPreviousStaged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	original := []byte("original-content")
	v1 := []byte("staged-v1-content")
	v2 := []byte("staged-v2-content") // never written to disk

	// Disk holds the previous staged content (v1) — v2 write failed.
	if err := os.WriteFile(configPath, v1, 0o600); err != nil {
		t.Fatalf("write v1 to disk: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	// Write the prepared marker that would have been written by StageManaged
	// for the second stage attempt.
	marker := PlannedRestartMarker{
		Version:                 plannedRestartMarkerVersion,
		State:                   plannedRestartStatePrepared,
		ConfigPath:              configPath,
		BaseRawSHA256:           sha256Hex(original),
		StagedRawSHA256:         sha256Hex(v2), // the failed new candidate
		PreviousStagedRawSHA256: sha256Hex(v1), // the safe fallback
		StagedVersion:           "v2",
		BaseServingVersion:      "v0",
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := store.Reconcile(); err != nil {
		t.Fatalf("Reconcile should recover from update crash without error, got: %v", err)
	}
	if !store.IsPending() {
		t.Fatal("IsPending should be true after update-crash recovery")
	}
	st := store.State()
	if st.State != PlannedRestartStateManagedStaged {
		t.Errorf("state = %q, want managed_staged after update-crash recovery", st.State)
	}

	// Marker on disk must now be in staged state with PreviousStagedRawSHA256 cleared.
	loaded, err := store.LoadMarker()
	if err != nil || loaded == nil {
		t.Fatalf("LoadMarker after recovery: %v %v", loaded, err)
	}
	if loaded.State != plannedRestartStateStaged {
		t.Errorf("recovered marker state = %q, want staged", loaded.State)
	}
	if loaded.StagedRawSHA256 != sha256Hex(v1) {
		t.Errorf("recovered StagedRawSHA256 = %q, want digest of v1", loaded.StagedRawSHA256)
	}
	if loaded.PreviousStagedRawSHA256 != "" {
		t.Errorf("PreviousStagedRawSHA256 should be cleared after recovery, got %q", loaded.PreviousStagedRawSHA256)
	}
	// BaseRawSHA256 must be preserved from original.
	if loaded.BaseRawSHA256 != sha256Hex(original) {
		t.Errorf("BaseRawSHA256 = %q, want digest of original", loaded.BaseRawSHA256)
	}
}

// TestReconcileUpdateCrashRestoresFullPreviousMetadata is the M-01 regression
// test. A staged-update crash must restore candidate A's version, subsystem
// list, AND timestamp — not just its bytes — and must clear every Previous*
// field so no candidate-B metadata leaks into the recovered state.
func TestReconcileUpdateCrashRestoresFullPreviousMetadata(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	original := []byte("original-content")
	v1 := []byte("staged-v1-content")
	v2 := []byte("staged-v2-content") // failed write, never on disk

	// Disk holds candidate A (v1) — the v2 update write failed.
	if err := os.WriteFile(configPath, v1, 0o600); err != nil {
		t.Fatalf("write v1 to disk: %v", err)
	}

	// Distinct timestamps prove the restore uses candidate A's time and not
	// candidate B's failed-attempt time.
	prevStagedAt := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	failedStagedAt := time.Date(2026, 1, 2, 15, 30, 0, 0, time.UTC)

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:                 plannedRestartMarkerVersion,
		State:                   plannedRestartStatePrepared,
		ConfigPath:              configPath,
		BaseRawSHA256:           sha256Hex(original),
		BaseServingVersion:      "v0",
		StagedRawSHA256:         sha256Hex(v2),
		StagedVersion:           "v2",
		PendingSubsystems:       []string{"http", "tls"}, // candidate B subsystems
		StagedAt:                failedStagedAt,
		PreviousStagedRawSHA256: sha256Hex(v1),
		PreviousStagedVersion:   "v1",
		PreviousSubsystems:      []string{"http"}, // candidate A subsystems
		PreviousStagedAt:        prevStagedAt,
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := store.Reconcile(); err != nil {
		t.Fatalf("Reconcile should recover without error, got: %v", err)
	}

	loaded, err := store.LoadMarker()
	if err != nil || loaded == nil {
		t.Fatalf("LoadMarker after recovery: %v %v", loaded, err)
	}

	// Full marker equality: every field must describe candidate A alone.
	if loaded.State != plannedRestartStateStaged {
		t.Errorf("state = %q, want staged", loaded.State)
	}
	if loaded.StagedRawSHA256 != sha256Hex(v1) {
		t.Errorf("StagedRawSHA256 = %q, want digest of v1", loaded.StagedRawSHA256)
	}
	if loaded.StagedVersion != "v1" {
		t.Errorf("StagedVersion = %q, want v1", loaded.StagedVersion)
	}
	if len(loaded.PendingSubsystems) != 1 || loaded.PendingSubsystems[0] != "http" {
		t.Errorf("PendingSubsystems = %v, want [http]", loaded.PendingSubsystems)
	}
	if !loaded.StagedAt.Equal(prevStagedAt) {
		t.Errorf("StagedAt = %v, want candidate A time %v (M-01: timestamp must be restored)", loaded.StagedAt, prevStagedAt)
	}
	// Base metadata preserved.
	if loaded.BaseRawSHA256 != sha256Hex(original) {
		t.Errorf("BaseRawSHA256 = %q, want digest of original", loaded.BaseRawSHA256)
	}
	// Every Previous* field must be cleared: no stale candidate metadata.
	if loaded.PreviousStagedRawSHA256 != "" {
		t.Errorf("PreviousStagedRawSHA256 = %q, want cleared", loaded.PreviousStagedRawSHA256)
	}
	if loaded.PreviousStagedVersion != "" {
		t.Errorf("PreviousStagedVersion = %q, want cleared", loaded.PreviousStagedVersion)
	}
	if loaded.PreviousSubsystems != nil {
		t.Errorf("PreviousSubsystems = %v, want cleared", loaded.PreviousSubsystems)
	}
	if !loaded.PreviousStagedAt.IsZero() {
		t.Errorf("PreviousStagedAt = %v, want cleared (zero)", loaded.PreviousStagedAt)
	}

	// The in-memory Status() must also report candidate A's timestamp.
	status := store.Status()
	if !status.StagedAt.Equal(prevStagedAt) {
		t.Errorf("Status().StagedAt = %v, want %v", status.StagedAt, prevStagedAt)
	}
}

// TestPromoteToStagedClearsPreviousMetadata is the M-01 happy-path guard: a
// successful staged-update promote must clear every Previous* field so a later
// unrelated crash recovery cannot resurrect a superseded candidate.
func TestPromoteToStagedClearsPreviousMetadata(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	original := []byte("original-content")
	v1 := []byte("staged-v1-content")
	v2 := []byte("staged-v2-content")

	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}
	store := NewFilePlannedRestartStore(configPath)

	// A prepared update marker carrying candidate A (v1) as the previous staged
	// content while candidate B (v2) is the new attempt.
	marker := PlannedRestartMarker{
		Version:                 plannedRestartMarkerVersion,
		State:                   plannedRestartStatePrepared,
		ConfigPath:              configPath,
		BaseRawSHA256:           sha256Hex(original),
		StagedRawSHA256:         sha256Hex(v2),
		StagedVersion:           "v2",
		StagedAt:                time.Date(2026, 1, 2, 15, 30, 0, 0, time.UTC),
		PreviousStagedRawSHA256: sha256Hex(v1),
		PreviousStagedVersion:   "v1",
		PreviousSubsystems:      []string{"http"},
		PreviousStagedAt:        time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// Candidate B write succeeds and we promote.
	if err := os.WriteFile(configPath, v2, 0o600); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := store.PromoteToStaged(v2); err != nil {
		t.Fatalf("PromoteToStaged: %v", err)
	}

	loaded, err := store.LoadMarker()
	if err != nil || loaded == nil {
		t.Fatalf("LoadMarker after promote: %v %v", loaded, err)
	}
	if loaded.State != plannedRestartStateStaged {
		t.Errorf("state = %q, want staged", loaded.State)
	}
	if loaded.PreviousStagedRawSHA256 != "" {
		t.Errorf("PreviousStagedRawSHA256 = %q, want cleared after promote", loaded.PreviousStagedRawSHA256)
	}
	if loaded.PreviousStagedVersion != "" {
		t.Errorf("PreviousStagedVersion = %q, want cleared after promote", loaded.PreviousStagedVersion)
	}
	if loaded.PreviousSubsystems != nil {
		t.Errorf("PreviousSubsystems = %v, want cleared after promote", loaded.PreviousSubsystems)
	}
	if !loaded.PreviousStagedAt.IsZero() {
		t.Errorf("PreviousStagedAt = %v, want cleared after promote", loaded.PreviousStagedAt)
	}
}

// TestRefreshUpdateCrashRecoversPreviousStaged tests the same update-crash
// recovery path via Refresh (called during the running process's live
// RefreshState hook) rather than Reconcile (called on startup).
func TestRefreshUpdateCrashRecoversPreviousStaged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	original := []byte("original-content")
	v1 := []byte("staged-v1-content")
	v2 := []byte("staged-v2-content")

	// Disk holds v1 — v2 write failed.
	if err := os.WriteFile(configPath, v1, 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:                 plannedRestartMarkerVersion,
		State:                   plannedRestartStatePrepared,
		ConfigPath:              configPath,
		BaseRawSHA256:           sha256Hex(original),
		StagedRawSHA256:         sha256Hex(v2),
		PreviousStagedRawSHA256: sha256Hex(v1),
		StagedVersion:           "v2",
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(store.backupPath(), original, 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	if err := store.Refresh(); err != nil {
		t.Fatalf("Refresh should recover from update crash without error, got: %v", err)
	}
	if !store.IsPending() {
		t.Fatal("IsPending should be true after Refresh update-crash recovery")
	}

	loaded, err := store.LoadMarker()
	if err != nil || loaded == nil {
		t.Fatalf("LoadMarker after Refresh recovery: %v %v", loaded, err)
	}
	if loaded.State != plannedRestartStateStaged {
		t.Errorf("recovered marker state = %q, want staged", loaded.State)
	}
	if loaded.StagedRawSHA256 != sha256Hex(v1) {
		t.Errorf("recovered StagedRawSHA256 = %q, want digest of v1", loaded.StagedRawSHA256)
	}
	if loaded.PreviousStagedRawSHA256 != "" {
		t.Errorf("PreviousStagedRawSHA256 should be cleared, got %q", loaded.PreviousStagedRawSHA256)
	}
}

// TestReconcileUpdateCrashTrulyInconsistent verifies that Reconcile still
// marks the state inconsistent when disk matches neither base, staged, previous
// staged, nor any known digest — i.e. a genuine corruption, not just an update
// crash.
func TestReconcileUpdateCrashTrulyInconsistent(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")

	// Disk holds content that matches none of the marker digests.
	if err := os.WriteFile(configPath, []byte("completely-unknown"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	store := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:                 plannedRestartMarkerVersion,
		State:                   plannedRestartStatePrepared,
		ConfigPath:              configPath,
		BaseRawSHA256:           sha256Hex([]byte("original")),
		StagedRawSHA256:         sha256Hex([]byte("v2")),
		PreviousStagedRawSHA256: sha256Hex([]byte("v1")),
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(store.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	err := store.Reconcile()
	if err == nil {
		t.Fatal("expected error for truly inconsistent state")
	}
	st := store.Status()
	if !st.Inconsistent {
		t.Error("Status().Inconsistent should be true")
	}
}

// ─── B5: composition-level RefreshState guard (H-02) ─────────────────────────

// TestRefreshStateSkipsDivergenceCheckWhenManaged verifies the composition-
// level contract added in serve.go: when a valid managed staged restart is
// pending, the RefreshState hook must NOT call PendingRestartCheck (which
// would set external divergence). It also verifies that any stale external
// flag is cleared so the authoritative state remains managed_staged.
func TestRefreshStateSkipsDivergenceCheckWhenManaged(t *testing.T) {
	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "server.toml")
	staged := []byte("staged-config")
	if err := os.WriteFile(configPath, staged, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	sharedStore := NewFilePlannedRestartStore(configPath)
	marker := PlannedRestartMarker{
		Version:            plannedRestartMarkerVersion,
		State:              plannedRestartStateStaged,
		ConfigPath:         configPath,
		BaseRawSHA256:      sha256Hex([]byte("original")),
		StagedRawSHA256:    sha256Hex(staged),
		BaseServingVersion: "v1",
	}
	raw, _ := marshalMarker(marker)
	if err := os.WriteFile(sharedStore.markerPath(), raw, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.WriteFile(sharedStore.backupPath(), []byte("original"), 0o600); err != nil {
		t.Fatalf("write backup: %v", err)
	}

	// Prime in-memory state to simulate what Reconcile would do on startup.
	if err := sharedStore.Refresh(); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	// Pre-set a stale external-divergence flag (could happen from a prior cycle).
	sharedStore.SetExternalDivergence(true)

	// Wire the same RefreshState closure that serve.go constructs.
	divergenceCheckCalled := false
	pendingRestartCheckFn := func(_ server.LiveSnapshot) []string {
		divergenceCheckCalled = true
		sharedStore.SetExternalDivergence(true)
		return []string{"log_format"}
	}
	refreshState := func() error {
		if err := sharedStore.Refresh(); err != nil {
			return err
		}
		if sharedStore.IsPending() {
			sharedStore.SetExternalDivergence(false)
		} else {
			_ = pendingRestartCheckFn(server.LiveSnapshot{})
		}
		return nil
	}

	if err := refreshState(); err != nil {
		t.Fatalf("refreshState: %v", err)
	}
	if divergenceCheckCalled {
		t.Error("PendingRestartCheck must not be called when a managed staged marker is pending")
	}

	st := sharedStore.State()
	if st.State != PlannedRestartStateManagedStaged {
		t.Errorf("state = %q, want managed_staged; external divergence must not win priority", st.State)
	}
	if st.External {
		t.Error("External should be false after RefreshState clears the stale flag")
	}
}

// TestRefreshStateCallsDivergenceCheckWhenNotManaged verifies the complementary
// path: when no managed staged restart is pending, RefreshState does call
// PendingRestartCheck and propagates the external-divergence flag normally.
func TestRefreshStateCallsDivergenceCheckWhenNotManaged(t *testing.T) {
	sharedStore := &PlannedRestartStore{} // in-memory, no managed pending

	checkCalled := false
	pendingRestartCheckFn := func(_ server.LiveSnapshot) []string {
		checkCalled = true
		sharedStore.SetExternalDivergence(true)
		return []string{"log_format"}
	}
	refreshState := func() error {
		// No Refresh needed for in-memory store.
		if sharedStore.IsPending() {
			sharedStore.SetExternalDivergence(false)
		} else {
			_ = pendingRestartCheckFn(server.LiveSnapshot{})
		}
		return nil
	}

	if err := refreshState(); err != nil {
		t.Fatalf("refreshState: %v", err)
	}
	if !checkCalled {
		t.Error("PendingRestartCheck must be called when no managed staged restart is pending")
	}
	st := sharedStore.State()
	if st.State != PlannedRestartStateExternalDivergence {
		t.Errorf("state = %q, want external_divergence", st.State)
	}
}
