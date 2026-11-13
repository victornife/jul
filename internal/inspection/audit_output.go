// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import "math"

// AuditOutputMaxEntries bounds the audit writer calls a process can have in
// flight or abandoned at once (ADR 0032 amendment, "Audit writers"). Each
// entry is at most one goroutine blocked in writer I/O.
const AuditOutputMaxEntries = 64

// AuditDropAbandonedFull is the drop reason of a record that was not handed
// to its writer because the bounded audit-output set was full.
const AuditDropAbandonedFull = "abandoned_full"

// AuditDropBudget is the drop reason of a record whose R5 construction or
// formatting charge was refused in enforce mode. Phase-5 rules still ran.
const AuditDropBudget = "budget"

// auditOutputMinBytes is the floor of the audit-output byte bound.
const auditOutputMinBytes = 1 << 20

// AuditOutputSnapshot reports the bounded audit-output set.
type AuditOutputSnapshot struct {
	// InFlight counts writer calls running within their deadline.
	InFlight uint64
	// Abandoned counts writer calls past their deadline that have not
	// returned; AbandonedBytes is their detached record bytes, which stay
	// charged under the audit class until each call returns.
	Abandoned      uint64
	AbandonedBytes uint64
	// MaxEntries and MaxBytes are the set's fixed bounds.
	MaxEntries uint64
	MaxBytes   uint64
	// Drops counts records not handed to the writer: the set was full, or
	// the record's R5 charge was refused.
	Drops uint64
}

type auditOutput struct {
	inFlight       uint64
	inFlightBytes  uint64
	abandoned      uint64
	abandonedBytes uint64
	drops          uint64
}

// auditOutputByteBoundLocked is the set's byte bound: a quarter of the
// published ceiling, never below 1 MiB.
func (g *Governor) auditOutputByteBoundLocked() uint64 {
	bound := g.limits.MaxBytes / 4
	if bound < auditOutputMinBytes {
		bound = auditOutputMinBytes
	}
	return bound
}

func (g *Governor) auditOutputFullLocked() bool {
	a := &g.audit
	if a.inFlight+a.abandoned >= AuditOutputMaxEntries {
		return true
	}
	held := a.inFlightBytes + a.abandonedBytes
	return held > 0 && held >= g.auditOutputByteBoundLocked()
}

func (g *Governor) auditSnapshotLocked() AuditOutputSnapshot {
	a := &g.audit
	return AuditOutputSnapshot{
		MaxBytes:       g.auditOutputByteBoundLocked(),
		InFlight:       a.inFlight,
		Abandoned:      a.abandoned,
		AbandonedBytes: a.abandonedBytes,
		MaxEntries:     AuditOutputMaxEntries,
		Drops:          a.drops,
	}
}

// AdmitAuditOutput is the R5 admission check made before a WAF transaction
// whose audit records can reach a writer is created. When the audit-output
// set is full, enforce refuses with AuditSaturated; observe records the
// would-refuse decision and admits.
func (g *Governor) AdmitAuditOutput() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	if !g.auditOutputFullLocked() {
		g.mu.Unlock()
		return nil
	}
	g.noteRejectionLocked(AuditSaturated)
	hook := g.hooks.OnRejection
	mode := g.limits.Mode
	g.mu.Unlock()
	if hook != nil {
		hook(AuditSaturated, mode)
	}
	if mode == Enforce {
		return &AdmissionError{Outcome: AuditSaturated}
	}
	return nil
}

// AuditWrite is one writer call registered in the audit-output set.
type AuditWrite struct {
	g         *Governor
	bytes     uint64
	abandoned bool
	finished  bool
}

// BeginAuditWrite registers a writer call for a detached record of n bytes.
// It reports false, counting a drop, when the set is full; the caller must
// then not call the writer. One record larger than the byte bound is accepted
// only into an empty set, so the bytes held never exceed the bound plus one
// record.
func (g *Governor) BeginAuditWrite(n uint64) (*AuditWrite, bool) {
	if g == nil {
		return nil, false
	}
	g.mu.Lock()
	a := &g.audit
	held := a.inFlightBytes + a.abandonedBytes
	full := a.inFlight+a.abandoned >= AuditOutputMaxEntries ||
		(held > 0 && (held >= g.auditOutputByteBoundLocked() || n > g.auditOutputByteBoundLocked()-held)) ||
		held > math.MaxUint64-n
	if full {
		if a.drops < math.MaxUint64 {
			a.drops++
		}
		hook := g.hooks.OnAuditDrop
		g.mu.Unlock()
		if hook != nil {
			hook(AuditDropAbandonedFull)
		}
		return nil, false
	}
	a.inFlight++
	a.inFlightBytes += n
	g.mu.Unlock()
	return &AuditWrite{g: g, bytes: n}, true
}

// Abandon records that the writer call passed its deadline. Its detached
// record stays charged under the audit class, outside any request lease,
// until Finish. Abandonment never refuses: the bytes were already reserved
// by the request's R5 and are existing debt.
func (w *AuditWrite) Abandon() {
	if w == nil || w.g == nil {
		return
	}
	g := w.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.abandoned || w.finished {
		return
	}
	w.abandoned = true
	a := &g.audit
	a.inFlight--
	a.inFlightBytes -= w.bytes
	a.abandoned++
	a.abandonedBytes += w.bytes
	if !addTo(&g.usage[AuditBytes], w.bytes) {
		g.usage[AuditBytes] = math.MaxUint64
	}
}

// Finish records that the writer call returned and refunds an abandoned
// call's charge.
func (w *AuditWrite) Finish() {
	if w == nil || w.g == nil {
		return
	}
	g := w.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if w.finished {
		return
	}
	w.finished = true
	a := &g.audit
	if !w.abandoned {
		a.inFlight--
		a.inFlightBytes -= w.bytes
		return
	}
	a.abandoned--
	a.abandonedBytes -= w.bytes
	subtractFrom(&g.usage[AuditBytes], w.bytes)
}

// NoteAuditDrop counts an audit record that is not built or not handed to
// its writer for reason (AuditDropBudget).
func (g *Governor) NoteAuditDrop(reason string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.audit.drops < math.MaxUint64 {
		g.audit.drops++
	}
	hook := g.hooks.OnAuditDrop
	g.mu.Unlock()
	if hook != nil {
		hook(reason)
	}
}
