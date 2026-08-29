// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"jul/internal/atomicfile"
)

const plannedRestartMarkerVersion = 1

// plannedRestartStatePrepared is written atomically before the config file is
// overwritten. On crash-recovery, a prepared marker whose disk digest matches
// the base digest means the write never happened: the marker is removed and
// the process is clean. If the disk digest matches the staged digest, the
// write completed and the marker is promoted to staged.
const plannedRestartStatePrepared = "prepared"

// plannedRestartStateStaged is written after the config file is successfully
// overwritten. A staged marker is the normal in-flight state while the process
// awaits restart.
const plannedRestartStateStaged = "staged"

// ErrNoManagedPendingRestart is returned by DiscardSafe when there is no
// managed planned restart to discard.
var ErrNoManagedPendingRestart = errors.New("no managed planned restart is pending")

// ErrInconsistentState is returned by DiscardSafe when the on-disk state does
// not match the marker, signalling a recovery situation.
var ErrInconsistentState = errors.New("planned restart state is inconsistent; manual recovery required")

// ErrServingVersionChanged is returned by DiscardSafe when the live serving
// version no longer matches the base version recorded in the marker, meaning
// a concurrent reload changed the live config while the staged restart was
// pending.
var ErrServingVersionChanged = errors.New("live serving version changed since the restart was staged; discard is unsafe")

// ErrNoManagedPreparedMarker is returned by PromoteToStaged when no marker
// file exists or the marker cannot be read. This indicates a programming
// error: PromoteToStaged must only be called after a successful StageManaged.
var ErrNoManagedPreparedMarker = errors.New("no managed prepared marker found; PromoteToStaged called out of sequence")

// ErrMarkerWrongState is returned by PromoteToStaged when the marker is not
// in the "prepared" state. The marker may already be staged (idempotent from a
// previous run) or in an unknown state.
var ErrMarkerWrongState = errors.New("marker is not in prepared state; PromoteToStaged called out of sequence")

// ErrMarkerCandidateMismatch is returned by PromoteToStagedVerified when the
// prepared marker's StagedRawSHA256 does not match the digest of the candidate
// bytes supplied to the promotion. This is a programming/state error: the
// marker describes different bytes than the caller believes it wrote.
var ErrMarkerCandidateMismatch = errors.New("prepared marker candidate digest does not match supplied candidate bytes")

// ErrStagedCandidateChanged is returned by PromoteToStagedVerified when the
// active config file on disk does not equal the expected candidate at the time
// of marker promotion — either an external writer replaced the candidate
// between the write and the promotion, or the file changed immediately after
// the marker was promoted. In both cases the stage cannot be reported as
// successful and the caller must surface a conflict/inconsistent state.
var ErrStagedCandidateChanged = errors.New("active config changed from the staged candidate during marker promotion")

// PlannedRestartMarker is the JSON sidecar written adjacent to the active
// config file when a staged restart is pending. It is the crash-recovery
// anchor: any process that starts up with this file present can determine
// exactly what happened and take the correct action.
//
// Never store resolved secrets in the marker. The backup contains the exact
// previous raw config, which may include secret references but not resolved
// values.
type PlannedRestartMarker struct {
	Version              int    `json:"version"`
	State                string `json:"state"` // prepared | staged
	ConfigPath           string `json:"config_path"`
	BaseRawSHA256        string `json:"base_raw_sha256"`
	BaseCanonicalVersion string `json:"base_canonical_version"`
	BaseServingVersion   string `json:"base_serving_version"`
	StagedRawSHA256      string `json:"staged_raw_sha256"`
	// StagedVersion retains its legacy meaning: the canonical resolved effective
	// candidate version. StagedPersistedVersion identifies the unresolved config
	// on disk without changing existing marker/API semantics.
	StagedVersion          string `json:"staged_version"`
	StagedPersistedVersion string `json:"staged_persisted_version,omitempty"`
	// PreviousStagedRawSHA256 is set only when this marker was written for a
	// staged-update (a second stage_restart while one was already pending). It
	// records the digest of the previous staged content so that crash recovery
	// can distinguish a failed update write (disk == previous staged) from a
	// genuine inconsistency (N-03).
	PreviousStagedRawSHA256 string `json:"previous_staged_raw_sha256,omitempty"`
	// M-01: Preserve the previous staged version, subsystems, and timestamp for
	// recovery. When a staged-update fails, these fields let the API report the
	// correct metadata instead of showing the failed candidate's values.
	PreviousStagedVersion          string   `json:"previous_staged_version,omitempty"`
	PreviousStagedPersistedVersion string   `json:"previous_staged_persisted_version,omitempty"`
	PreviousSubsystems             []string `json:"previous_subsystems,omitempty"`
	// PreviousStagedAt is the timestamp of the previous staged configuration.
	// It is restored when the update write fails so the API reports the
	// correct time instead of the failed-attempt timestamp.
	PreviousStagedAt  time.Time `json:"previous_staged_at,omitempty"`
	PendingSubsystems []string  `json:"pending_subsystems"`
	StagedAt          time.Time `json:"staged_at"`
}

