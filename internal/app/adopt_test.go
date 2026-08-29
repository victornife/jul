// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"testing"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

func TestAdoptExternalNoBaseline(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	external := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}

	assessment, err := c.AssessAdoptExternal()
	if err != nil {
		t.Fatalf("AssessAdoptExternal: %v", err)
	}
	if assessment.Origin != "no_baseline" || !assessment.OK {
		t.Fatalf("assessment = %+v, want origin=no_baseline OK=true", assessment)
	}
	if assessment.PreviousRaw != nil {
		t.Error("no_baseline origin must not report a previous snapshot")
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: assessment.ObservedDigest,
		Mode:           "hot",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK || res.Origin != "no_baseline" {
		t.Fatalf("AdoptExternal result = %+v, want OK=true origin=no_baseline", res)
	}
	st := c.ManagedBaseline.Status()
	if st.State != ConfigStateManagedClean || st.BaselineRawSHA256 != digestHex(external) {
		t.Fatalf("baseline not established by adoption: %+v", st)
	}
}

// TestAssessAdoptExternalRejectsFileOwned pins that the assessment refuses to
// run at all outside managed mode, rather than reporting a hollow result.
func TestAssessAdoptExternalRejectsFileOwned(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	if _, err := c.AssessAdoptExternal(); err == nil {
		t.Fatal("expected an error outside managed mode")
	}
}

// TestAssessAdoptExternalParseError pins that an unparseable external file
// still returns a usable, side-effect-free assessment (origin, digest,
// baseline version) carrying the parse error, rather than failing outright.
func TestAssessAdoptExternalParseError(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	if err := os.WriteFile(path, []byte("{not valid toml"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}

	assessment, err := c.AssessAdoptExternal()
	if err != nil {
		t.Fatalf("AssessAdoptExternal: %v", err)
	}
	if assessment.OK {
		t.Fatal("a parse error must not report OK=true")
	}
	if len(assessment.ValidationErrors) == 0 {
		t.Error("expected a validation error for unparseable content")
	}
	if assessment.Origin != "no_baseline" {
		t.Errorf("origin = %q, want no_baseline even on a parse error", assessment.Origin)
	}
}

// TestAssessAdoptExternalRestartRequired pins that a restart-required
// external candidate is reported (RestartRequired=true, OK=true) rather than
// rejected — a preview classifies, it never refuses.
func TestAssessAdoptExternalRestartRequired(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	restartRequired := restartRequiredConfigRaw(t, ":8080")
	if err := os.WriteFile(path, restartRequired, 0o600); err != nil {
		t.Fatalf("write restart-required external file: %v", err)
	}

	assessment, err := c.AssessAdoptExternal()
	if err != nil {
		t.Fatalf("AssessAdoptExternal: %v", err)
	}
	if !assessment.OK {
		t.Fatalf("assessment = %+v, want OK=true", assessment)
	}
	if !assessment.RestartRequired {
		t.Error("expected RestartRequired=true for a restart-bound candidate")
	}
}

// TestAdoptExternalRejectsFileOwnedDirectly pins AdoptExternal's own
// defense-in-depth authority check (distinct from the HTTP-layer
// denyIfFileOwned gate tested in internal/admin).
func TestAdoptExternalRejectsFileOwnedDirectly(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityFileOwned, nil, nil)
	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK || !res.AuthorityDenied {
		t.Fatalf("result = %+v, want OK=false AuthorityDenied=true", res)
	}
}

// TestAdoptExternalRejectsNilManagedBaseline pins the guard for a
// misconfigured coordinator: managed authority with no baseline store wired.
func TestAdoptExternalRejectsNilManagedBaseline(t *testing.T) {
	c, _ := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK {
		t.Fatal("expected rejection with no ManagedBaseline wired")
	}
}

// TestAdoptExternalStaleBaseVersionConflict pins the base_version fence: a
// preview bound to a baseline version that has since moved on must conflict
// rather than adopt against stale context.
func TestAdoptExternalStaleBaseVersionConflict(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		Mode: "hot", Confirm: true, BaseVersion: "stale-version-from-an-earlier-preview",
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK || !res.Conflict {
		t.Fatalf("result = %+v, want a conflict", res)
	}
}

