// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"sync"
)

// RuntimeComponent identifies a resource type participating in the prepared
// runtime transaction (accepted decision D08, #90). The set is closed by
// design: adding a member is a reviewed code change — an enum value, a typed
// prepared type/setter/accessor, an explicit Prepare call site, a publication
// order entry and a retirement category — never a runtime registration.
//
// No production component exists yet. #100 (static certificate/key rotation)
// and #98 (access-log sink enablement) are the first and second consumers and
// each adds its own value when it lands; do not add a value here merely
// because a gated issue exists (egress, tracing, ACME, cache and the admin
// listener all remain restart-required until their own issue lands).
type RuntimeComponent uint8

// preparedComponent is a candidate resource built during a reload's Prepare
// phase and installed at Publish. Every fallible step — parsing, dialing,
// opening a file, validating — happens before commit is called; commit itself
// must not fail, block or perform blocking I/O beyond an atomic pointer/store
// swap, matching the no-fail Publish contract the rest of ReloadPlan already
// holds.
type preparedComponent interface {
	// component identifies this slot for duplicate detection and diagnostics.
	component() RuntimeComponent
	// commit installs the candidate resource live. It must not fail. The
	// returned retirement releases whatever resource this commit replaced, or
	// is nil when there is nothing to release (e.g. the first generation).
	commit() retirement
	// abort releases the candidate resource without installing it. Safe to
	// call after a partial Prepare and safe to call more than once.
	abort()
}

// retirement releases a resource that a commit replaced. It runs after
// Publish, on a context independent of the pre-Publish deadline, and must be
// bounded and exactly once per component. Its own failure is advisory only:
// nothing here can roll back a Publish that already happened.
type retirement func(ctx context.Context)

// PreparedRuntime is the pre-Publish preparation aggregate for typed runtime
// resources that need candidate construction ahead of Publish and bounded
// retirement of whatever they replace. It is the constrained-hybrid design
// accepted for D08: a closed, explicitly ordered set of components, not an
// open registry, a dynamic callback list or a reflection-driven framework.
//
// The zero value is ready to use and commits, aborts and retires nothing, so
// a ReloadPlan with no staged component pays no cost beyond the struct
// itself. Callers add components, in Prepare, in the exact order the
// publication rules require (see docs/architecture.md); PreparedRuntime never
// reorders or deduplicates on their behalf — it only detects a duplicate slot
// as a programming error.
type PreparedRuntime struct {
	components []preparedComponent
	once       sync.Once
	retire     []retirement
}

// add stages a candidate component in the order Prepare calls it. Adding the
// same component twice is a programming error, not a runtime condition that a
// caller can trigger through configuration, so it panics rather than silently
// keeping the first or the last registration.
func (r *PreparedRuntime) add(c preparedComponent) {
	if r == nil || c == nil {
		return
	}
	for _, existing := range r.components {
		if existing.component() == c.component() {
			panic("server: duplicate PreparedRuntime component")
		}
	}
	r.components = append(r.components, c)
}

// Commit installs every staged component, in the order they were added, and
// collects the retirement each one returns. It never fails — every fallible
// step already happened in Prepare — and it runs at most once; a redundant
// call is a no-op, matching PreparedCommit's exactly-once contract. Commit
// must be called only after validation, handler preparation, listener staging
// and every CAS check this reload's Publish depends on have already
// succeeded.
func (r *PreparedRuntime) Commit() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		for _, c := range r.components {
			if ret := c.commit(); ret != nil {
				r.retire = append(r.retire, ret)
			}
		}
	})
}

// Abort releases every staged component without installing any of them. Safe
// to call after a partial Prepare (only the components actually added are
// released) and safe to call more than once. It is a no-op once Commit has
// run.
func (r *PreparedRuntime) Abort() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		for _, c := range r.components {
			c.abort()
		}
	})
}

// Retire runs the retirements collected by Commit, on ctx. The caller (see
// ReloadPlan.RetirePreparedRuntime) bounds ctx so a component that never
// signals completion cannot hang shutdown or the next reload indefinitely. A
// retirement's own error is advisory only and is not returned: Publish
// already happened and nothing here can undo it. Retire must be called only
// after Commit has succeeded, and at most once.
func (r *PreparedRuntime) Retire(ctx context.Context) {
	if r == nil {
		return
	}
	for _, ret := range r.retire {
		ret(ctx)
	}
}
