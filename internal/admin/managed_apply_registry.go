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
	// ManagedApplyPending marks a transaction that has been accepted but has
	// not yet reached a terminal state. Pending records are never evicted.
	ManagedApplyPending ManagedApplyState = "pending"
	// ManagedApplyTerminal marks a transaction that has reached exactly one
	// terminal result. Terminal records are retained subject to the ledger's
	// capacity and TTL bounds.
	ManagedApplyTerminal ManagedApplyState = "terminal"
)

// ManagedApplyRecord is one entry in the terminal-result ledger. It is the
// single object a browser can retrieve by exact apply ID to observe the exact
// terminal result of a recent accepted apply regardless of later transactions
// (AC-02). It deliberately carries no raw TOML, bearer credentials, token
// digests, secret-expanded configuration, or public source IP: the struct is
// serialized directly to the status:read result endpoint.
type ManagedApplyRecord struct {
	ID          string            `json:"id"`
	State       ManagedApplyState `json:"state"`
	Operation   ApplyOperation    `json:"operation"`
	StartedAt   time.Time         `json:"started_at"`
	CompletedAt time.Time         `json:"completed_at,omitempty"`

	Result ConfigApplyResult `json:"result"`

	HistorySnapshotID string `json:"history_snapshot_id,omitempty"`
	HistoryError      string `json:"history_error,omitempty"`
	FinalizationError string `json:"finalization_error,omitempty"`
}

// Ledger errors surfaced to callers.
var (
	// ErrManagedApplyIDMismatch is returned when BeginPending is called twice
	// for the same ID with a different operation, indicating a programming
	// error rather than a benign idempotent retry.
	ErrManagedApplyIDMismatch = errors.New("managed apply: pending id reused with different operation")
	// ErrManagedApplyInvalidID is returned when a lookup ID does not match the
	// strict rl_N format.
	ErrManagedApplyInvalidID = errors.New("managed apply: invalid id")
)

// defaultManagedApplyMaxTerminal and defaultManagedApplyTTL implement the
// conservative default retention chosen in the ADR: terminal results are kept
// for at least 512 entries or one hour, whichever retains more recent results.
const (
	defaultManagedApplyMaxTerminal = 512
	defaultManagedApplyTTL         = time.Hour
)

// ManagedApplyRegistry is a bounded, concurrency-safe ledger of managed apply
// transactions. A browser can retrieve the exact terminal result of any recent
// accepted apply by ID even after newer transactions have completed. Pending
// transactions are never evicted; terminal transactions are retained for at
// least maxTerminal entries or ttl, whichever retains more useful results.
type ManagedApplyRegistry struct {
	mu sync.RWMutex

	maxTerminal int
	ttl         time.Duration

	byID  map[string]*ManagedApplyRecord
	order []string // insertion order of terminal IDs, oldest first

	latest atomic.Pointer[ManagedApplyRecord]

	// finalized guards BeginFinalization so a duplicate terminal callback for
	// the same ID returns false exactly once the first has claimed it.
	finalized map[string]struct{}
}

// NewManagedApplyRegistry constructs a ledger with the given bounds. Non-positive
// values fall back to the conservative defaults.
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

// validManagedApplyID enforces the strict rl_N format before any lookup so an
// attacker cannot traverse paths or create unbounded map entries.
func validManagedApplyID(id string) bool {
	const prefix = "rl_"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	n := id[len(prefix):]
	if n == "" || len(n) > 19 { // bound the numeric portion
		return false
	}
	for i := 0; i < len(n); i++ {
		if n[i] < '0' || n[i] > '9' {
			return false
		}
	}
	// Reject leading zeros beyond a single "0" to keep IDs canonical.
	if len(n) > 1 && n[0] == '0' {
		return false
	}
	if _, err := strconv.ParseUint(n, 10, 64); err != nil {
		return false
	}
	return true
}

// BeginPending records a new pending transaction. It is idempotent for the same
// ID and operation: a repeated call for an already-tracked ID is a no-op. It
// returns ErrManagedApplyIDMismatch if the same ID is reused with a different
// operation, and ErrManagedApplyInvalidID for a malformed ID.
func (r *ManagedApplyRegistry) BeginPending(rec ManagedApplyRecord) error {
	if !validManagedApplyID(rec.ID) {
		return ErrManagedApplyInvalidID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.byID[rec.ID]; ok {
		if existing.Operation != rec.Operation {
			return ErrManagedApplyIDMismatch
		}
		return nil // idempotent
	}
	rec.State = ManagedApplyPending
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now().UTC()
	}
	cp := rec
	r.byID[rec.ID] = &cp
	// Pending records are not placed in the terminal eviction order; they are
	// promoted into it on Complete.
	return nil
}

// BeginFinalization claims the single terminal callback for an ID. It returns
// true for the first caller and false for any duplicate, so terminal side
// effects (history, audit, metrics, ledger) run exactly once.
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

// Complete records the terminal result for a transaction and promotes it into
// the bounded terminal order. A pending record is transitioned to terminal in
// place; an unknown ID is inserted. Complete prunes expired/overflow terminal
// records but never evicts pending ones.
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
	if ok && rec.StartedAt.IsZero() {
		rec.StartedAt = existing.StartedAt
	}
	cp := rec
	r.byID[rec.ID] = &cp

	// Append to terminal order only once per ID.
	alreadyOrdered := false
	for _, id := range r.order {
		if id == rec.ID {
			alreadyOrdered = true
			break
		}
	}
	if !alreadyOrdered {
		r.order = append(r.order, rec.ID)
	}

	r.pruneLocked(time.Now().UTC())
	return nil
}

// SetLatest updates the convenience "latest" pointer to the record for id. It
// is only a navigation aid: Get remains the authoritative per-ID lookup.
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

// Get returns a copy of the record for id, if tracked.
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

// Latest returns a copy of the most recently published terminal record, or nil.
func (r *ManagedApplyRegistry) Latest() *ManagedApplyRecord {
	rec := r.latest.Load()
	if rec == nil {
		return nil
	}
	cp := *rec
	return &cp
}

// Prune evicts expired and overflow terminal records. Pending records are never
// evicted.
func (r *ManagedApplyRegistry) Prune(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
}

// pruneLocked enforces retention: a terminal record is evicted only when both
// it is older than ttl AND the terminal count exceeds maxTerminal. This keeps
// at least maxTerminal entries or one ttl window, whichever retains more.
func (r *ManagedApplyRegistry) pruneLocked(now time.Time) {
	// Evict from the oldest terminal records first, but only while over the
	// capacity bound; within capacity, records are retained regardless of age.
	for len(r.order) > r.maxTerminal {
		oldestID := r.order[0]
		rec, ok := r.byID[oldestID]
		if !ok {
			r.order = r.order[1:]
			continue
		}
		// Only evict when also past the TTL; otherwise stop (order is oldest
		// first, so nothing later is older).
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

// TerminalCount returns the number of terminal records currently retained. Used
// by metrics and tests.
func (r *ManagedApplyRegistry) TerminalCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.order)
}
