// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ManagedApplyState is the lifecycle state of a managed apply transaction in
// the terminal-result ledger (AC-02).
type ManagedApplyState string

const (
	ManagedApplyPending    ManagedApplyState = "pending"
	ManagedApplyFinalizing ManagedApplyState = "finalizing"
	ManagedApplyTerminal   ManagedApplyState = "terminal"
)

// ManagedApplyRecord is one entry in the terminal-result ledger. Private
// idempotency metadata is retained on the same record as the apply outcome so
// replay lifetime is exactly the ledger lifetime and exactly one boot scope
// (ADR 0019 §27.1/§27.2).
type ManagedApplyRecord struct {
	ID          string            `json:"id"`
	State       ManagedApplyState `json:"state"`
	Operation   ApplyOperation    `json:"operation"`
	StartedAt   time.Time         `json:"started_at"`
	Deadline    time.Time         `json:"deadline,omitempty"`
	CompletedAt time.Time         `json:"completed_at,omitempty"`

	Result ConfigApplyResult `json:"result"`

	HistorySnapshotID string `json:"history_snapshot_id,omitempty"`
	HistoryError      string `json:"history_error,omitempty"`
	FinalizationError string `json:"finalization_error,omitempty"`

	// OwnerTokenID is credential ownership metadata used only for result
	// authorization. It is never serialized.
	OwnerTokenID string `json:"-"`

	// ADR 0019 §27.1: these are the five private idempotency fields. They live
	// on the existing ledger record rather than in a second retained store.
	IdempotencyKey         string   `json:"-"`
	IdempotencyFingerprint [32]byte `json:"-"`
	IdempotencyMethod      string   `json:"-"`
	IdempotencyOperation   string   `json:"-"`
	IdempotencyPrincipal   string   `json:"-"`
}

var (
	ErrManagedApplyIDMismatch          = errors.New("managed apply: pending id reused with different operation")
	ErrManagedApplyInvalidID           = errors.New("managed apply: invalid id")
	ErrManagedApplyRecordIncomplete    = errors.New("managed apply: terminal record is incomplete")
	ErrManagedApplyIdempotencyMismatch = errors.New("managed apply: idempotency metadata mismatch")
)

const (
	defaultManagedApplyMaxTerminal = 512
	defaultManagedApplyTTL         = time.Hour
)

type ManagedApplyRegistry struct {
	mu sync.RWMutex

	maxTerminal int
	ttl         time.Duration

	byID  map[string]*ManagedApplyRecord
	order []string

	latest    atomic.Pointer[ManagedApplyRecord]
	finalized map[string]struct{}
}

func NewManagedApplyRegistry(maxTerminal int, ttl time.Duration) *ManagedApplyRegistry {
	if maxTerminal <= 0 {
		maxTerminal = defaultManagedApplyMaxTerminal
	}
	if ttl <= 0 {
		ttl = defaultManagedApplyTTL
	}
	return &ManagedApplyRegistry{
		maxTerminal: maxTerminal,
		ttl:         ttl,
		byID:        make(map[string]*ManagedApplyRecord),
		finalized:   make(map[string]struct{}),
	}
}

func (r *ManagedApplyRegistry) RetentionBounds() (minTerminalRecords int, minAge time.Duration) {
	if r == nil {
		return defaultManagedApplyMaxTerminal, defaultManagedApplyTTL
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.maxTerminal, r.ttl
}

type managedApplyID struct {
	Instance string
	Sequence uint64
	Legacy   bool
}

func parseManagedApplyID(id string) (managedApplyID, error) {
	if !strings.HasPrefix(id, "rl_") {
		return managedApplyID{}, ErrManagedApplyInvalidID
	}
	rest := strings.TrimPrefix(id, "rl_")
	parts := strings.Split(rest, "_")
	switch len(parts) {
	case 1:
		seq, err := parseCanonicalApplySequence(parts[0])
		if err != nil {
			return managedApplyID{}, err
		}
		return managedApplyID{Sequence: seq, Legacy: true}, nil
	case 2:
		instance := parts[0]
		if len(instance) != 12 {
			return managedApplyID{}, ErrManagedApplyInvalidID
		}
		for _, r := range instance {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return managedApplyID{}, ErrManagedApplyInvalidID
			}
		}
		seq, err := parseCanonicalApplySequence(parts[1])
		if err != nil {
			return managedApplyID{}, err
		}
		return managedApplyID{Instance: instance, Sequence: seq}, nil
	default:
		return managedApplyID{}, ErrManagedApplyInvalidID
	}
}

func parseCanonicalApplySequence(raw string) (uint64, error) {
	if raw == "" || len(raw) > 19 {
		return 0, ErrManagedApplyInvalidID
	}
	if len(raw) > 1 && raw[0] == '0' {
		return 0, ErrManagedApplyInvalidID
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, ErrManagedApplyInvalidID
		}
	}
	seq, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, ErrManagedApplyInvalidID
	}
	return seq, nil
}

