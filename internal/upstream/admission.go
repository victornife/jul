// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"container/list"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/resilience"
)

// Admission rejections. They are distinct sentinels because the operator-facing
// reason and the client-facing status are different resolutions of the same
// event (ADR 0017): all three are 503 to a client, and none of them is ever
// conflated with another in a log, a metric or the runtime API.
var (
	// ErrOverloaded means the admission limit was reached and the request could
	// not be queued: either there is no queue, or the queue was full, or the
	// request waited past pending_timeout.
	ErrOverloaded = errors.New("upstream admission: proxy overloaded")

	// ErrRetired means the handler generation that parked this request was
	// forcibly retired. Admitting the request now would run it against a
	// transport that is already closed.
	ErrRetired = errors.New("upstream admission: handler generation retired")
)

// Admission bounds the logical work in flight for one admission owner — a pool,
// or a location for a literal FastCGI/uWSGI target.
//
// It is deliberately standalone: it has no *Pool dependency, so internal/stream,
// internal/handler and later internal/auth all reach it without importing a
// sibling. That is an import-graph commitment, not a stylistic preference.
//
// The shape is counters plus limits, never a rebuilt primitive:
//
//   - the fast path is a CAS loop on active while active < limit, and takes no
//     lock at all below the limit;
//   - the slow path parks the caller's own goroutine on a single-slot channel,
//     so no goroutine is ever created;
//   - release hands the slot directly to the FIFO head instead of decrementing,
//     which gives strict FIFO, prevents barging, and makes recovery after a
//     limit decrease monotonic.
//
// A counting channel cannot be resized on reload, x/sync/semaphore cannot bound
// its waiter list, and sync.Cond cannot honour request cancellation, so none of
// the three can implement this contract.
type Admission struct {
	// policy is the resolved resilience policy. It is swapped atomically on
	// reload and is the single source of truth for this owner's limits: nothing
	// derived from it is cached anywhere else.
	policy atomic.Pointer[resilience.Policy]

	// active counts admitted logical requests, streams and connections. It may
	// legitimately exceed the current limit after a limit decrease; the excess
	// is non-increasing because release only hands off while active-1 < limit.
	active atomic.Int64

	// pending counts parked requests. It mirrors waiters.Len() and exists so an
	// observer never has to take mu.
	pending atomic.Int64

	// mu guards the waiter FIFO only, never the fast path.
	mu sync.Mutex
	// waiters is the pending FIFO. It is a linked list rather than a slice
	// because a cancelled waiter is removed from the middle, and
	// max_pending_requests allows 100000 of them: an O(n) removal under mu would
	// turn mass client cancellation into a self-inflicted stall. Order is still
	// strict FIFO, and each entry still holds one single-slot channel.
	waiters list.List
}

// waiter is one parked request. state is guarded by Admission.mu; a woken
// waiter may read it without the lock because the channel send that woke it
// happens after the state write.
type waiter struct {
	ch    chan struct{} // capacity 1, so a grant never blocks the releaser
	state waiterState
	elem  *list.Element
}

type waiterState uint8

const (
	waiterParked waiterState = iota
	waiterGranted
	waiterRetired
)

// NewAdmission returns an admission owner governed by policy. A nil policy is
// the unlimited default.
func NewAdmission(policy *resilience.Policy) *Admission {
	a := &Admission{}
	a.waiters.Init()
	a.SetPolicy(policy)
	return a
}

// SetPolicy swaps the resolved policy. It is the whole of a resilience reload:
// counters, the waiter FIFO and every backend's state are untouched, which is
// what makes a limit change preserve in-flight accounting instead of rebuilding
// the pool.
//
// Raising the limit wakes as many parked waiters as the new limit allows, so an
// increase takes effect immediately rather than on the next release.
func (a *Admission) SetPolicy(p *resilience.Policy) {
	if p == nil {
		p = resilience.Default
	}
	a.policy.Store(p)
	a.wakeUpToLimit(p.MaxActiveRequests())
}

// Policy returns the current resolved policy. It never returns nil.
func (a *Admission) Policy() *resilience.Policy { return a.policy.Load() }

// Active returns the number of admitted logical requests.
func (a *Admission) Active() int64 { return a.active.Load() }

// Pending returns the number of parked requests.
func (a *Admission) Pending() int64 { return a.pending.Load() }

// Admit acquires one admission slot, returning a release closure that is safe
// to call exactly once from any path — success, transport failure, cancellation
// or panic recovery.
//
// retire is the caller's generation-retirement signal. It is closed when the
// handler generation that owns the transport is torn down, so a parked request
// is rejected rather than admitted onto a closed transport. A nil channel blocks
// forever in select, which is exactly the right no-op for callers that have no
// generation, and the fast path never touches it.
func (a *Admission) Admit(ctx context.Context, retire <-chan struct{}) (func(), error) {
	limit := a.policy.Load().MaxActiveRequests()
	if limit <= 0 {
		a.active.Add(1)
		return a.releaser(), nil
	}
	for {
		cur := a.active.Load()
		if cur >= limit {
			return a.park(ctx, retire)
		}
		if a.active.CompareAndSwap(cur, cur+1) {
			return a.releaser(), nil
		}
	}
}

