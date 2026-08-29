// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"jul/internal/atomicfile"
)

const managedBaselineMarkerVersion = 1

// Managed-baseline marker states (ADR 0019 §11.2).
const (
	// baselineStatePreparing is T-write's only intermediate state: a
	// configuration write is in flight, rolling from a prior digest to an
	// intended one. It is written once, before the configuration file
	// changes, and is reachable only from T-write — T-mark never writes it.
	baselineStatePreparing = "preparing"
	// baselineStateCurrent means the marker names the bytes Jul last
	// persisted (or adopted). This is the steady state.
	baselineStateCurrent = "current"
	// baselineStateClosed is the handoff tombstone written once by a
	// file_owned startup that inherits a managed epoch (§17.2). It carries no
	// configuration bytes, so it is safe to leave in place indefinitely.
	baselineStateClosed = "closed"
)

// ManagedBaselineMarker is the JSON sidecar recording what Jul last
// persisted as the managed configuration. It carries digests, canonical
// versions and timestamps — never configuration content (ADR 0019 §11.2).
type ManagedBaselineMarker struct {
	Version int    `json:"version"`
	State   string `json:"state"` // preparing | current | closed

	// Prior* names the digest/version a "preparing" marker is rolling FROM.
	// Empty once the marker is "current" or "closed".
	PriorRawSHA256        string `json:"prior_raw_sha256,omitempty"`
	PriorCanonicalVersion string `json:"prior_canonical_version,omitempty"`

	// Current* names the bytes Jul last persisted (state "current"), or the
	// bytes a T-write is in flight toward (state "preparing").
	CurrentRawSHA256        string `json:"current_raw_sha256,omitempty"`
	CurrentCanonicalVersion string `json:"current_canonical_version,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`

	// Closed (tombstone) fields, set only when State == "closed".
	ClosedAt      time.Time `json:"closed_at,omitempty"`
	LastRawSHA256 string    `json:"last_raw_sha256,omitempty"`
}

// BaselineStatus is the authoritative, cheaply-readable managed-baseline
// state exposed to the coordinator, status endpoints, and the Console banner.
// It is computed at exactly the four event-driven points ADR 0019 §12 names
// (startup reconciliation, watcher event, SIGHUP, explicit refresh) plus the
// existing pre-write CAS, never by polling.
type BaselineStatus struct {
	// State is one of the managed_* ConfigState values. This store never
	// reports a file_owned_* value; the coordinator combines it with the
	// process authority mode.
	State ConfigState
	// Reason is set only when State is ConfigStateManagedInconsistent.
	Reason ManagedInconsistentReason

	// Drift and DriftDetectedAt describe whether the file currently differs
	// from the managed baseline. Meaningful only when a baseline exists
	// (State is ConfigStateManagedClean or ConfigStateManagedDrift).
	Drift           bool
	DriftDetectedAt time.Time

	// BaselineRawSHA256/BaselineCanonicalVersion identify what Jul last
	// persisted (the marker's "current" digest/version), when known.
	BaselineRawSHA256        string
	BaselineCanonicalVersion string

	// DiskRawSHA256/DiskCanonicalVersion/DiskParseError describe the current
	// on-disk content. DiskCanonicalVersion is set only when the disk content
	// parses; DiskParseError is set only when it does not. Never the raw
	// bytes themselves (ADR 0019 §13).
	DiskRawSHA256        string
	DiskCanonicalVersion string
	DiskParseError       string
}

// ManagedBaselineStore owns the managed-baseline sidecar state: a marker
// (digests, versions, timestamps — no configuration content) and a snapshot
// (the exact bytes Jul last persisted), adjacent to the configuration file.
// Both are written with the same discipline PlannedRestartStore already
// uses: atomicfile.Write, 0o600, temp-file rename (ADR 0019 §11.2).
//
// When ConfigPath is empty the store is inert: every method is a no-op and
// Status always reports ConfigStateManagedUnadopted. This matches
// PlannedRestartStore's in-memory-only mode for tests and for a process with
// no configuration file (where authority is file_owned anyway, per §9.1.1,
// and the baseline is never consulted).
type ManagedBaselineStore struct {
	ConfigPath string

	mu     sync.Mutex
	status BaselineStatus
}

