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
	// ManagedApplyFinalizing marks a transaction whose single terminal callback
	// has been claimed and whose terminal side effects (history, audit,
	// metrics, ledger) are being applied. It is an explicit intermediate claim
	// state between pending and terminal so a duplicate finalization callback
	// is rejected without losing the pending record. Like pending records,
	// finalizing records are never evicted.
	ManagedApplyFinalizing ManagedApplyState = "finalizing"
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
	ID        string            `json:"id"`
	State     ManagedApplyState `json:"state"`
	Operation ApplyOperation    `json:"operation"`
	StartedAt time.Time         `json:"started_at"`
	// Deadline is the absolute transaction deadline for deadline-aware polling.
	// It is optional: a zero value means no deadline was recorded.
	Deadline    time.Time `json:"deadline,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	Result ConfigApplyResult `json:"result"`

	HistorySnapshotID string `json:"history_snapshot_id,omitempty"`
	HistoryError      string `json:"history_error,omitempty"`
	FinalizationError string `json:"finalization_error,omitempty"`

	// OwnerTokenID is private ownership metadata used to authorize retrieval of
	// a caller's own apply result. It is deliberately excluded from JSON
	// serialization ("-") so it never leaks through the status:read result
	// endpoint.
	OwnerTokenID string `json:"-"`
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
	// ErrManagedApplyRecordIncomplete is returned by FailFinalization when the
	// emergency terminal record cannot be reconstructed into a valid, parseable
	// terminal result (no existing record and no complete original result).
	ErrManagedApplyRecordIncomplete = errors.New("managed apply: terminal record is incomplete")
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

// RetentionBounds returns the ledger's minimum guarantees: a terminal record is
// evicted only once it is both past the age bound and over the count bound.
//
// They are read from the registry rather than restated by the caller, so the
// bounds GET /api/v1/capabilities publishes cannot drift from the ones
// pruneLocked actually enforces (ADR 0019 §30).
func (r *ManagedApplyRegistry) RetentionBounds() (minTerminalRecords int, minAge time.Duration) {
	if r == nil {
		return defaultManagedApplyMaxTerminal, defaultManagedApplyTTL
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.maxTerminal, r.ttl
}

// managedApplyID is the structured decomposition of a managed apply ID. Instance
// is the 12-hex boot-scoped prefix (empty for legacy IDs); Sequence is the
// monotonic per-process counter; Legacy is true for the pre-boot-scoped
// rl_<sequence> format retained for one compatibility release.
type managedApplyID struct {
	Instance string
	Sequence uint64
	Legacy   bool
}

// parseManagedApplyID parses either the boot-scoped rl_<instance>_<sequence>
// format or the legacy rl_<sequence> format. It enforces a strict grammar
// before any lookup so an attacker cannot traverse paths or create unbounded
// map entries.
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

// parseCanonicalApplySequence parses the canonical decimal sequence portion of
// a managed apply ID. It rejects empty input, over-long input, leading zeros
// (beyond a single "0"), and non-digit characters so IDs stay canonical and
// bounded. The 19-character bound matches the repository's existing invariant
// that a 20-digit sequence is invalid.
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

// validManagedApplyID reports whether id is a well-formed managed apply ID in
// either the boot-scoped or legacy format before any lookup so an attacker
// cannot traverse paths or create unbounded map entries.
func validManagedApplyID(id string) bool {
	_, err := parseManagedApplyID(id)
	return err == nil
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
		if existing.Operation != "" &&
			rec.Operation != "" &&
			existing.Operation != rec.Operation {
			return ErrManagedApplyIDMismatch
		}

		// Never downgrade a finalizing or terminal record back to pending.
		if existing.State != ManagedApplyPending {
			return nil
		}

		// Idempotent enrichment: a repeated pending call fills in any fields
		// the first (possibly empty) call left blank rather than silently
		// retaining an empty shell.
		if existing.Operation == "" {
			existing.Operation = rec.Operation
		}
		if existing.StartedAt.IsZero() {
			existing.StartedAt = rec.StartedAt
		}
		if existing.Deadline.IsZero() {
			existing.Deadline = rec.Deadline
		}
		if existing.OwnerTokenID == "" {
			existing.OwnerTokenID = rec.OwnerTokenID
		}
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
	// Pending records are not placed in the terminal eviction order; they are
	// promoted into it on Complete.
	return nil
}

// ClaimFinalization claims the single terminal finalization for a record and
// transitions it to the explicit finalizing state (WS2 finalization claim). It
// returns (true, nil) for the first caller, (false, nil) for a record already
// finalizing or terminal, and a typed error for an invalid ID or an operation
// mismatch. Unlike BeginFinalization it operates on the same record object so
// the pending record is enriched rather than shadowed by a separate claim set.
func (r *ManagedApplyRegistry) ClaimFinalization(rec ManagedApplyRecord) (bool, error) {
	if !validManagedApplyID(rec.ID) {
		return false, ErrManagedApplyInvalidID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.byID[rec.ID]
	if ok {
		if existing.Operation != "" &&
			rec.Operation != "" &&
			existing.Operation != rec.Operation {
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
		if existing.OwnerTokenID == "" {
			existing.OwnerTokenID = rec.OwnerTokenID
		}
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
	if ok {
		// Complete accepts a pending or finalizing record and transitions it to
		// terminal in place, preserving private/lifecycle metadata the terminal
		// caller may not carry (ownership, start/deadline).
		if rec.StartedAt.IsZero() {
			rec.StartedAt = existing.StartedAt
		}
		if rec.Deadline.IsZero() {
			rec.Deadline = existing.Deadline
		}
		if rec.OwnerTokenID == "" {
			rec.OwnerTokenID = existing.OwnerTokenID
		}
	}
	cp := rec
	r.byID[rec.ID] = &cp

	// Append to terminal order only once per ID.
	r.addTerminalOrderLocked(rec.ID)

	r.pruneLocked(time.Now().UTC())
	return nil
}

// FailFinalization transitions a transaction to terminal after its finalization
// callback failed (e.g. panicked). Unlike Complete it preserves the established
// transaction identity and complete result IN PLACE instead of overwriting the
// record with the incomplete emergency fallback carried by the panic path, and
// it refuses to publish a record that cannot be reconstructed into a valid
// terminal result. The resulting record must stay parseable by
// ManagedApplyRecordSchema; it may change only state, completion time, and the
// finalization error.
func (r *ManagedApplyRegistry) FailFinalization(rec ManagedApplyRecord) error {
	if !validManagedApplyID(rec.ID) {
		return ErrManagedApplyInvalidID
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, exists := r.byID[rec.ID]; exists {
		if existing.Operation != "" &&
			rec.Operation != "" &&
			existing.Operation != rec.Operation {
			return ErrManagedApplyIDMismatch
		}

		// Preserve established transaction identity and metadata.
		if rec.Operation == "" {
			rec.Operation = existing.Operation
		}
		if rec.StartedAt.IsZero() {
			rec.StartedAt = existing.StartedAt
		}
		if rec.Deadline.IsZero() {
			rec.Deadline = existing.Deadline
		}
		if rec.OwnerTokenID == "" {
			rec.OwnerTokenID = existing.OwnerTokenID
		}

		// Never replace a valid result with an empty fallback result.
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

	// A missing existing record is allowed only when the caller supplied a
	// complete original terminal result: a bare ID/error fallback cannot be
	// reconstructed into a parseable terminal record.
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

// addTerminalOrderLocked appends id to the terminal eviction order exactly once.
// The caller must hold r.mu.
func (r *ManagedApplyRegistry) addTerminalOrderLocked(id string) {
	for _, existingID := range r.order {
		if existingID == id {
			return
		}
	}
	r.order = append(r.order, id)
}

// joinManagedApplyErrors concatenates two finalization-error strings, dropping
// empties, so a prior finalization error is preserved alongside a new one.
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
