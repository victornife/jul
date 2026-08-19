// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
)

// Reason is the closed set of operator-facing explanations for an upstream
// failure.
//
// It lives here, in the lowest package every consumer already depends on, so
// internal/handler, internal/transcode, internal/stream, internal/auth,
// internal/admin and internal/observability all import downward. Putting it in
// internal/handler would force internal/stream to import internal/handler — a
// layering inversion, and an expensive one to undo.
//
// A Reason is safe as a metric label and as an access-log field: the set is
// closed and fixed at compile time. A backend address, route path, tenant
// identifier, hostname or raw error text is never one of these values. Logs may
// carry unbounded strings; metrics may not.
type Reason string

const (
	// ReasonUpstreamUnavailable means no backend was eligible — the pool is
	// empty, discovery has not resolved one, or active health ejected them all.
	ReasonUpstreamUnavailable Reason = "upstream_unavailable"
	// ReasonCircuitOpen means every candidate's circuit is open. It is a 503 to
	// a client exactly like upstream_unavailable, and is never conflated with it
	// for an operator: one says the backends are gone, the other says Jul is
	// deliberately not calling them.
	ReasonCircuitOpen Reason = "circuit_open"
	// ReasonProxyOverloaded means admission rejected the request.
	ReasonProxyOverloaded Reason = "proxy_overloaded"
	// ReasonBackendAtCapacity means every candidate is at max_active_per_backend.
	ReasonBackendAtCapacity Reason = "backend_at_capacity"
	// ReasonUpstreamConnectFailed means a dial, handshake or transport failure.
	ReasonUpstreamConnectFailed Reason = "upstream_connect_failed"
	// ReasonUpstreamTimeout means a per-attempt or overall timeout elapsed.
	ReasonUpstreamTimeout Reason = "upstream_timeout"
	// ReasonUpstreamTLSIdentity means the backend failed to prove its identity.
	// It is deterministic and therefore never retried.
	ReasonUpstreamTLSIdentity Reason = "upstream_tls_identity"
	// ReasonRetryBudgetExhausted means retries were suppressed by the pool's
	// budget. The client sees the last attempt's own status.
	ReasonRetryBudgetExhausted Reason = "retry_budget_exhausted"
	// ReasonRetryDeadlineExhausted means the overall retry deadline was consumed.
	ReasonRetryDeadlineExhausted Reason = "retry_deadline_exhausted"
	// ReasonRequestNotReplayable means the method, body or an already-started
	// response forbade another attempt. The client sees the last attempt's status.
	ReasonRequestNotReplayable Reason = "request_not_replayable"
	// ReasonClientCancelled means the inbound request context was cancelled: the
	// client went away.
	ReasonClientCancelled Reason = "client_cancelled"
)

// StatusClientClosedRequest is nginx's 499. It is not an IANA status and is only
// ever recorded, never negotiated — by the time it is chosen the client has
// already disconnected, so nothing is transmitted either way.
const StatusClientClosedRequest = 499

// StatusFromLastAttempt is returned for the reasons whose client-facing status
// is whatever the final attempt produced. Retry suppression is an operator
// concern; it must not overwrite the answer the backend actually gave.
const StatusFromLastAttempt = 0

// gRPC status codes, by number, so this package stays stdlib-only and a lean
// build does not pull in gRPC to know what a reason means. The numbers are fixed
// by the gRPC specification; the grpc-tagged test asserts each one against the
// codes package so a typo cannot survive.
const (
	GRPCCodeCancelled        uint32 = 1
	GRPCCodeDeadlineExceeded uint32 = 4
	GRPCCodeUnavailable      uint32 = 14

	// grpcCodeFromLastAttempt pairs with StatusFromLastAttempt.
	grpcCodeFromLastAttempt uint32 = 0
)

// reasonTable is the single source for both mappings, so an added Reason cannot
// acquire an HTTP status and silently keep a zero gRPC code.
var reasonTable = []struct {
	reason Reason
	status int
	code   uint32
}{
	{ReasonUpstreamUnavailable, http.StatusServiceUnavailable, GRPCCodeUnavailable},
	{ReasonCircuitOpen, http.StatusServiceUnavailable, GRPCCodeUnavailable},
	// 503, not 429: 429 means the *client* sent too many requests and is already
	// Jul's rate-limiter status, whereas overload is not the client's fault. The
	// gRPC consequence is UNAVAILABLE rather than RESOURCE_EXHAUSTED, because
	// RESOURCE_EXHAUSTED maps back to 429 and would contradict the HTTP path.
	{ReasonProxyOverloaded, http.StatusServiceUnavailable, GRPCCodeUnavailable},
	{ReasonBackendAtCapacity, http.StatusServiceUnavailable, GRPCCodeUnavailable},
	{ReasonUpstreamConnectFailed, http.StatusBadGateway, GRPCCodeUnavailable},
	{ReasonUpstreamTimeout, http.StatusGatewayTimeout, GRPCCodeDeadlineExceeded},
	{ReasonUpstreamTLSIdentity, http.StatusBadGateway, GRPCCodeUnavailable},
	{ReasonRetryBudgetExhausted, StatusFromLastAttempt, grpcCodeFromLastAttempt},
	{ReasonRetryDeadlineExhausted, http.StatusGatewayTimeout, GRPCCodeDeadlineExceeded},
	{ReasonRequestNotReplayable, StatusFromLastAttempt, grpcCodeFromLastAttempt},
	{ReasonClientCancelled, StatusClientClosedRequest, GRPCCodeCancelled},
}