// NewManagedBaselineStore constructs a store bound to configPath. It does not
// touch disk; call Reconcile once the configuration file's final startup
// content is settled (after any planned-restart reconciliation, per ADR 0019
// §11.2.3's layering rule).
func NewManagedBaselineStore(configPath string) *ManagedBaselineStore {
	return &ManagedBaselineStore{
		ConfigPath: configPath,
		status:     BaselineStatus{State: ConfigStateManagedUnadopted},
	}
}

func (s *ManagedBaselineStore) markerPath() string { return s.ConfigPath + ".managed-baseline.json" }
func (s *ManagedBaselineStore) snapshotPath() string {
	return s.ConfigPath + ".managed-baseline.snapshot"
}

// Status returns the last-assessed managed-baseline state. It never touches
// disk; callers that need a fresh assessment call Reconcile or AssessDrift.
func (s *ManagedBaselineStore) Status() BaselineStatus {
	if s == nil {
		return BaselineStatus{State: ConfigStateManagedUnadopted}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// loadMarkerLocked reads and decodes the marker file. It returns (nil, nil)
// when no marker file exists.
func (s *ManagedBaselineStore) loadMarkerLocked() (*ManagedBaselineMarker, error) {
	data, err := os.ReadFile(s.markerPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m ManagedBaselineMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode managed-baseline marker: %w", err)
	}
	return &m, nil
}

func (s *ManagedBaselineStore) writeMarkerLocked(m ManagedBaselineMarker) error {
	m.Version = managedBaselineMarkerVersion
	m.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode managed-baseline marker: %w", err)
	}
	return atomicfile.Write(s.markerPath(), data, 0o600)
}

func (s *ManagedBaselineStore) readSnapshotLocked() ([]byte, error) {
	return os.ReadFile(s.snapshotPath())
}

func (s *ManagedBaselineStore) writeSnapshotLocked(raw []byte) error {
	return atomicfile.Write(s.snapshotPath(), raw, 0o600)
}

// BeginWrite is T-write step 1 (ADR 0019 §11.2): it records, before the
// configuration file changes, that a managed write is rolling the baseline
// from (priorRawSHA256, priorCanonicalVersion) to (intendedRawSHA256,
// intendedCanonicalVersion). The caller must hold the coordinator's mutation
// lock and must call it before writing the candidate to disk.
func (s *ManagedBaselineStore) BeginWrite(priorRawSHA256, priorCanonicalVersion, intendedRawSHA256, intendedCanonicalVersion string) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeMarkerLocked(ManagedBaselineMarker{
		State:                   baselineStatePreparing,
		PriorRawSHA256:          priorRawSHA256,
		PriorCanonicalVersion:   priorCanonicalVersion,
		CurrentRawSHA256:        intendedRawSHA256,
		CurrentCanonicalVersion: intendedCanonicalVersion,
	})
}

// CompleteWrite is T-write step 4's success arm (ADR 0019 §11.2.0/§11.2.0.1):
// called at terminalization — the end of the configuration-path mutation
// phase, while the mutation gate is still held, before it is released to
// admit the next apply — with the exact bytes that were committed to disk.
// It writes the snapshot from committedRaw first, then promotes the marker to
// current(digest(committedRaw)). A failure here never changes the apply's own
// terminal outcome (ADR 0019 §11.2.1a); the caller records it as a
// baseline_error degradation and may retry once.
func (s *ManagedBaselineStore) CompleteWrite(committedRaw []byte, canonicalVersion string) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.writeSnapshotLocked(committedRaw); err != nil {
		return fmt.Errorf("managed baseline: write snapshot: %w", err)
	}
	digest := sha256Hex(committedRaw)
	if err := s.writeMarkerLocked(ManagedBaselineMarker{
		State:                   baselineStateCurrent,
		CurrentRawSHA256:        digest,
		CurrentCanonicalVersion: canonicalVersion,
	}); err != nil {
		return fmt.Errorf("managed baseline: promote marker: %w", err)
	}
	s.status = BaselineStatus{
		State:                    ConfigStateManagedClean,
		BaselineRawSHA256:        digest,
		BaselineCanonicalVersion: canonicalVersion,
		DiskRawSHA256:            digest,
		DiskCanonicalVersion:     canonicalVersion,
	}
	return nil
}

