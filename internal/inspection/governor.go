// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"jul/internal/config"
)

type Mode string

const (
	Enforce Mode = "enforce"
	Observe Mode = "observe"
)

type ByteClass uint8

const (
	BaseBytes ByteClass = iota
	BufferBytes
	WAFStateBytes
	// AuditBytes is R5: phase-5 rule work, audit-record construction and
	// output, reserved before the WAF transaction exists (ADR 0032
	// amendment, "R5").
	AuditBytes
	byteClassCount
)

func (c ByteClass) String() string {
	switch c {
	case BaseBytes:
		return "base"
	case BufferBytes:
		return "buffer"
	case WAFStateBytes:
		return "waf_state"
	case AuditBytes:
		return "audit"
	default:
		return "unknown"
	}
}

type Outcome string

const (
	Acquired            Outcome = "acquired"
	WouldReject         Outcome = "would_reject"
	Unslotted           Outcome = "unslotted"
	ByteSaturated       Outcome = "bytes"
	WAFStateSaturated   Outcome = "waf_state"
	EvaluationSaturated Outcome = "evaluation"
	AuditSaturated      Outcome = "audit"
	QueueExhausted      Outcome = "queue_exhausted"
	WaitExpired         Outcome = "wait_expired"
	Cancelled           Outcome = "cancelled"
	LeaseClosed         Outcome = "lease_closed"
	InvalidAccounting   Outcome = "invalid_accounting"
)

var (
	ErrInvalidLimits = errors.New("inspection: invalid limits")
	ErrForeignLease  = errors.New("inspection: foreign lease in context")
)

// Limits is an immutable candidate for the process-wide governor. Configured
// slots preserve the operator's 0=auto choice; EvaluationSlots is the resolved
// process-wide value used by admission.
type Limits struct {
	Mode                      Mode
	MaxBytes                  uint64
	MaxResponseHeaderBytes    uint64
	ConfiguredEvaluationSlots int
	EvaluationSlots           int
	EvaluationQueueTimeout    time.Duration
	BodyTimeout               time.Duration
	BaseChargeBytes           uint64
	// MinimumBytes is the effective-route worst-case single-request charge
	// of the generation these limits are published with (ADR 0032 §7). Zero
	// means no inspected route; enforce mode rejects MaxBytes below it.
	MinimumBytes uint64
	// MinimumRoute names the route that sets MinimumBytes, for errors.
	MinimumRoute string
}

// Validate reports whether the limits can be published.
func (l Limits) Validate() error { return l.validate() }

func (l Limits) validate() error {
	if l.Mode != Enforce && l.Mode != Observe {
		return fmt.Errorf("%w: mode must be enforce or observe", ErrInvalidLimits)
	}
	if l.MaxBytes == 0 {
		return fmt.Errorf("%w: max bytes must be positive", ErrInvalidLimits)
	}
	if l.MaxResponseHeaderBytes == 0 {
		return fmt.Errorf("%w: max response header bytes must be positive", ErrInvalidLimits)
	}
	if l.ConfiguredEvaluationSlots < 0 || l.ConfiguredEvaluationSlots > 1024 {
		return fmt.Errorf("%w: configured evaluation slots must be between 0 and 1024", ErrInvalidLimits)
	}
	if l.EvaluationSlots < 1 {
		return fmt.Errorf("%w: effective evaluation slots must be positive", ErrInvalidLimits)
	}
	if l.EvaluationQueueTimeout < 0 || l.EvaluationQueueTimeout > 10*time.Second {
		return fmt.Errorf("%w: queue timeout must be between 0 and 10s", ErrInvalidLimits)
	}
	if l.BodyTimeout < 0 {
		return fmt.Errorf("%w: body timeout must not be negative", ErrInvalidLimits)
	}
	if l.BaseChargeBytes == 0 {
		return fmt.Errorf("%w: base charge must be positive", ErrInvalidLimits)
	}
	if l.MaxResponseHeaderBytes > math.MaxUint64/uint64(config.InspectionResponseHeaderEstimateFactor) {
		return fmt.Errorf("%w: global.inspection.max_response_header_bytes overflows the R3 estimate", ErrInvalidLimits)
	}
	if l.Mode == Enforce && l.MinimumBytes > l.MaxBytes {
		return fmt.Errorf("%w: global.inspection.max_buffered_bytes is %d B; enforce mode requires at least %d B for route %s", ErrInvalidLimits, l.MaxBytes, l.MinimumBytes, l.MinimumRoute)
	}
	return nil
}

