// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/admin"
	"jul/internal/server"
)

func newAuthorityTestCoordinator(t *testing.T, authority ConfigAuthority, baseline *ManagedBaselineStore, submit func(server.ReloadRequest) error) (*ConfigApplyCoordinator, string) {
	t.Helper()
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	var watchDigest atomicPointer32
	if submit == nil {
		submit = func(req server.ReloadRequest) error {
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
		}
	}
	return &ConfigApplyCoordinator{
		BaseCtx:         context.Background(),
		Path:            path,
		Preflight:       testPreflight(),
		SubmitReload:    submit,
		LiveSnapshot:    func() server.LiveSnapshot { return server.LiveSnapshot{} },
		WatchDigest:     &watchDigest,
		PlannedRestart:  &PlannedRestartStore{},
		Authority:       authority,
		ManagedBaseline: baseline,
	}, path
}

func TestCoordinatorFileOwnedDeniesApplyRawBeforeAnyWrite(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":9999"), ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw error: %v", err)
	}
	if res.OK || !res.AuthorityDenied {
		t.Fatalf("result = %+v, want OK=false AuthorityDenied=true", res)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(onDisk) != string(seed) {
		t.Error("file-owned denial must not touch the file")
	}
}

func TestCoordinatorFileOwnedDeniesDiscard(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	res, err := c.DiscardPlannedRestart()
	if err != nil {
		t.Fatalf("DiscardPlannedRestart error: %v", err)
	}
	if res.OK || !res.AuthorityDenied {
		t.Fatalf("result = %+v, want OK=false AuthorityDenied=true", res)
	}
}

func TestCoordinatorManagedUnadoptedRefusesHotApply(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":9999"), ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw error: %v", err)
	}
	if res.OK {
		t.Fatalf("unadopted managed baseline must refuse hot apply; got %+v", res)
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Error("refused apply must not touch the file")
	}
}

func TestCoordinatorManagedDriftRefusesHotApply(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	// An external writer edits the file without going through the coordinator.
	drifted := validConfigRaw(t, ":7000")
	if err := os.WriteFile(path, drifted, 0o600); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}

	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":9999"), ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw error: %v", err)
	}
	if res.OK {
		t.Fatalf("drift must refuse the managed write; got %+v", res)
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(drifted) {
		t.Error("a refused apply must not overwrite the drifted file")
	}
	if st := c.ManagedBaseline.Status(); !st.Drift {
		t.Error("refreshStateLocked must have assessed and recorded drift")
	}
}

// TestAssessDriftNowIsTheExplicitRefreshTrigger pins ADR 0019 §12's fourth
// event-driven drift trigger: an external edit is invisible to Status() until
// one of the four points assesses it, and AssessDriftNow is the one an
// operator- or Console-initiated refresh calls on demand (the other three are
// the watcher, SIGHUP, and the pre-write CAS).
func TestAssessDriftNowIsTheExplicitRefreshTrigger(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.WriteFile(path, validConfigRaw(t, ":7000"), 0o600); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}

	if st := c.ManagedBaseline.Status(); st.Drift {
		t.Fatal("drift must not be reported before any of the four assessment points runs")
	}
	c.AssessDriftNow()
	if st := c.ManagedBaseline.Status(); !st.Drift {
		t.Error("AssessDriftNow must have assessed and recorded the external edit as drift")
	}
}

func TestCoordinatorManagedCleanApplyAdvancesBaseline(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw error: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected success, got %+v", res)
	}

	st := c.ManagedBaseline.Status()
	if st.State != ConfigStateManagedClean {
		t.Fatalf("state = %v, want managed_clean", st.State)
	}
	if st.BaselineRawSHA256 != digestHex(newRaw) {
		t.Fatalf("baseline did not advance to the new content (N+1)")
	}
	snap, err := c.ManagedBaseline.Snapshot()
	if err != nil || string(snap) != string(newRaw) {
		t.Fatalf("snapshot mismatch: err=%v snap=%q want=%q", err, snap, newRaw)
	}
}

func TestCoordinatorManagedStageRestartAdvancesBaselineNotDrift(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	staged := restartRequiredConfigRaw(t, ":8080")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, staged, ApplyStageRestart)
	if err != nil {
		t.Fatalf("ApplyRaw error: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected stage to succeed, got %+v", res)
	}

	st := c.ManagedBaseline.Status()
	if st.State != ConfigStateManagedClean || st.Drift {
		t.Fatalf("a managed stage_restart must not be reported as drift, got %+v", st)
	}
	if st.BaselineRawSHA256 != digestHex(staged) {
		t.Fatalf("baseline must advance to the staged candidate")
	}
}