// RewindWrite is T-write step 4's restored arm: the candidate write did not
// take effect (a pre-Publish restoration put the previous bytes back), so the
// marker returns to naming the prior digest/version. The snapshot already
// holds the prior bytes — CompleteWrite never ran — so no snapshot write is
// needed. It is a no-op (not an error) only when no "preparing" marker is
// found, so a restoration path that races a concurrent repair does not fail
// loudly. A genuine read failure (I/O error, corrupt JSON) is a different
// thing entirely — an unreadable marker is not evidence that nothing needs
// rewinding, and swallowing it here would report a successful rewind (no
// degraded entry) while the on-disk marker still reads whatever it read
// before, which no later trigger would ever revisit.
func (s *ManagedBaselineStore) RewindWrite() error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil {
		return fmt.Errorf("managed baseline: read marker before rewind: %w", err)
	}
	if marker == nil || marker.State != baselineStatePreparing {
		return nil
	}
	if err := s.writeMarkerLocked(ManagedBaselineMarker{
		State:                   baselineStateCurrent,
		CurrentRawSHA256:        marker.PriorRawSHA256,
		CurrentCanonicalVersion: marker.PriorCanonicalVersion,
	}); err != nil {
		return fmt.Errorf("managed baseline: rewind marker: %w", err)
	}
	s.status = BaselineStatus{
		State:                    ConfigStateManagedClean,
		BaselineRawSHA256:        marker.PriorRawSHA256,
		BaselineCanonicalVersion: marker.PriorCanonicalVersion,
		DiskRawSHA256:            marker.PriorRawSHA256,
		DiskCanonicalVersion:     marker.PriorCanonicalVersion,
	}
	return nil
}

// CommitMark is the T-mark primitive (ADR 0019 §11.2/§11.2.0): establishing
// the first baseline, or adopting bytes already on disk. raw is the exact,
// already-verified buffer — callers must never re-read the path here, closing
// the time-of-check/time-of-use window a repair or adoption must avoid. It
// writes the marker "current"(digest) first — the commit point; no
// configuration is written and no reload is performed — then the snapshot
// from raw. A snapshot-write failure after a successful marker write still
// returns an error so the caller can report baseline_error; the marker itself
// is durable.
//
// This is CommitMarkerOnly and CommitSnapshotOnly performed back to back;
// every caller except the adopt-and-stage composition (ADR 0019 §11.2.4) uses
// this combined form because nothing needs to happen between the two writes.
func (s *ManagedBaselineStore) CommitMark(raw []byte, canonicalVersion string) error {
	if err := s.CommitMarkerOnly(raw, canonicalVersion); err != nil {
		return err
	}
	return s.CommitSnapshotOnly(raw)
}

// CommitMarkerOnly is CommitMark's first half — ADR 0019 §11.2.4 step 3, the
// actual T-mark commit point ("nothing a later Reconcile can complete may
// precede the commit point"). It writes only the "current" marker. The
// adopt-and-stage composition must interpose the planned-restart stage
// (steps 4-5) between this call and CommitSnapshotOnly; every other caller
// uses CommitMark, which performs both halves together.
func (s *ManagedBaselineStore) CommitMarkerOnly(raw []byte, canonicalVersion string) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	digest := sha256Hex(raw)
	if err := s.writeMarkerLocked(ManagedBaselineMarker{
		State:                   baselineStateCurrent,
		CurrentRawSHA256:        digest,
		CurrentCanonicalVersion: canonicalVersion,
	}); err != nil {
		return fmt.Errorf("managed baseline: commit marker: %w", err)
	}
	s.status = BaselineStatus{
		State:                    ConfigStateManagedClean,
		BaselineRawSHA256:        digest,
		BaselineCanonicalVersion: canonicalVersion,
		DiskRawSHA256:            digest,
		DiskCanonicalVersion:     canonicalVersion,
	}
	return nil
}

// CommitSnapshotOnly is CommitMark's second half — ADR 0019 §11.2.4 step 6.
// Call only after CommitMarkerOnly has already committed the marker for the
// same raw bytes. A crash (or a caller that never gets here) between the two
// halves leaves the marker naming digest(raw) with the snapshot still holding
// the prior bytes; §11.2.1b's Reconcile repairs the snapshot from the
// configuration file, which in every caller of this split already matches
// digest(raw) by construction.
func (s *ManagedBaselineStore) CommitSnapshotOnly(raw []byte) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeSnapshotLocked(raw); err != nil {
		return fmt.Errorf("managed baseline: write snapshot: %w", err)
	}
	return nil
}

