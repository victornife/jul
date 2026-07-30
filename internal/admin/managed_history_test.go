// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"os"
	"path/filepath"
	"testing"

	"jul/internal/server"
)

// newHistoryServer builds a minimal *Server with a file-backed history rooted
// at dir, for exercising RecordManagedHistory without a full admin listener.
func newHistoryServer(dir string) *Server {
	return &Server{hist: newHistory(dir, 50)}
}

const managedHistoryPrevRaw = "listen = \":8080\"\n"

// TestRecordManagedHistoryLiveWritesPreApply verifies AC-05: a committed live
// apply records a pre_apply snapshot of the prior configuration with redacted
// provenance, and returns its id with no degradation error.
func TestRecordManagedHistoryLiveWritesPreApply(t *testing.T) {
	dir := t.TempDir()
	s := newHistoryServer(dir)
	res := ConfigApplyResult{
		ApplyID:          "rl_1",
		OK:               true,
		Mode:             "hot",
		PersistedVersion: "v-next",
		Reload:           &server.ReloadResult{Outcome: server.ReloadAppliedLive},
	}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply, Actor: "alice"}, res, []byte(managedHistoryPrevRaw))
	if err != nil {
		t.Fatalf("RecordManagedHistory err: %v", err)
	}
	if id == "" {
		t.Fatal("expected a snapshot id for a committed live apply")
	}
	m, err := s.hist.getMeta(id)
	if err != nil || m == nil {
		t.Fatalf("getMeta: %v (meta=%v)", err, m)
	}
	if m.Reason != historyReasonPreApply {
		t.Errorf("reason = %q, want %q", m.Reason, historyReasonPreApply)
	}
	if m.Operation != ApplyOperationConfigApply || m.Actor != "alice" || m.Outcome != string(server.ReloadAppliedLive) {
		t.Errorf("metadata mismatch: %+v", m)
	}
}

// TestRecordManagedHistoryDegradedWritesPreApply verifies a committed degraded
// apply (ok=true) still snapshots pre_apply.
func TestRecordManagedHistoryDegradedWritesPreApply(t *testing.T) {
	s := newHistoryServer(t.TempDir())
	res := ConfigApplyResult{
		ApplyID: "rl_2",
		OK:      true,
		Mode:    "hot",
		Reload:  &server.ReloadResult{Outcome: server.ReloadAppliedDegraded},
	}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, res, []byte(managedHistoryPrevRaw))
	if err != nil {
		t.Fatalf("RecordManagedHistory err: %v", err)
	}
	if id == "" {
		t.Fatal("expected a snapshot for a committed degraded apply")
	}
}

// TestRecordManagedHistoryRestoredWritesNothing verifies a failed apply that was
// cleanly restored records no snapshot.
func TestRecordManagedHistoryRestoredWritesNothing(t *testing.T) {
	dir := t.TempDir()
	s := newHistoryServer(dir)
	res := ConfigApplyResult{
		ApplyID:  "rl_3",
		OK:       false,
		Mode:     "hot",
		Restored: true,
		Reload:   &server.ReloadResult{Outcome: server.ReloadNotApplied},
	}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, res, []byte(managedHistoryPrevRaw))
	if err != nil {
		t.Fatalf("RecordManagedHistory err: %v", err)
	}
	if id != "" {
		t.Fatalf("expected no snapshot for a cleanly restored failure, got id %q", id)
	}
	entries, _ := s.hist.list()
	if len(entries) != 0 {
		t.Fatalf("expected empty history, got %d entries", len(entries))
	}
}

// TestRecordManagedHistoryRestorationFailedWritesRecovery verifies a failed
// apply whose restoration also failed records a recovery snapshot so the prior
// config stays recoverable even though the candidate lingers on disk.
func TestRecordManagedHistoryRestorationFailedWritesRecovery(t *testing.T) {
	s := newHistoryServer(t.TempDir())
	res := ConfigApplyResult{
		ApplyID:      "rl_4",
		OK:           false,
		Mode:         "hot",
		Restored:     false,
		RestoreError: "disk write failed",
		Reload:       &server.ReloadResult{Outcome: server.ReloadNotApplied},
	}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, res, []byte(managedHistoryPrevRaw))
	if err != nil {
		t.Fatalf("RecordManagedHistory err: %v", err)
	}
	if id == "" {
		t.Fatal("expected a recovery snapshot when restoration failed")
	}
	m, err := s.hist.getMeta(id)
	if err != nil || m == nil {
		t.Fatalf("getMeta: %v (meta=%v)", err, m)
	}
	if m.Reason != historyReasonRecovery {
		t.Errorf("reason = %q, want %q", m.Reason, historyReasonRecovery)
	}
}

// TestRecordManagedHistoryStageWritesPreApply verifies a committed stage_restart
// (no reload submitted) snapshots pre_apply with an empty outcome.
func TestRecordManagedHistoryStageWritesPreApply(t *testing.T) {
	s := newHistoryServer(t.TempDir())
	res := ConfigApplyResult{
		ApplyID:          "rl_5",
		OK:               true,
		Mode:             "stage_restart",
		PersistedVersion: "v-staged",
	}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, res, []byte(managedHistoryPrevRaw))
	if err != nil {
		t.Fatalf("RecordManagedHistory err: %v", err)
	}
	if id == "" {
		t.Fatal("expected a snapshot for a committed stage")
	}
	m, err := s.hist.getMeta(id)
	if err != nil || m == nil {
		t.Fatalf("getMeta: %v (meta=%v)", err, m)
	}
	if m.Outcome != "" {
		t.Errorf("stage outcome = %q, want empty", m.Outcome)
	}
}

// TestRecordManagedHistoryEmptyPrevWritesNothing verifies that an empty previous
// configuration (e.g. first-ever write) records nothing.
func TestRecordManagedHistoryEmptyPrevWritesNothing(t *testing.T) {
	s := newHistoryServer(t.TempDir())
	res := ConfigApplyResult{ApplyID: "rl_6", OK: true, Mode: "hot", Reload: &server.ReloadResult{Outcome: server.ReloadAppliedLive}}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, res, nil)
	if err != nil {
		t.Fatalf("RecordManagedHistory err: %v", err)
	}
	if id != "" {
		t.Fatalf("expected no snapshot for empty previousRaw, got %q", id)
	}
}

// TestRecordManagedHistoryRawSnapshotAlwaysRetrievable verifies AC-05/AC-14: the
// raw TOML snapshot is always written and retrievable for a committed apply,
// independent of any metadata-sidecar degradation, so the prior configuration
// stays roll-back-able.
func TestRecordManagedHistoryRawSnapshotAlwaysRetrievable(t *testing.T) {
	dir := t.TempDir()
	s := newHistoryServer(dir)
	res := ConfigApplyResult{ApplyID: "rl_7", OK: true, Mode: "hot", Reload: &server.ReloadResult{Outcome: server.ReloadAppliedLive}}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, res, []byte(managedHistoryPrevRaw))
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if id == "" {
		t.Fatal("expected a snapshot id")
	}
	if _, gerr := s.hist.get(id); gerr != nil {
		t.Fatalf("raw snapshot not retrievable: %v", gerr)
	}
	if _, serr := os.Stat(filepath.Join(dir, id+historyExt)); serr != nil {
		t.Fatalf("raw snapshot file missing: %v", serr)
	}
}
