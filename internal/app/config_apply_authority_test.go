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
	"jul/internal/config"
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

// TestManagedFailedApplyObservableDuringRestorationWindow pins ADR 0019
// §10/§16's managed_failed_apply state: it must actually be produced when a
// commit's reload does not apply and restoration follows, not merely
// declared and never emitted. It is transient — the same critical section
// resolves it to managed_clean (restored) or managed_inconsistent
// (restoration failed) moments later — so this uses the beforeRestore test
// seam to observe it while the finalizer is wedged mid-restoration.
func TestManagedFailedApplyObservableDuringRestorationWindow(t *testing.T) {
	restoreStarted := make(chan struct{})
	restoreContinue := make(chan struct{})
	submit := func(req server.ReloadRequest) error {
		go func() {
			req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadNotApplied, FailedPhase: "prepare", Error: "build failed"}
		}()
		return nil
	}
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, submit)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	c.beforeRestore = func() {
		close(restoreStarted)
		<-restoreContinue
	}

	resultCh := make(chan ApplyResult, 1)
	go func() {
		result, _ := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8081"), ApplyHot)
		resultCh <- result
	}()
	<-restoreStarted
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedFailedApply {
		t.Errorf("state during restoration = %v, want managed_failed_apply", st.State)
	}
	close(restoreContinue)
	<-resultCh

	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedClean {
		t.Errorf("state after a successful restoration = %v, want managed_clean", st.State)
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

// TestCurrentConfigStateDriftOverridesPendingRestart pins that an external
// write after a restart is staged is never masked behind
// managed_pending_restart: a planned restart's durability says nothing about
// whether the file has since drifted out from under it, and drift is
// alertable (ADR 0019 §12) in a way a staged restart alone is not.
func TestCurrentConfigStateDriftOverridesPendingRestart(t *testing.T) {
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

	// An external writer edits the file again, on top of the staged
	// candidate, without going through the coordinator.
	drifted := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, drifted, 0o600); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}
	c.ManagedBaseline.AssessDrift(drifted, nil, "v-ext", "")

	state, reason := c.currentConfigState()
	if state != ConfigStateManagedDrift || reason != "" {
		t.Errorf("currentConfigState() = (%v, %v), want (managed_drift, \"\") even with a restart staged", state, reason)
	}
}