// PlannedRestartStateEnum is the authoritative single enum representing the
// planned-restart/divergence state. It is derived from on-disk markers plus a
// runtime/disk comparison and is used to block hot applies and surface UI
// banners.
type PlannedRestartStateEnum string

const (
	// PlannedRestartStateNone means no pending restart, no external divergence,
	// and no inconsistency.
	PlannedRestartStateNone PlannedRestartStateEnum = "none"
	// PlannedRestartStateManagedStaged means a managed staged restart is
	// pending and the on-disk config is consistent with the marker.
	PlannedRestartStateManagedStaged PlannedRestartStateEnum = "managed_staged"
	// PlannedRestartStateExternalDivergence means the on-disk config differs
	// from the running runtime in an unmanaged way (e.g. external editor).
	// Hot applies are blocked until the divergence is resolved.
	PlannedRestartStateExternalDivergence PlannedRestartStateEnum = "external_divergence"
	// PlannedRestartStateInconsistent means the on-disk marker and config are
	// in an inconsistent state that cannot be repaired automatically.
	PlannedRestartStateInconsistent PlannedRestartStateEnum = "inconsistent"
)

// PlannedRestartState is the authoritative store state exposed to callers.
// It captures managed pending staged restarts, external disk/runtime
// divergence, and post-reconciliation inconsistent states so callers can
// block hot applies and surface recovery banners for any of them.
type PlannedRestartState struct {
	Pending      bool
	Inconsistent bool
	External     bool
	State        PlannedRestartStateEnum
}

// PlannedRestartStore owns the managed planned-restart sidecar state. When
// ConfigPath is empty the store operates entirely in memory (useful in tests
// that only need IsPending/Stage/Discard behavior without a real file system).
// When ConfigPath is set the store reads and writes two adjacent sidecar files:
//
//   - <ConfigPath>.pending-restart.json  — the marker (state, digests, versions)
//   - <ConfigPath>.pending-restart.bak   — the exact previous raw config bytes
//
// Both files use 0600 permissions and are written atomically via temp-file
// rename so a mid-write crash leaves the state readable.
type PlannedRestartStore struct {
	// ConfigPath is the active config file path. When empty, the store operates
	// in in-memory-only mode. Set by NewFilePlannedRestartStore.
	ConfigPath string

	mu sync.Mutex

	// In-memory state (used in both modes for caching and in-memory-only tests).
	pending      bool
	raw          []byte // staged candidate raw bytes
	baseRaw      []byte // original pre-stage raw bytes (used for diagnostics)
	stagedAt     time.Time
	inconsistent bool // set by Reconcile when sidecar state cannot be repaired
	external     bool // true when an unmanaged external disk/runtime divergence is present

	// testHookAfterStagedMarkerWritten, when non-nil, runs once immediately
	// after PromoteToStagedVerified durably writes the "staged" marker and
	// before its post-promotion disk re-read. It exists so a test can
	// deterministically simulate an external write landing in that exact
	// window (ADR 0019 §11.2.4.1); always nil in production.
	testHookAfterStagedMarkerWritten func()
}

// NewFilePlannedRestartStore creates a PlannedRestartStore backed by sidecar
// files adjacent to configPath. On construction it loads any existing marker
// from disk into memory so IsPending is accurate immediately after creation.
func NewFilePlannedRestartStore(configPath string) *PlannedRestartStore {
	s := &PlannedRestartStore{ConfigPath: configPath}
	// Hydrate in-memory state from any existing on-disk marker. Errors are
	// ignored here: a corrupt marker is treated as no pending restart; the
	// caller can call Reconcile to obtain a full diagnosis.
	if m, err := s.LoadMarker(); err == nil && m != nil && m.State == plannedRestartStateStaged {
		s.pending = true
		s.stagedAt = m.StagedAt
	}
	return s
}

// markerPath returns the path of the marker sidecar file.
func (s *PlannedRestartStore) markerPath() string {
	return s.ConfigPath + ".pending-restart.json"
}

// backupPath returns the path of the backup sidecar file.
func (s *PlannedRestartStore) backupPath() string {
	return s.ConfigPath + ".pending-restart.bak"
}

// IsPending reports whether a managed planned restart is currently staged.
func (s *PlannedRestartStore) IsPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pending
}

// State returns the authoritative planned-restart state. Callers should use
// this for gating hot applies and rendering banners because it includes
// managed pending states, external divergence, and inconsistent states.
func (s *PlannedRestartStore) State() PlannedRestartState {
	if s == nil {
		return PlannedRestartState{State: PlannedRestartStateNone}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stateLocked()
}

func (s *PlannedRestartStore) stateLocked() PlannedRestartState {
	switch {
	case s.inconsistent:
		return PlannedRestartState{Pending: false, Inconsistent: true, External: false, State: PlannedRestartStateInconsistent}
	case s.pending:
		// A valid managed staged marker takes priority over any external-divergence
		// flag. The disk/runtime difference is expected when a staged config is
		// waiting for a restart (H-02).
		return PlannedRestartState{Pending: true, Inconsistent: false, External: false, State: PlannedRestartStateManagedStaged}
	case s.external:
		return PlannedRestartState{Pending: false, Inconsistent: false, External: true, State: PlannedRestartStateExternalDivergence}
	default:
		return PlannedRestartState{Pending: false, Inconsistent: false, External: false, State: PlannedRestartStateNone}
	}
}

// Stage records a planned-restart candidate using in-memory state only. It is
// the backward-compatible method used by tests that do not require file backing.
// In production use StageManaged, which writes the crash-consistent sidecar
// files.
func (s *PlannedRestartStore) Stage(raw []byte) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = true
	s.raw = raw
	if s.baseRaw == nil {
		s.baseRaw = raw
	}
	s.stagedAt = time.Now()
}