// Snapshot returns the exact bytes Jul last persisted (the previous managed
// configuration), for adoption's diff and history-snapshot steps (ADR 0019
// §14 steps 5/11). It returns an error when the snapshot is missing or
// unreadable; callers must not fall back to reading the configuration path,
// which by the time this is called may already hold the adopted bytes.
func (s *ManagedBaselineStore) Snapshot() ([]byte, error) {
	if s == nil || s.ConfigPath == "" {
		return nil, os.ErrNotExist
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readSnapshotLocked()
}

// AssessDrift is the shared implementation of ADR 0019 §12's four
// event-driven drift-detection triggers (watcher event, SIGHUP, the existing
// pre-write CAS, and an explicit refresh). diskRaw/diskErr are the result of
// reading the configuration file once; diskCanonicalVersion/diskParseError
// are the caller's best-effort parse of diskRaw (empty/empty when diskErr is
// set). It never re-reads the path itself. It is a no-op when no managed
// baseline has been established or the store is inconsistent — drift is only
// meaningful relative to a trustworthy baseline.
//
// managed_desired_ahead is a live entry state, not only managed_clean and
// managed_drift (ADR 0019 §10's state diagram has an explicit
// managed_desired_ahead -> managed_drift edge for "external write after
// adoption"): the file and baseline agree on entry, but the runtime is
// simply behind, so a later external edit must still be caught. A match
// found while already managed_desired_ahead leaves that state alone rather
// than collapsing it to managed_clean — AssessDrift observes only the
// baseline-vs-disk dimension, never the runtime-serving one, so it must not
// assert a convergence it cannot see.
func (s *ManagedBaselineStore) AssessDrift(diskRaw []byte, diskErr error, diskCanonicalVersion, diskParseError string) {
	if s == nil || s.ConfigPath == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.State != ConfigStateManagedClean && s.status.State != ConfigStateManagedDrift && s.status.State != ConfigStateManagedDesiredAhead {
		return
	}

	var diskDigest string
	if diskErr == nil {
		diskDigest = sha256Hex(diskRaw)
	}
	s.status.DiskCanonicalVersion = diskCanonicalVersion
	s.status.DiskParseError = diskParseError
	if diskErr == nil {
		s.status.DiskRawSHA256 = diskDigest
	} else {
		s.status.DiskRawSHA256 = ""
	}

	if diskErr == nil && diskDigest == s.status.BaselineRawSHA256 {
		if s.status.State != ConfigStateManagedDesiredAhead {
			s.status.State = ConfigStateManagedClean
		}
		s.status.Drift = false
		s.status.DriftDetectedAt = time.Time{}
		return
	}
	if !s.status.Drift {
		s.status.DriftDetectedAt = time.Now().UTC()
	}
	s.status.Drift = true
	s.status.State = ConfigStateManagedDrift
}

// digestMatch classifies raw's digest against two candidates, used by
// Reconcile's three-input recovery matrix (ADR 0019 §11.2.1b).
func digestMatch(raw []byte, err error, prior, current string) (matchesPrior, matchesCurrent bool, digest string, ok bool) {
	if err != nil {
		return false, false, "", false
	}
	digest = sha256Hex(raw)
	return digest == prior, digest == current, digest, true
}

// Reconcile runs the startup recovery decision procedure (ADR 0019 §11.2.1 +
// §11.2.1b). It must be called after the planned-restart store has already
// reconciled (§11.2.3: planned restart is authoritative for which bytes
// belong on disk; the baseline reconciles second, against what that leaves).
// diskRaw/diskErr are the current, already-settled configuration file
// content; diskCanonicalVersion/diskParseError are the caller's best-effort
// parse of diskRaw. A non-nil error means the resolved state is
// ConfigStateManagedInconsistent; the caller decides whether that is fatal
// (it is not — the process still serves the file).
func (s *ManagedBaselineStore) Reconcile(diskRaw []byte, diskErr error, diskCanonicalVersion, diskParseError string) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, markerErr := s.loadMarkerLocked()
	snapshotRaw, snapshotErr := s.readSnapshotLocked()
	snapshotPresent := snapshotErr == nil

	inconsistent := func(reason ManagedInconsistentReason, baselineDigest, baselineVersion string, err error) error {
		s.status = BaselineStatus{
			State:                    ConfigStateManagedInconsistent,
			Reason:                   reason,
			BaselineRawSHA256:        baselineDigest,
			BaselineCanonicalVersion: baselineVersion,
			DiskCanonicalVersion:     diskCanonicalVersion,
			DiskParseError:           diskParseError,
		}
		if diskErr == nil {
			s.status.DiskRawSHA256 = sha256Hex(diskRaw)
		}
		return fmt.Errorf("managed baseline inconsistent (%s): %w", reason, err)
	}

	clean := func(digest, version string) {
		s.status = BaselineStatus{
			State:                    ConfigStateManagedClean,
			BaselineRawSHA256:        digest,
			BaselineCanonicalVersion: version,
			DiskRawSHA256:            digest,
			DiskCanonicalVersion:     diskCanonicalVersion,
		}
	}

	drift := func(digest, version string) {
		s.status = BaselineStatus{
			State:                    ConfigStateManagedDrift,
			Drift:                    true,
			DriftDetectedAt:          time.Now().UTC(),
			BaselineRawSHA256:        digest,
			BaselineCanonicalVersion: version,
			DiskCanonicalVersion:     diskCanonicalVersion,
			DiskParseError:           diskParseError,
		}
		if diskErr == nil {
			s.status.DiskRawSHA256 = sha256Hex(diskRaw)
		}
	}

	if markerErr != nil {
		return inconsistent(ReasonMarkerUnreadable, "", "", markerErr)
	}

	// Absent marker or a closed tombstone: origin is managed_unadopted unless
	// a surviving snapshot proves Jul's own state was lost (§11.2.1).
	if marker == nil {
		if snapshotPresent {
			return inconsistent(ReasonMarkerMissing, "", "", errors.New("snapshot present with no marker"))
		}
		s.status = BaselineStatus{State: ConfigStateManagedUnadopted}
		return nil
	}
	if marker.State == baselineStateClosed {
		if snapshotPresent {
			return inconsistent(ReasonCleanupIncomplete, "", "", errors.New("tombstone present with a surviving snapshot"))
		}
		s.status = BaselineStatus{State: ConfigStateManagedUnadopted}
		return nil
	}

	if marker.State == baselineStateCurrent {
		_, diskMatches, _, diskOK := digestMatch(diskRaw, diskErr, "", marker.CurrentRawSHA256)
		_, snapMatchesCurrent, _, snapOK := digestMatch(snapshotRaw, snapshotErr, "", marker.CurrentRawSHA256)
		switch {
		case diskOK && diskMatches && snapOK && snapMatchesCurrent:
			clean(marker.CurrentRawSHA256, marker.CurrentCanonicalVersion)
			return nil
		case diskOK && diskMatches:
			// Repair the snapshot from the verified, matching disk buffer.
			if err := s.writeSnapshotLocked(diskRaw); err != nil {
				return inconsistent(ReasonBaselineUnwritable, marker.CurrentRawSHA256, marker.CurrentCanonicalVersion, err)
			}
			clean(marker.CurrentRawSHA256, marker.CurrentCanonicalVersion)
			return nil
		case snapOK && snapMatchesCurrent:
			drift(marker.CurrentRawSHA256, marker.CurrentCanonicalVersion)
			return nil
		case !snapshotPresent:
			return inconsistent(ReasonSnapshotMissing, marker.CurrentRawSHA256, marker.CurrentCanonicalVersion, snapshotErr)
		case !snapOK:
			return inconsistent(ReasonSnapshotUnreadable, marker.CurrentRawSHA256, marker.CurrentCanonicalVersion, snapshotErr)
		default:
			return inconsistent(ReasonSnapshotDigestMismatch, marker.CurrentRawSHA256, marker.CurrentCanonicalVersion, errors.New("snapshot digest matches neither current nor disk"))
		}
	}

	// marker.State == "preparing": three-input matrix over (config: prior |
	// current | neither) x (snapshot: prior | current | missing/other).
	if marker.State == baselineStatePreparing {
		diskMatchesPrior, diskMatchesCurrent, _, diskOK := digestMatch(diskRaw, diskErr, marker.PriorRawSHA256, marker.CurrentRawSHA256)
		snapMatchesPrior, snapMatchesCurrent, _, snapOK := digestMatch(snapshotRaw, snapshotErr, marker.PriorRawSHA256, marker.CurrentRawSHA256)

		rollForward := func() error {
			// The rename landed; only the snapshot and/or the promotion did
			// not finish. Repair the snapshot from the verified disk buffer
			// (which matches "current") if needed, then promote.
			if !snapOK || !snapMatchesCurrent {
				if err := s.writeSnapshotLocked(diskRaw); err != nil {
					return inconsistent(ReasonBaselineUnwritable, marker.PriorRawSHA256, marker.PriorCanonicalVersion, err)
				}
			}
			if err := s.writeMarkerLocked(ManagedBaselineMarker{
				State:                   baselineStateCurrent,
				CurrentRawSHA256:        marker.CurrentRawSHA256,
				CurrentCanonicalVersion: marker.CurrentCanonicalVersion,
			}); err != nil {
				return inconsistent(ReasonBaselineUnwritable, marker.PriorRawSHA256, marker.PriorCanonicalVersion, err)
			}
			clean(marker.CurrentRawSHA256, marker.CurrentCanonicalVersion)
			return nil
		}
		rollBack := func() error {
			if (!snapOK || !snapMatchesPrior) && diskOK && diskMatchesPrior {
				if err := s.writeSnapshotLocked(diskRaw); err != nil {
					return inconsistent(ReasonBaselineUnwritable, marker.PriorRawSHA256, marker.PriorCanonicalVersion, err)
				}
			}
			if err := s.writeMarkerLocked(ManagedBaselineMarker{
				State:                   baselineStateCurrent,
				CurrentRawSHA256:        marker.PriorRawSHA256,
				CurrentCanonicalVersion: marker.PriorCanonicalVersion,
			}); err != nil {
				return inconsistent(ReasonBaselineUnwritable, marker.PriorRawSHA256, marker.PriorCanonicalVersion, err)
			}
			clean(marker.PriorRawSHA256, marker.PriorCanonicalVersion)
			return nil
		}

		switch {
		case diskOK && diskMatchesPrior && snapOK && snapMatchesPrior:
			// Pre-commit abort: the write never landed (or a restoration
			// already completed). Roll back to current(P).
			return rollBack()
		case diskOK && diskMatchesPrior && snapOK && snapMatchesCurrent:
			// The snapshot landed and the config was restored afterwards (the
			// failed-apply path). Restoration wins.
			return rollBack()
		case diskOK && diskMatchesPrior:
			// Snapshot missing/unreadable/mismatched: roll back and repair.
			return rollBack()
		case diskOK && diskMatchesCurrent && snapOK && snapMatchesCurrent:
			// The rename landed and the snapshot already advanced; only the
			// final marker promotion was lost. Roll forward.
			return rollForward()
		case diskOK && diskMatchesCurrent:
			// The rename landed but the snapshot did not advance yet: repair
			// from the verified config buffer, then promote.
			return rollForward()
		case snapOK && snapMatchesCurrent:
			// Config matches neither, but Jul's committed bytes survived in
			// the snapshot: promote and report ordinary drift with a usable
			// baseline.
			if err := s.writeMarkerLocked(ManagedBaselineMarker{
				State:                   baselineStateCurrent,
				CurrentRawSHA256:        marker.CurrentRawSHA256,
				CurrentCanonicalVersion: marker.CurrentCanonicalVersion,
			}); err != nil {
				return inconsistent(ReasonBaselineUnwritable, marker.PriorRawSHA256, marker.PriorCanonicalVersion, err)
			}
			drift(marker.CurrentRawSHA256, marker.CurrentCanonicalVersion)
			return nil
		case snapOK && snapMatchesPrior:
			// Config matches neither and the snapshot still names the prior
			// bytes: the rename outcome is unknown.
			return inconsistent(ReasonMarkerContradictsDisk, marker.PriorRawSHA256, marker.PriorCanonicalVersion, errors.New("disk matches neither prior nor intended digest"))
		default:
			return inconsistent(ReasonSnapshotMissing, marker.PriorRawSHA256, marker.PriorCanonicalVersion, errors.New("disk matches neither digest and no usable snapshot survives"))
		}
	}

	return inconsistent(ReasonMarkerUnreadable, "", "", fmt.Errorf("unknown managed-baseline marker state %q", marker.State))
}

