// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// stagePrepared writes a prepared marker plus a candidate on disk so
// PromoteToStagedVerified has a realistic starting state. It returns the store,
// the config path, and the candidate bytes.
func stagePrepared(t *testing.T) (*PlannedRestartStore, string, []byte) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	base := []byte("listen = \":8080\"\n")
	candidate := []byte("listen = \":9090\"\n")
	if err := os.WriteFile(path, base, 0o600); err != nil {
		t.Fatalf("write base: %v", err)
	}
	store := NewFilePlannedRestartStore(path)
	marker := PlannedRestartMarker{
		StagedRawSHA256:   sha256Hex(candidate),
		StagedVersion:     "v2",
		PendingSubsystems: []string{"listener"},
	}
	if err := store.StageManaged(base, candidate, marker); err != nil {
		t.Fatalf("StageManaged: %v", err)
	}
	// Caller writes the candidate to disk (step 3), exactly as the coordinator
	// does before promotion.
	if err := os.WriteFile(path, candidate, 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	return store, path, candidate
}

// TestPromoteVerifiedSucceedsWhenDiskMatches verifies the happy path: the disk
// holds exactly the candidate, so the verified promotion staged the marker.
func TestPromoteVerifiedSucceedsWhenDiskMatches(t *testing.T) {
	store, _, candidate := stagePrepared(t)
	if err := store.PromoteToStagedVerified(candidate); err != nil {
		t.Fatalf("PromoteToStagedVerified: %v", err)
	}
	if !store.IsPending() {
		t.Fatal("store should be pending after a verified promotion")
	}
	m, err := store.LoadMarker()
	if err != nil {
		t.Fatalf("LoadMarker: %v", err)
	}
	if m == nil || m.State != plannedRestartStateStaged {
		t.Fatalf("marker = %+v, want staged", m)
	}
}

// TestPromoteVerifiedRejectsExternalWriteBeforePromotion verifies AC-06: an
// external writer replaces the just-written candidate before promotion. The
// verified promotion must return ErrStagedCandidateChanged and must not stage
// the marker or report success.
func TestPromoteVerifiedRejectsExternalWriteBeforePromotion(t *testing.T) {
	store, path, candidate := stagePrepared(t)
	// External writer lands different bytes after the candidate write but
	// before promotion.
	external := []byte("listen = \":7777\"\n")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatalf("external write: %v", err)
	}
	err := store.PromoteToStagedVerified(candidate)
	if !errors.Is(err, ErrStagedCandidateChanged) {
		t.Fatalf("err = %v, want ErrStagedCandidateChanged", err)
	}
	// The marker must NOT have been promoted to staged.
	m, lerr := store.LoadMarker()
	if lerr != nil {
		t.Fatalf("LoadMarker: %v", lerr)
	}
	if m == nil || m.State != plannedRestartStatePrepared {
		t.Fatalf("marker = %+v, want still prepared (not promoted)", m)
	}
	// The external bytes must remain untouched — no blind repair.
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(external) {
		t.Fatal("verified promotion overwrote the external write")
	}
}

// TestPromoteVerifiedRejectsMarkerCandidateMismatch verifies AC-06: when the
// supplied candidate bytes do not match the digest the marker was prepared for,
// the promotion returns ErrMarkerCandidateMismatch and flags inconsistency.
func TestPromoteVerifiedRejectsMarkerCandidateMismatch(t *testing.T) {
	store, _, _ := stagePrepared(t)
	err := store.PromoteToStagedVerified([]byte("listen = \":1234\"\n"))
	if !errors.Is(err, ErrMarkerCandidateMismatch) {
		t.Fatalf("err = %v, want ErrMarkerCandidateMismatch", err)
	}
	if !store.State().Inconsistent {
		t.Fatal("store should be flagged inconsistent after a marker/candidate mismatch")
	}
}