// BaseRaw returns the original serving configuration bytes preserved as the
// rollback base. It is used when computing preflight diffs for a staged update.
func (s *PlannedRestartStore) BaseRaw() []byte {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.baseRaw
}

// StageManaged performs a crash-consistent file-backed stage. It is the
// production staging path. The caller must hold the coordinator mutex before
// calling this so no concurrent apply can interleave with the staging order.
//
// baseRaw are the original serving config bytes that must be preserved as the
// rollback base. For a fresh stage this is the config on disk before the
// candidate is written. For a staged update this is the base from the existing
// marker/backup so the operator can still roll back to the live config that
// was serving when the first stage happened.
//
// Crash-consistent order (§17.4):
//
//  1. Atomically write baseRaw to .bak (only on a fresh stage; updates keep the
//     existing backup).
//  2. Atomically write marker state "prepared" with base/candidate digests.
//  3. Caller writes the candidate to the active config path (AFTER this call).
//  4. Atomically update marker state to "staged".
//
// This function performs steps 1 and 2 and returns. The caller MUST write the
// candidate and then call PromoteToStaged to perform step 4. If the candidate
// write fails, the marker remains "prepared" so Reconcile can clean up safely.
//
// When ConfigPath is empty the method falls back to in-memory Stage.
func (s *PlannedRestartStore) StageManaged(baseRaw, candidateRaw []byte, marker PlannedRestartMarker) error {
	if s == nil {
		return nil
	}
	if s.ConfigPath == "" {
		s.Stage(candidateRaw)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// Load any existing marker to detect a staged update. A staged update
	// preserves the original base metadata and backup so the operator can
	// still roll back to the live config that was serving when the first
	// stage happened.
	existing, _ := s.loadMarkerLocked()
	isUpdate := existing != nil && existing.State == plannedRestartStateStaged

	if isUpdate {
		// Preserve the original base metadata and backup.
		marker.BaseRawSHA256 = existing.BaseRawSHA256
		marker.BaseCanonicalVersion = existing.BaseCanonicalVersion
		marker.BaseServingVersion = existing.BaseServingVersion
		// Record the digest of the previous staged content so crash recovery can
		// distinguish a failed update write (disk == previous staged) from a true
		// inconsistency (N-03).
		marker.PreviousStagedRawSHA256 = existing.StagedRawSHA256
		// M-01: Preserve the previous staged version, subsystems, and timestamp.
		marker.PreviousStagedVersion = existing.StagedVersion
		marker.PreviousStagedPersistedVersion = existing.StagedPersistedVersion
		marker.PreviousSubsystems = existing.PendingSubsystems
		marker.PreviousStagedAt = existing.StagedAt
		if s.baseRaw == nil {
			// Load base raw from backup if in-memory cache was lost.
			if base, err := os.ReadFile(s.backupPath()); err == nil {
				s.baseRaw = base
			}
		}
	} else {
		// Fresh stage: baseRaw is the original serving config on disk.
		if err := s.writeBackupLocked(baseRaw); err != nil {
			return fmt.Errorf("planned-restart stage: write backup: %w", err)
		}
		marker.BaseRawSHA256 = sha256Hex(baseRaw)
		s.baseRaw = baseRaw
	}

	// Step 2: write marker in "prepared" state before the candidate is written.
	// If the process crashes between here and when the caller writes the
	// candidate, Reconcile will see prepared+disk==base and clean up.
	marker.Version = plannedRestartMarkerVersion
	marker.State = plannedRestartStatePrepared
	marker.ConfigPath = s.ConfigPath
	marker.StagedAt = time.Now()
	if err := s.writeMarkerLocked(marker); err != nil {
		return fmt.Errorf("planned-restart stage: write prepared marker: %w", err)
	}

	// Step 3 (write candidate to disk) is performed by the caller AFTER this
	// function returns so that watcher suppression is registered before the
	// rename becomes visible.
	return nil
}

// WriteAdoptBackup performs step 2 of ADR 0019 §11.2.4's adopt-and-stage
// composition in isolation: write baseRaw (the baseline snapshot, never the
// file) to .bak. Unlike StageManaged, it never branches on an existing
// staged update — adopt-and-stage only ever runs when IsPending() is false
// (AdoptExternal rejects while a restart is already pending), so there is
// always exactly one fresh backup to write.
func (s *PlannedRestartStore) WriteAdoptBackup(baseRaw []byte) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeBackupLocked(baseRaw); err != nil {
		return fmt.Errorf("planned-restart stage: write adopt backup: %w", err)
	}
	s.baseRaw = baseRaw
	return nil
}