func TestCoordinatorManagedFailedApplyRewindsBaseline(t *testing.T) {
	failingSubmit := func(server.ReloadRequest) error {
		return errors.New("enqueue failed for test")
	}
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, failingSubmit)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err == nil {
		t.Fatal("expected enqueue failure to surface an error")
	}
	if res.OK {
		t.Fatalf("expected failure result, got %+v", res)
	}

	st := c.ManagedBaseline.Status()
	if st.State != ConfigStateManagedClean {
		t.Fatalf("state = %v, want managed_clean after rewind", st.State)
	}
	if st.BaselineRawSHA256 != digestHex(seed) {
		t.Fatalf("baseline must rewind to the prior (seed) content after a failed apply")
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(seed) {
		t.Fatalf("disk should have been restored to seed content")
	}
}

// ─── currentConfigState / fileOwnedConfigState (ADR 0019 §16) ───────────────

// TestCurrentConfigStatePendingRestartTakesPriority pins that a durable
// staged restart is reported as managed_pending_restart even though the
// baseline itself already advanced to the staged candidate and independently
// reports managed_clean (ADR 0019 §11.2.3: a stage is not drift).
func TestCurrentConfigStatePendingRestartTakesPriority(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	c.PlannedRestart = NewFilePlannedRestartStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	c.PlannedRestart.Stage([]byte("staged-candidate"))

	if bst := c.ManagedBaseline.Status(); bst.State != ConfigStateManagedClean {
		t.Fatalf("precondition: baseline state = %v, want managed_clean", bst.State)
	}
	state, reason := c.currentConfigState()
	if state != ConfigStateManagedPendingRestart || reason != "" {
		t.Errorf("currentConfigState() = (%v, %v), want (managed_pending_restart, \"\")", state, reason)
	}
}

// TestCurrentConfigStateDelegatesToBaselineWhenNotPending pins the ordinary
// case: with no staged restart, the managed state is exactly what the
// baseline reports, reason included.
func TestCurrentConfigStateDelegatesToBaselineWhenNotPending(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	drifted := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, drifted, 0o600); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}
	c.ManagedBaseline.AssessDrift(drifted, nil, "v-ext", "")

	state, reason := c.currentConfigState()
	if state != ConfigStateManagedDrift || reason != "" {
		t.Errorf("currentConfigState() = (%v, %v), want (managed_drift, \"\")", state, reason)
	}
}

// TestCurrentConfigStateFileOwnedNeverLeaksManagedBaseline pins the exact
// bug ADR 0019 §16 warns against: a file_owned process must never surface a
// managed_* config_state merely because a ManagedBaselineStore happens to
// exist (it is constructed regardless of authority so a file_owned startup
// can clean up artifacts from a prior managed epoch).
func TestCurrentConfigStateFileOwnedNeverLeaksManagedBaseline(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	// A stale managed baseline as if inherited from a prior epoch, not yet
	// (or never) cleaned up.
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if bst := c.ManagedBaseline.Status(); bst.State != ConfigStateManagedClean {
		t.Fatalf("precondition: baseline state = %v, want managed_clean", bst.State)
	}

	state, _ := c.currentConfigState()
	if state != ConfigStateFileOwnedClean {
		t.Errorf("currentConfigState() = %v, want file_owned_clean (never a managed_* leak)", state)
	}
}

