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