// WritePreparedAfterAdoptBackup performs step 4 of the adopt-and-stage
// composition in isolation: write the "prepared" planned-restart marker.
// Call only after WriteAdoptBackup, and only after the managed baseline has
// already committed (step 3) — the commit interposed between the two halves
// is exactly why adoption cannot use StageManaged, which writes both halves
// back to back in one call.
func (s *PlannedRestartStore) WritePreparedAfterAdoptBackup(marker PlannedRestartMarker) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	marker.Version = plannedRestartMarkerVersion
	marker.State = plannedRestartStatePrepared
	marker.ConfigPath = s.ConfigPath
	marker.StagedAt = time.Now()
	marker.BaseRawSHA256 = sha256Hex(s.baseRaw)
	if err := s.writeMarkerLocked(marker); err != nil {
		return fmt.Errorf("planned-restart stage: write prepared marker: %w", err)
	}
	return nil
}

// AbortPrepared restores the sidecar state that existed before StageManaged
// when the final expected-base check rejects the candidate. It never touches
// the active config file. A fresh stage removes its new marker and backup; a
// staged update restores the previous staged marker and keeps its backup.
func (s *PlannedRestartStore) AbortPrepared(previous *PlannedRestartMarker) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	current, err := s.loadMarkerLocked()
	if err != nil || current == nil || current.State != plannedRestartStatePrepared {
		return ErrNoManagedPreparedMarker
	}
	if previous == nil {
		if err := os.Remove(s.markerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("planned-restart abort: remove marker: %w", err)
		}
		if err := os.Remove(s.backupPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("planned-restart abort: remove backup: %w", err)
		}
		s.pending = false
		s.raw = nil
		s.baseRaw = nil
		s.stagedAt = time.Time{}
		s.inconsistent = false
		return nil
	}
	if previous.State != plannedRestartStateStaged {
		return ErrMarkerWrongState
	}
	if err := s.writeMarkerLocked(*previous); err != nil {
		return fmt.Errorf("planned-restart abort: restore previous marker: %w", err)
	}
	s.pending = true
	s.stagedAt = previous.StagedAt
	s.inconsistent = false
	return nil
}

// ClearStagingArtifacts removes the marker and backup unconditionally,
// whatever state the marker is in (or even if only the backup exists, with
// no marker at all), without touching the configuration file. The
// adopt-and-stage composition (ADR 0019 §11.2.4) uses it wherever its table
// requires the planned-restart artifacts removed after the baseline has
// already committed: a prepared-marker write failure (row 4), and a
// promotion mismatch where AbortPrepared cannot help because the marker was
// already written "staged" before the mismatch was caught (§11.2.4.1's
// post-promotion row) — AbortPrepared requires the marker to still be
// "prepared" and returns ErrNoManagedPreparedMarker otherwise. Left alone in
// either case, a later Reconcile would find the marker (or an orphaned
// backup) and either complete an operation already reported as failed, or
// leave secret-bearing bytes on disk indefinitely. Callers must not report
// success on the operation this clears; removing Jul's own bookkeeping is
// not the same as restoring the file, and only the second is forbidden.
func (s *PlannedRestartStore) ClearStagingArtifacts() error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(s.markerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear staging marker: %w", err)
	}
	if err := os.Remove(s.backupPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear staging backup: %w", err)
	}
	s.pending = false
	s.raw = nil
	s.baseRaw = nil
	s.stagedAt = time.Time{}
	s.inconsistent = false
	return nil
}

