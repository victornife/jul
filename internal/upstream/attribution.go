// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"net"
	"time"
)

// FailureOrigin is the closed set of owners for one upstream attempt result.
//
// It is deliberately separate from Reason. Reason is what an operator sees;
// origin decides whether the result is evidence about shared backend health.
// Several origins can map to the same client-facing status without acquiring
// the same circuit consequence.
type FailureOrigin string

const (
	OriginSuccess            FailureOrigin = "success"
	OriginClientCancellation FailureOrigin = "client_cancellation"
	OriginClientDeadline     FailureOrigin = "client_deadline"
	OriginJulTimeout         FailureOrigin = "jul_timeout"
	OriginJulPolicy          FailureOrigin = "jul_policy"
	OriginBackendTransport   FailureOrigin = "backend_transport"
	OriginBackendIdentity    FailureOrigin = "backend_identity"
	OriginBackendProtocol    FailureOrigin = "backend_protocol"
	OriginBackendApplication FailureOrigin = "backend_application"
)

// HealthConsequence says how an attempt result changes passive backend health.
// The zero value is neutral so an unclassified result can never poison a
// circuit accidentally.
type HealthConsequence uint8

const (
	HealthNeutral HealthConsequence = iota
	HealthFailure
	HealthSuccess
)

// AttemptClassification binds one result's origin and bounded reason to its
// passive-health consequence. Fields are private so callers must use the
// constructors below rather than assembling contradictory combinations.
type AttemptClassification struct {
	origin FailureOrigin
	reason Reason
	health HealthConsequence
}

// Origin reports who owns the result.
func (c AttemptClassification) Origin() FailureOrigin { return c.origin }

// Reason reports the bounded operator-facing reason, or "" on success and
// application-level backend results whose own response remains authoritative.
func (c AttemptClassification) Reason() Reason { return c.reason }

// Health reports the passive-health consequence.
func (c AttemptClassification) Health() HealthConsequence { return c.health }

// SuccessfulAttempt reports a completed backend exchange.
func SuccessfulAttempt() AttemptClassification {
	return AttemptClassification{origin: OriginSuccess, health: HealthSuccess}
}

// BackendApplicationResult reports an application-level answer from a backend.
// It is circuit success because the backend demonstrably completed the
// transport/protocol exchange; the application's status is not a health fault.
func BackendApplicationResult() AttemptClassification {
	return AttemptClassification{origin: OriginBackendApplication, health: HealthSuccess}
}

// ClientCancellationResult reports a downstream write/stream termination that
// is known to belong to the client even when its context has not propagated the
// cancellation sentinel yet.
func ClientCancellationResult() AttemptClassification {
	return AttemptClassification{
		origin: OriginClientCancellation,
		reason: ReasonClientCancelled,
		health: HealthNeutral,
	}
}

// JulPolicyFailure reports a local validation, replay or policy decision made
// after backend selection but before a backend-owned result existed.
func JulPolicyFailure(reason Reason) AttemptClassification {
	return AttemptClassification{origin: OriginJulPolicy, reason: reason, health: HealthNeutral}
}

// BackendProtocolFailure reports a malformed or explicitly unhealthy backend
// protocol result. The reason must come from the closed upstream taxonomy.
func BackendProtocolFailure(reason Reason) AttemptClassification {
	if !reason.Valid() {
		reason = ReasonUpstreamConnectFailed
	}
	return AttemptClassification{
		origin: OriginBackendProtocol,
		reason: reason,
		health: HealthFailure,
	}
}

// ClassifyAttemptError attributes an error before any shared health state is
// mutated. inbound is the caller/client context; attempt is the context Jul
// supplied to the backend operation, which may carry Jul's shorter retry
// deadline. The order is intentional: a backend operation commonly returns the
// same context sentinel after its parent was cancelled, so ownership must be
// read from the contexts before the raw error is classified.
func ClassifyAttemptError(err error, inbound, attempt context.Context) AttemptClassification {
	if err == nil {
		return SuccessfulAttempt()
	}

	if inbound != nil {
		switch {
		case errors.Is(inbound.Err(), context.Canceled):
			return AttemptClassification{
				origin: OriginClientCancellation,
				reason: ReasonClientCancelled,
				health: HealthNeutral,
			}
		case errors.Is(inbound.Err(), context.DeadlineExceeded):
			return AttemptClassification{
				origin: OriginClientDeadline,
				reason: ReasonClientDeadline,
				health: HealthNeutral,
			}
		}
	}

	// A child transport can enforce the same deadline with its own timer and
	// return DeadlineExceeded just before the parent context's timer goroutine
	// publishes Err(). Deadline values are immutable, so use them to make this
	// boundary deterministic instead of letting scheduler order blame a backend.
	if errors.Is(err, context.DeadlineExceeded) {
		now := time.Now()
		if deadlineReached(inbound, now) {
			return AttemptClassification{
				origin: OriginClientDeadline,
				reason: ReasonClientDeadline,
				health: HealthNeutral,
			}
		}
		if deadlineReached(attempt, now) {
			return AttemptClassification{
				origin: OriginJulTimeout,
				reason: ReasonRetryDeadlineExhausted,
				health: HealthNeutral,
			}
		}
	}

	if attempt != nil && attempt.Err() != nil {
		return AttemptClassification{
			origin: OriginJulTimeout,
			reason: ReasonRetryDeadlineExhausted,
			health: HealthNeutral,
		}
	}

	// A route-level trust policy can differ between two consumers sharing one
	// pool. A certificate mismatch is therefore a configuration/trust result,
	// not evidence that the shared backend is unavailable. Pool-level active
	// probes retain their own, separate unhealthy verdict.
	if tlsIdentityFailure(err) {
		return AttemptClassification{
			origin: OriginBackendIdentity,
			reason: ReasonUpstreamTLSIdentity,
			health: HealthNeutral,
		}
	}

	reason := ReasonUpstreamConnectFailed
	if errors.Is(err, context.DeadlineExceeded) {
		reason = ReasonUpstreamTimeout
	} else {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			reason = ReasonUpstreamTimeout
		}
	}
	return AttemptClassification{
		origin: OriginBackendTransport,
		reason: reason,
		health: HealthFailure,
	}
}

func deadlineReached(ctx context.Context, now time.Time) bool {
	if ctx == nil {
		return false
	}
	deadline, ok := ctx.Deadline()
	return ok && !now.Before(deadline)
}

// ClassifyProtocolError applies the same ownership checks as a transport error
// and then refines a genuine backend-owned failure to protocol origin.
func ClassifyProtocolError(err error, inbound, attempt context.Context) AttemptClassification {
	c := ClassifyAttemptError(err, inbound, attempt)
	if c.origin == OriginBackendTransport {
		c.origin = OriginBackendProtocol
	}
	return c
}

// RecordAttempt is the only adapter-facing mutation seam for passive health.
// A neutral half-open result returns its probe allowance without crediting or
// blaming the backend; closed-state neutral results are no-ops.
func (p *Pool) RecordAttempt(at Attempt, c AttemptClassification) bool {
	switch c.health {
	case HealthFailure:
		return p.MarkFailure(at)
	case HealthSuccess:
		return p.MarkSuccess(at)
	default:
		p.ReleaseProbe(at)
		return false
	}
}
