// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func newBaselineStoreForTest(t *testing.T) (*ManagedBaselineStore, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "server.toml")
	return NewManagedBaselineStore(cfgPath), cfgPath
}

func digestHex(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func TestManagedBaselineCommitMarkEstablishesClean(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("global.config_authority = \"managed\"\n")

	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean {
		t.Fatalf("state = %v, want managed_clean", st.State)
	}
	if st.BaselineRawSHA256 != digestHex(raw) {
		t.Errorf("baseline digest mismatch")
	}

	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if string(snap) != string(raw) {
		t.Errorf("snapshot content mismatch: got %q want %q", snap, raw)
	}
}

// TestManagedBaselineWriteRegression is the ADR-mandated regression test:
// after a completed managed write, sha256(snapshot) == marker.current_digest
// == sha256(config file). An earlier draft protocol wrote the snapshot from
// the CURRENT (pre-write) bytes and never updated it, leaving the snapshot at
// revision N while the marker/file moved to N+1.
func TestManagedBaselineWriteRegression(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")

	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := store.BeginWrite(digestHex(prior), "v-prior", digestHex(intended), "v-intended"); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	// The caller would now write `intended` to the real config path; here we
	// simulate reaching terminalization with the committed bytes in hand.
	if err := store.CompleteWrite(intended, "v-intended"); err != nil {
		t.Fatalf("CompleteWrite: %v", err)
	}

	st := store.Status()
	if st.State != ConfigStateManagedClean {
		t.Fatalf("state = %v, want managed_clean", st.State)
	}
	wantDigest := digestHex(intended)
	if st.BaselineRawSHA256 != wantDigest {
		t.Fatalf("baseline digest = %s, want %s (N+1, not N)", st.BaselineRawSHA256, wantDigest)
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if digestHex(snap) != wantDigest {
		t.Fatalf("snapshot digest = %s, want %s", digestHex(snap), wantDigest)
	}
}

func TestManagedBaselineRewindWriteRestoresPrior(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")

	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := store.BeginWrite(digestHex(prior), "v-prior", digestHex(intended), "v-intended"); err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}
	// The reload failed and the coordinator restored the prior bytes instead
	// of committing the intended ones.
	if err := store.RewindWrite(); err != nil {
		t.Fatalf("RewindWrite: %v", err)
	}

	st := store.Status()
	if st.State != ConfigStateManagedClean {
		t.Fatalf("state = %v, want managed_clean", st.State)
	}
	if st.BaselineRawSHA256 != digestHex(prior) {
		t.Fatalf("baseline digest = %s, want prior %s", st.BaselineRawSHA256, digestHex(prior))
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if string(snap) != string(prior) {
		t.Fatalf("snapshot content = %q, want prior %q", snap, prior)
	}
}

func TestManagedBaselineReconcileAbsentIsUnadopted(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	disk := []byte("a = 1\n")
	if err := store.Reconcile(disk, nil, "v1", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := store.Status().State; got != ConfigStateManagedUnadopted {
		t.Errorf("state = %v, want managed_unadopted", got)
	}
}

func TestManagedBaselineReconcileClosedTombstoneIsUnadoptedNotAlertable(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := store.CloseEpoch(); err != nil {
		t.Fatalf("CloseEpoch: %v", err)
	}
	// The ordinary managed -> file_owned -> managed round trip: reconcile
	// against whatever the external owner now has on disk.
	newExternal := []byte("a = 99\n")
	if err := store.Reconcile(newExternal, nil, "v99", ""); err != nil {
		t.Fatalf("Reconcile after close: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedUnadopted {
		t.Fatalf("state = %v, want managed_unadopted (round trip must not be alertable)", st.State)
	}
}

// TestManagedBaselineReconcileClosedTombstoneWithSurvivingSnapshotIsInconsistent
// pins the anomaly case: a tombstone should never coexist with a snapshot
// (CloseEpoch always removes the snapshot first), so finding one anyway means
// something else wrote it back — not a clean handoff.
func TestManagedBaselineReconcileClosedTombstoneWithSurvivingSnapshotIsInconsistent(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := store.CloseEpoch(); err != nil {
		t.Fatalf("CloseEpoch: %v", err)
	}
	// Simulate a snapshot reappearing next to the tombstone.
	if err := os.WriteFile(cfgPath+".managed-baseline.snapshot", raw, 0o600); err != nil {
		t.Fatalf("write anomalous snapshot: %v", err)
	}

	err := store.Reconcile(raw, nil, "v1", "")
	if err == nil {
		t.Fatal("want error for cleanup_incomplete")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonCleanupIncomplete {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/cleanup_incomplete", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcileMarkerMissingWithSnapshotIsInconsistent(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	// Simulate marker loss while the snapshot survives.
	if err := os.Remove(cfgPath + ".managed-baseline.json"); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	err := store.Reconcile(raw, nil, "v1", "")
	if err == nil {
		t.Fatal("Reconcile: want error for marker_missing")
	}
	st := store.Status()
	if st.State != ConfigStateManagedInconsistent || st.Reason != ReasonMarkerMissing {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/marker_missing", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcileCleanRepairsLostSnapshot(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	// Disk still matches the marker's current digest, so the snapshot is
	// repairable rather than fatal.
	if err := store.Reconcile(raw, nil, "v1", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean {
		t.Fatalf("state = %v, want managed_clean (repaired)", st.State)
	}
	snap, err := store.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot after repair: %v", err)
	}
	if string(snap) != string(raw) {
		t.Fatalf("repaired snapshot content mismatch")
	}
}

func TestManagedBaselineReconcileDriftWithUsableBaseline(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	external := []byte("a = 2\n")
	if err := store.Reconcile(external, nil, "v-ext", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedDrift {
		t.Fatalf("state = %v, want managed_drift", st.State)
	}
	if !st.Drift || st.DriftDetectedAt.IsZero() {
		t.Errorf("drift flags not set: %+v", st)
	}
	if st.BaselineRawSHA256 != digestHex(raw) {
		t.Errorf("drift must retain a usable baseline")
	}
}

func TestManagedBaselineReconcileDriftMissingSnapshotIsInconsistent(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	external := []byte("a = 2\n")
	err := store.Reconcile(external, nil, "v-ext", "")
	if err == nil {
		t.Fatal("want error for snapshot_missing")
	}
	st := store.Status()
	if st.State != ConfigStateManagedInconsistent || st.Reason != ReasonSnapshotMissing {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/snapshot_missing", st.State, st.Reason)
	}
}

// ─── preparing-marker recovery matrix (ADR 0019 §11.2.1b) ───────────────────

func writePreparingMarker(t *testing.T, store *ManagedBaselineStore, prior, priorVersion, intended, intendedVersion string) {
	t.Helper()
	if err := store.writeMarkerLocked(ManagedBaselineMarker{
		State:                   baselineStatePreparing,
		PriorRawSHA256:          prior,
		PriorCanonicalVersion:   priorVersion,
		CurrentRawSHA256:        intended,
		CurrentCanonicalVersion: intendedVersion,
	}); err != nil {
		t.Fatalf("writePreparingMarker: %v", err)
	}
}

func TestManagedBaselineReconcilePreparingPreCommitAbort(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	// Establish P as the current baseline (snapshot holds P), then simulate a
	// crash immediately after BeginWrite: disk still equals P.
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")

	if err := store.Reconcile(prior, nil, "v-prior", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean || st.BaselineRawSHA256 != digestHex(prior) {
		t.Fatalf("state=%v baseline=%s, want managed_clean at prior", st.State, st.BaselineRawSHA256)
	}
}

func TestManagedBaselineReconcilePreparingRollForward(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")

	// The rename landed (disk == intended) and the snapshot already advanced
	// (simulated below); only the final marker promotion was lost.
	store.mu.Lock()
	if err := store.writeSnapshotLocked(intended); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	store.mu.Unlock()

	if err := store.Reconcile(intended, nil, "v-intended", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean || st.BaselineRawSHA256 != digestHex(intended) {
		t.Fatalf("state=%v baseline=%s, want managed_clean at intended", st.State, st.BaselineRawSHA256)
	}
}

func TestManagedBaselineReconcilePreparingRollForwardRepairsSnapshot(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	// Snapshot still holds P (never advanced); disk already holds intended.
	if err := store.Reconcile(intended, nil, "v-intended", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean || st.BaselineRawSHA256 != digestHex(intended) {
		t.Fatalf("state=%v baseline=%s, want managed_clean at intended", st.State, st.BaselineRawSHA256)
	}
	snap, err := store.Snapshot()
	if err != nil || string(snap) != string(intended) {
		t.Fatalf("snapshot not repaired to intended bytes: err=%v snap=%q", err, snap)
	}
}

func TestManagedBaselineReconcilePreparingNeitherWithSnapshotAtIntended(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	external := []byte("a = 3\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	store.mu.Lock()
	if err := store.writeSnapshotLocked(intended); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	store.mu.Unlock()

	// Disk matches neither P nor I: an external writer raced in after Jul's
	// commit. Jul's bytes survived in the snapshot.
	if err := store.Reconcile(external, nil, "v-ext", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedDrift || st.BaselineRawSHA256 != digestHex(intended) {
		t.Fatalf("state=%v baseline=%s, want managed_drift with usable baseline at intended", st.State, st.BaselineRawSHA256)
	}
}

func TestManagedBaselineReconcilePreparingNeitherWithSnapshotAtPriorIsInconsistent(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	external := []byte("a = 3\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")

	err := store.Reconcile(external, nil, "v-ext", "")
	if err == nil {
		t.Fatal("want error for marker_contradicts_disk")
	}
	st := store.Status()
	if st.State != ConfigStateManagedInconsistent || st.Reason != ReasonMarkerContradictsDisk {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/marker_contradicts_disk", st.State, st.Reason)
	}
}

func TestManagedBaselineAssessDriftDetectsAndClears(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if store.Status().Drift {
		t.Fatal("drift must be false immediately after CommitMark")
	}

	external := []byte("a = 2\n")
	store.AssessDrift(external, nil, "v-ext", "")
	st := store.Status()
	if !st.Drift || st.State != ConfigStateManagedDrift || st.DriftDetectedAt.IsZero() {
		t.Fatalf("drift not detected: %+v", st)
	}
	firstDetected := st.DriftDetectedAt

	// Re-assessing the same drifted content must not reset DetectedAt.
	store.AssessDrift(external, nil, "v-ext", "")
	if store.Status().DriftDetectedAt != firstDetected {
		t.Error("DriftDetectedAt must not change while drift persists")
	}

	// Restoring the original bytes clears drift.
	store.AssessDrift(raw, nil, "v1", "")
	st = store.Status()
	if st.Drift || st.State != ConfigStateManagedClean {
		t.Fatalf("drift not cleared after restoring baseline bytes: %+v", st)
	}
}

// TestManagedBaselineAssessDriftDiskReadErrorReportsDrift pins that a disk
// read error (the file became unreadable) is treated as drift too, with an
// empty DiskRawSHA256 rather than a stale one.
func TestManagedBaselineAssessDriftDiskReadErrorReportsDrift(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	store.AssessDrift(nil, os.ErrPermission, "", "")
	st := store.Status()
	if !st.Drift || st.State != ConfigStateManagedDrift {
		t.Fatalf("a disk read error must report drift: %+v", st)
	}
	if st.DiskRawSHA256 != "" {
		t.Errorf("DiskRawSHA256 = %q, want empty on a disk read error", st.DiskRawSHA256)
	}
}

func TestManagedBaselineCloseEpochRemovesSnapshotAndWritesTombstone(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := store.CloseEpoch(); err != nil {
		t.Fatalf("CloseEpoch: %v", err)
	}
	if _, err := os.Stat(cfgPath + ".managed-baseline.snapshot"); !os.IsNotExist(err) {
		t.Errorf("snapshot must be removed by CloseEpoch, stat err = %v", err)
	}
	if !store.HasArtifacts() {
		t.Error("tombstone marker itself is a surviving artifact")
	}
	if store.Status().State != ConfigStateManagedUnadopted {
		t.Errorf("state after CloseEpoch = %v, want managed_unadopted", store.Status().State)
	}
}

func TestManagedBaselineCloseEpochIsIdempotent(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := store.CloseEpoch(); err != nil {
		t.Fatalf("first CloseEpoch: %v", err)
	}
	if err := store.CloseEpoch(); err != nil {
		t.Fatalf("second CloseEpoch (idempotent) must not fail: %v", err)
	}
}

func TestManagedBaselineNilAndEmptyPathAreInert(t *testing.T) {
	var nilStore *ManagedBaselineStore
	if err := nilStore.CommitMark([]byte("x"), "v"); err != nil {
		t.Errorf("nil store CommitMark should be a no-op: %v", err)
	}
	if got := nilStore.Status().State; got != ConfigStateManagedUnadopted {
		t.Errorf("nil store status = %v, want managed_unadopted", got)
	}
	if err := nilStore.BeginWrite("p", "pv", "i", "iv"); err != nil {
		t.Errorf("nil store BeginWrite should be a no-op: %v", err)
	}
	if err := nilStore.CompleteWrite([]byte("x"), "v"); err != nil {
		t.Errorf("nil store CompleteWrite should be a no-op: %v", err)
	}
	if err := nilStore.RewindWrite(); err != nil {
		t.Errorf("nil store RewindWrite should be a no-op: %v", err)
	}
	nilStore.AssessDrift([]byte("x"), nil, "v", "")
	if err := nilStore.Reconcile([]byte("x"), nil, "v", ""); err != nil {
		t.Errorf("nil store Reconcile should be a no-op: %v", err)
	}
	if err := nilStore.CloseEpoch(); err != nil {
		t.Errorf("nil store CloseEpoch should be a no-op: %v", err)
	}
	if nilStore.HasArtifacts() {
		t.Error("nil store HasArtifacts should be false")
	}
	nilStore.MarkInconsistent(ReasonMarkerMissing)
	nilStore.MarkDesiredAhead()

	memStore := NewManagedBaselineStore("")
	if err := memStore.CommitMark([]byte("x"), "v"); err != nil {
		t.Errorf("empty-path store CommitMark should be a no-op: %v", err)
	}
	if _, err := memStore.Snapshot(); err == nil {
		t.Error("empty-path store Snapshot should report not-exist")
	}
	if err := memStore.BeginWrite("p", "pv", "i", "iv"); err != nil {
		t.Errorf("empty-path store BeginWrite should be a no-op: %v", err)
	}
	if err := memStore.CompleteWrite([]byte("x"), "v"); err != nil {
		t.Errorf("empty-path store CompleteWrite should be a no-op: %v", err)
	}
	if err := memStore.RewindWrite(); err != nil {
		t.Errorf("empty-path store RewindWrite should be a no-op: %v", err)
	}
	memStore.AssessDrift([]byte("x"), nil, "v", "")
	if err := memStore.Reconcile([]byte("x"), nil, "v", ""); err != nil {
		t.Errorf("empty-path store Reconcile should be a no-op: %v", err)
	}
	if err := memStore.CloseEpoch(); err != nil {
		t.Errorf("empty-path store CloseEpoch should be a no-op: %v", err)
	}
	if memStore.HasArtifacts() {
		t.Error("empty-path store HasArtifacts should be false")
	}
	memStore.MarkInconsistent(ReasonMarkerMissing)
	memStore.MarkDesiredAhead()
}

// ─── loadMarkerLocked error branches (surfaced through Reconcile/CloseEpoch) ─

func TestManagedBaselineReconcileMarkerUnreadableIsInconsistent(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.Remove(cfgPath + ".managed-baseline.json"); err != nil {
		t.Fatalf("remove marker: %v", err)
	}
	if err := os.Mkdir(cfgPath+".managed-baseline.json", 0o755); err != nil {
		t.Fatalf("occupy marker path with a directory: %v", err)
	}

	err := store.Reconcile(raw, nil, "v1", "")
	if err == nil {
		t.Fatal("want error when the marker cannot be read")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonMarkerUnreadable {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/marker_unreadable", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcileMarkerCorruptJSONIsInconsistent(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	if err := os.WriteFile(cfgPath+".managed-baseline.json", []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write corrupt marker: %v", err)
	}

	err := store.Reconcile([]byte("a = 1\n"), nil, "v1", "")
	if err == nil {
		t.Fatal("want error when the marker cannot be decoded")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonMarkerUnreadable {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/marker_unreadable", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcileUnknownMarkerStateIsInconsistent(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	store.mu.Lock()
	if err := store.writeMarkerLocked(ManagedBaselineMarker{State: "bogus"}); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	store.mu.Unlock()

	err := store.Reconcile([]byte("a = 1\n"), nil, "v1", "")
	if err == nil {
		t.Fatal("want error for an unrecognized marker state")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonMarkerUnreadable {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/marker_unreadable", st.State, st.Reason)
	}
}

// ─── "current" marker branches beyond the already-tested clean/drift paths ──

func TestManagedBaselineReconcileCurrentRepairSnapshotWriteFails(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(cfgPath+".managed-baseline.snapshot", 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}

	// Disk matches the marker's current digest, so a repair is attempted —
	// and fails because the snapshot path is occupied by a directory.
	err := store.Reconcile(raw, nil, "v1", "")
	if err == nil {
		t.Fatal("want error when the snapshot repair write fails")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcileCurrentSnapshotDigestMismatchIsInconsistent(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	// Overwrite the snapshot with bytes that match neither the marker's
	// current digest nor the (also different) disk content.
	store.mu.Lock()
	if err := store.writeSnapshotLocked([]byte("a = 999\n")); err != nil {
		t.Fatalf("seed mismatched snapshot: %v", err)
	}
	store.mu.Unlock()
	external := []byte("a = 2\n")

	err := store.Reconcile(external, nil, "v-ext", "")
	if err == nil {
		t.Fatal("want error for snapshot_digest_mismatch")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonSnapshotDigestMismatch {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/snapshot_digest_mismatch", st.State, st.Reason)
	}
}

// ─── "preparing" marker branches beyond the already-tested happy paths ──────

func TestManagedBaselineReconcilePreparingRollForwardSnapshotWriteFails(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(cfgPath+".managed-baseline.snapshot", 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}

	// Disk == intended, snapshot needs repair (missing), and the repair
	// write fails because the snapshot path is occupied.
	err := store.Reconcile(intended, nil, "v-intended", "")
	if err == nil {
		t.Fatal("want error when roll-forward's snapshot repair fails")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcilePreparingRollBackSnapshotWriteFails(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}
	if err := os.Mkdir(cfgPath+".managed-baseline.snapshot", 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}

	// Disk == prior (pre-commit abort), snapshot needs repair (missing), and
	// the repair write fails because the snapshot path is occupied.
	err := store.Reconcile(prior, nil, "v-prior", "")
	if err == nil {
		t.Fatal("want error when roll-back's snapshot repair fails")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestManagedBaselineReconcilePreparingRestorationWinsRollsBack(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	// The snapshot landed at intended, but the config was restored to prior
	// afterwards (the failed-apply path): restoration wins.
	store.mu.Lock()
	if err := store.writeSnapshotLocked(intended); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	store.mu.Unlock()

	if err := store.Reconcile(prior, nil, "v-prior", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean || st.BaselineRawSHA256 != digestHex(prior) {
		t.Fatalf("state=%v baseline=%s, want managed_clean at prior (restoration wins)", st.State, st.BaselineRawSHA256)
	}
	snap, err := store.Snapshot()
	if err != nil || string(snap) != string(prior) {
		t.Fatalf("snapshot not repaired to prior: err=%v snap=%q", err, snap)
	}
}

func TestManagedBaselineReconcilePreparingRollBackSnapshotMismatchedRepairs(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	// Snapshot matches neither prior nor intended; disk == prior. Roll back
	// and repair the snapshot from the verified disk buffer.
	store.mu.Lock()
	if err := store.writeSnapshotLocked([]byte("a = 999\n")); err != nil {
		t.Fatalf("seed mismatched snapshot: %v", err)
	}
	store.mu.Unlock()

	if err := store.Reconcile(prior, nil, "v-prior", ""); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	st := store.Status()
	if st.State != ConfigStateManagedClean || st.BaselineRawSHA256 != digestHex(prior) {
		t.Fatalf("state=%v baseline=%s, want managed_clean at prior", st.State, st.BaselineRawSHA256)
	}
	snap, err := store.Snapshot()
	if err != nil || string(snap) != string(prior) {
		t.Fatalf("snapshot not repaired to prior: err=%v snap=%q", err, snap)
	}
}

func TestManagedBaselineReconcilePreparingNeitherNoUsableSnapshotIsInconsistent(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	prior := []byte("a = 1\n")
	intended := []byte("a = 2\n")
	external := []byte("a = 3\n")
	if err := store.CommitMark(prior, "v-prior"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	writePreparingMarker(t, store, digestHex(prior), "v-prior", digestHex(intended), "v-intended")
	// No usable snapshot at all: disk matches neither digest, and the
	// snapshot is missing.
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove snapshot: %v", err)
	}

	err := store.Reconcile(external, nil, "v-ext", "")
	if err == nil {
		t.Fatal("want error for snapshot_missing")
	}
	if st := store.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonSnapshotMissing {
		t.Fatalf("state=%v reason=%v, want managed_inconsistent/snapshot_missing", st.State, st.Reason)
	}
}

// ─── CompleteWrite / RewindWrite failure branches ────────────────────────────

func TestManagedBaselineCompleteWriteSnapshotFailure(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	if err := os.Mkdir(cfgPath+".managed-baseline.snapshot", 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}

	if err := store.CompleteWrite([]byte("a = 1\n"), "v1"); err == nil {
		t.Fatal("want error when the snapshot write fails")
	}
}

func TestManagedBaselineCompleteWriteMarkerFailure(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	if err := os.Mkdir(cfgPath+".managed-baseline.json", 0o755); err != nil {
		t.Fatalf("occupy marker path: %v", err)
	}

	if err := store.CompleteWrite([]byte("a = 1\n"), "v1"); err == nil {
		t.Fatal("want error when the marker promotion fails")
	}
	if _, err := os.Stat(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Errorf("the snapshot write must still have succeeded before the marker failure: %v", err)
	}
}

func TestManagedBaselineRewindWriteNoopWithoutPreparingMarker(t *testing.T) {
	store, _ := newBaselineStoreForTest(t)
	// No marker at all.
	if err := store.RewindWrite(); err != nil {
		t.Fatalf("RewindWrite with no marker: %v", err)
	}

	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	// Marker exists but is "current", not "preparing".
	if err := store.RewindWrite(); err != nil {
		t.Fatalf("RewindWrite with a current marker: %v", err)
	}
	if st := store.Status(); st.State != ConfigStateManagedClean {
		t.Errorf("a no-op RewindWrite must not disturb the existing state, got %v", st.State)
	}
}

// ─── CloseEpoch / HasArtifacts remaining branches ────────────────────────────

func TestManagedBaselineCloseEpochMarkerUnreadableFails(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	if err := os.Mkdir(cfgPath+".managed-baseline.json", 0o755); err != nil {
		t.Fatalf("occupy marker path: %v", err)
	}

	if err := store.CloseEpoch(); err == nil {
		t.Fatal("want error when the marker cannot be read")
	}
}

func TestManagedBaselineCloseEpochSnapshotRemoveFailure(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	raw := []byte("a = 1\n")
	if err := store.CommitMark(raw, "v1"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	// Replace the real snapshot with a non-empty directory: os.Remove fails
	// with "directory not empty" rather than succeeding or ErrNotExist.
	if err := os.Remove(cfgPath + ".managed-baseline.snapshot"); err != nil {
		t.Fatalf("remove real snapshot: %v", err)
	}
	if err := os.Mkdir(cfgPath+".managed-baseline.snapshot", 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgPath+".managed-baseline.snapshot", "occupant"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write occupant file: %v", err)
	}

	if err := store.CloseEpoch(); err == nil {
		t.Fatal("want error when the snapshot cannot be removed")
	}
}

func TestManagedBaselineHasArtifactsVariants(t *testing.T) {
	store, cfgPath := newBaselineStoreForTest(t)
	if store.HasArtifacts() {
		t.Error("a fresh store should report no artifacts")
	}

	if err := os.WriteFile(cfgPath+".managed-baseline.json", []byte("{}"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if !store.HasArtifacts() {
		t.Error("a surviving marker alone must count as an artifact")
	}
	if err := os.Remove(cfgPath + ".managed-baseline.json"); err != nil {
		t.Fatalf("remove marker: %v", err)
	}

	if err := os.WriteFile(cfgPath+".managed-baseline.snapshot", []byte("x"), 0o600); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	if !store.HasArtifacts() {
		t.Error("a surviving snapshot alone must count as an artifact")
	}
}
