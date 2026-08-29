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
