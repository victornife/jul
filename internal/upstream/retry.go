// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"sync/atomic"
	"time"

	"jul/internal/resilience"
)

// StopReason is the bounded set of reasons a retry sequence stopped. It is
// closed by design (ADR 0017 constraint 6): it is safe as a metric label and as
// a log field, and an unrecognized condition is not permitted to invent a new
// value.
type StopReason string

const (
	// StopNone means a retry is permitted.
	StopNone StopReason = ""
	// StopSuccess means the attempt succeeded; nothing was stopped.
	StopSuccess StopReason = "success"
	// StopAttempts means the attempt cap was reached.
	StopAttempts StopReason = "attempts_exhausted"
	// StopNotRetryable means the request itself may not be replayed: a method
	// whose retry is not safe, or a body that cannot be rewound.
	StopNotRetryable StopReason = "not_retryable"
	// StopResponseStarted means a byte may already have reached the client, so
	// there is nothing left to retry into. It dominates every other signal.
	StopResponseStarted StopReason = "response_started"
	// StopTerminalError means the failure is deterministic and would repeat: a
	// TLS identity mismatch is the same mismatch against every backend.
	StopTerminalError StopReason = "terminal_error"
	// StopCancelled means the client went away.
	StopCancelled StopReason = "cancelled"
	// StopDeadline means the overall retry deadline left no room for another
	// attempt.
	StopDeadline StopReason = "deadline_exceeded"
	// StopBudget means the pool's retry budget is spent.
	StopBudget StopReason = "budget_exhausted"
	// StopNoBackend means no untried eligible backend remains.
	StopNoBackend StopReason = "no_untried_backend"
)

// Decision is everything the retry rule needs, gathered by the adapter. It is a
// value: the rule that consumes it is pure, so the same eligibility can be
// evaluated by the HTTP transport, the CGI adapters and the transcoder without
// any of them re-deriving it, and tested exhaustively without a network.
type Decision struct {
	// Err is the failure that ended the attempt.
	Err error
	// Attempts counts attempts already made, including the one that just failed.
	Attempts int
	// MaxAttempts caps them. 0 means "every distinct backend once", which the
	// backend supply bounds instead.
	MaxAttempts int
	// ResponseStarted records that a byte may already have reached the client.
	ResponseStarted bool
	// Replayable records that the request may be sent again: a retry-safe method
	// with a body that is absent or rewindable.
	Replayable bool
	// Terminal records a deterministic failure that would repeat identically
	// against any backend.
	Terminal bool
	// Remaining is what is left of the overall deadline. A non-positive value
	// stops the sequence. Callers with no deadline pass a positive sentinel.
	Remaining time.Duration
	// UntriedBackend records that an eligible backend nobody has tried remains.
	UntriedBackend bool
	// BudgetAllows records that the pool's retry budget permitted this retry.
	// Evaluating it consumes budget, so the caller evaluates it last.
	BudgetAllows bool
}

// Retryable applies ADR 0017's eligibility rule. Every condition must hold, and
// the returned reason names the first one that did not.
//
// The order is not arbitrary. Conditions that describe what already happened to
// the client come before conditions that describe what Jul is willing to spend,
// because a request whose response has started cannot be retried no matter how
// much budget or deadline remains — reporting "budget exhausted" for it would
// send an operator to tune the wrong control.
func Retryable(d Decision) StopReason {
	switch {
	case d.Err == nil:
		return StopSuccess
	case d.ResponseStarted:
		return StopResponseStarted
	case !d.Replayable:
		return StopNotRetryable
	case d.Terminal:
		return StopTerminalError
	case errors.Is(d.Err, context.Canceled):
		return StopCancelled
	case d.MaxAttempts > 0 && d.Attempts >= d.MaxAttempts:
		return StopAttempts
	case d.Remaining <= 0:
		return StopDeadline
	case !d.UntriedBackend:
		return StopNoBackend
	case !d.BudgetAllows:
		return StopBudget
	}
	return StopNone
}