// Refresh reloads the marker from disk and reconciles it with the current
// disk content, updating the in-memory state. It does NOT check runtime
// divergence; call SetExternalDivergence separately for that. Failures mark
// the store inconsistent (fail-closed) and return an error.
func (s *PlannedRestartStore) Refresh() error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No marker file: clear managed state. External divergence is set
			// separately by the coordinator's RefreshState hook.
			s.pending = false
			s.raw = nil
			s.baseRaw = nil
			s.inconsistent = false
			return nil
		}
		s.inconsistent = true
		return fmt.Errorf("planned-restart refresh: load marker: %w", err)
	}
	if marker == nil {
		s.pending = false
		s.raw = nil
		s.baseRaw = nil
		s.inconsistent = false
		return nil
	}

	diskBytes, err := os.ReadFile(s.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.inconsistent = true
		return fmt.Errorf("planned-restart refresh: read config: %w", err)
	}
	diskDigest := sha256Hex(diskBytes)

	switch marker.State {
	case plannedRestartStatePrepared:
		switch diskDigest {
		case marker.BaseRawSHA256:
			// Write never completed; config is the original base. Remove marker
			// and backup: the process is starting clean.
			_ = os.Remove(s.markerPath())
			_ = os.Remove(s.backupPath())
			s.pending = false
			s.raw = nil
			s.baseRaw = nil
			s.inconsistent = false
		case marker.StagedRawSHA256:
			// Write completed but the state transition to "staged" was lost.
			// Promote the marker to "staged" so subsequent operations see a
			// consistent state.
			marker.State = plannedRestartStateStaged
			if werr := s.writeMarkerLocked(*marker); werr != nil {
				s.inconsistent = true
				return fmt.Errorf("planned-restart refresh: promote to staged: %w", werr)
			}
			s.pending = true
			if s.baseRaw == nil {
				if base, rerr := os.ReadFile(s.backupPath()); rerr == nil {
					s.baseRaw = base
				}
			}
			s.stagedAt = marker.StagedAt
			s.inconsistent = false
		default:
			// Check for a staged-update crash: disk matches the previous staged
			// digest, meaning the update write failed after the prepared marker
			// was written but before the candidate reached disk (N-03).
			if marker.PreviousStagedRawSHA256 != "" && diskDigest == marker.PreviousStagedRawSHA256 {
				// The update write failed; disk still holds the previous staged
				// content. Restore the marker to a clean staged state.
				marker.State = plannedRestartStateStaged
				marker.StagedRawSHA256 = marker.PreviousStagedRawSHA256
				// M-01: Restore the previous staged version and subsystems.
				marker.StagedVersion = marker.PreviousStagedVersion
				marker.StagedPersistedVersion = marker.PreviousStagedPersistedVersion
				marker.PendingSubsystems = marker.PreviousSubsystems
				marker.StagedAt = marker.PreviousStagedAt
				marker.PreviousStagedRawSHA256 = ""
				marker.PreviousStagedVersion = ""
				marker.PreviousStagedPersistedVersion = ""
				marker.PreviousSubsystems = nil
				marker.PreviousStagedAt = time.Time{}
				if werr := s.writeMarkerLocked(*marker); werr != nil {
					s.inconsistent = true
					return fmt.Errorf("planned-restart refresh: restore previous staged after update crash: %w", werr)
				}
				s.pending = true
				if s.baseRaw == nil {
					if base, rerr := os.ReadFile(s.backupPath()); rerr == nil {
						s.baseRaw = base
					}
				}
				s.stagedAt = marker.StagedAt
				s.inconsistent = false
			} else {
				s.pending = false
				s.raw = nil
				s.baseRaw = nil
				s.inconsistent = true
				return fmt.Errorf("planned-restart refresh: inconsistent state: disk digest %s matches neither base %s nor staged %s",
					diskDigest, marker.BaseRawSHA256, marker.StagedRawSHA256)
			}
		}

	case plannedRestartStateStaged:
		if diskDigest == marker.StagedRawSHA256 {
			s.pending = true
			if s.baseRaw == nil {
				if base, rerr := os.ReadFile(s.backupPath()); rerr == nil {
					s.baseRaw = base
				}
			}
			s.stagedAt = marker.StagedAt
			s.inconsistent = false
		} else {
			s.pending = false
			s.raw = nil
			s.baseRaw = nil
			s.inconsistent = true
			return fmt.Errorf("planned-restart refresh: staged marker present but disk digest %s does not match staged digest %s",
				diskDigest, marker.StagedRawSHA256)
		}

	default:
		s.pending = false
		s.raw = nil
		s.baseRaw = nil
		s.inconsistent = true
		return fmt.Errorf("planned-restart refresh: unknown marker state %q", marker.State)
	}

	return nil
}

// PromoteToStaged atomically promotes a previously prepared marker to
// "staged". It must be called only after the candidate bytes have been
// successfully written to the active config path. If the marker is missing,
// unreadable, or not in the "prepared" state this method returns a sentinel
// error so the caller can surface the programming/sequencing bug rather than
// silently losing the staged state.
func (s *PlannedRestartStore) PromoteToStaged(candidateRaw []byte) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil || marker == nil {
		return ErrNoManagedPreparedMarker
	}
	if marker.State != plannedRestartStatePrepared {
		return ErrMarkerWrongState
	}
	marker.State = plannedRestartStateStaged
	// M-01: A successful promote finalizes this staged candidate, so clear all
	// Previous* recovery metadata. Leaving it set would let a later unrelated
	// staged-update crash recovery resurrect a superseded candidate's digest,
	// version, subsystem list, or timestamp.
	marker.PreviousStagedRawSHA256 = ""
	marker.PreviousStagedVersion = ""
	marker.PreviousStagedPersistedVersion = ""
	marker.PreviousSubsystems = nil
	marker.PreviousStagedAt = time.Time{}
	if err := s.writeMarkerLocked(*marker); err != nil {
		return fmt.Errorf("planned-restart stage: promote to staged: %w", err)
	}

	s.pending = true
	s.raw = candidateRaw
	if s.baseRaw == nil {
		s.baseRaw = candidateRaw
	}
	s.stagedAt = marker.StagedAt
	return nil
}

