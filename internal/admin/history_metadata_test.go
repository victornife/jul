// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHistorySnapshotWithMetaWritesSidecar verifies AC-05: a snapshot written
// with metadata produces both the raw <id>.toml and the <id>.json sidecar, and
// getMeta round-trips the provenance with the schema version stamped in.
func TestHistorySnapshotWithMetaWritesSidecar(t *testing.T) {
	h := newHistory(t.TempDir(), 50)
	id, metaErr, err := h.snapshotWithMeta([]byte("listen = \":8080\"\n"), &HistoryMetadata{
		ApplyID:          "rl_7",
		Operation:        ApplyOperationConfigApply,
		Mode:             "hot",
		Outcome:          "applied_live",
		Actor:            "alice",
		Reason:           historyReasonPreApply,
		PreviousVersion:  "v-prev",
		CandidateVersion: "v-next",
	})
	if err != nil {
		t.Fatalf("snapshotWithMeta err: %v", err)
	}
	if metaErr != nil {
		t.Fatalf("snapshotWithMeta metaErr: %v", metaErr)
	}
	if id == "" {
		t.Fatal("expected non-empty snapshot id")
	}
	// Raw snapshot is listable and retrievable.
	if _, err := h.get(id); err != nil {
		t.Fatalf("get raw: %v", err)
	}
	// Sidecar round-trips.
	m, err := h.getMeta(id)
	if err != nil {
		t.Fatalf("getMeta: %v", err)
	}
	if m == nil {
		t.Fatal("expected metadata, got nil")
	}
	if m.SchemaVersion != historyMetaSchemaVersion {
		t.Errorf("schema_version = %d, want %d", m.SchemaVersion, historyMetaSchemaVersion)
	}
	if m.ApplyID != "rl_7" || m.Operation != ApplyOperationConfigApply || m.Mode != "hot" {
		t.Errorf("metadata mismatch: %+v", m)
	}
	if m.Outcome != "applied_live" || m.Actor != "alice" || m.Reason != historyReasonPreApply {
		t.Errorf("metadata mismatch: %+v", m)
	}
	if m.PreviousVersion != "v-prev" || m.CandidateVersion != "v-next" {
		t.Errorf("version metadata mismatch: %+v", m)
	}
	if m.CreatedAt.IsZero() {
		t.Error("created_at should be populated from the snapshot id")
	}
}

// TestHistoryRawOnlySnapshotHasNoMeta verifies backward compatibility: a legacy
// raw-only snapshot (no sidecar) remains listable and roll-back-able, and
// getMeta returns (nil, nil) rather than an error.
func TestHistoryRawOnlySnapshotHasNoMeta(t *testing.T) {
	h := newHistory(t.TempDir(), 50)
	id, err := h.snapshot([]byte("listen = \":9090\"\n"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	list, err := h.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 || list[0].ID != id {
		t.Fatalf("list = %+v, want single entry %q", list, id)
	}
	m, err := h.getMeta(id)
	if err != nil {
		t.Fatalf("getMeta on raw-only snapshot: %v", err)
	}
	if m != nil {
		t.Errorf("expected nil metadata for raw-only snapshot, got %+v", m)
	}
}

// TestHistoryPruneRemovesSidecar verifies AC-05: pruning past the retention
// bound removes both the raw snapshot and its metadata sidecar so the two never
// drift.
func TestHistoryPruneRemovesSidecar(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 1)
	first, _, err := h.snapshotWithMeta([]byte("listen = \":1\"\n"), &HistoryMetadata{Operation: ApplyOperationConfigApply})
	if err != nil {
		t.Fatalf("first snapshot: %v", err)
	}
	// The retention bound is 1, so the second snapshot prunes the first,
	// including its sidecar.
	if _, _, err := h.snapshotWithMeta([]byte("listen = \":2\"\n"), &HistoryMetadata{Operation: ApplyOperationConfigApply}); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, first+historyExt)); !os.IsNotExist(err) {
		t.Errorf("pruned raw snapshot still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, first+historyMetaExt)); !os.IsNotExist(err) {
		t.Errorf("pruned metadata sidecar still present: %v", err)
	}
}

// TestHistorySnapshotWithNilMetaSkipsSidecar verifies that snapshotWithMeta with
// nil metadata behaves exactly like snapshot: it writes only the raw file.
func TestHistorySnapshotWithNilMetaSkipsSidecar(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 50)
	id, metaErr, err := h.snapshotWithMeta([]byte("listen = \":3\"\n"), nil)
	if err != nil || metaErr != nil {
		t.Fatalf("snapshotWithMeta err=%v metaErr=%v", err, metaErr)
	}
	if _, err := os.Stat(filepath.Join(dir, id+historyMetaExt)); !os.IsNotExist(err) {
		t.Errorf("nil-meta snapshot wrote a sidecar: %v", err)
	}
}
