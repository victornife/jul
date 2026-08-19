// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"sync"
	"sync/atomic"
	"time"
)

// BackendState is the single answer to "why can this backend not take traffic".
//
// There is exactly one state per backend and one mechanism behind it. Jul used
// to describe the same condition two ways — "passively down" and, in the design
// that was rejected, "circuit open" — which would have given operators two
// overlapping verdicts about one backend with no explainable interaction
// between them.
type BackendState string

const (
	// StateAvailable means the backend may be selected.
	StateAvailable BackendState = "available"
	// StateCircuitOpen means consecutive failures took it out of rotation.
	StateCircuitOpen BackendState = "circuit_open"
	// StateCircuitHalfOpen means it is being probed by a bounded number of
	// requests to find out whether it recovered.
	StateCircuitHalfOpen BackendState = "circuit_half_open"
	// StateHealthUnhealthy means the active checker took it out of rotation.
	// It outranks circuit state: an out-of-band prover of liveness that says
	// "no" is more authoritative than traffic that has not been tried yet.
	StateHealthUnhealthy BackendState = "health_unhealthy"
	// StateAtCapacity means max_active_per_backend is reached. It is the only
	// state that is about load rather than health.
	StateAtCapacity BackendState = "at_capacity"
)

// circuitPhase is the internal state; BackendState is what operators see.
type circuitPhase uint8

const (
	phaseClosed circuitPhase = iota
	phaseOpen
	phaseHalfOpen
)

// circuit is the per-backend failure state machine.
//
// It is not a second mechanism beside passive health: it *is* the mechanism
// that was already there, made explicit. `max_fails` was already the failure
// threshold and `fail_timeout` already the open duration; what was missing was
// a real HALF_OPEN state with a bounded probe allowance, so that when the
// cooldown elapsed every concurrent request saw the backend as available at
// once and a recovering backend took the full production load.
//
// Transitions are guarded by mu. They only execute when a backend is already
// failing, so there is nothing to win by making them lock-free — and two
// earlier lock-free designs for exactly this were specified and both were
// wrong. Do not reintroduce one without a benchmark showing the mutex is
// material.
type circuit struct {
	// closedEpoch is both the publication gate and the admission-generation
	// token. Zero means "take the slow path"; non-zero means "CLOSED, and this
	// is your epoch". One atomic load answers both questions, which is what
	// makes an epoch available on a path that does not take the lock. A
	// separate atomic epoch would reintroduce a multi-load race, and a
	// mu-guarded int64 could not be read here at all.
	closedEpoch atomic.Uint64

	// fails is written only under mu; the success fast path reads it without
	// the lock, which is the only reason it is atomic.
	fails atomic.Int32

	mu             sync.Mutex
	phase          circuitPhase
	epoch          uint64
	openUntil      time.Time
	halfOpenUntil  time.Time
	probesInFlight int

	maxFails    int
	failTimeout time.Duration
	maxProbes   int

	// now is the clock seam. Tests substitute it to make transitions
	// deterministic; production never sets it.
	now func() time.Time

	// onTransition is called under mu on every state change, which is the only
	// place HALF_OPEN entry is observable at all. It must not call back into
	// this circuit: it is a counter increment, nothing more.
	onTransition func(BackendState)
}

// setTransitionHook installs the hook under mu, because every reader of it
// holds mu and a plain assignment would be a data race with a backend that is
// already taking traffic.
func (c *circuit) setTransitionHook(h func(BackendState)) {
	c.mu.Lock()
	c.onTransition = h
	c.mu.Unlock()
}

// transitioned reports a state change to the hook. Called with mu held.
func (c *circuit) transitioned(to BackendState) {
	if c.onTransition != nil {
		c.onTransition(to)
	}
}

func newCircuit(maxFails int, failTimeout time.Duration, maxProbes int) *circuit {
	if maxFails < 1 {
		maxFails = 1
	}
	if failTimeout <= 0 {
		failTimeout = 10 * time.Second
	}
	c := &circuit{
		phase:       phaseClosed,
		epoch:       1,
		maxFails:    maxFails,
		failTimeout: failTimeout,
		maxProbes:   maxProbes,
		now:         time.Now,
	}
	// Constructed CLOSED at epoch 1, so zero is never a live generation and is
	// unambiguous as the gate's sentinel.
	c.closedEpoch.Store(1)
	return c
}