// PromoteToStagedVerified is the linearizable AC-06 replacement for
// PromoteToStaged. In addition to promoting a prepared marker to "staged" it
// verifies, while holding the store mutex, that:
//
//   - the prepared marker's StagedRawSHA256 equals the digest of candidateRaw
//     (ErrMarkerCandidateMismatch — a programming/state error), and
//   - the active config file on disk still equals candidateRaw both immediately
//     before the marker is promoted and immediately after (ErrStagedCandidateChanged).
//
// The pre-promotion disk check closes the TOCTOU window where an external
// writer replaces the just-written candidate before the marker is promoted,
// which would otherwise let the API report success while the marker describes
// different bytes. The post-promotion check detects a write landing during the
// marker update. A successful stage therefore linearizes at the final disk
// digest verification after the marker is staged; any external write before
// that point prevents a success response and any write after is a normal new
// external-divergence event detected by the refresh path.
//
// The caller MUST hold the coordinator mutex across the candidate write and
// this call so the write→verify→promote→verify sequence is atomic with respect
// to other managed applies. When ConfigPath is empty this is a no-op (in-memory
// stores have no disk to verify).
func (s *PlannedRestartStore) PromoteToStagedVerified(candidateRaw []byte) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	expectedDigest := sha256Hex(candidateRaw)

	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil || marker == nil {
		return ErrNoManagedPreparedMarker
	}
	if marker.State != plannedRestartStatePrepared {
		return ErrMarkerWrongState
	}
	if marker.StagedRawSHA256 != expectedDigest {
		// The marker was prepared for different bytes than the caller supplied.
		s.inconsistent = true
		return ErrMarkerCandidateMismatch
	}

	// Pre-promotion disk check: the active config must still be the candidate.
	before, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return fmt.Errorf("planned-restart promote: read config before promotion: %w", err)
	}
	if sha256Hex(before) != expectedDigest {
		// An external writer replaced the candidate between the write and the
		// promotion. Do not report success and do not repair the file.
		return ErrStagedCandidateChanged
	}

	marker.State = plannedRestartStateStaged
	// M-01: finalize the staged candidate; clear all Previous* recovery metadata.
	marker.PreviousStagedRawSHA256 = ""
	marker.PreviousStagedVersion = ""
	marker.PreviousStagedPersistedVersion = ""
	marker.PreviousSubsystems = nil
	marker.PreviousStagedAt = time.Time{}
	if err := s.writeMarkerLocked(*marker); err != nil {
		return fmt.Errorf("planned-restart promote: write staged marker: %w", err)
	}
	if s.testHookAfterStagedMarkerWritten != nil {
		s.testHookAfterStagedMarkerWritten()
	}

	// Post-promotion disk check: detect a write that landed during the marker
	// update. The marker is already staged, so a mismatch is an inconsistency.
	after, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		s.inconsistent = true
		return fmt.Errorf("planned-restart promote: read config after promotion: %w", err)
	}
	if sha256Hex(after) != expectedDigest {
		s.inconsistent = true
		return ErrStagedCandidateChanged
	}

	s.pending = true
	s.raw = candidateRaw
	if s.baseRaw == nil {
		s.baseRaw = candidateRaw
	}
	s.stagedAt = marker.StagedAt
	return nil
}

// Discard clears a pending planned restart. In in-memory mode (empty
// ConfigPath) it unconditionally clears the in-memory state and returns the
// raw bytes and true. For file-backed stores, use DiscardSafe which enforces
// the consistency and serving-version checks from §17.5.
func (s *PlannedRestartStore) Discard() ([]byte, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.pending {
		return nil, false
	}
	raw := s.raw
	s.pending = false
	s.raw = nil
	s.baseRaw = nil
	s.stagedAt = time.Time{}
	s.external = false
	s.inconsistent = false
	return raw, true
}