type Hooks struct {
	OnRejection    func(reason Outcome, mode Mode)
	OnQueueAbandon func()
	OnUnslotted    func()
	// OnBodyTimeout receives inspection body-arrival results (ADR 0032 H6):
	// protocol h1, h2 or h3; result timeout, would_timeout or unsupported.
	OnBodyTimeout func(protocol, result string)
	// OnAuditDrop receives an audit record that was not handed to its
	// writer: AuditDropAbandonedFull when the bounded audit-output set was
	// full, AuditDropBudget when its R5 record charge was refused.
	OnAuditDrop func(reason string)
}

type Governor struct {
	mu      sync.Mutex
	limits  Limits
	hooks   Hooks
	usage   [byteClassCount]uint64
	leases  uint64
	active  uint64
	waiters []*evaluationWaiter

	rejections [rejectionReasons][2]uint64
	abandoned  uint64
	unslotted  uint64
	// committedOverruns counts committed charges that exceeded the ceiling.
	committedOverruns uint64

	audit auditOutput
}

func New(limits Limits, hooks Hooks) (*Governor, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &Governor{limits: limits, hooks: hooks}, nil
}

// PublishLimits changes the coherent policy snapshot without resetting usage,
// leases, reservations, waiters, or counters. Held work drains naturally after
// a limit decrease; no new work is granted until it fits the published limit.
func (g *Governor) PublishLimits(limits Limits) error {
	if err := limits.validate(); err != nil {
		return err
	}
	g.mu.Lock()
	if g.limits == limits {
		g.mu.Unlock()
		return nil
	}
	g.limits = limits
	g.grantWaitersLocked()
	g.mu.Unlock()
	return nil
}

// Limits returns the published limits that govern new decisions.
func (g *Governor) Limits() Limits {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limits
}

// RecordBodyTimeout reports one body-arrival result to the OnBodyTimeout hook.
// The caller records each result at most once per request.
func (g *Governor) RecordBodyTimeout(protocol, result string) {
	if g != nil && g.hooks.OnBodyTimeout != nil {
		g.hooks.OnBodyTimeout(protocol, result)
	}
}

type Snapshot struct {
	Limits               Limits
	Usage                [byteClassCount]uint64
	ActiveLeases         uint64
	ActiveEvaluations    uint64
	WaitingEvaluations   uint64
	Draining             bool
	AbandonedEvaluations uint64
	UnslottedLogging     uint64
	Rejections           [rejectionReasons][2]uint64
	// AuditOutput is the bounded audit-output set: writes in flight, writes
	// abandoned at their deadline and still charged, and dropped records.
	AuditOutput AuditOutputSnapshot
	// CommittedOverruns counts non-refusable charges (ChargeCommitted) that
	// exceeded the ceiling.
	CommittedOverruns uint64
}

func (g *Governor) Snapshot() Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return Snapshot{
		Limits:               g.limits,
		Usage:                g.usage,
		ActiveLeases:         g.leases,
		ActiveEvaluations:    g.active,
		WaitingEvaluations:   uint64(len(g.waiters)),
		Draining:             g.overLimitLocked(),
		AbandonedEvaluations: g.abandoned,
		UnslottedLogging:     g.unslotted,
		Rejections:           g.rejections,
		AuditOutput:          g.auditSnapshotLocked(),
		CommittedOverruns:    g.committedOverruns,
	}
}

func (g *Governor) overLimitLocked() bool {
	total, ok := sumBytes(g.usage[:])
	return !ok || total > g.limits.MaxBytes || g.active > uint64(g.limits.EvaluationSlots)
}

type contextKey struct{}