// TestFileOwnedConfigStateDesiredAheadFromExternalDivergence pins that a
// restart-required external edit not yet live is file_owned_desired_ahead,
// reusing the same external-divergence signal PendingRestartCheck already
// maintains.
func TestFileOwnedConfigStateDesiredAheadFromExternalDivergence(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	c.PlannedRestart = NewFilePlannedRestartStore(path)
	if err := os.WriteFile(path, validConfigRaw(t, ":8080"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	c.PlannedRestart.SetExternalDivergence(true)

	if state := c.fileOwnedConfigState(); state != ConfigStateFileOwnedDesiredAhead {
		t.Errorf("fileOwnedConfigState() = %v, want file_owned_desired_ahead", state)
	}
}

// TestFileOwnedConfigStateInvalidWhenFileFailsToParse pins the third
// file_owned state: a current file that fails to parse.
func TestFileOwnedConfigStateInvalidWhenFileFailsToParse(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	if err := os.WriteFile(path, []byte("{not valid toml"), 0o600); err != nil {
		t.Fatalf("write invalid config: %v", err)
	}

	if state := c.fileOwnedConfigState(); state != ConfigStateFileOwnedInvalid {
		t.Errorf("fileOwnedConfigState() = %v, want file_owned_invalid", state)
	}
}

// TestFileOwnedConfigStateCleanForValidFileOrNoConfigPath covers the two
// remaining file_owned_clean paths: an ordinary valid file, and a process
// with no configuration file at all (ADR 0019 §9.1.1).
func TestFileOwnedConfigStateCleanForValidFileOrNoConfigPath(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	if err := os.WriteFile(path, validConfigRaw(t, ":8080"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if state := c.fileOwnedConfigState(); state != ConfigStateFileOwnedClean {
		t.Errorf("fileOwnedConfigState() = %v, want file_owned_clean for a valid file", state)
	}

	c.Path = ""
	if state := c.fileOwnedConfigState(); state != ConfigStateFileOwnedClean {
		t.Errorf("fileOwnedConfigState() = %v, want file_owned_clean with no config path", state)
	}
}

// ─── scheduleBaselineWriteRetry (ADR 0019 §11.2.1a's "one retry") ───────────
//
// After a post-commit baseline write failure, the ADR requires exactly one
// retry: under applyMu, re-verify the configuration still matches the digest
// the failed write intended to record, abandon if it does not (a later write
// superseded it), and otherwise retry the write once. A failed or abandoned
// retry must resolve managed_inconsistent/baseline_unwritable — never leave
// the stale pre-failure state in place forever, and never resolve
// managed_clean except through the retry's own success.

func awaitBaselineWriteRetry(c *ConfigApplyCoordinator) <-chan struct{} {
	done := make(chan struct{})
	c.afterBaselineWriteRetry = func() { close(done) }
	return done
}

func TestScheduleBaselineWriteRetrySucceedsWhenDigestUnchanged(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	done := awaitBaselineWriteRetry(c)
	commitCalled := false
	c.scheduleBaselineWriteRetry(committed, func(b []byte) error {
		commitCalled = true
		return c.ManagedBaseline.CompleteWrite(b, "v1")
	})
	<-done

	if !commitCalled {
		t.Fatal("an unchanged digest must retry the commit, not abandon it")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedClean {
		t.Errorf("state = %v, want managed_clean after a successful retry", st.State)
	}
}

func TestScheduleBaselineWriteRetryMarksInconsistentWhenRetryFails(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	done := awaitBaselineWriteRetry(c)
	c.scheduleBaselineWriteRetry(committed, func(b []byte) error {
		return errors.New("disk still unwritable")
	})
	<-done

	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestScheduleBaselineWriteRetryAbandonsOnSupersededDigest(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	// The file on disk no longer holds what the failed write intended to
	// record — a concurrent apply or restoration already moved it on. A
	// retry must not record committed's digest over that.
	superseded := validConfigRaw(t, ":9090")
	if err := os.WriteFile(path, superseded, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	done := awaitBaselineWriteRetry(c)
	commitCalled := false
	c.scheduleBaselineWriteRetry(committed, func(b []byte) error {
		commitCalled = true
		return nil
	})
	<-done

	if commitCalled {
		t.Fatal("a superseded digest must abandon the retry without calling commit")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestScheduleBaselineWriteRetryAbandonsOnReadError(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	// path is never written, so readConfigRaw fails with os.ErrNotExist.

	done := awaitBaselineWriteRetry(c)
	commitCalled := false
	c.scheduleBaselineWriteRetry(committed, func(b []byte) error {
		commitCalled = true
		return nil
	})
	<-done

	if commitCalled {
		t.Fatal("a read error must abandon the retry without calling commit")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestScheduleBaselineWriteRetryNilBaselineIsNoop(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	commitCalled := false
	// Must return synchronously without spawning anything to await.
	c.scheduleBaselineWriteRetry([]byte("a = 1\n"), func(b []byte) error {
		commitCalled = true
		return nil
	})
	if commitCalled {
		t.Error("a nil ManagedBaseline must be a no-op")
	}
}

// TestCoordinatorManagedApplyRetriesBaselineWriteAfterSnapshotFailure pins the
// end-to-end wiring: a hot apply whose CompleteWrite fails still reports its
// own success unchanged (ADR 0019 §11.2.1a — a post-commit baseline failure
// never changes the operation's outcome) plus a baseline_error degradation,
// and the scheduled retry then resolves the baseline once the obstruction is
// gone.
func TestCoordinatorManagedApplyRetriesBaselineWriteAfterSnapshotFailure(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	snapshotPath := path + ".managed-baseline.snapshot"
	if err := os.Remove(snapshotPath); err != nil {
		t.Fatalf("remove snapshot written by CommitMark: %v", err)
	}
	if err := os.Mkdir(snapshotPath, 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}

	done := awaitBaselineWriteRetry(c)
	newRaw := validConfigRaw(t, ":8081")
	res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw error: %v", err)
	}
	if !res.OK {
		t.Fatalf("a post-commit baseline failure must not change the apply's own outcome, got %+v", res)
	}
	found := false
	for _, d := range res.Degraded {
		if d.Kind == DegradedBaselineError {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a baseline_error degradation, got %+v", res.Degraded)
	}

	// The retry (blocked on applyMu until ApplyRaw released it) still sees
	// the occupied snapshot path and must mark the baseline inconsistent
	// rather than leave it silently stale.
	<-done
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}
