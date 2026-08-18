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
	PendingTimeoutCeiling      = 60 * time.Second
)

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
}

// Policy is the resolved, immutable resilience policy. Consumers receive only
// this type and never the public configuration, which is what keeps a future
// control additive rather than a per-protocol rewrite.
type Policy struct {
	maxActiveRequests   int64
	maxActivePerBackend int64
	maxPendingRequests  int
	pendingTimeout      time.Duration
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
	return &Policy{
		maxActiveRequests:   int64(o.MaxActiveRequests),
		maxActivePerBackend: int64(o.MaxActivePerBackend),
		maxPendingRequests:  o.MaxPendingRequests,
		pendingTimeout:      o.PendingTimeout,
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
	case o.MaxPendingRequests > 0 && o.MaxActiveRequests == 0:
		return fmt.Errorf("max_pending_requests requires max_active_requests: with no admission limit nothing ever queues")
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

// Bounded reports whether the policy constrains anything at all. Consumers use
// it to keep the completely-unconfigured path free of even a counter update.
func (p *Policy) Bounded() bool {
	return p.maxActiveRequests > 0 || p.maxActivePerBackend > 0
}
