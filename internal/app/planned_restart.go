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

// PlannedRestartMarker is the JSON sidecar written adjacent to the active
// config file when a staged restart is pending. It is the crash-recovery
// anchor: any process that starts up with this file present can determine
// exactly what happened and take the correct action.
//
// Never store resolved secrets in the marker. The backup contains the exact
// previous raw config, which may include secret references but not resolved
// values.
type PlannedRestartMarker struct {
	Version              int       `json:"version"`
	State                string    `json:"state"` // prepared | staged
	ConfigPath           string    `json:"config_path"`
	BaseRawSHA256        string    `json:"base_raw_sha256"`
	BaseCanonicalVersion string    `json:"base_canonical_version"`
	BaseServingVersion   string    `json:"base_serving_version"`
	StagedRawSHA256      string    `json:"staged_raw_sha256"`
	StagedVersion        string    `json:"staged_version"`
	PendingSubsystems    []string  `json:"pending_subsystems"`
	StagedAt             time.Time `json:"staged_at"`
}

// PlannedRestartState is the authoritative store state exposed to callers.
// It captures both normal pending staged restarts and post-reconciliation
// inconsistent states so callers can block hot applies and surface recovery
// banners for either condition.
type PlannedRestartState struct {
	Pending      bool
	Inconsistent bool
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
	baseRaw      []byte // original pre-stage raw bytes (used for update diffs)
	stagedAt     time.Time
	inconsistent bool // set by Reconcile when sidecar state cannot be repaired
	external     bool // true when an unmanaged external disk/runtime divergence is present
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
// this for gating hot applies and rendering banners because it includes both
// managed pending states and post-reconciliation inconsistent states.
func (s *PlannedRestartStore) State() PlannedRestartState {
	if s == nil {
		return PlannedRestartState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return PlannedRestartState{
		Pending:      s.pending,
		Inconsistent: s.inconsistent || s.external,
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

// BaseRaw returns the original pre-stage raw config bytes, or nil when no
// staged restart is pending. This is used to compute lifecycle diffs for staged
// updates against the original serving config rather than the staged file.
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
// prevRaw are the exact bytes that were on disk BEFORE the candidate was
// written. The caller reads them before any write and supplies them here so
// the backup is always the original, pre-stage configuration — never the
// candidate that was just written to disk.
//
// Crash-consistent order (§17.4):
//
//  1. Atomically write prevRaw to .bak (first stage only; updates keep original).
//  2. Atomically write marker state "prepared" with base/candidate digests.
//  3. Caller writes the candidate to the active config path (AFTER this call).
//  4. Atomically update marker state to "staged".
//
// This function performs steps 1 and 2 and returns. The caller MUST write the
// candidate and then call PromoteToStaged to perform step 4. If the candidate
// write fails, the marker remains "prepared" so Reconcile can clean up safely.
//
// When ConfigPath is empty the method falls back to in-memory Stage.
func (s *PlannedRestartStore) StageManaged(prevRaw, candidateRaw []byte, marker PlannedRestartMarker) error {
	if s == nil {
		return nil
	}
	if s.ConfigPath == "" {
		s.Stage(candidateRaw)
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	// For a staged update (already pending), preserve the original .bak and
	// base fields from the existing marker so the rollback base is never lost.
	if s.pending {
		existing, err := s.loadMarkerLocked()
		if err == nil && existing != nil && existing.BaseRawSHA256 != "" {
			marker.BaseRawSHA256 = existing.BaseRawSHA256
			marker.BaseCanonicalVersion = existing.BaseCanonicalVersion
			marker.BaseServingVersion = existing.BaseServingVersion
			// Keep the original .bak intact — it holds the pre-stage original config.
		} else {
			// Existing marker is unreadable: best-effort fallback — write the
			// prevRaw bytes as the new backup (they are the last known good state).
			if err := s.writeBackupLocked(prevRaw); err != nil {
				return fmt.Errorf("planned-restart stage (update): write backup: %w", err)
			}
			marker.BaseRawSHA256 = sha256Hex(prevRaw)
		}
	} else {
		// First stage: write the original pre-candidate bytes as the backup.
		// prevRaw was captured by the coordinator BEFORE writing the candidate,
		// so it always contains the original configuration.
		if err := s.writeBackupLocked(prevRaw); err != nil {
			return fmt.Errorf("planned-restart stage: write backup: %w", err)
		}
		marker.BaseRawSHA256 = sha256Hex(prevRaw)
		s.baseRaw = prevRaw
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

// PromoteToStaged atomically promotes a previously prepared marker to
// "staged". It must be called only after the candidate bytes have been
// successfully written to the active config path. If the marker is not in the
// "prepared" state, this is a no-op.
func (s *PlannedRestartStore) PromoteToStaged(candidateRaw []byte) error {
	if s == nil || s.ConfigPath == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	marker, err := s.loadMarkerLocked()
	if err != nil || marker == nil {
		return nil
	}
	if marker.State != plannedRestartStatePrepared {
		return nil
	}
	marker.State = plannedRestartStateStaged
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
			// Inconsistent: disk matches neither the base nor the staged
			// digest. Preserve the backup, set the inconsistent flag so
			// Status() can surface it, and report the problem.
			s.pending = false
			s.raw = nil
			s.baseRaw = nil
			s.inconsistent = true
			return fmt.Errorf("reconcile: inconsistent state: disk digest %s matches neither base %s nor staged %s; backup preserved at %s",
				diskDigest, marker.BaseRawSHA256, marker.StagedRawSHA256, s.backupPath())
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
	// Return inconsistent/external status even when !pending so callers know
	// recovery is required and hot applies should be blocked.
	if !s.pending && !s.inconsistent && !s.external {
		return pendingRestartStatus{}
	}
	if s.inconsistent && !s.pending {
		return pendingRestartStatus{
			Managed:          true,
			Staged:           false,
			DiscardAvailable: false,
			Inconsistent:     true,
		}
	}
	if s.external && !s.pending {
		return pendingRestartStatus{
			Managed:      false,
			Staged:       false,
			External:     true,
			Inconsistent: false,
		}
	}
	st := pendingRestartStatus{
		Managed:          true,
		Staged:           true,
		DiscardAvailable: s.ConfigPath != "",
		Inconsistent:     s.inconsistent,
		External:         s.external,
		StagedAt:         s.stagedAt,
	}
	// Load version metadata from the marker when available.
	if s.ConfigPath != "" {
		if m, err := s.loadMarkerLocked(); err == nil && m != nil {
			st.StagedVersion = m.StagedVersion
			st.ServingVersion = m.BaseServingVersion
			st.Subsystems = m.PendingSubsystems
		}
	}
	return st
}

// pendingRestartStatus is the internal representation of pending-restart
// status. It maps directly to admin.PendingRestartStatus but avoids an import
// cycle between the app and admin packages.
type pendingRestartStatus struct {
	Managed          bool
	Staged           bool
	External         bool
	StagedAt         time.Time
	StagedVersion    string
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