// park queues the caller behind the limit, or rejects it when there is no room.
func (a *Admission) park(ctx context.Context, retire <-chan struct{}) (func(), error) {
	pol := a.policy.Load()

	a.mu.Lock()
	// Re-check under mu: a release may have freed a slot between the failed CAS
	// and this lock. Taking it here rather than looping keeps a late arrival from
	// barging ahead of a waiter that is already queued, which is why the retry is
	// guarded on an empty FIFO. The slot is still claimed with CAS: mu guards the
	// FIFO only and does not exclude the lock-free fast path.
	limit := pol.MaxActiveRequests()
	if a.waiters.Len() == 0 {
		for {
			cur := a.active.Load()
			if limit > 0 && cur >= limit {
				break
			}
			if a.active.CompareAndSwap(cur, cur+1) {
				a.mu.Unlock()
				return a.releaser(), nil
			}
		}
	}
	maxPending := pol.MaxPendingRequests()
	if maxPending <= 0 || a.waiters.Len() >= maxPending {
		a.mu.Unlock()
		return nil, ErrOverloaded
	}
	w := &waiter{ch: make(chan struct{}, 1)}
	w.elem = a.waiters.PushBack(w)
	a.pending.Add(1)
	a.mu.Unlock()

	// At most one timer per queued request, so live timers are bounded by
	// max_pending_requests. A timing wheel would add machinery to bound
	// something that is already bounded.
	var timeout <-chan time.Time
	if d := pol.PendingTimeout(); d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}

	select {
	case <-w.ch:
		if w.state == waiterGranted {
			return a.releaser(), nil
		}
		return nil, ErrRetired
	case <-ctx.Done():
		return nil, a.abandon(w, ctx.Err())
	case <-timeout:
		return nil, a.abandon(w, ErrOverloaded)
	case <-retire:
		return nil, a.abandon(w, ErrRetired)
	}
}

// abandon removes a waiter that gave up. When the grant and the abandonment
// race, the grant wins the slot and abandon releases it rather than dropping
// it: a leaked grant would permanently shrink the pool's effective limit.
func (a *Admission) abandon(w *waiter, err error) error {
	a.mu.Lock()
	if w.state != waiterParked {
		a.mu.Unlock()
		// The slot was already handed to us and charged to active. Give it back
		// through the normal path so the next waiter gets it.
		a.release()
		return err
	}
	a.waiters.Remove(w.elem)
	a.pending.Add(-1)
	a.mu.Unlock()
	return err
}

// releaser returns a release closure bound to this admission, guarded so a
// double call from two teardown paths cannot double-decrement.
func (a *Admission) releaser() func() {
	var once sync.Once
	return func() { once.Do(a.release) }
}

// release returns one slot, preferring a direct handoff to the FIFO head over a
// decrement. Handing off without decrementing is what makes the queue strictly
// FIFO and the drain after a limit decrease monotonic: while active-1 is still
// at or above the limit no handoff happens, so the excess only ever shrinks.
func (a *Admission) release() {
	a.mu.Lock()
	if a.waiters.Len() > 0 {
		limit := a.policy.Load().MaxActiveRequests()
		if limit <= 0 || a.active.Load()-1 < limit {
			a.grantLocked()
			a.mu.Unlock()
			return
		}
	}
	a.mu.Unlock()
	a.active.Add(-1)
}

// grantLocked hands the caller's slot to the FIFO head. active is deliberately
// not touched: the slot moves from one holder to the next.
func (a *Admission) grantLocked() {
	e := a.waiters.Front()
	w := e.Value.(*waiter)
	a.waiters.Remove(e)
	a.pending.Add(-1)
	w.state = waiterGranted
	w.ch <- struct{}{}
}

// wakeUpToLimit admits parked waiters while the new limit allows it. It runs on
// a policy swap so raising a limit takes effect at once.
func (a *Admission) wakeUpToLimit(limit int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.waiters.Len() > 0 {
		if !a.claim(limit) {
			return
		}
		a.grantLocked()
	}
}

// claim takes one slot for a waiter that is about to be granted. It reports
// false when the limit is reached. CAS rather than Add because the lock-free
// fast path runs without mu.
func (a *Admission) claim(limit int64) bool {
	for {
		cur := a.active.Load()
		if limit > 0 && cur >= limit {
			return false
		}
		if a.active.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Retire wakes every parked waiter with ErrRetired. It is the pool-close path;
// per-generation teardown uses the retire channel passed to Admit, which rejects
// only the waiters that belong to the generation being torn down.
func (a *Admission) Retire() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for a.waiters.Len() > 0 {
		e := a.waiters.Front()
		w := e.Value.(*waiter)
		a.waiters.Remove(e)
		a.pending.Add(-1)
		w.state = waiterRetired
		w.ch <- struct{}{}
	}
}