// AcquireLease creates the outer request lease, or returns the current
// governor's lease from ctx for nested inspection layers. The caller that gets
// owner=true must Close the lease; nested callers must not.
func (g *Governor) AcquireLease(ctx context.Context) (*Lease, context.Context, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if existing, ok := ctx.Value(contextKey{}).(*Lease); ok && existing != nil {
		if existing.g != g {
			return nil, ctx, false, fmt.Errorf("%w: context contains a lease from another governor", ErrForeignLease)
		}
		g.mu.Lock()
		closed := existing.closed
		g.mu.Unlock()
		if closed {
			return nil, ctx, false, &AdmissionError{Outcome: LeaseClosed}
		}
		return existing, ctx, false, nil
	}

	g.mu.Lock()
	if g.leases == math.MaxUint64 {
		g.mu.Unlock()
		return nil, ctx, false, &AdmissionError{Outcome: InvalidAccounting}
	}
	wouldExceed := !g.canChargeLocked(g.limits.BaseChargeBytes)
	if g.limits.Mode == Enforce && wouldExceed {
		g.noteRejectionLocked(ByteSaturated)
		hook := g.hooks.OnRejection
		mode := g.limits.Mode
		g.mu.Unlock()
		if hook != nil {
			hook(ByteSaturated, mode)
		}
		return nil, ctx, false, &AdmissionError{Outcome: ByteSaturated}
	}
	if !addTo(&g.usage[BaseBytes], g.limits.BaseChargeBytes) {
		g.mu.Unlock()
		return nil, ctx, false, &AdmissionError{Outcome: InvalidAccounting}
	}
	g.leases++
	lease := &Lease{g: g}
	lease.attachLocked(&Charge{lease: lease, class: BaseBytes, amount: g.limits.BaseChargeBytes})
	if wouldExceed {
		g.noteRejectionLocked(ByteSaturated)
		hook := g.hooks.OnRejection
		mode := g.limits.Mode
		g.mu.Unlock()
		if hook != nil {
			hook(ByteSaturated, mode)
		}
		return lease, context.WithValue(ctx, contextKey{}, lease), true, nil
	}
	g.mu.Unlock()

	return lease, context.WithValue(ctx, contextKey{}, lease), true, nil
}

// LeaseFrom returns the request lease carried by ctx, or nil. Nested layers
// use it to charge the outer layer's lease; only that layer closes it.
func LeaseFrom(ctx context.Context) *Lease {
	if ctx == nil {
		return nil
	}
	lease, _ := ctx.Value(contextKey{}).(*Lease)
	return lease
}

type Lease struct {
	g               *Governor
	closed          bool
	chargesReleased bool
	evaluations     uint64
	// charges holds only live (unreleased) reservations so retained
	// bookkeeping scales with live charges, not with charge history.
	charges []*Charge
}

func (l *Lease) attachLocked(c *Charge) {
	c.index = len(l.charges)
	l.charges = append(l.charges, c)
}

func (l *Lease) detachLocked(c *Charge) {
	i := c.index
	if i < 0 || i >= len(l.charges) || l.charges[i] != c {
		return
	}
	last := len(l.charges) - 1
	if i != last {
		l.charges[i] = l.charges[last]
		l.charges[i].index = i
	}
	l.charges[last] = nil
	l.charges = l.charges[:last]
	c.index = -1
	// Shrink the backing array after a burst so a long-lived lease does not
	// pin peak bookkeeping capacity.
	if cap(l.charges) > 64 && len(l.charges) < cap(l.charges)/4 {
		l.charges = append(make([]*Charge, 0, cap(l.charges)/2), l.charges...)
	}
}

// Charge reserves bytes before the represented allocation. Existing charges
// remain held while a replacement allocation is built; release the old charge
// only after references to that allocation have been dropped.
func (l *Lease) Charge(class ByteClass, amount uint64) (*Charge, error) {
	return l.charge(class, amount, false)
}