// backoffFor returns the jittered delay before attempt number n (1-based: the
// delay after the first failure is n=1), clamped by max and then by what is
// left of the overall deadline.
//
// Jitter is full — uniform over [0, d) rather than d/2 ± d/2 — because the
// failure mode being defended against is synchronised failover: every client of
// a backend that just died computes the same interval and returns together.
// Halving the spread halves the defence for no benefit.
//
// ok is false when the deadline leaves no room to sleep and still attempt, in
// which case the caller must stop rather than sleep into a guaranteed failure.
func backoffFor(n int, initial, max, remaining time.Duration, jitter func(time.Duration) time.Duration) (time.Duration, bool) {
	if initial <= 0 {
		return 0, remaining > 0
	}
	d := initial
	for i := 1; i < n && d < max; i++ {
		d *= 2
		if d <= 0 { // overflow; max clamps below anyway
			d = max
			break
		}
	}
	if max > 0 && d > max {
		d = max
	}
	d = jitter(d)
	// Room for the sleep alone is not enough: waking at the deadline only
	// converts one failure into a slower one.
	if d >= remaining {
		return 0, false
	}
	return d, true
}

// fullJitter draws uniformly from [0, d).
func fullJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(d)))
}

// retryBudgetWindow is the trailing accounting window. Two of them are live at
// once, so the effective window is between one and two of these wide.
const retryBudgetWindow = 10 * time.Second

// minFreeRetries is the floor of retries permitted regardless of ratio, so a
// pool with almost no traffic can still fail over. Without it a budget would
// make the first request after an idle period unretryable, which is exactly
// when a stale connection is most likely.
const minFreeRetries = 3

// budgetWindow is one immutable-epoch accounting bucket. The previous window's
// totals are copied in at rotation, so a reader needs one pointer load to see
// both.
type budgetWindow struct {
	epoch         int64
	prevPrimaries int64
	prevRetries   int64
	primaries     atomic.Int64
	retries       atomic.Int64
}

// Budget is the pool-scoped retry allowance: over a trailing window, retries
// are permitted while
//
//	retries < floor(primaries * percent / 100) + minFreeRetries
//
// It bounds upstream load at (1 + percent/100) × client load + a small floor,
// which is the difference between a failing backend being helped and being
// finished off by the mechanism meant to route around it.
//
// The window is deliberately not weighted across the two buckets. A weighted
// estimate would make the counter approximate, and the consumption CAS could
// then no longer claim to be exact; an exact bound over a window whose width
// varies between W and 2W is more useful than an approximate bound over a
// window of exactly W.
//
// One consequence of the two buckets is worth knowing, because it looks like a
// bug the first time it is seen: a retry is recorded in the bucket it was
// granted in, while the primaries that justified it may span both. When the
// older bucket ages out those primaries leave and the retry does not, so the
// stored ratio can briefly read above the limit. Nothing is over-spent — every
// grant satisfied the bound at the moment it was made, which is the property
// the CAS enforces and the fuzz asserts — and the effect is self-correcting,
// because Allow simply denies until the ratio recovers. Ageing retries out
// with the traffic that earned them would need a timestamp per retry and would
// make the hot path allocate.
//
// The counters live here, on the pool, and a policy swap changes only percent.
// That is what stops a reload from handing out a fresh burst of retries: an
// operator reloading during an incident is the least appropriate moment to
// forgive the retry load that helped cause it.
type Budget struct {
	percent atomic.Int64
	win     atomic.Pointer[budgetWindow]
	// now is the clock seam. Tests substitute it to make window rotation
	// deterministic; production never sets it.
	now func() time.Time
}

// NewBudget builds a budget for the given percentage. 0 is unbudgeted.
func NewBudget(percent int) *Budget {
	b := &Budget{now: time.Now}
	b.percent.Store(int64(percent))
	b.win.Store(&budgetWindow{epoch: time.Now().UnixNano() / int64(retryBudgetWindow)})
	return b
}

// SetPercent swaps the allowance without touching the accumulated window.
func (b *Budget) SetPercent(percent int) { b.percent.Store(int64(percent)) }

// Percent returns the current allowance; 0 is unbudgeted.
func (b *Budget) Percent() int { return int(b.percent.Load()) }

// window returns the live bucket, rotating first if the epoch advanced. The
// rotation is a CAS, so concurrent rotators agree on one winner and the losers
// simply retry the load.
func (b *Budget) window() *budgetWindow {
	epoch := b.now().UnixNano() / int64(retryBudgetWindow)
	for {
		w := b.win.Load()
		if w.epoch >= epoch {
			// Equal is the common case. Greater means the clock moved backwards,
			// where keeping the newer window is the conservative choice: it
			// preserves accounting rather than granting an unearned reset.
			return w
		}
		next := &budgetWindow{epoch: epoch}
		if epoch == w.epoch+1 {
			next.prevPrimaries = w.primaries.Load()
			next.prevRetries = w.retries.Load()
		}
		// A gap of more than one window means the pool was idle; carrying
		// nothing forward is correct, because there is no recent load to take a
		// ratio of.
		if b.win.CompareAndSwap(w, next) {
			return next
		}
	}
}