// setLimits retunes the thresholds without disturbing accumulated state, which
// is what makes `max_fails` and `fail_timeout` hot-reloadable rather than
// pool-identity fields.
func (c *circuit) setLimits(maxFails int, failTimeout time.Duration, maxProbes int) {
	if maxFails < 1 {
		maxFails = 1
	}
	if failTimeout <= 0 {
		failTimeout = 10 * time.Second
	}
	c.mu.Lock()
	c.maxFails, c.failTimeout, c.maxProbes = maxFails, failTimeout, maxProbes
	c.mu.Unlock()
}

// admission is what a request carries from selection to result, so its outcome
// can be attributed to the generation that admitted it.
type admission struct {
	epoch uint64
	probe bool
}

// ok reports whether this admission came from a live generation. The zero value
// is not admitted, so a result reported without one is discarded.
func (a admission) ok() bool { return a.epoch != 0 }

// eligible reports whether the backend could take a request now, without
// claiming anything.
//
// Selection needs a look before it claims: the balancer chooses among
// candidates, and claiming a half-open probe slot for every candidate would
// spend the allowance on backends that are then not chosen.
func (c *circuit) eligible() bool {
	if c.closedEpoch.Load() != 0 {
		return true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.phase {
	case phaseClosed:
		// The gate is conservative, never optimistic: it may still read zero
		// just after a close. Slower, not wrong.
		return true
	case phaseOpen:
		return !c.now().Before(c.openUntil)
	default:
		return c.maxProbes <= 0 || c.probesInFlight < c.maxProbes
	}
}

// admit claims the right to send one request, returning the generation it
// belongs to. A zero admission means the circuit refused.
func (c *circuit) admit() admission {
	if epoch := c.closedEpoch.Load(); epoch != 0 {
		return admission{epoch: epoch}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	switch c.phase {
	case phaseClosed:
		return admission{epoch: c.epoch}

	case phaseOpen:
		if now.Before(c.openUntil) {
			return admission{}
		}
		// The cooldown elapsed: this request becomes the first probe.
		c.phase = phaseHalfOpen
		c.probesInFlight = 1
		c.halfOpenUntil = now.Add(c.failTimeout)
		c.transitioned(StateCircuitHalfOpen)
		return admission{epoch: c.epoch, probe: true}

	default: // phaseHalfOpen
		if !now.Before(c.halfOpenUntil) {
			// A probe may legitimately be a multi-hour gRPC stream, so
			// HALF_OPEN is bounded too: without this, one outstanding probe
			// pins the state forever, never closing and never re-opening. It
			// also bounds the one leak this design allows — an attempt
			// abandoned between admission and result never returns its slot.
			c.openLocked(now)
			return admission{}
		}
		if c.maxProbes > 0 && c.probesInFlight >= c.maxProbes {
			return admission{}
		}
		c.probesInFlight++
		return admission{epoch: c.epoch, probe: true}
	}
}

// success records a completed request, reporting whether this call is the one
// that returned the backend to rotation.
//
// The caller cannot derive that by sampling the gate before and after: with
// several probes in flight, two of them would both observe a zero-to-non-zero
// transition and both claim the recovery. Only the goroutine that ran
// closeLocked knows, and it knows under the lock.
func (c *circuit) success(a admission) bool {
	if !a.ok() {
		return false
	}
	// Same generation, gate open, no failures on record: nothing to write. This
	// is the common case, and it costs two atomic loads and no store — where
	// the previous code performed two unconditional stores on every success.
	if !a.probe && c.closedEpoch.Load() == a.epoch && c.fails.Load() == 0 {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if a.epoch != c.epoch {
		// The circuit moved on while this request was in flight. Its result
		// describes a generation that no longer exists, so it neither clears
		// live failures nor counts toward a fresh sequence.
		if a.probe {
			c.releaseProbeLocked()
		}
		return false
	}
	if a.probe {
		c.closeLocked()
		return true
	}
	c.fails.Store(0)
	return false
}

// failure records a failed request and reports whether it took the backend out
// of rotation.
func (c *circuit) failure(a admission) bool {
	if !a.ok() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.epoch != c.epoch {
		if a.probe {
			c.releaseProbeLocked()
		}
		return false
	}
	if a.probe {
		// A failed probe re-opens immediately, ignoring max_fails: counting
		// probes toward the threshold would admit max_fails full rounds of
		// them per window, which is the load the circuit exists to prevent.
		c.openLocked(c.now())
		return true
	}
	if c.phase != phaseClosed {
		return false
	}
	// The same critical section increments and tests, so a count can never
	// authorise a transition on its own.
	if int(c.fails.Add(1)) < c.maxFails {
		return false
	}
	c.openLocked(c.now())
	return true
}

// releaseProbe returns a probe slot without deciding anything, for a request
// that was admitted but never reached the backend.
func (c *circuit) releaseProbe(a admission) {
	if !a.ok() || !a.probe {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if a.epoch == c.epoch {
		c.releaseProbeLocked()
	}
}

func (c *circuit) releaseProbeLocked() {
	if c.probesInFlight > 0 {
		c.probesInFlight--
	}
}

// openLocked takes the backend out of rotation for a fresh failTimeout, from
// any phase. The gate is closed *before* the state mutates, so a request that
// read a non-zero gate linearizes before this transition rather than after it.
//
// Bumping the epoch is what makes every request admitted under the old
// generation harmless: their results arrive against an epoch that no longer
// matches and are discarded instead of clearing failures this transition is
// based on.
func (c *circuit) openLocked(now time.Time) {
	c.closedEpoch.Store(0)
	c.phase = phaseOpen
	c.openUntil = now.Add(c.failTimeout)
	c.probesInFlight = 0
	c.fails.Store(0)
	c.epoch++
	c.transitioned(StateCircuitOpen)
}

// closeLocked establishes CLOSED and publishes the new generation *last*, so
// the gate is never non-zero for a generation that is not yet fully set up.
// The reverse interval — state CLOSED with the gate still zero — is intentional
// and merely costs those requests the slow path.
func (c *circuit) closeLocked() {
	c.phase = phaseClosed
	c.probesInFlight = 0
	c.openUntil = time.Time{}
	c.halfOpenUntil = time.Time{}
	c.fails.Store(0)
	c.epoch++
	c.closedEpoch.Store(c.epoch)
	c.transitioned(StateAvailable)
}

// forceClose closes the circuit from outside the request path. The active
// health checker uses it on a transition to healthy: an out-of-band prover of
// liveness outranks stale traffic failures.
func (c *circuit) forceClose() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase == phaseClosed && c.fails.Load() == 0 {
		return false
	}
	c.closeLocked()
	return true
}

// state reports the operator-facing verdict.
func (c *circuit) state() BackendState {
	if c.closedEpoch.Load() != 0 {
		return StateAvailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.phase {
	case phaseClosed:
		return StateAvailable
	case phaseOpen:
		if !c.now().Before(c.openUntil) {
			// The cooldown elapsed; the next request becomes a probe.
			return StateCircuitHalfOpen
		}
		return StateCircuitOpen
	default:
		return StateCircuitHalfOpen
	}
}

// Attempt is one admitted use of a backend: the backend itself, plus the
// circuit generation that authorised it.
//
// It embeds *Backend so every existing field and method access still reads the
// same, which matters because the alternative — threading a second value
// through twenty-five result call sites across eight files — is exactly the
// kind of mechanical edit that goes wrong quietly.
//
// The generation is what makes a late result harmless. An ordinary request
// admitted while CLOSED may complete after the circuit has opened, recovered
// and closed again; without a token its stale success would clear live failures
// or its stale failure would count toward a fresh sequence.
type Attempt struct {
	*Backend
	adm admission
}

// Valid reports whether this attempt refers to a backend at all.
func (a Attempt) Valid() bool { return a.Backend != nil }