// CloseEpoch performs the writes file_owned mode ever makes (ADR 0019
// §17.2), in the exact order the ADR requires: (1) delete the secret-bearing
// snapshot, (2) remove any orphan planned-restart backup via
// removeOrphanArtifacts, (3) replace the marker with a state-only,
// secret-free tombstone. Both secret-bearing artifacts are gone before the
// safe tombstone is ever written, so a failure partway through leaves the
// safe artifact behind rather than a secret-bearing one — which is why the
// tombstone write (3) must never run before removeOrphanArtifacts (2)
// returns successfully.
//
// removeOrphanArtifacts is called only when a marker or snapshot was found
// (there is an epoch to close); it is typically
// PlannedRestartStore.RemoveOrphanBackup, kept out of this package to avoid
// coupling the baseline store to the planned-restart store's type. A nil
// callback is a no-op step 2, so existing tests that only exercise the
// baseline's own two artifacts pass unchanged.
//
// It is idempotent and safe to call on every file_owned startup, including
// when no managed artifacts exist. A failure (e.g. a read-only mount) is
// returned so the caller can warn and report a lint finding rather than fail
// startup.
func (s *ManagedBaselineStore) CloseEpoch(removeOrphanArtifacts func() error) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil {
		return fmt.Errorf("managed baseline: read marker before closing epoch: %w", err)
	}

	// Step 1: the snapshot is secret-bearing and is removed unconditionally,
	// even when no marker survives to name it — an orphan snapshot with no
	// marker at all is a reachable, ADR-anticipated state (§11.2.1b's
	// "absent marker, present snapshot" row), and it must not outlive a
	// missing marker just because there is nothing left to tombstone.
	if err := os.Remove(s.snapshotPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("managed baseline: remove snapshot: %w", err)
	}

	if marker == nil {
		// No marker to replace with a tombstone, but step 2 still runs: an
		// orphan planned-restart backup is secret-bearing configuration bytes
		// regardless of whether a baseline marker exists.
		if removeOrphanArtifacts != nil {
			if err := removeOrphanArtifacts(); err != nil {
				return fmt.Errorf("managed baseline: remove orphan planned-restart backup: %w", err)
			}
		}
		return nil
	}

	// Already closed or not, the digest the tombstone should carry is the
	// same either way.
	lastDigest := marker.CurrentRawSHA256
	if marker.State == baselineStateClosed {
		lastDigest = marker.LastRawSHA256
	}

	// Step 2, strictly before step 3: a failure here must leave the tombstone
	// unwritten, so a crash or storage failure never reports "closed" while a
	// secret-bearing backup still survives.
	if removeOrphanArtifacts != nil {
		if err := removeOrphanArtifacts(); err != nil {
			return fmt.Errorf("managed baseline: remove orphan planned-restart backup: %w", err)
		}
	}

	// Step 3.
	if err := s.writeMarkerLocked(ManagedBaselineMarker{
		State:         baselineStateClosed,
		ClosedAt:      time.Now().UTC(),
		LastRawSHA256: lastDigest,
	}); err != nil {
		return fmt.Errorf("managed baseline: write tombstone: %w", err)
	}
	s.status = BaselineStatus{State: ConfigStateManagedUnadopted}
	return nil
}