// Primary records a first attempt. Primaries accrue automatically, so the
// budget needs no separate success signal.
func (b *Budget) Primary() {
	if b.percent.Load() <= 0 {
		return
	}
	b.window().primaries.Add(1)
}

// Allow consumes one retry if the budget permits it. It is exact under
// concurrency: the allowance is claimed by CAS, so two goroutines cannot both
// spend the last one.
func (b *Budget) Allow() bool {
	percent := b.percent.Load()
	if percent <= 0 {
		return true
	}
	w := b.window()
	for {
		cur := w.retries.Load()
		total := cur + w.prevRetries
		primaries := w.primaries.Load() + w.prevPrimaries
		if total >= primaries*percent/100+minFreeRetries {
			return false
		}
		if w.retries.CompareAndSwap(cur, cur+1) {
			return true
		}
	}
}

// Budget returns the pool's retry budget. It never returns nil.
func (p *Pool) Budget() *Budget { return p.budget }

// RetryRequest is the per-call retry configuration, resolved by the adapter
// from the pool policy and any location override.
type RetryRequest struct {
	// MaxAttempts caps total attempts. 0 means every distinct backend once.
	MaxAttempts int
	// Deadline bounds the whole sequence. 0 leaves the caller's context as the
	// only bound.
	Deadline time.Duration
	// BackoffInitial is the first interval, doubling with full jitter. 0 means
	// immediate failover.
	BackoffInitial time.Duration
	// BackoffMax clamps the doubling.
	BackoffMax time.Duration
	// Replayable records that this request may be sent again at all.
	Replayable bool

	// OnBackoff reports the interval the driver is about to wait before attempt
	// next. It exists because the wait is the driver's to compute and the span
	// to annotate is the caller's, and a backoff nobody records is latency with
	// no explanation in a trace. It is called on the request goroutine, before
	// sleeping, and must not block.
	OnBackoff func(next int, d time.Duration)
}

// AttemptResult is what an adapter reports after one attempt.
type AttemptResult struct {
	// Err is the failure, nil on success.
	Err error
	// ResponseStarted records that a byte may already have reached the client.
	// Setting it makes the failure terminal regardless of everything else.
	ResponseStarted bool
	// Terminal records a deterministic failure that would repeat identically
	// against any backend, such as a TLS identity mismatch. Retrying one is
	// pure amplification with no chance of a different answer.
	Terminal bool
	// Retain keeps the backend's in-flight slot alive past this call. An HTTP
	// response body outlives the round trip that produced it, so its adapter
	// sets this and then owns exactly one Release.
	Retain bool
}

// AttemptFunc performs one attempt against a chosen backend. n is 1-based.
type AttemptFunc func(ctx context.Context, b Attempt, n int) AttemptResult

