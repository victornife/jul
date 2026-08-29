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

	memStore := NewManagedBaselineStore("")
	if err := memStore.CommitMark([]byte("x"), "v"); err != nil {
		t.Errorf("empty-path store CommitMark should be a no-op: %v", err)
	}
	if _, err := memStore.Snapshot(); err == nil {
		t.Error("empty-path store Snapshot should report not-exist")
	}
}