// DiscardSafe performs the safety-verified discard from §17.5. It:
//
//  1. Loads the marker and backup from disk.
//  2. Verifies the marker is managed and consistent.
//  3. Verifies the current disk digest equals the marker's staged digest.
//  4. Verifies the current live serving version equals the marker's base version.
//  5. Atomically restores the backup bytes to the active config path.
//  6. Removes the marker and backup files.
//  7. Returns the restored bytes so the caller can suppress the watcher echo.
//
// On any verification failure the method returns an error and leaves all files
// untouched. The caller is responsible for suppressing the file-watcher echo
// of the restoration write.
func (s *PlannedRestartStore) DiscardSafe(liveServingVersion string) (restoredBytes []byte, err error) {
	if s == nil || s.ConfigPath == "" {
		return nil, ErrNoManagedPendingRestart
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil || marker == nil {
		return nil, ErrNoManagedPendingRestart
	}
	if marker.State != plannedRestartStateStaged {
		return nil, ErrNoManagedPendingRestart
	}

	// Verify current disk digest matches the staged digest.
	currentDisk, err := os.ReadFile(s.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("discard: cannot read current config: %w", err)
	}
	if sha256Hex(currentDisk) != marker.StagedRawSHA256 {
		return nil, ErrInconsistentState
	}

	// Verify the live serving version matches the base (what the running process
	// started with). If a concurrent reload promoted a different config to live,
	// restoring the backup would silently undo that reload.
	if liveServingVersion != "" && marker.BaseServingVersion != "" &&
		liveServingVersion != marker.BaseServingVersion {
		return nil, ErrServingVersionChanged
	}

	// Load the backup bytes.
	backupBytes, err := os.ReadFile(s.backupPath())
	if err != nil {
		return nil, fmt.Errorf("discard: cannot read backup: %w", err)
	}

	// Atomically restore the backup to the active config path.
	if err := atomicfile.Write(s.ConfigPath, backupBytes, 0o600); err != nil {
		return nil, fmt.Errorf("discard: restore backup: %w", err)
	}

	// Remove sidecar files only after the restore succeeds. A failure here
	// leaves orphaned sidecars which Reconcile will clean up on next startup.
	_ = os.Remove(s.markerPath())
	_ = os.Remove(s.backupPath())

	s.pending = false
	s.raw = nil
	s.baseRaw = nil
	s.stagedAt = time.Time{}
	s.external = false
	s.inconsistent = false
	return backupBytes, nil
}

// SetExternalDivergence marks the store as externally divergent. This is used
// when the on-disk config differs from the running runtime in an unmanaged
// way (e.g. an external editor changed the file). The store treats this as a
// blocking state similar to inconsistency: hot applies are blocked until the
// divergence is resolved (restart the process or restore the file).
func (s *PlannedRestartStore) SetExternalDivergence(present bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.external = present
}

// RemoveOrphanBackup removes the planned-restart backup sidecar when no
// marker exists to claim it. Reconcile treats an absent marker as a clean
// state and never collects such a backup, so it can otherwise survive
// indefinitely holding literal configuration bytes, including secrets — a
// file_owned startup performs this cleanup once, alongside closing any
// inherited managed epoch (ADR 0019 §17.2). It is idempotent and a no-op when
// a marker is present (a discard or reconciliation still owns the backup) or
// when nothing exists.
func (s *PlannedRestartStore) RemoveOrphanBackup() error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.markerPath()); err == nil {
		return nil
	}
	if err := os.Remove(s.backupPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove orphan planned-restart backup: %w", err)
	}
	return nil
}

// Reconcile runs the crash-recovery rules from §17.4 on startup. It inspects
// the marker file (if any) and the current disk content and takes the minimum
// safe action:
//
//   - No marker → clean state; nothing to do.
//   - Marker "prepared" + disk equals base digest → write never completed;
//     remove marker and backup (leaving disk unchanged).
//   - Marker "prepared" + disk equals staged digest → write completed before
//     the state update; promote marker to "staged".
//   - Marker "staged" + disk equals staged digest → successful prior startup;
//     remove marker and backup.
//   - Any other combination → inconsistent state; preserve backup, mark the
//     store inconsistent, and return an error so the caller can surface a
//     warning but not crash.
func (s *PlannedRestartStore) Reconcile() error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No marker file: clean state.
			s.pending = false
			return nil
		}
		return fmt.Errorf("reconcile: load marker: %w", err)
	}
	if marker == nil {
		s.pending = false
		return nil
	}

	diskBytes, err := os.ReadFile(s.ConfigPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reconcile: read config: %w", err)
	}
	diskDigest := sha256Hex(diskBytes)

	switch marker.State {
	case plannedRestartStatePrepared:
		switch diskDigest {
		case marker.BaseRawSHA256:
			// Write never completed; config is the original base. Remove marker
			// and backup: the process is starting clean.
			_ = os.Remove(s.markerPath())
			_ = os.Remove(s.backupPath())
			s.pending = false
			s.raw = nil
			s.baseRaw = nil
		case marker.StagedRawSHA256:
			// Write completed but the state transition to "staged" was lost.
			// Promote the marker to "staged" so subsequent operations see a
			// consistent state.
			marker.State = plannedRestartStateStaged
			if werr := s.writeMarkerLocked(*marker); werr != nil {
				return fmt.Errorf("reconcile: promote to staged: %w", werr)
			}
			s.pending = true
			// Load base raw from backup so update diffs can be computed against
			// the original serving config.
			if base, rerr := os.ReadFile(s.backupPath()); rerr == nil {
				s.baseRaw = base
			}
			s.stagedAt = marker.StagedAt
		default:
			// Check for a staged-update crash: disk matches the previous staged
			// digest, meaning the update write failed after the prepared marker
			// was written but before the candidate reached disk (N-03).
			if marker.PreviousStagedRawSHA256 != "" && diskDigest == marker.PreviousStagedRawSHA256 {
				// The update write failed; disk still holds the previous staged
				// content. Restore the marker to a clean staged state so the
				// operator can still restart with the previous candidate.
				marker.State = plannedRestartStateStaged
				marker.StagedRawSHA256 = marker.PreviousStagedRawSHA256
				// M-01: Restore the previous staged version and subsystems.
				marker.StagedVersion = marker.PreviousStagedVersion
				marker.StagedPersistedVersion = marker.PreviousStagedPersistedVersion
				marker.PendingSubsystems = marker.PreviousSubsystems
				marker.StagedAt = marker.PreviousStagedAt
				marker.PreviousStagedRawSHA256 = ""
				marker.PreviousStagedVersion = ""
				marker.PreviousStagedPersistedVersion = ""
				marker.PreviousSubsystems = nil
				marker.PreviousStagedAt = time.Time{}
				if werr := s.writeMarkerLocked(*marker); werr != nil {
					s.inconsistent = true
					return fmt.Errorf("reconcile: restore previous staged after update crash: %w", werr)
				}
				s.pending = true
				if base, rerr := os.ReadFile(s.backupPath()); rerr == nil {
					s.baseRaw = base
				}
				s.stagedAt = marker.StagedAt
			} else {
				// Truly inconsistent: disk matches none of the known digests.
				// Preserve the backup, set the inconsistent flag so Status()
				// can surface it, and report the problem.
				s.pending = false
				s.raw = nil
				s.baseRaw = nil
				s.inconsistent = true
				return fmt.Errorf("reconcile: inconsistent state: disk digest %s matches neither base %s nor staged %s; backup preserved at %s",
					diskDigest, marker.BaseRawSHA256, marker.StagedRawSHA256, s.backupPath())
			}
		}

	case plannedRestartStateStaged:
		if diskDigest == marker.StagedRawSHA256 {
			// Successful startup with the staged config; clear the sidecar files.
			_ = os.Remove(s.markerPath())
			_ = os.Remove(s.backupPath())
			s.pending = false
			s.raw = nil
			s.baseRaw = nil
		} else {
			// Staged digest does not match disk; inconsistent.
			s.pending = false
			s.inconsistent = true
			return fmt.Errorf("reconcile: staged marker present but disk digest %s does not match staged digest %s; backup preserved at %s",
				diskDigest, marker.StagedRawSHA256, s.backupPath())
		}

	default:
		s.pending = false
		return fmt.Errorf("reconcile: unknown marker state %q; backup preserved at %s",
			marker.State, s.backupPath())
	}

	return nil
}