// TestCurrentConfigStateInconsistentOverridesPendingRestart mirrors the
// drift case for damage to Jul's own baseline tracking: a staged restart
// must never hide managed_inconsistent, since the reason is required
// operator-actionable information (ADR 0019 §16).
func TestCurrentConfigStateInconsistentOverridesPendingRestart(t *testing.T) {
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
	c.ManagedBaseline.MarkInconsistent(ReasonRestorationFailed)

	state, reason := c.currentConfigState()
	if state != ConfigStateManagedInconsistent || reason != ReasonRestorationFailed {
		t.Errorf("currentConfigState() = (%v, %v), want (managed_inconsistent, restoration_failed) even with a restart staged", state, reason)
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

// TestFileOwnedConfigStateInvalidWhenFileFailsValidation pins ADR 0019 §16's
// exact definition of file_owned_invalid: "the current file fails
// validation", not merely "fails to parse". config.Parse performs no
// semantic validation of its own (callers must run Validate separately), so
// a syntactically well-formed file with a semantic error — here, a
// proxy_pass referencing an upstream that does not exist — must still be
// reported as file_owned_invalid, not file_owned_clean.
func TestFileOwnedConfigStateInvalidWhenFileFailsValidation(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
	cfg.Servers[0].Locations[0].ProxyPass = "http://ghost-upstream"
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if _, perr := config.Parse(raw); perr != nil {
		t.Fatalf("precondition: the file must parse syntactically, got %v", perr)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if state := c.fileOwnedConfigState(); state != ConfigStateFileOwnedInvalid {
		t.Errorf("fileOwnedConfigState() = %v, want file_owned_invalid for a file that parses but fails validation", state)
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

// ─── resolveBaselineWriteRetry (ADR 0019 §11.2.1a's "one retry") ────────────
//
// After a post-commit baseline write failure, the ADR requires exactly one
// retry: under applyMu, re-verify the configuration still matches the digest
// the failed write intended to record, abandon if it does not (a later write
// superseded it), and otherwise retry the write once. A failed or abandoned
// retry must resolve managed_inconsistent/baseline_unwritable — never leave
// the stale pre-failure state in place forever, and never resolve
// managed_clean except through the retry's own success. The function itself
// is synchronous; only the hot-apply finalizer's own goroutine calls it
// asynchronously relative to ApplyRaw's original caller, which is why the
// end-to-end test below still needs awaitBaselineWriteRetry to observe it.

func awaitBaselineWriteRetry(c *ConfigApplyCoordinator) <-chan struct{} {
	done := make(chan struct{})
	c.afterBaselineWriteRetry = func() { close(done) }
	return done
}

func TestResolveBaselineWriteRetrySucceedsWhenDigestUnchanged(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	commitCalled := false
	c.resolveBaselineWriteRetry(committed, func(b []byte) error {
		commitCalled = true
		return c.ManagedBaseline.CompleteWrite(b, "v1")
	})

	if !commitCalled {
		t.Fatal("an unchanged digest must retry the commit, not abandon it")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedClean {
		t.Errorf("state = %v, want managed_clean after a successful retry", st.State)
	}
}

func TestResolveBaselineWriteRetryMarksInconsistentWhenRetryFails(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	c.resolveBaselineWriteRetry(committed, func(b []byte) error {
		return errors.New("disk still unwritable")
	})

	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestResolveBaselineWriteRetryAbandonsOnSupersededDigest(t *testing.T) {
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

	commitCalled := false
	c.resolveBaselineWriteRetry(committed, func(b []byte) error {
		commitCalled = true
		return nil
	})

	if commitCalled {
		t.Fatal("a superseded digest must abandon the retry without calling commit")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

// TestResolveBaselineWriteRetryAbandonsSilentlyWhenSupersededByValidNewerBaseline
// pins the residual this retry still cannot fully close by admission alone:
// inFlightState blocks a later ordinary hot apply for the whole of this
// retry's window, but it does not gate adoption or stage_restart, either of
// which could in principle establish its own valid baseline for whatever the
// file now holds before this retry gets its turn at applyMu. Before this
// check existed, any digest mismatch — including one fully explained by that
// later transaction's own valid commit — was reported as
// managed_inconsistent/baseline_unwritable, which would wrongly regress a
// newer successful commit into a failure state an old, now-irrelevant retry
// happened to observe last.
func TestResolveBaselineWriteRetryAbandonsSilentlyWhenSupersededByValidNewerBaseline(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	stale := validConfigRaw(t, ":8080")
	// A later, independent transaction already established its own valid
	// baseline for different content — the system is already consistent.
	newer := validConfigRaw(t, ":9090")
	if err := os.WriteFile(path, newer, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(newer, "v-newer"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	commitCalled := false
	c.resolveBaselineWriteRetry(stale, func(b []byte) error {
		commitCalled = true
		return nil
	})

	if commitCalled {
		t.Fatal("a digest already explained by a valid newer baseline must not retry the commit")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedClean {
		t.Errorf("state = %v, want managed_clean left undisturbed — the newer transaction's own state must not be regressed", st.State)
	}
}

func TestResolveBaselineWriteRetryAbandonsOnReadError(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	// path is never written, so readConfigRaw fails with os.ErrNotExist.

	commitCalled := false
	c.resolveBaselineWriteRetry(committed, func(b []byte) error {
		commitCalled = true
		return nil
	})

	if commitCalled {
		t.Fatal("a read error must abandon the retry without calling commit")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestResolveBaselineWriteRetryNilBaselineIsNoop(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	commitCalled := false
	c.resolveBaselineWriteRetry([]byte("a = 1\n"), func(b []byte) error {
		commitCalled = true
		return nil
	})
	if commitCalled {
		t.Error("a nil ManagedBaseline must be a no-op")
	}
}

// ─── retryBaselineWriteLocked (inline retry for callers already holding
// applyMu: adoption's T-mark commit, stage_restart's T-write commit) ────────
//
// Unlike scheduleBaselineWriteRetry, these run synchronously — no goroutine,
// no test hook to await — so admission-gate serialization is exact rather
// than best-effort (ADR 0019 §11.2.0.1).

func TestRetryBaselineWriteLockedSucceedsWhenDigestUnchanged(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	commitCalled := false
	c.retryBaselineWriteLocked(committed, func(b []byte) error {
		commitCalled = true
		return c.ManagedBaseline.CompleteWrite(b, "v1")
	})

	if !commitCalled {
		t.Fatal("an unchanged digest must retry the commit, not abandon it")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedClean {
		t.Errorf("state = %v, want managed_clean after a successful retry", st.State)
	}
}

func TestRetryBaselineWriteLockedMarksInconsistentWhenRetryFails(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, committed, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	c.retryBaselineWriteLocked(committed, func(b []byte) error {
		return errors.New("disk still unwritable")
	})

	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestRetryBaselineWriteLockedMarksInconsistentOnMismatch(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	committed := validConfigRaw(t, ":8080")
	// An external write landed inside this same held-applyMu critical
	// section — the only way a mismatch can happen here at all, since no
	// other coordinator transaction can run concurrently while applyMu is
	// held for this one.
	mismatched := validConfigRaw(t, ":9090")
	if err := os.WriteFile(path, mismatched, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	commitCalled := false
	c.retryBaselineWriteLocked(committed, func(b []byte) error {
		commitCalled = true
		return nil
	})

	if commitCalled {
		t.Fatal("a digest mismatch must abandon the retry without calling commit")
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

func TestRetryBaselineWriteLockedNilBaselineIsNoop(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	commitCalled := false
	c.retryBaselineWriteLocked([]byte("a = 1\n"), func(b []byte) error {
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

	// The retry resolves before ApplyRaw's own caller ever sees a result
	// (ADR 0015 §4 / #226: the mutation gate and terminal publication both
	// wait for it), so by the time res is available here the retry has
	// already run and still sees the occupied snapshot path.
	<-done
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}
}

// TestCoordinatorHotApplyBlocksAdmissionUntilBaselineRetryResolves pins ADR
// 0019 §11.2.0.1's admission invariant for the one call site that cannot run
// its retry inline: a later apply must be refused as still-in-flight for the
// whole of the retry's window, not just for the initial write. Before this
// fix, inFlightState cleared as soon as the initial CompleteWrite failed and
// the retry was merely scheduled, so a second apply could be admitted and
// fully commit its own valid baseline while the first apply's retry was
// still pending — exactly the interleaving the ADR says must not be
// possible.
func TestCoordinatorHotApplyBlocksAdmissionUntilBaselineRetryResolves(t *testing.T) {
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

	retryStarted := make(chan struct{})
	retryContinue := make(chan struct{})
	c.beforeBaselineWriteRetry = func() {
		close(retryStarted)
		<-retryContinue
	}
	// ADR 0015 §4 / #226: the mutation gate must clear, and only then may the
	// terminal ledger publish — this must hold true even when what remains
	// unresolved is only the baseline retry. OnManagedApplyComplete fires
	// exactly at terminal publication (inside completeManagedApply), so
	// recording inFlightState there proves the ordering directly rather than
	// inferring it from timing.
	var inFlightAtCompletion ApplyInFlightState
	var configStateAtCompletion ConfigState
	completed := make(chan struct{})
	c.OnManagedApplyComplete = func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
		c.mu.Lock()
		inFlightAtCompletion = c.inFlightState
		c.mu.Unlock()
		configStateAtCompletion = ConfigState(comp.Result.ConfigState)
		close(completed)
		return admin.ManagedApplyFinalization{}
	}

	newRaw := validConfigRaw(t, ":8081")
	type applyOutcome struct {
		res ApplyResult
		err error
	}
	firstDone := make(chan applyOutcome, 1)
	go func() {
		res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
		firstDone <- applyOutcome{res, err}
	}()
	<-retryStarted

	// A second apply arriving while the first's baseline retry is still
	// pending must be refused as in-flight, never admitted.
	second, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8082"), ApplyHot)
	if err != nil {
		t.Fatalf("second ApplyRaw error: %v", err)
	}
	if second.OK {
		t.Fatalf("a second apply must be refused while the first's baseline retry is unresolved, got %+v", second)
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(newRaw) {
		t.Error("a refused second apply must not have touched the file")
	}

	// Neither the first apply's own caller-visible result nor its terminal
	// ledger publication may be observable yet: both are gated by the same
	// unresolved retry.
	select {
	case out := <-firstDone:
		t.Fatalf("the first apply must not complete while its own baseline retry is still blocked, got %+v (err=%v)", out.res, out.err)
	case <-completed:
		t.Fatal("terminal ledger publication must not occur while the baseline retry is still blocked")
	default:
	}

	close(retryContinue)
	<-completed
	first := <-firstDone
	if first.err != nil {
		t.Fatalf("ApplyRaw error: %v", first.err)
	}
	if !first.res.OK {
		t.Fatalf("a post-commit baseline failure must not change the apply's own outcome, got %+v", first.res)
	}
	found := false
	for _, d := range first.res.Degraded {
		if d.Kind == DegradedBaselineError {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a baseline_error degradation, got %+v", first.res.Degraded)
	}

	// The gate must have cleared before terminal publication observed it,
	// and the published config_state must already reflect the retry's own
	// outcome (managed_inconsistent) rather than a stale pre-retry value —
	// both are the same completeManagedApply call this hook fired from.
	if inFlightAtCompletion != ApplyInFlightNone {
		t.Errorf("inFlightState at terminal publication = %q, want cleared — the gate must clear before publication, not after", inFlightAtCompletion)
	}
	if configStateAtCompletion != ConfigStateManagedInconsistent {
		t.Errorf("config_state at terminal publication = %q, want managed_inconsistent (the retry's own outcome), not a stale pre-retry value", configStateAtCompletion)
	}

	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedInconsistent || st.Reason != ReasonBaselineUnwritable {
		t.Errorf("state=%v reason=%v, want managed_inconsistent/baseline_unwritable", st.State, st.Reason)
	}

	// Now that the retry has resolved, admission must be open again.
	third, err := c.ApplyRaw(admin.ApplyRequestContext{}, validConfigRaw(t, ":8083"), ApplyHot)
	if err != nil {
		t.Fatalf("third ApplyRaw error: %v", err)
	}
	if third.OK {
		t.Fatalf("a managed_inconsistent baseline must itself refuse further writes, got %+v", third)
	}
	if third.Message == "A previous apply is still in flight; wait for it to complete or check the runtime overview for status." {
		t.Error("admission must have reopened after the retry resolved; the refusal must come from the inconsistent baseline, not the stale in-flight gate")
	}
}

// TestManagedApplyDriftDuringReloadWaitSurvivesTerminalization pins ADR 0019
// §12: a watcher/SIGHUP-triggered drift assessment during the async
// hot-apply reload wait (when applyMu has already been released) must never
// be silently erased by the finalizer's own CompleteWrite call, which
// otherwise assumes disk holds exactly what this transaction wrote.
//
// Sequence: apply A persists candidate I and enqueues its reload; before the
// reload result arrives, an external writer changes the file to J and the
// watcher fires AssessDriftNow, which (correctly, at that instant) sees J
// against the still-old baseline; A's reload then succeeds and its
// finalizer calls CompleteWrite(I, ...). The final state must still be
// managed_drift naming baseline I and disk J — CompleteWrite's optimistic
// managed_clean must not overwrite what actually happened on disk.
func TestManagedApplyDriftDuringReloadWaitSurvivesTerminalization(t *testing.T) {
	submitCalled := make(chan struct{})
	proceedReload := make(chan struct{})
	submit := func(req server.ReloadRequest) error {
		close(submitCalled)
		go func() {
			<-proceedReload
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
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, submit)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	newRaw := validConfigRaw(t, ":8081")
	type applyOutcome struct {
		res ApplyResult
		err error
	}
	done := make(chan applyOutcome, 1)
	go func() {
		res, err := c.ApplyRaw(admin.ApplyRequestContext{}, newRaw, ApplyHot)
		done <- applyOutcome{res, err}
	}()

	// SubmitReload is called synchronously, immediately after candidate I is
	// persisted — waiting for it guarantees I is already on disk.
	<-submitCalled

	// An external writer changes the file to J while A's baseline has not
	// yet terminalized to I.
	external := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatalf("simulate external write: %v", err)
	}

	// The watcher/SIGHUP entry point fires here, exactly as it would in
	// production, and may briefly block on applyMu before proceeding.
	c.AssessDriftNow()

	// Allow A's reload to complete and its finalizer to terminalize.
	close(proceedReload)
	out := <-done
	if out.err != nil {
		t.Fatalf("ApplyRaw error: %v", out.err)
	}
	if !out.res.OK {
		t.Fatalf("the apply itself must still succeed, got %+v", out.res)
	}

	st := c.ManagedBaseline.Status()
	if st.State != ConfigStateManagedDrift {
		t.Fatalf("state = %v, want managed_drift — an external write during the reload wait must not be silently erased by CompleteWrite's assumed-clean baseline", st.State)
	}
	if st.BaselineRawSHA256 != digestHex(newRaw) {
		t.Errorf("baseline digest = %q, want I (%q) — the baseline must still name what this apply committed", st.BaselineRawSHA256, digestHex(newRaw))
	}
	if st.DiskRawSHA256 != digestHex(external) {
		t.Errorf("disk digest = %q, want J (%q) — drift must reflect the actual current disk content, not an assumption", st.DiskRawSHA256, digestHex(external))
	}
}