// ChargeCommitted records bytes whose allocation can no longer be refused,
// because the work that allocates them has already been admitted and must
// run (for example an audit record whose phase-5 inputs grew after its
// estimate was reserved). It never refuses: in both modes the bytes are
// charged, and an amount beyond the ceiling counts a committed overrun
// rather than a rejection, so the debt stays visible until released.
func (l *Lease) ChargeCommitted(class ByteClass, amount uint64) (*Charge, error) {
	return l.charge(class, amount, true)
}

func (l *Lease) charge(class ByteClass, amount uint64, committed bool) (*Charge, error) {
	if l == nil || l.g == nil || class >= byteClassCount || class == BaseBytes {
		return nil, &AdmissionError{Outcome: InvalidAccounting}
	}
	g := l.g
	g.mu.Lock()
	if l.closed {
		g.mu.Unlock()
		return nil, &AdmissionError{Outcome: LeaseClosed}
	}
	wouldExceed := amount > 0 && !g.canChargeLocked(amount)
	reason := class.saturation()
	if committed && wouldExceed {
		if !addTo(&g.usage[class], amount) {
			g.mu.Unlock()
			return nil, &AdmissionError{Outcome: InvalidAccounting}
		}
		if g.committedOverruns < math.MaxUint64 {
			g.committedOverruns++
		}
		charge := &Charge{lease: l, class: class, amount: amount}
		l.attachLocked(charge)
		g.mu.Unlock()
		return charge, nil
	}
	if g.limits.Mode == Enforce && wouldExceed {
		g.noteRejectionLocked(reason)
		hook := g.hooks.OnRejection
		mode := g.limits.Mode
		g.mu.Unlock()
		if hook != nil {
			hook(reason, mode)
		}
		return nil, &AdmissionError{Outcome: reason}
	}
	if !addTo(&g.usage[class], amount) {
		g.mu.Unlock()
		return nil, &AdmissionError{Outcome: InvalidAccounting}
	}
	charge := &Charge{lease: l, class: class, amount: amount}
	l.attachLocked(charge)
	if wouldExceed {
		g.noteRejectionLocked(reason)
		hook := g.hooks.OnRejection
		mode := g.limits.Mode
		g.mu.Unlock()
		if hook != nil {
			hook(reason, mode)
		}
		return charge, nil
	}
	g.mu.Unlock()
	return charge, nil
}