// HasArtifacts reports whether any managed-baseline sidecar file exists,
// including a closed tombstone. It is used by `jul lint` to report surviving
// artifacts after a file_owned cleanup could not run (a read-only mount).
func (s *ManagedBaselineStore) HasArtifacts() bool {
	if s == nil || s.ConfigPath == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.markerPath()); err == nil {
		return true
	}
	if _, err := os.Stat(s.snapshotPath()); err == nil {
		return true
	}
	return false
}

// MarkInconsistent updates the in-memory status to managed_inconsistent with
// reason, without touching the marker or snapshot on disk. It is used when a
// live failure (e.g. a failed restoration) leaves the on-disk marker
// correctly describing an interrupted transaction that only a future
// Reconcile can resolve deterministically, but managed writes must be
// refused immediately rather than waiting for a restart.
func (s *ManagedBaselineStore) MarkInconsistent(reason ManagedInconsistentReason) {
	if s == nil || s.ConfigPath == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = ConfigStateManagedInconsistent
	s.status.Reason = reason
}

// MarkDesiredAhead transitions to managed_desired_ahead (ADR 0019 §11.2.5):
// the baseline and file are coherent — this call does not touch either — but
// the runtime is behind and nothing is staged. Managed writes remain allowed
// in this state; only a restart or an explicit re-stage converges it. It is
// reached only from a caller that has already confirmed the baseline itself
// is correct (e.g. after AdoptExternal commits but the hot reload does not
// take), so it does not re-verify Status().
func (s *ManagedBaselineStore) MarkDesiredAhead() {
	if s == nil || s.ConfigPath == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = ConfigStateManagedDesiredAhead
	s.status.Drift = false
	s.status.DriftDetectedAt = time.Time{}
}