// LoadMarker reads and decodes the marker sidecar file. It returns (nil, nil)
// when no marker file exists and an error when the file exists but cannot be
// decoded. The caller must not hold the store mutex; use loadMarkerLocked when
// already inside a lock.
func (s *PlannedRestartStore) LoadMarker() (*PlannedRestartMarker, error) {
	if s == nil || s.ConfigPath == "" {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadMarkerLocked()
}

// Status returns an admin.PendingRestartStatus reflecting the current state.
// It reads the in-memory cache; call Reconcile first to ensure accuracy.
func (s *PlannedRestartStore) Status() pendingRestartStatus {
	if s == nil {
		return pendingRestartStatus{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stateLocked()
	if st.State == PlannedRestartStateNone {
		return pendingRestartStatus{}
	}

	prs := pendingRestartStatus{
		State:            string(st.State),
		Managed:          st.State == PlannedRestartStateManagedStaged,
		Staged:           st.State == PlannedRestartStateManagedStaged,
		External:         st.State == PlannedRestartStateExternalDivergence,
		Inconsistent:     st.State == PlannedRestartStateInconsistent,
		DiscardAvailable: st.State == PlannedRestartStateManagedStaged && s.ConfigPath != "",
		StagedAt:         s.stagedAt,
	}
	// Load version metadata from the marker when available.
	if s.ConfigPath != "" && (st.State == PlannedRestartStateManagedStaged || st.State == PlannedRestartStateInconsistent) {
		if m, err := s.loadMarkerLocked(); err == nil && m != nil {
			prs.StagedVersion = m.StagedVersion
			prs.PersistedVersion = m.StagedPersistedVersion
			prs.ServingVersion = m.BaseServingVersion
			prs.Subsystems = m.PendingSubsystems
		}
	}
	return prs
}

// pendingRestartStatus is the internal representation of pending-restart
// status. It maps directly to admin.PendingRestartStatus but avoids an import
// cycle between the app and admin packages.
type pendingRestartStatus struct {
	State            string
	Managed          bool
	Staged           bool
	External         bool
	StagedAt         time.Time
	StagedVersion    string
	PersistedVersion string
	ServingVersion   string
	Subsystems       []string
	DiscardAvailable bool
	Inconsistent     bool
}

// loadMarkerLocked reads and decodes the marker file. Must be called with
// s.mu held.
func (s *PlannedRestartStore) loadMarkerLocked() (*PlannedRestartMarker, error) {
	data, err := os.ReadFile(s.markerPath())
	if err != nil {
		return nil, err
	}
	var m PlannedRestartMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("decode marker: %w", err)
	}
	return &m, nil
}

// writeMarkerLocked encodes and atomically writes marker to the marker path.
// Must be called with s.mu held.
func (s *PlannedRestartStore) writeMarkerLocked(m PlannedRestartMarker) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode marker: %w", err)
	}
	return atomicfile.Write(s.markerPath(), data, 0o600)
}

// writeBackupLocked atomically writes data to the backup path.
// Must be called with s.mu held.
func (s *PlannedRestartStore) writeBackupLocked(data []byte) error {
	return atomicfile.Write(s.backupPath(), data, 0o600)
}

// sha256Hex returns the lowercase hex-encoded SHA-256 digest of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