// TestPromoteVerifiedNoMarkerFails verifies the sequencing guard: with no
// prepared marker present, the verified promotion returns
// ErrNoManagedPreparedMarker rather than silently succeeding.
func TestPromoteVerifiedNoMarkerFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(path, []byte("listen = \":8080\"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	store := NewFilePlannedRestartStore(path)
	err := store.PromoteToStagedVerified([]byte("listen = \":8080\"\n"))
	if !errors.Is(err, ErrNoManagedPreparedMarker) {
		t.Fatalf("err = %v, want ErrNoManagedPreparedMarker", err)
	}
}

// TestPromoteVerifiedWrongStateFails verifies the verified promotion rejects a
// marker that is already staged (a duplicate promotion) with ErrMarkerWrongState.
func TestPromoteVerifiedWrongStateFails(t *testing.T) {
	store, _, candidate := stagePrepared(t)
	if err := store.PromoteToStagedVerified(candidate); err != nil {
		t.Fatalf("first promotion: %v", err)
	}
	// A second promotion finds the marker already staged.
	err := store.PromoteToStagedVerified(candidate)
	if !errors.Is(err, ErrMarkerWrongState) {
		t.Fatalf("err = %v, want ErrMarkerWrongState", err)
	}
}

// TestAbortPreparedCannotCleanUpAStagedMarker pins the precondition that made
// ClearStagingArtifacts necessary: AbortPrepared only recognizes
// a marker still in "prepared" state. A marker PromoteToStagedVerified has
// already written "staged" — which its own post-promotion check can then find
// disagrees with disk — is left completely untouched by AbortPrepared. A
// caller that only ever calls AbortPrepared on such a failure leaks a staged
// marker and its backup while reporting the operation as failed (ADR 0019
// §11.2.4.1).
func TestAbortPreparedCannotCleanUpAStagedMarker(t *testing.T) {
	store, path, candidate := stagePrepared(t)
	if err := store.PromoteToStagedVerified(candidate); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	if err := store.AbortPrepared(nil); !errors.Is(err, ErrNoManagedPreparedMarker) {
		t.Fatalf("AbortPrepared on a staged marker = %v, want ErrNoManagedPreparedMarker", err)
	}
	// The marker and backup are still there — AbortPrepared did nothing.
	if _, err := os.Stat(path + ".pending-restart.json"); err != nil {
		t.Fatalf("marker should still exist after a no-op AbortPrepared: %v", err)
	}
	if _, err := os.Stat(path + ".pending-restart.bak"); err != nil {
		t.Fatalf("backup should still exist after a no-op AbortPrepared: %v", err)
	}
}

// TestClearStagingArtifactsRemovesStagedArtifacts is the fix for
// the gap TestAbortPreparedCannotCleanUpAStagedMarker documents: the dedicated
// cleanup removes a marker regardless of its state, leaves the configuration
// file untouched, and a subsequent Reconcile finds a clean slate rather than
// resurrecting the abandoned stage.
func TestClearStagingArtifactsRemovesStagedArtifacts(t *testing.T) {
	store, path, candidate := stagePrepared(t)
	if err := store.PromoteToStagedVerified(candidate); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	diskBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	if err := store.ClearStagingArtifacts(); err != nil {
		t.Fatalf("ClearStagingArtifacts: %v", err)
	}
	if _, err := os.Stat(path + ".pending-restart.json"); !os.IsNotExist(err) {
		t.Errorf("marker should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(path + ".pending-restart.bak"); !os.IsNotExist(err) {
		t.Errorf("backup should be removed, stat err = %v", err)
	}
	diskAfter, err := os.ReadFile(path)
	if err != nil || string(diskAfter) != string(diskBefore) {
		t.Errorf("configuration file must be untouched: before=%q after=%q err=%v", diskBefore, diskAfter, err)
	}
	if store.IsPending() {
		t.Error("store must not report a pending restart after the clear")
	}

	// A restart's ordinary reconciliation must not resurrect anything: no
	// marker exists, so Reconcile finds a clean state.
	fresh := NewFilePlannedRestartStore(path)
	if err := fresh.Reconcile(); err != nil {
		t.Fatalf("Reconcile after clear: %v", err)
	}
	if fresh.IsPending() {
		t.Error("a fresh store must not find a resurrected staged restart")
	}
}

// TestClearStagingArtifactsMarkerRemoveFailure exercises the marker-removal
// error branch: a non-empty directory sitting at the marker's path cannot be
// removed by os.Remove (it is not empty), so the failure surfaces instead of
// being silently swallowed.
func TestClearStagingArtifactsMarkerRemoveFailure(t *testing.T) {
	store, path, candidate := stagePrepared(t)
	if err := store.PromoteToStagedVerified(candidate); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	markerPath := path + ".pending-restart.json"
	if err := os.Remove(markerPath); err != nil {
		t.Fatalf("remove real marker: %v", err)
	}
	if err := os.Mkdir(markerPath, 0o755); err != nil {
		t.Fatalf("mkdir in place of marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(markerPath, "occupant"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write occupant file: %v", err)
	}

	if err := store.ClearStagingArtifacts(); err == nil {
		t.Fatal("expected an error when the marker path is a non-empty directory")
	}
}

// TestClearStagingArtifactsBackupRemoveFailure mirrors the marker case for
// the backup path.
func TestClearStagingArtifactsBackupRemoveFailure(t *testing.T) {
	store, path, candidate := stagePrepared(t)
	if err := store.PromoteToStagedVerified(candidate); err != nil {
		t.Fatalf("promotion: %v", err)
	}
	backupPath := path + ".pending-restart.bak"
	if err := os.Remove(backupPath); err != nil {
		t.Fatalf("remove real backup: %v", err)
	}
	if err := os.Mkdir(backupPath, 0o755); err != nil {
		t.Fatalf("mkdir in place of backup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(backupPath, "occupant"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write occupant file: %v", err)
	}

	if err := store.ClearStagingArtifacts(); err == nil {
		t.Fatal("expected an error when the backup path is a non-empty directory")
	}
}