// Reasons returns every Reason, in taxonomy order. Callers that must enumerate
// the set — the metric label-set test above all — use this rather than repeating
// the list, so the enum has exactly one definition.
func Reasons() []Reason {
	out := make([]Reason, 0, len(reasonTable))
	for _, e := range reasonTable {
		out = append(out, e.reason)
	}
	return out
}

// Valid reports whether r is a member of the closed set.
func (r Reason) Valid() bool {
	for _, e := range reasonTable {
		if e.reason == r {
			return true
		}
	}
	return false
}

// HTTPStatus returns the client-facing status for a reason, or
// StatusFromLastAttempt when the final attempt's own status stands.
//
// The client status and the operator reason are different resolutions of the
// same event: four reasons share 503 and are never merged for an operator.
func (r Reason) HTTPStatus() int {
	for _, e := range reasonTable {
		if e.reason == r {
			return e.status
		}
	}
	return http.StatusBadGateway
}

// GRPCCode returns the gRPC status code number for a reason, or
// grpcCodeFromLastAttempt when the final attempt's own code stands.
func (r Reason) GRPCCode() uint32 {
	for _, e := range reasonTable {
		if e.reason == r {
			return e.code
		}
	}
	return GRPCCodeUnavailable
}

// RetryAfter reports whether a response carrying this reason should advertise
// Retry-After. Only overload does: it is the one reason Jul knows the condition
// is transient and load-shaped.
func (r Reason) RetryAfter() bool { return r == ReasonProxyOverloaded }

// ReasonFor classifies a failure.
//
// inbound is the *client's* request context, and it is the only way to tell a
// client that went away from Jul's own machinery giving up. context.Canceled
// arrives here from both: the retry driver derives a context from the inbound
// one, so its cancellation propagates with the same sentinel. Without this
// distinction the retry deadline added in #142 would be reported as client
// cancellation, and a dashboard of "clients disconnecting" would actually be
// measuring Jul's own timeouts.
//
// inbound may be nil for callers with no client context, such as the active
// health checker.
func ReasonFor(err error, inbound context.Context) Reason {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrOverloaded), errors.Is(err, ErrRetired):
		return ReasonProxyOverloaded
	case errors.Is(err, ErrBackendAtCapacity):
		return ReasonBackendAtCapacity
	case errors.Is(err, ErrNoAvailableBackend):
		return ReasonUpstreamUnavailable
	}
	if tlsIdentityFailure(err) {
		return ReasonUpstreamTLSIdentity
	}
	if errors.Is(err, context.Canceled) {
		if inbound != nil && errors.Is(inbound.Err(), context.Canceled) {
			return ReasonClientCancelled
		}
		// A cancellation that the client did not cause is Jul abandoning the
		// attempt itself, which is a deadline in every path that produces one.
		return ReasonRetryDeadlineExhausted
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ReasonUpstreamTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ReasonUpstreamTimeout
	}
	return ReasonUpstreamConnectFailed
}

// ReasonForStop refines a classification with the retry driver's own verdict,
// which knows things the error alone cannot express — that a budget was spent,
// or that the request was never replayable.
func ReasonForStop(stop StopReason, err error, inbound context.Context) Reason {
	switch stop {
	case StopBudget:
		return ReasonRetryBudgetExhausted
	case StopNotRetryable, StopResponseStarted:
		return ReasonRequestNotReplayable
	case StopDeadline:
		return ReasonRetryDeadlineExhausted
	case StopCancelled:
		// Still checked against the inbound context: StopCancelled is reached
		// from any cancellation, including one of Jul's own.
		return ReasonFor(context.Canceled, inbound)
	case StopNoBackend:
		// The driver reports "no untried backend" for an exhausted candidate set,
		// but the underlying error says *why* none was eligible.
		if r := ReasonFor(err, inbound); r != "" {
			return r
		}
		return ReasonUpstreamUnavailable
	}
	return ReasonFor(err, inbound)
}

// tlsIdentityFailure reports whether err is a backend failing to prove its
// identity, as opposed to any other handshake or transport failure. It is
// deterministic: retrying the same backend produces the same answer.
func tlsIdentityFailure(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidCert x509.CertificateInvalidError
	var verification *tls.CertificateVerificationError
	return errors.As(err, &unknownAuthority) ||
		errors.As(err, &hostnameErr) ||
		errors.As(err, &invalidCert) ||
		errors.As(err, &verification)
}
