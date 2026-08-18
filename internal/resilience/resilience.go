// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package resilience resolves the public [upstreams.resilience] configuration
// into one immutable policy that every upstream consumer shares: the HTTP
// reverse proxy, native gRPC passthrough, gRPC-JSON transcoding, FastCGI/uWSGI
// and L4 stream routes.
//
// The separation mirrors backendtls (ADR 0016) and is required by ADR 0017.
// The request path performs one atomic pointer load and reads pre-parsed
// scalars: no configuration tree traversal, no inheritance merge, no duration
// parsing and no allocation. A policy change swaps a pointer; it never rebuilds
// a pool, which is what keeps admission counters and breaker state alive across
// reload.
//
// The package imports only the standard library, so it can be used from config
// validation and from every protocol adapter without an import cycle.
package resilience

import (
	"fmt"
	"time"
)

// Limits accepted by validation. They are generous bounds whose purpose is to
// make a typo (a pasted port number, a millisecond value written as seconds)
// fail at load rather than at 3am, not to express an opinion about sizing.
const (
	MaxActiveRequestsCeiling   = 10_000_000
	MaxActivePerBackendCeiling = 10_000_000
	MaxPendingRequestsCeiling  = 100_000
	MaxConnectionsCeiling      = 100_000
	PendingTimeoutCeiling      = 60 * time.Second
	RetryAttemptsCeiling       = 100
	RetryDeadlineCeiling       = 5 * time.Minute
	RetryBackoffCeiling        = 60 * time.Second
	RetryBudgetPercentCeiling  = 1000
)

// DefaultRetryBackoffMax caps exponential growth when backoff is enabled and no
// ceiling was given. Backoff exists to spread failover, not to become a second
// timeout, so an unstated maximum is a small one.
const DefaultRetryBackoffMax = 500 * time.Millisecond

// Options is the public [upstreams.resilience] block in the shape this package
// consumes. config converts to it, so this package never imports config.
//
// Every field's zero value means "behave exactly as Jul does today", which is
// what makes the defaults compatible by construction rather than by review.
type Options struct {
	// MaxActiveRequests bounds admitted logical requests, streams and
	// connections for one admission owner. 0 is unlimited.
	MaxActiveRequests int
	// MaxActivePerBackend bounds admitted logical requests per backend. It is a
	// selection filter, never a second queue. 0 is unlimited.
	MaxActivePerBackend int
	// MaxPendingRequests bounds the waiter FIFO. 0 means no queue — reject
	// immediately — and never "unlimited": an unbounded pending queue is the
	// memory failure this control exists to prevent, so it is unrepresentable.
	MaxPendingRequests int
	// PendingTimeout bounds how long a request may stay parked. 0 means the
	// request context is the only bound.
	PendingTimeout time.Duration
	// MaxConnectionsPerBackend bounds physical sockets to one backend host on
	// one transport. 0 is unlimited. It is stateless — a transport is built per
	// location — so a location may override it.
	MaxConnectionsPerBackend int
	// RetryAttempts caps total attempts for one retryable request. 0 means try
	// every distinct backend once, which is what Jul does today.
	RetryAttempts int
	// RetryDeadline bounds the whole retry sequence, attempts and backoff sleeps
	// alike. 0 leaves the request context as the only bound.
	RetryDeadline time.Duration
	// RetryBackoffInitial is the first backoff interval, doubling per attempt
	// with full jitter. 0 means immediate failover, which is what Jul does today.
	RetryBackoffInitial time.Duration
	// RetryBackoffMax clamps the doubling. It is only consulted when backoff is
	// enabled; 0 then means DefaultRetryBackoffMax.
	RetryBackoffMax time.Duration
	// RetryBudgetPercent bounds retries as a percentage of primary attempts over
	// a trailing window. 0 is unbudgeted. It owns a window, so unlike the other
	// retry controls it is pool-scoped and a location may not override it.
	RetryBudgetPercent int
}

// Policy is the resolved, immutable resilience policy. Consumers receive only
// this type and never the public configuration, which is what keeps a future
// control additive rather than a per-protocol rewrite.
type Policy struct {
	maxActiveRequests        int64
	maxActivePerBackend      int64
	maxPendingRequests       int
	pendingTimeout           time.Duration
	maxConnectionsPerBackend int
	retryAttempts            int
	retryDeadline            time.Duration
	retryBackoffInitial      time.Duration
	retryBackoffMax          time.Duration
	retryBudgetPercent       int
}

// Default is the policy every zero-valued configuration resolves to: no
// admission bound, no queue, no per-backend filter. It is shared, so a pool
// with no resilience block allocates nothing.
var Default = &Policy{}

// Resolve normalizes public options into an immutable policy. It returns an
// error only for values that validation should already have rejected; callers
// in the data path never see one.
func Resolve(o Options) (*Policy, error) {
	if o == (Options{}) {
		return Default, nil
	}
	if err := check(o); err != nil {
		return nil, err
	}
	backoffMax := o.RetryBackoffMax
	if o.RetryBackoffInitial > 0 && backoffMax == 0 {
		backoffMax = DefaultRetryBackoffMax
	}
	return &Policy{
		maxActiveRequests:        int64(o.MaxActiveRequests),
		maxActivePerBackend:      int64(o.MaxActivePerBackend),
		maxPendingRequests:       o.MaxPendingRequests,
		pendingTimeout:           o.PendingTimeout,
		maxConnectionsPerBackend: o.MaxConnectionsPerBackend,
		retryAttempts:            o.RetryAttempts,
		retryDeadline:            o.RetryDeadline,
		retryBackoffInitial:      o.RetryBackoffInitial,
		retryBackoffMax:          backoffMax,
		retryBudgetPercent:       o.RetryBudgetPercent,
	}, nil
}