// TestAdoptExternalReadConfigRawFailure pins the digest-fence read's own
// storage-error path.
func TestAdoptExternalReadConfigRawFailure(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	c.ReadConfigRaw = func() ([]byte, error) { return nil, errors.New("disk unavailable") }

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err == nil {
		t.Fatal("expected a storage error")
	}
	if res.OK {
		t.Fatalf("result = %+v, want OK=false", res)
	}
}

// TestAdoptExternalParseErrorDirect pins AdoptExternal's own parse-error
// branch (distinct from AssessAdoptExternal's preview equivalent).
func TestAdoptExternalParseErrorDirect(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	if err := os.WriteFile(path, []byte("{not valid toml"), 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK || len(res.ValidationErrors) == 0 {
		t.Fatalf("result = %+v, want OK=false with a validation error", res)
	}
}

// TestAdoptExternalHotModeRestartRequiredCandidate pins that hot-mode
// adoption of a restart-bound candidate is rejected (with CanStage=true)
// rather than silently persisted.
func TestAdoptExternalHotModeRestartRequiredCandidate(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.WriteFile(path, restartRequiredConfigRaw(t, ":8080"), 0o600); err != nil {
		t.Fatalf("write restart-required external file: %v", err)
	}
	// Hot-mode restart-required detection compares against the startup
	// fingerprint, not the managed baseline, so it must be wired here.
	c.Preflight.StartupFP = lifecycle.ComputeFingerprint(mustParseForTest(t, seed))

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK || !res.RestartRequired || !res.CanStage {
		t.Fatalf("result = %+v, want OK=false RestartRequired=true CanStage=true", res)
	}
}

// TestAdoptExternalPreflightValidationError pins a non-restart-required
// preflight failure (e.g. handler construction rejecting the config).
func TestAdoptExternalPreflightValidationError(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	external := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	c.Preflight = &Preflight{
		BuildHandlers: func(context.Context, *config.Config, bool) (map[string]http.Handler, func(), error) {
			return nil, nil, errors.New("handler construction rejected this configuration")
		},
		Stream: &mockStreamPreflighter{},
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK || res.RestartRequired || len(res.ValidationErrors) == 0 {
		t.Fatalf("result = %+v, want OK=false RestartRequired=false with a validation error", res)
	}
}

// TestAdoptExternalHotReloadNotPublished pins ADR 0019 §14 step 10's second
// non-fatal outcome: the reload is enqueued and runs, but the runtime does
// not publish it. The adoption still succeeds as owned_not_serving.
func TestAdoptExternalHotReloadNotPublished(t *testing.T) {
	submit := func(req server.ReloadRequest) error {
		go func() {
			req.Result <- server.ReloadResult{ID: req.ID, Source: server.ReloadSourceAdmin, Outcome: server.ReloadNotApplied, Published: false}
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
	drifted := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, drifted, 0o600); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}
	liveCfg := mustParseForTest(t, seed)
	c.LiveSnapshot = func() server.LiveSnapshot { return server.LiveSnapshot{EffectiveConfig: liveCfg} }

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(drifted), Mode: "hot", Confirm: true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK {
		t.Fatalf("a reload that does not publish must not fail the adoption, got %+v", res)
	}
	if res.AppOutcome != "owned_not_serving" {
		t.Errorf("AppOutcome = %q, want owned_not_serving", res.AppOutcome)
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedDesiredAhead {
		t.Errorf("state = %v, want managed_desired_ahead", st.State)
	}
}

// TestAdoptExternalHotModeCommitMarkFailure pins that a failed T-mark commit
// during hot adoption fails cleanly with a storage error, changing nothing.
func TestAdoptExternalHotModeCommitMarkFailure(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	external := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatalf("write external file: %v", err)
	}
	if err := os.Mkdir(path+".managed-baseline.json", 0o755); err != nil {
		t.Fatalf("occupy baseline marker path: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot", Confirm: true})
	if err == nil {
		t.Fatal("expected a storage error")
	}
	if res.OK {
		t.Fatalf("result = %+v, want OK=false", res)
	}
}

func TestAdoptExternalDriftWithDiffAndHistorySnapshot(t *testing.T) {
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

	var completedPreviousRaw []byte
	c.OnManagedApplyComplete = func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
		completedPreviousRaw = append([]byte(nil), comp.PreviousRaw...)
		return admin.ManagedApplyFinalization{}
	}

	assessment, err := c.AssessAdoptExternal()
	if err != nil {
		t.Fatalf("AssessAdoptExternal: %v", err)
	}
	if assessment.Origin != "drift" {
		t.Fatalf("origin = %q, want drift", assessment.Origin)
	}
	if string(assessment.PreviousRaw) != string(seed) {
		t.Fatalf("previous raw mismatch: got %q want %q", assessment.PreviousRaw, seed)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: assessment.ObservedDigest,
		Mode:           "hot",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK || res.Origin != "drift" {
		t.Fatalf("result = %+v, want OK=true origin=drift", res)
	}
	// §14 step 11: the history snapshot must carry the PREVIOUS managed bytes
	// (seed), not the path — which by now holds the adopted bytes.
	if string(completedPreviousRaw) != string(seed) {
		t.Fatalf("history provenance = %q, want previous managed bytes %q", completedPreviousRaw, seed)
	}
	st := c.ManagedBaseline.Status()
	if st.BaselineRawSHA256 != digestHex(drifted) {
		t.Fatalf("baseline did not advance to the adopted bytes")
	}
}

func TestAdoptExternalDigestFenceConflict(t *testing.T) {
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

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: "stale-digest-from-an-earlier-preview",
		Mode:           "hot",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK || !res.Conflict {
		t.Fatalf("result = %+v, want a conflict", res)
	}
	onDisk, _ := os.ReadFile(path)
	if string(onDisk) != string(drifted) {
		t.Error("a fenced-out adoption must not touch the file")
	}
}

func TestAdoptExternalRejectsWithoutConfirmation(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{Mode: "hot"})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK {
		t.Fatal("adoption without confirmation must be rejected")
	}
}

func TestAdoptExternalRejectsWhilePlannedRestartPending(t *testing.T) {
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	c.PlannedRestart.Stage([]byte("staged-candidate"))

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(seed),
		Mode:           "hot",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if res.OK {
		t.Fatal("adoption must be rejected while a planned restart is pending")
	}
}

func TestAdoptExternalAlreadyLiveNeedsNoReload(t *testing.T) {
	submitCalled := false
	submit := func(server.ReloadRequest) error {
		submitCalled = true
		return nil
	}
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, submit)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	// The runtime already serves `seed` (LiveSnapshot below returns it), so
	// adoption after a restart needs no reload (ADR 0019 §11.2.2).
	liveCfg := mustParseForTest(t, seed)
	c.LiveSnapshot = func() server.LiveSnapshot {
		return server.LiveSnapshot{EffectiveConfig: liveCfg}
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(seed),
		Mode:           "hot",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK {
		t.Fatalf("expected success, got %+v", res)
	}
	if submitCalled {
		t.Error("adoption of already-live bytes must not submit a reload")
	}
	if res.Reload != nil {
		t.Error("no reload should be attached when nothing was reloaded")
	}
}

func TestAdoptExternalReloadFailureIsOwnedNotServing(t *testing.T) {
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
	drifted := validConfigRaw(t, ":9999")
	if err := os.WriteFile(path, drifted, 0o600); err != nil {
		t.Fatalf("simulate external edit: %v", err)
	}
	// LiveSnapshot returns something OTHER than drifted, so the coordinator
	// must attempt (and fail) to enqueue a reload.
	liveCfg := mustParseForTest(t, seed)
	c.LiveSnapshot = func() server.LiveSnapshot {
		return server.LiveSnapshot{EffectiveConfig: liveCfg}
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(drifted),
		Mode:           "hot",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK {
		t.Fatalf("a reload that cannot enqueue must not fail the adoption, got %+v", res)
	}
	if res.AppOutcome != "owned_not_serving" {
		t.Fatalf("AppOutcome = %q, want owned_not_serving", res.AppOutcome)
	}
	found := false
	for _, d := range res.Degraded {
		if d.Kind == DegradedStagingIncomplete {
			found = true
		}
	}
	if !found {
		t.Errorf("degraded array missing staging_incomplete: %+v", res.Degraded)
	}
	if st := c.ManagedBaseline.Status(); st.State != ConfigStateManagedDesiredAhead {
		t.Fatalf("state = %v, want managed_desired_ahead", st.State)
	}
	// The baseline itself must still have committed to the adopted bytes even
	// though the runtime is not (yet) serving them.
	if c.ManagedBaseline.Status().BaselineRawSHA256 != digestHex(drifted) {
		t.Fatal("baseline must have committed to the adopted bytes")
	}
}

func mustParseForTest(t *testing.T, raw []byte) *config.Config {
	t.Helper()
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

// newStageRestartAdoptFixture seeds a managed baseline at `seed`, writes
// `candidate` to disk (simulating the external edit being adopted), and
// wires a real file-backed PlannedRestartStore — newAuthorityTestCoordinator's
// default PlannedRestartStore has no ConfigPath and is therefore inert, which
// would make every write in adoptAndStageLocked a silent no-op.
func newStageRestartAdoptFixture(t *testing.T, seed, candidate []byte) (*ConfigApplyCoordinator, string) {
	t.Helper()
	c, path := newAuthorityTestCoordinator(t, AuthorityManaged, nil, nil)
	c.ManagedBaseline = NewManagedBaselineStore(path)
	c.PlannedRestart = NewFilePlannedRestartStore(path)
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	if err := c.ManagedBaseline.CommitMark(seed, "seed-version"); err != nil {
		t.Fatalf("CommitMark: %v", err)
	}
	if err := os.WriteFile(path, candidate, 0o600); err != nil {
		t.Fatalf("write candidate: %v", err)
	}
	liveCfg := mustParseForTest(t, seed)
	c.LiveSnapshot = func() server.LiveSnapshot { return server.LiveSnapshot{EffectiveConfig: liveCfg} }
	return c, path
}

// TestAdoptExternalStageRestartHappyPath pins ADR 0019 §11.2.4's ordinary
// success outcome end to end: `.bak` holds the previous baseline bytes (never
// the file), the planned-restart marker is durably "staged" naming the
// adopted candidate, and the managed baseline has committed to the same
// bytes in both its marker and its snapshot.
func TestAdoptExternalStageRestartHappyPath(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	candidate := validConfigRaw(t, ":9999")
	c, path := newStageRestartAdoptFixture(t, seed, candidate)

	assessment, err := c.AssessAdoptExternal()
	if err != nil {
		t.Fatalf("AssessAdoptExternal: %v", err)
	}
	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: assessment.ObservedDigest,
		Mode:           "stage_restart",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK || len(res.Degraded) != 0 {
		t.Fatalf("result = %+v, want OK=true with no degradations", res)
	}
	if res.PendingRestart == nil || !res.PendingRestart.Staged {
		t.Fatalf("PendingRestart = %+v, want a staged restart", res.PendingRestart)
	}

	backup, err := os.ReadFile(path + ".pending-restart.bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if string(backup) != string(seed) {
		t.Errorf(".bak = %q, want the previous baseline bytes %q (never the file)", backup, seed)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil || string(onDisk) != string(candidate) {
		t.Errorf("configuration file must be untouched by staging: %q", onDisk)
	}
	bst := c.ManagedBaseline.Status()
	if bst.State != ConfigStateManagedClean || bst.BaselineRawSHA256 != digestHex(candidate) {
		t.Fatalf("baseline = %+v, want managed_clean at the adopted candidate", bst)
	}
	snap, err := c.ManagedBaseline.Snapshot()
	if err != nil || string(snap) != string(candidate) {
		t.Errorf("baseline snapshot = %q, err=%v, want the adopted candidate", snap, err)
	}
}

// TestAdoptAndStageLockedSnapshotWriteFailureDegrades pins §11.2.4 step 6: a
// failure writing the baseline snapshot after staging is already durable
// must not unmake it — the marker already commits to the adopted bytes and
// the configuration file can repair the snapshot later (§11.2.1b). The
// adoption still succeeds, with a baseline_error degradation. Calls
// adoptAndStageLocked directly (same package) with an explicit prevRaw so
// the snapshot path can be occupied by a directory purely to fail the step 6
// write, without also failing the step 1 read AdoptExternal would otherwise
// perform from the same path.
func TestAdoptAndStageLockedSnapshotWriteFailureDegrades(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	candidate := validConfigRaw(t, ":9999")
	c, path := newStageRestartAdoptFixture(t, seed, candidate)

	if err := os.Remove(path + ".managed-baseline.snapshot"); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove existing snapshot: %v", err)
	}
	if err := os.Mkdir(path+".managed-baseline.snapshot", 0o755); err != nil {
		t.Fatalf("occupy snapshot path: %v", err)
	}

	cfg := mustParseForTest(t, candidate)
	pfResult, err := c.Preflight.Apply(context.Background(), cfg, mustParseForTest(t, seed), PreflightStageRestart)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	persistedVersion := server.CanonicalVersion(cfg)
	desiredVersion := server.CanonicalVersion(pfResult.Candidate.Effective)
	result := &ApplyResult{OK: true}

	if err := c.adoptAndStageLocked("drift", digestHex(seed), seed, candidate, persistedVersion, desiredVersion, pfResult, result); err != nil {
		t.Fatalf("adoptAndStageLocked: %v", err)
	}
	if !result.OK {
		t.Fatalf("a snapshot-write failure must still succeed, got %+v", result)
	}
	found := false
	for _, d := range result.Degraded {
		if d.Kind == DegradedBaselineError {
			found = true
		}
	}
	if !found {
		t.Errorf("degraded array missing baseline_error: %+v", result.Degraded)
	}
	if !c.PlannedRestart.IsPending() {
		t.Error("the restart must still be staged despite the degradation")
	}
	if bst := c.ManagedBaseline.Status(); bst.BaselineRawSHA256 != digestHex(candidate) {
		t.Error("the baseline marker must still have committed to the adopted bytes")
	}
}

// TestAdoptExternalStageRestartBaselineSnapshotMismatchFails pins §11.2.4 row
// 1: if the retained baseline snapshot cannot be verified against the
// marker's own recorded digest, adoption must fail before writing anything —
// staging without a trustworthy backup source would make a later discard
// unsafe.
func TestAdoptExternalStageRestartBaselineSnapshotMismatchFails(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	candidate := validConfigRaw(t, ":9999")
	c, path := newStageRestartAdoptFixture(t, seed, candidate)

	// Corrupt the baseline snapshot sidecar directly, without updating the
	// marker, so Snapshot() returns bytes that no longer match what the
	// marker's own digest names.
	if err := os.WriteFile(path+".managed-baseline.snapshot", []byte("corrupted"), 0o600); err != nil {
		t.Fatalf("corrupt snapshot: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(candidate),
		Mode:           "stage_restart",
		Confirm:        true,
	})
	if err == nil {
		t.Fatal("expected an error reporting the inconsistency")
	}
	if res.OK {
		t.Fatalf("result = %+v, want OK=false", res)
	}
	if _, statErr := os.Stat(path + ".pending-restart.json"); !os.IsNotExist(statErr) {
		t.Error("no planned-restart marker should be written when the snapshot cannot be verified")
	}
	if _, statErr := os.Stat(path + ".pending-restart.bak"); !os.IsNotExist(statErr) {
		t.Error("no backup should be written when the snapshot cannot be verified")
	}
	if bst := c.ManagedBaseline.Status(); bst.State != ConfigStateManagedInconsistent {
		t.Errorf("baseline state = %v, want managed_inconsistent", bst.State)
	}
}

// TestAdoptExternalStageRestartBaselineMarkerCommitFailureRow3 pins §11.2.4
// row 3: when the T-mark commit itself (the baseline marker write) fails,
// nothing can complete the operation later, so the orphaned .bak must be
// removed rather than left to survive indefinitely.
func TestAdoptExternalStageRestartBaselineMarkerCommitFailureRow3(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	candidate := validConfigRaw(t, ":9999")
	c, path := newStageRestartAdoptFixture(t, seed, candidate)

	// Block the managed-baseline marker write by occupying its path with a
	// directory: atomicfile.Write's rename over it must fail. The marker
	// already exists as a regular file (CommitMark wrote it), so remove it
	// first.
	if err := os.Remove(path + ".managed-baseline.json"); err != nil {
		t.Fatalf("remove existing baseline marker: %v", err)
	}
	if err := os.Mkdir(path+".managed-baseline.json", 0o755); err != nil {
		t.Fatalf("occupy baseline marker path: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(candidate),
		Mode:           "stage_restart",
		Confirm:        true,
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if res.OK {
		t.Fatalf("result = %+v, want OK=false", res)
	}
	if _, statErr := os.Stat(path + ".pending-restart.bak"); !os.IsNotExist(statErr) {
		t.Error("the orphaned backup must be removed when the baseline commit fails")
	}
	if _, statErr := os.Stat(path + ".pending-restart.json"); !os.IsNotExist(statErr) {
		t.Error("no planned-restart marker should exist")
	}
}

// TestAdoptExternalStageRestartPreparedMarkerFailureRow4 pins §11.2.4 row 4:
// the baseline has already committed to the adopted bytes when the
// planned-restart "prepared" marker write fails, so the adoption still
// succeeds — as owned_not_serving with a staging_incomplete degradation and
// managed_desired_ahead — and the orphaned backup is removed because no
// planned marker exists for Reconcile to ever collect it.
func TestAdoptExternalStageRestartPreparedMarkerFailureRow4(t *testing.T) {
	seed := validConfigRaw(t, ":8080")
	candidate := validConfigRaw(t, ":9999")
	c, path := newStageRestartAdoptFixture(t, seed, candidate)

	// Block the planned-restart marker write the same way.
	if err := os.Mkdir(path+".pending-restart.json", 0o755); err != nil {
		t.Fatalf("occupy planned-restart marker path: %v", err)
	}

	res, err := c.AdoptExternal(admin.ApplyRequestContext{}, admin.AdoptExternalRequest{
		ObservedDigest: digestHex(candidate),
		Mode:           "stage_restart",
		Confirm:        true,
	})
	if err != nil {
		t.Fatalf("AdoptExternal: %v", err)
	}
	if !res.OK {
		t.Fatalf("row 4 must still report success (the baseline already committed), got %+v", res)
	}
	if res.AppOutcome != "owned_not_serving" {
		t.Errorf("AppOutcome = %q, want owned_not_serving", res.AppOutcome)
	}
	found := false
	for _, d := range res.Degraded {
		if d.Kind == DegradedStagingIncomplete {
			found = true
		}
	}
	if !found {
		t.Errorf("degraded array missing staging_incomplete: %+v", res.Degraded)
	}
	if bst := c.ManagedBaseline.Status(); bst.State != ConfigStateManagedDesiredAhead {
		t.Errorf("baseline state = %v, want managed_desired_ahead", bst.State)
	}
	if bst := c.ManagedBaseline.Status(); bst.BaselineRawSHA256 != digestHex(candidate) {
		t.Error("the baseline must still have committed to the adopted bytes")
	}
	if _, statErr := os.Stat(path + ".pending-restart.bak"); !os.IsNotExist(statErr) {
		t.Error("the orphaned backup must be removed: no planned marker exists for Reconcile to ever collect it")
	}
}