func (l *Lease) Close() error {
	if l == nil || l.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := l.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if l.closed {
		return nil
	}
	var totals [byteClassCount]uint64
	for _, charge := range l.charges {
		if charge.released {
			continue
		}
		if !addTo(&totals[charge.class], charge.amount) {
			return &AdmissionError{Outcome: InvalidAccounting}
		}
	}
	for class, amount := range totals {
		if g.usage[class] < amount {
			return &AdmissionError{Outcome: InvalidAccounting}
		}
	}
	if g.leases == 0 {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	l.closed = true
	g.leases--
	for i := 0; i < len(g.waiters); {
		waiter := g.waiters[i]
		if waiter.lease != l {
			i++
			continue
		}
		copy(g.waiters[i:], g.waiters[i+1:])
		g.waiters[len(g.waiters)-1] = nil
		g.waiters = g.waiters[:len(g.waiters)-1]
		waiter.outcome = LeaseClosed
		close(waiter.ready)
	}
	if l.evaluations == 0 {
		if !l.releaseChargesLocked() {
			return &AdmissionError{Outcome: InvalidAccounting}
		}
	}
	g.grantWaitersLocked()
	return nil
}

func (l *Lease) releaseChargesLocked() bool {
	if l.chargesReleased {
		return true
	}
	g := l.g
	var totals [byteClassCount]uint64
	for _, charge := range l.charges {
		if charge.released {
			continue
		}
		if !addTo(&totals[charge.class], charge.amount) {
			return false
		}
	}
	if !l.canReleaseChargesLocked(totals) {
		return false
	}
	for class, amount := range totals {
		if !subtractFrom(&g.usage[class], amount) {
			return false
		}
	}
	for i, charge := range l.charges {
		charge.amount = 0
		charge.released = true
		charge.index = -1
		l.charges[i] = nil
	}
	l.charges = nil
	l.chargesReleased = true
	return true
}

func (l *Lease) canReleaseChargesLocked(totals [byteClassCount]uint64) bool {
	for class, amount := range totals {
		if l.g.usage[class] < amount {
			return false
		}
	}
	return true
}

type Charge struct {
	lease    *Lease
	class    ByteClass
	amount   uint64
	released bool
	index    int
}

// Resize changes one independently-accounted reservation. A decrease refunds
// only this reservation; earlier WAF phase reservations are unaffected.
func (c *Charge) Resize(amount uint64) error {
	if c == nil || c.lease == nil || c.lease.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := c.lease.g
	g.mu.Lock()
	if c.released || c.lease.closed {
		g.mu.Unlock()
		return &AdmissionError{Outcome: LeaseClosed}
	}
	if amount > c.amount {
		delta := amount - c.amount
		wouldExceed := !g.canChargeLocked(delta)
		reason := c.class.saturation()
		if g.limits.Mode == Enforce && wouldExceed {
			g.noteRejectionLocked(reason)
			hook := g.hooks.OnRejection
			mode := g.limits.Mode
			g.mu.Unlock()
			if hook != nil {
				hook(reason, mode)
			}
			return &AdmissionError{Outcome: reason}
		}
		if !addTo(&g.usage[c.class], delta) {
			g.mu.Unlock()
			return &AdmissionError{Outcome: InvalidAccounting}
		}
		c.amount = amount
		if wouldExceed {
			g.noteRejectionLocked(reason)
			hook := g.hooks.OnRejection
			mode := g.limits.Mode
			g.mu.Unlock()
			if hook != nil {
				hook(reason, mode)
			}
			return nil
		}
	} else if !subtractFrom(&g.usage[c.class], c.amount-amount) {
		g.mu.Unlock()
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	c.amount = amount
	g.mu.Unlock()
	return nil
}

func (c *Charge) Release() error {
	if c == nil || c.lease == nil || c.lease.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := c.lease.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if c.released {
		return nil
	}
	if c.lease.closed {
		return &AdmissionError{Outcome: LeaseClosed}
	}
	if !subtractFrom(&g.usage[c.class], c.amount) {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	c.amount = 0
	c.released = true
	c.lease.detachLocked(c)
	return nil
}

type AdmissionError struct {
	Outcome Outcome
}

func (e *AdmissionError) Error() string { return "inspection admission: " + string(e.Outcome) }

func IsOutcome(err error, outcome Outcome) bool {
	var admission *AdmissionError
	return errors.As(err, &admission) && admission.Outcome == outcome
}

type evaluationWaiter struct {
	ready   chan struct{}
	granted *Evaluation
	lease   *Lease
	outcome Outcome
}

type Evaluation struct {
	g         *Governor
	lease     *Lease
	released  bool
	abandoned bool
}

// MarkAbandoned records that the request context ended while this evaluation
// remains active. Release removes it from the gauge only after the actual work
// has completed.
func (e *Evaluation) MarkAbandoned() error {
	if e == nil || e.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := e.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if e.released {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	if e.abandoned {
		return nil
	}
	if g.abandoned == math.MaxUint64 {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g.abandoned++
	e.abandoned = true
	return nil
}

func (g *Governor) TryEvaluation(ctx context.Context, lease *Lease) (*Evaluation, Outcome, error) {
	return g.acquireEvaluation(ctx, lease, false, false)
}

func (g *Governor) AcquireEvaluation(ctx context.Context, lease *Lease) (*Evaluation, Outcome, error) {
	return g.acquireEvaluation(ctx, lease, true, false)
}

// LoggingPolicy selects how a phase-5 evaluation obtains its slot (ADR 0032
// §3). The caller chooses it from whether unread request-body data remains on
// the network, never from the phase number.
type LoggingPolicy uint8

const (
	// LoggingTry is required while unread request-body data remains: phase 5
	// try-acquires and overflows immediately when no slot is free.
	LoggingTry LoggingPolicy = iota
	// LoggingWait is permitted once no unread request-body data remains:
	// phase 5 joins the bounded FIFO wait before overflowing.
	LoggingWait
)

// AcquireLoggingEvaluation obtains a slot for phase 5 or reports Unslotted.
// Phase 5 is never refused: client cancellation, saturation, queue exhaustion
// and wait expiry all degrade to an unslotted (nil token, Unslotted, nil
// error) evaluation that is counted, never to a rejection. Only a closed,
// foreign or invalid lease is an error, because those are caller defects.
// A cancelled ctx skips the wait but still takes a free slot when one exists.
func (g *Governor) AcquireLoggingEvaluation(ctx context.Context, lease *Lease, policy LoggingPolicy) (*Evaluation, Outcome, error) {
	if ctx == nil || ctx.Err() != nil {
		ctx = context.Background()
		policy = LoggingTry
	}
	token, outcome, err := g.acquireEvaluation(ctx, lease, policy == LoggingWait, true)
	if err == nil && token != nil {
		return token, outcome, nil
	}
	if outcome == LeaseClosed || outcome == InvalidAccounting {
		return nil, outcome, err
	}
	if outcome == Cancelled {
		// The client went away mid-wait; logging still runs, slotted if a
		// slot is free now.
		token, outcome, err = g.acquireEvaluation(context.Background(), lease, false, true)
		if err == nil && token != nil {
			return token, outcome, nil
		}
		if outcome == LeaseClosed || outcome == InvalidAccounting {
			return nil, outcome, err
		}
	}
	g.mu.Lock()
	if g.unslotted < math.MaxUint64 {
		g.unslotted++
	}
	hook := g.hooks.OnUnslotted
	g.mu.Unlock()
	if hook != nil {
		hook()
	}
	return nil, Unslotted, nil
}

// acquireEvaluation implements slot admission. logging marks a phase-5
// acquisition: its overflow is reported as Unslotted by the caller and is
// never recorded as an evaluation rejection or would-reject decision.
func (g *Governor) acquireEvaluation(ctx context.Context, lease *Lease, allowWait, logging bool) (*Evaluation, Outcome, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, Cancelled, &AdmissionError{Outcome: Cancelled}
	}
	g.mu.Lock()
	if lease == nil || lease.g != g || lease.closed {
		g.mu.Unlock()
		return nil, InvalidAccounting, &AdmissionError{Outcome: InvalidAccounting}
	}
	timeout := g.limits.EvaluationQueueTimeout
	if g.limits.Mode == Observe {
		saturated := g.active >= uint64(g.limits.EvaluationSlots) || len(g.waiters) > 0
		if logging && saturated {
			// Enforce would overflow this phase-5 call (it cannot wait in
			// observe, where no FIFO forms). Report it unslotted so it does
			// not inflate the active count later phase 1–4 decisions use.
			g.mu.Unlock()
			return nil, EvaluationSaturated, &AdmissionError{Outcome: EvaluationSaturated}
		}
		wouldReject := !logging && saturated
		if g.active == math.MaxUint64 || lease.evaluations == math.MaxUint64 {
			g.mu.Unlock()
			return nil, InvalidAccounting, &AdmissionError{Outcome: InvalidAccounting}
		}
		token := &Evaluation{g: g, lease: lease}
		g.active++
		lease.evaluations++
		if wouldReject {
			g.noteRejectionLocked(EvaluationSaturated)
		}
		g.mu.Unlock()
		if wouldReject && g.hooks.OnRejection != nil {
			g.hooks.OnRejection(EvaluationSaturated, Observe)
		}
		if wouldReject {
			return token, WouldReject, nil
		}
		return token, Acquired, nil
	}
	if len(g.waiters) == 0 && g.active < uint64(g.limits.EvaluationSlots) {
		if g.active == math.MaxUint64 || lease.evaluations == math.MaxUint64 {
			g.mu.Unlock()
			return nil, InvalidAccounting, &AdmissionError{Outcome: InvalidAccounting}
		}
		g.active++
		lease.evaluations++
		g.mu.Unlock()
		return &Evaluation{g: g, lease: lease}, Acquired, nil
	}
	if !allowWait || timeout == 0 {
		hook := g.rejectionHookLocked(EvaluationSaturated, logging)
		mode := g.limits.Mode
		g.mu.Unlock()
		if hook != nil {
			hook(EvaluationSaturated, mode)
		}
		return nil, EvaluationSaturated, &AdmissionError{Outcome: EvaluationSaturated}
	}
	waitingLimit := uint64(g.limits.EvaluationSlots)
	if waitingLimit <= math.MaxUint64/16 {
		waitingLimit *= 16
	} else {
		waitingLimit = math.MaxUint64
	}
	if uint64(len(g.waiters)) >= waitingLimit {
		hook := g.rejectionHookLocked(EvaluationSaturated, logging)
		mode := g.limits.Mode
		g.mu.Unlock()
		if hook != nil {
			hook(EvaluationSaturated, mode)
		}
		return nil, QueueExhausted, &AdmissionError{Outcome: QueueExhausted}
	}
	w := &evaluationWaiter{ready: make(chan struct{}), lease: lease}
	g.waiters = append(g.waiters, w)
	g.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-w.ready:
		if w.granted != nil {
			return w.granted, Acquired, nil
		}
		return nil, w.outcome, &AdmissionError{Outcome: w.outcome}
	case <-ctx.Done():
		return g.abandonWaiter(w, Cancelled, logging)
	case <-timer.C:
		return g.abandonWaiter(w, WaitExpired, logging)
	}
}

// rejectionHookLocked records an evaluation rejection and returns the hook to
// call after unlocking. Phase-5 overflow is not a rejection.
func (g *Governor) rejectionHookLocked(reason Outcome, logging bool) func(Outcome, Mode) {
	if logging {
		return nil
	}
	g.noteRejectionLocked(reason)
	return g.hooks.OnRejection
}

func (g *Governor) abandonWaiter(w *evaluationWaiter, outcome Outcome, logging bool) (*Evaluation, Outcome, error) {
	g.mu.Lock()
	if w.granted != nil {
		token := w.granted
		if !g.releaseEvaluationLocked(token) {
			g.mu.Unlock()
			return nil, InvalidAccounting, &AdmissionError{Outcome: InvalidAccounting}
		}
		g.grantWaitersLocked()
		var hook func(Outcome, Mode)
		mode := g.limits.Mode
		if outcome == WaitExpired {
			hook = g.rejectionHookLocked(EvaluationSaturated, logging)
		}
		g.mu.Unlock()
		if hook != nil {
			hook(EvaluationSaturated, mode)
		}
		return nil, outcome, &AdmissionError{Outcome: outcome}
	}
	if w.outcome != "" {
		result := w.outcome
		g.mu.Unlock()
		return nil, result, &AdmissionError{Outcome: result}
	}
	for i, queued := range g.waiters {
		if queued == w {
			copy(g.waiters[i:], g.waiters[i+1:])
			g.waiters[len(g.waiters)-1] = nil
			g.waiters = g.waiters[:len(g.waiters)-1]
			break
		}
	}
	queueHook := g.hooks.OnQueueAbandon
	var rejectionHook func(Outcome, Mode)
	mode := g.limits.Mode
	if outcome == WaitExpired {
		rejectionHook = g.rejectionHookLocked(EvaluationSaturated, logging)
	}
	g.grantWaitersLocked()
	g.mu.Unlock()
	if queueHook != nil {
		queueHook()
	}
	if rejectionHook != nil {
		rejectionHook(EvaluationSaturated, mode)
	}
	return nil, outcome, &AdmissionError{Outcome: outcome}
}

func (e *Evaluation) Release() error {
	if e == nil || e.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := e.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if e.released {
		return nil
	}
	if !g.releaseEvaluationLocked(e) {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g.grantWaitersLocked()
	return nil
}

func (g *Governor) releaseEvaluationLocked(e *Evaluation) bool {
	if e.released || g.active == 0 {
		return e.released
	}
	if e.lease == nil || e.lease.evaluations == 0 {
		return false
	}
	if e.lease.closed && e.lease.evaluations == 1 && !e.lease.chargesReleased {
		var totals [byteClassCount]uint64
		for _, charge := range e.lease.charges {
			if charge.released {
				continue
			}
			if !addTo(&totals[charge.class], charge.amount) {
				return false
			}
		}
		if !e.lease.canReleaseChargesLocked(totals) {
			return false
		}
	}
	if e.abandoned {
		if g.abandoned == 0 {
			return false
		}
		g.abandoned--
	}
	g.active--
	e.lease.evaluations--
	e.released = true
	if e.lease.closed && e.lease.evaluations == 0 && !e.lease.releaseChargesLocked() {
		return false
	}
	return true
}

func (g *Governor) grantWaitersLocked() {
	for len(g.waiters) > 0 {
		w := g.waiters[0]
		if w.lease != nil && w.lease.closed {
			copy(g.waiters, g.waiters[1:])
			g.waiters[len(g.waiters)-1] = nil
			g.waiters = g.waiters[:len(g.waiters)-1]
			w.outcome = LeaseClosed
			close(w.ready)
			continue
		}
		if g.limits.Mode == Enforce && g.active >= uint64(g.limits.EvaluationSlots) {
			return
		}
		copy(g.waiters, g.waiters[1:])
		g.waiters[len(g.waiters)-1] = nil
		g.waiters = g.waiters[:len(g.waiters)-1]
		if g.active == math.MaxUint64 {
			w.outcome = InvalidAccounting
			close(w.ready)
			continue
		}
		if w.lease.evaluations == math.MaxUint64 {
			w.outcome = InvalidAccounting
			close(w.ready)
			continue
		}
		g.active++
		w.lease.evaluations++
		w.granted = &Evaluation{g: g, lease: w.lease}
		close(w.ready)
	}
}

func (g *Governor) canChargeLocked(amount uint64) bool {
	total, ok := sumBytes(g.usage[:])
	if !ok || total > g.limits.MaxBytes {
		return false
	}
	return amount <= g.limits.MaxBytes-total
}

// Rejection reason indexes of Snapshot.Rejections.
const (
	RejectBytes = iota
	RejectWAFState
	RejectEvaluation
	RejectAudit
	rejectionReasons
)

// saturation is the refusal reason of a charge on class c.
func (c ByteClass) saturation() Outcome {
	switch c {
	case WAFStateBytes:
		return WAFStateSaturated
	case AuditBytes:
		return AuditSaturated
	default:
		return ByteSaturated
	}
}

func (g *Governor) noteRejectionLocked(reason Outcome) {
	g.noteRejectionForModeLocked(reason, g.limits.Mode)
}

// RecordRejection records a bounded policy rejection or observe-mode
// would-reject decision made at a boundary outside the governor's byte and
// slot acquisitions.
func (g *Governor) RecordRejection(reason Outcome, mode Mode) error {
	if g == nil || (mode != Enforce && mode != Observe) {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	switch reason {
	case ByteSaturated, WAFStateSaturated, EvaluationSaturated, AuditSaturated:
	default:
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g.mu.Lock()
	g.noteRejectionForModeLocked(reason, mode)
	hook := g.hooks.OnRejection
	g.mu.Unlock()
	if hook != nil {
		hook(reason, mode)
	}
	return nil
}

func (g *Governor) noteRejectionForModeLocked(reason Outcome, mode Mode) {
	index := RejectBytes
	switch reason {
	case WAFStateSaturated:
		index = RejectWAFState
	case EvaluationSaturated:
		index = RejectEvaluation
	case AuditSaturated:
		index = RejectAudit
	}
	modeIndex := 0
	if mode == Observe {
		modeIndex = 1
	}
	if g.rejections[index][modeIndex] < math.MaxUint64 {
		g.rejections[index][modeIndex]++
	}
}

func addTo(dst *uint64, n uint64) bool {
	if math.MaxUint64-*dst < n {
		return false
	}
	*dst += n
	return true
}

func subtractFrom(dst *uint64, n uint64) bool {
	if *dst < n {
		return false
	}
	*dst -= n
	return true
}

func sumBytes(values []uint64) (uint64, bool) {
	var total uint64
	for _, value := range values {
		if math.MaxUint64-total < value {
			return 0, false
		}
		total += value
	}
	return total, true
}