// Do drives attempts against the pool's backends until one succeeds or the
// retry rule stops the sequence, and returns why it stopped.
//
// The driver owns backend selection, the overall deadline, backoff and budget
// accounting; the closure owns the protocol. That split is what lets the HTTP
// transport, the CGI adapters and the transcoder share one retry rule instead
// of three that drift.
//
// Every backend the driver hands out is released by the driver, except after a
// successful attempt that asked to retain it.
func (p *Pool) Do(ctx context.Context, rr RetryRequest, fn AttemptFunc) (StopReason, error) {
	deadline := ctx
	if rr.Deadline > 0 {
		// WithTimeout already takes the minimum with any deadline the caller
		// brought, so this is exactly min(request deadline, start + retry_deadline).
		var cancel context.CancelFunc
		deadline, cancel = context.WithTimeout(ctx, rr.Deadline)
		defer cancel()
	}

	tried := make(map[BackendIdentity]struct{})
	var lastErr error
	for n := 1; ; n++ {
		b, err := p.PickExcluding(deadline, tried)
		if err != nil {
			if lastErr != nil {
				// The reason the request failed is the upstream failure that
				// started the sequence, not the exhaustion that ended it.
				return StopNoBackend, lastErr
			}
			return StopNoBackend, err
		}
		tried[b.Identity()] = struct{}{}
		if n == 1 {
			p.budget.Primary()
		}

		res := fn(deadline, b, n)
		if res.Err == nil {
			if !res.Retain {
				p.Release(b.Backend)
			}
			return StopSuccess, nil
		}
		p.Release(b.Backend)
		lastErr = res.Err

		remaining := remainingFor(deadline)
		d := Decision{
			Err:             res.Err,
			Attempts:        n,
			MaxAttempts:     rr.MaxAttempts,
			ResponseStarted: res.ResponseStarted,
			Replayable:      rr.Replayable,
			Terminal:        res.Terminal,
			Remaining:       remaining,
			UntriedBackend:  p.hasUntried(deadline, tried),
			// Asking the budget costs an allowance, so the free conditions are
			// asked first and the budget only if they all agree. Retryable
			// evaluates BudgetAllows last precisely so this is sound.
			BudgetAllows: true,
		}
		if reason := Retryable(d); reason != StopNone {
			return reason, lastErr
		}
		if !p.budget.Allow() {
			return StopBudget, lastErr
		}

		delay, ok := backoffFor(n, rr.BackoffInitial, rr.BackoffMax, remaining, fullJitter)
		if !ok {
			return StopDeadline, lastErr
		}
		if rr.OnBackoff != nil {
			rr.OnBackoff(n+1, delay)
		}
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-deadline.Done():
				timer.Stop()
				return StopDeadline, lastErr
			case <-timer.C:
			}
		}
	}
}

// noDeadline is the sentinel Remaining for a sequence bounded only by the
// caller's context. It is a duration rather than a flag so the decision rule
// keeps one comparison instead of two.
const noDeadline = time.Duration(1<<63 - 1)

func remainingFor(ctx context.Context) time.Duration {
	dl, ok := ctx.Deadline()
	if !ok {
		if ctx.Err() != nil {
			return 0
		}
		return noDeadline
	}
	return time.Until(dl)
}

// hasUntried reports whether any backend outside excluded is currently
// eligible. It is deliberately a look rather than a claim: selection happens on
// the next iteration, and a backend that becomes ineligible in between simply
// ends the sequence there.
func (p *Pool) hasUntried(ctx context.Context, excluded map[BackendIdentity]struct{}) bool {
	now := time.Now().UnixNano()
	for _, b := range p.candidates(ctx) {
		if _, ok := excluded[b.Identity()]; ok {
			continue
		}
		if b.available(now) {
			return true
		}
	}
	return false
}

// RetryOverride is the location-scoped half of the retry configuration. A zero
// field inherits the pool policy rather than meaning "unlimited", matching every
// other override in the resilience block.
type RetryOverride struct {
	Attempts       int
	Deadline       time.Duration
	BackoffInitial time.Duration
	BackoffMax     time.Duration
}

// RetryRequestFor merges a location's overrides with the pool's live policy.
//
// It lives here so every adapter resolves the override rule the same way, and
// so the policy is read per request: a resilience reload swaps a pointer, and
// nothing needs rebuilding for it to take effect.
func (p *Pool) RetryRequestFor(o RetryOverride, replayable bool) RetryRequest {
	rr := RetryRequest{
		MaxAttempts:    o.Attempts,
		Deadline:       o.Deadline,
		BackoffInitial: o.BackoffInitial,
		BackoffMax:     o.BackoffMax,
		Replayable:     replayable,
	}
	if p == nil {
		return rr
	}
	policy := p.Policy()
	if rr.MaxAttempts == 0 {
		rr.MaxAttempts = policy.RetryAttempts()
	}
	if rr.Deadline == 0 {
		rr.Deadline = policy.RetryDeadline()
	}
	if rr.BackoffInitial == 0 {
		rr.BackoffInitial = policy.RetryBackoffInitial()
		rr.BackoffMax = policy.RetryBackoffMax()
	} else if rr.BackoffMax == 0 {
		rr.BackoffMax = resilience.DefaultRetryBackoffMax
	}
	return rr
}

// RetrySafeMethod reports whether an HTTP method may be retried after a
// transport failure.
//
// It is deliberately one definition rather than one per adapter: the HTTP
// proxy, the CGI adapters and the transcoder all gate on it, and a list that
// drifted between them would mean a method retried on one route and not on
// another for no reason anybody could state.
func RetrySafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}