func validManagedApplyID(id string) bool {
	_, err := parseManagedApplyID(id)
	return err == nil
}

func mergeManagedApplyPrivate(dst *ManagedApplyRecord, src ManagedApplyRecord) {
	if dst.OwnerTokenID == "" {
		dst.OwnerTokenID = src.OwnerTokenID
	}
	if dst.IdempotencyKey == "" {
		dst.IdempotencyKey = src.IdempotencyKey
		dst.IdempotencyFingerprint = src.IdempotencyFingerprint
		dst.IdempotencyMethod = src.IdempotencyMethod
		dst.IdempotencyOperation = src.IdempotencyOperation
		dst.IdempotencyPrincipal = src.IdempotencyPrincipal
	}
}

// BindIdempotency attaches the ADR 0019 §27.1 binding to an existing apply
// record. The binding is immutable: a second call may only restate the same
// metadata. No second retained idempotency store exists.
func (r *ManagedApplyRegistry) BindIdempotency(
	id, key string,
	fingerprint [32]byte,
	method, operation, principal string,
) error {
	if !validManagedApplyID(id) {
		return ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	if !ok {
		return ErrManagedApplyRecordIncomplete
	}
	if rec.IdempotencyKey != "" {
		if rec.IdempotencyKey != key || rec.IdempotencyFingerprint != fingerprint ||
			rec.IdempotencyMethod != method || rec.IdempotencyOperation != operation ||
			rec.IdempotencyPrincipal != principal {
			return ErrManagedApplyIdempotencyMismatch
		}
		return nil
	}
	rec.IdempotencyKey = key
	rec.IdempotencyFingerprint = fingerprint
	rec.IdempotencyMethod = method
	rec.IdempotencyOperation = operation
	rec.IdempotencyPrincipal = principal
	return nil
}

// FindIdempotency returns the retained binding for one principal/key pair. The
// ledger is deliberately scanned rather than maintaining a second index/store;
// the retained set is bounded and admin mutations are low-rate control-plane
// operations.
func (r *ManagedApplyRegistry) FindIdempotency(principal, key string) (ManagedApplyRecord, bool) {
	if r == nil || principal == "" || key == "" {
		return ManagedApplyRecord{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, rec := range r.byID {
		if rec.IdempotencyPrincipal == principal && rec.IdempotencyKey == key {
			return *rec, true
		}
	}
	return ManagedApplyRecord{}, false
}

func (r *ManagedApplyRegistry) BeginPending(rec ManagedApplyRecord) error {
	if !validManagedApplyID(rec.ID) {
		return ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byID[rec.ID]; ok {
		if existing.Operation != "" && rec.Operation != "" && existing.Operation != rec.Operation {
			return ErrManagedApplyIDMismatch
		}
		if existing.State != ManagedApplyPending {
			return nil
		}
		if existing.Operation == "" {
			existing.Operation = rec.Operation
		}
		if existing.StartedAt.IsZero() {
			existing.StartedAt = rec.StartedAt
		}
		if existing.Deadline.IsZero() {
			existing.Deadline = rec.Deadline
		}
		mergeManagedApplyPrivate(existing, rec)
		if rec.Result.ApplyID != "" {
			existing.Result = rec.Result
		}
		return nil
	}
	rec.State = ManagedApplyPending
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now().UTC()
	}
	cp := rec
	r.byID[rec.ID] = &cp
	return nil
}

// AbortPending removes a pre-side-effect idempotency reservation when the
// mutation fails before its durable commit point. It refuses to remove a record
// that has already progressed beyond pending, so a committed/in-flight
// transaction can never lose its replay attribution accidentally.
func (r *ManagedApplyRegistry) AbortPending(id string) bool {
	if !validManagedApplyID(id) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byID[id]
	if !ok || rec.State != ManagedApplyPending {
		return false
	}
	delete(r.byID, id)
	delete(r.finalized, id)
	return true
}

func (r *ManagedApplyRegistry) ClaimFinalization(rec ManagedApplyRecord) (bool, error) {
	if !validManagedApplyID(rec.ID) {
		return false, ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.byID[rec.ID]
	if ok {
		if existing.Operation != "" && rec.Operation != "" && existing.Operation != rec.Operation {
			return false, ErrManagedApplyIDMismatch
		}
		switch existing.State {
		case ManagedApplyFinalizing, ManagedApplyTerminal:
			return false, nil
		}
		existing.State = ManagedApplyFinalizing
		if existing.Operation == "" {
			existing.Operation = rec.Operation
		}
		if existing.StartedAt.IsZero() {
			existing.StartedAt = rec.StartedAt
		}
		if existing.Deadline.IsZero() {
			existing.Deadline = rec.Deadline
		}
		mergeManagedApplyPrivate(existing, rec)
		return true, nil
	}
	rec.State = ManagedApplyFinalizing
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now().UTC()
	}
	cp := rec
	r.byID[rec.ID] = &cp
	return true, nil
}

func (r *ManagedApplyRegistry) BeginFinalization(id string) (bool, error) {
	if !validManagedApplyID(id) {
		return false, ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, done := r.finalized[id]; done {
		return false, nil
	}
	r.finalized[id] = struct{}{}
	return true, nil
}

func (r *ManagedApplyRegistry) Complete(rec ManagedApplyRecord) error {
	if !validManagedApplyID(rec.ID) {
		return ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec.State = ManagedApplyTerminal
	if rec.CompletedAt.IsZero() {
		rec.CompletedAt = time.Now().UTC()
	}
	existing, ok := r.byID[rec.ID]
	if ok {
		if rec.StartedAt.IsZero() {
			rec.StartedAt = existing.StartedAt
		}
		if rec.Deadline.IsZero() {
			rec.Deadline = existing.Deadline
		}
		mergeManagedApplyPrivate(&rec, *existing)
	}
	cp := rec
	r.byID[rec.ID] = &cp
	r.addTerminalOrderLocked(rec.ID)
	r.pruneLocked(time.Now().UTC())
	return nil
}

func (r *ManagedApplyRegistry) FailFinalization(rec ManagedApplyRecord) error {
	if !validManagedApplyID(rec.ID) {
		return ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, exists := r.byID[rec.ID]; exists {
		if existing.Operation != "" && rec.Operation != "" && existing.Operation != rec.Operation {
			return ErrManagedApplyIDMismatch
		}
		if rec.Operation == "" {
			rec.Operation = existing.Operation
		}
		if rec.StartedAt.IsZero() {
			rec.StartedAt = existing.StartedAt
		}
		if rec.Deadline.IsZero() {
			rec.Deadline = existing.Deadline
		}
		mergeManagedApplyPrivate(&rec, *existing)
		if rec.Result.ApplyID == "" || rec.Result.Mode == "" {
			rec.Result = existing.Result
		}
		if rec.HistorySnapshotID == "" {
			rec.HistorySnapshotID = existing.HistorySnapshotID
		}
		if rec.HistoryError == "" {
			rec.HistoryError = existing.HistoryError
		}
		rec.FinalizationError = joinManagedApplyErrors(existing.FinalizationError, rec.FinalizationError)
	}
	if rec.Result.ApplyID == "" {
		rec.Result.ApplyID = rec.ID
	}
	if rec.Result.Mode != "hot" && rec.Result.Mode != "stage_restart" {
		return ErrManagedApplyRecordIncomplete
	}
	rec.State = ManagedApplyTerminal
	if rec.CompletedAt.IsZero() {
		rec.CompletedAt = time.Now().UTC()
	}
	cp := rec
	r.byID[rec.ID] = &cp
	r.addTerminalOrderLocked(rec.ID)
	r.pruneLocked(time.Now().UTC())
	return nil
}

func (r *ManagedApplyRegistry) addTerminalOrderLocked(id string) {
	for _, existingID := range r.order {
		if existingID == id {
			return
		}
	}
	r.order = append(r.order, id)
}

func joinManagedApplyErrors(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	switch {
	case existing == "":
		return next
	case next == "":
		return existing
	default:
		return existing + "; " + next
	}
}

func (r *ManagedApplyRegistry) SetLatest(id string) {
	r.mu.RLock()
	rec, ok := r.byID[id]
	r.mu.RUnlock()
	if !ok {
		return
	}
	cp := *rec
	r.latest.Store(&cp)
}

func (r *ManagedApplyRegistry) Get(id string) (ManagedApplyRecord, bool) {
	if !validManagedApplyID(id) {
		return ManagedApplyRecord{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rec, ok := r.byID[id]
	if !ok {
		return ManagedApplyRecord{}, false
	}
	return *rec, true
}

func (r *ManagedApplyRegistry) Latest() *ManagedApplyRecord {
	rec := r.latest.Load()
	if rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

func (r *ManagedApplyRegistry) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
}

func (r *ManagedApplyRegistry) pruneLocked(now time.Time) {
	for len(r.order) > r.maxTerminal {
		oldestID := r.order[0]
		rec, ok := r.byID[oldestID]
		if !ok {
			r.order = r.order[1:]
			continue
		}
		if now.Sub(rec.CompletedAt) < r.ttl {
			break
		}
		delete(r.byID, oldestID)
		delete(r.finalized, oldestID)
		r.order = r.order[1:]
		if latest := r.latest.Load(); latest != nil && latest.ID == oldestID {
			r.latest.Store(nil)
		}
	}
}

func (r *ManagedApplyRegistry) TerminalCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}