func check(o Options) error {
	switch {
	case o.MaxActiveRequests < 0 || o.MaxActiveRequests > MaxActiveRequestsCeiling:
		return fmt.Errorf("max_active_requests must be between 0 and %d", MaxActiveRequestsCeiling)
	case o.MaxActivePerBackend < 0 || o.MaxActivePerBackend > MaxActivePerBackendCeiling:
		return fmt.Errorf("max_active_per_backend must be between 0 and %d", MaxActivePerBackendCeiling)
	case o.MaxPendingRequests < 0 || o.MaxPendingRequests > MaxPendingRequestsCeiling:
		return fmt.Errorf("max_pending_requests must be between 0 and %d", MaxPendingRequestsCeiling)
	case o.PendingTimeout < 0 || o.PendingTimeout > PendingTimeoutCeiling:
		return fmt.Errorf("pending_timeout must be between 0s and %s", PendingTimeoutCeiling)
	case o.MaxConnectionsPerBackend < 0 || o.MaxConnectionsPerBackend > MaxConnectionsCeiling:
		return fmt.Errorf("max_connections_per_backend must be between 0 and %d", MaxConnectionsCeiling)
	case o.MaxPendingRequests > 0 && o.MaxActiveRequests == 0:
		return fmt.Errorf("max_pending_requests requires max_active_requests: with no admission limit nothing ever queues")
	case o.RetryAttempts < 0 || o.RetryAttempts > RetryAttemptsCeiling:
		return fmt.Errorf("retry_attempts must be between 0 and %d", RetryAttemptsCeiling)
	case o.RetryDeadline < 0 || o.RetryDeadline > RetryDeadlineCeiling:
		return fmt.Errorf("retry_deadline must be between 0s and %s", RetryDeadlineCeiling)
	case o.RetryBackoffInitial < 0 || o.RetryBackoffInitial > RetryBackoffCeiling:
		return fmt.Errorf("retry_backoff_initial must be between 0s and %s", RetryBackoffCeiling)
	case o.RetryBackoffMax < 0 || o.RetryBackoffMax > RetryBackoffCeiling:
		return fmt.Errorf("retry_backoff_max must be between 0s and %s", RetryBackoffCeiling)
	case o.RetryBudgetPercent < 0 || o.RetryBudgetPercent > RetryBudgetPercentCeiling:
		return fmt.Errorf("retry_budget_percent must be between 0 and %d", RetryBudgetPercentCeiling)
	case o.RetryBackoffMax > 0 && o.RetryBackoffInitial > o.RetryBackoffMax:
		return fmt.Errorf("retry_backoff_initial (%s) must not exceed retry_backoff_max (%s)", o.RetryBackoffInitial, o.RetryBackoffMax)
	case o.RetryBackoffMax > 0 && o.RetryBackoffInitial == 0:
		return fmt.Errorf("retry_backoff_max requires retry_backoff_initial: with no initial interval there is no backoff to clamp")
	case o.RetryDeadline > 0 && o.RetryBackoffInitial > o.RetryDeadline:
		return fmt.Errorf("retry_backoff_initial (%s) must not exceed retry_deadline (%s), which would leave no room for a second attempt", o.RetryBackoffInitial, o.RetryDeadline)
	}
	return nil
}

// MaxActiveRequests returns the pool admission limit; 0 is unlimited.
func (p *Policy) MaxActiveRequests() int64 { return p.maxActiveRequests }

// MaxActivePerBackend returns the per-backend selection filter; 0 is unlimited.
func (p *Policy) MaxActivePerBackend() int64 { return p.maxActivePerBackend }

// MaxPendingRequests returns the waiter FIFO bound; 0 means no queue.
func (p *Policy) MaxPendingRequests() int { return p.maxPendingRequests }

// PendingTimeout returns the parked-request bound; 0 means context-bounded.
func (p *Policy) PendingTimeout() time.Duration { return p.pendingTimeout }

// MaxConnectionsPerBackend returns the physical socket bound per backend host
// on one transport; 0 is unlimited.
func (p *Policy) MaxConnectionsPerBackend() int { return p.maxConnectionsPerBackend }

// RetryAttempts returns the attempt cap; 0 means every distinct backend once.
func (p *Policy) RetryAttempts() int { return p.retryAttempts }

// RetryDeadline returns the bound on the whole retry sequence; 0 means the
// request context is the only bound.
func (p *Policy) RetryDeadline() time.Duration { return p.retryDeadline }

// RetryBackoffInitial returns the first backoff interval; 0 means immediate
// failover.
func (p *Policy) RetryBackoffInitial() time.Duration { return p.retryBackoffInitial }

// RetryBackoffMax returns the clamp on exponential growth. It is meaningful
// only when RetryBackoffInitial is non-zero.
func (p *Policy) RetryBackoffMax() time.Duration { return p.retryBackoffMax }

// RetryBudgetPercent returns the retry allowance as a percentage of primary
// attempts over a trailing window; 0 is unbudgeted.
func (p *Policy) RetryBudgetPercent() int { return p.retryBudgetPercent }

// Bounded reports whether the policy constrains anything at all. Consumers use
// it to keep the completely-unconfigured path free of even a counter update.
func (p *Policy) Bounded() bool {
	return p.maxActiveRequests > 0 || p.maxActivePerBackend > 0
}
