// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
)

// TestReasonMappingIsExhaustive walks the whole enum. A Reason added without a
// row here, or with a zero status it did not ask for, fails.
func TestReasonMappingIsExhaustive(t *testing.T) {
	want := map[Reason]struct {
		status int
		code   uint32
	}{
		ReasonUpstreamUnavailable:    {http.StatusServiceUnavailable, GRPCCodeUnavailable},
		ReasonCircuitOpen:            {http.StatusServiceUnavailable, GRPCCodeUnavailable},
		ReasonProxyOverloaded:        {http.StatusServiceUnavailable, GRPCCodeUnavailable},
		ReasonBackendAtCapacity:      {http.StatusServiceUnavailable, GRPCCodeUnavailable},
		ReasonUpstreamConnectFailed:  {http.StatusBadGateway, GRPCCodeUnavailable},
		ReasonUpstreamTimeout:        {http.StatusGatewayTimeout, GRPCCodeDeadlineExceeded},
		ReasonUpstreamTLSIdentity:    {http.StatusBadGateway, GRPCCodeUnavailable},
		ReasonRetryBudgetExhausted:   {StatusFromLastAttempt, 0},
		ReasonRetryDeadlineExhausted: {http.StatusGatewayTimeout, GRPCCodeDeadlineExceeded},
		ReasonRequestNotReplayable:   {StatusFromLastAttempt, 0},
		ReasonClientCancelled:        {StatusClientClosedRequest, GRPCCodeCancelled},
	}

	reasons := Reasons()
	if len(reasons) != len(want) {
		t.Fatalf("Reasons() has %d entries, the table here has %d", len(reasons), len(want))
	}
	seen := map[Reason]bool{}
	for _, r := range reasons {
		if seen[r] {
			t.Errorf("reason %q appears twice", r)
		}
		seen[r] = true
		if !r.Valid() {
			t.Errorf("reason %q is in Reasons() but not Valid()", r)
		}
		exp, ok := want[r]
		if !ok {
			t.Errorf("reason %q has no expected mapping; add it here deliberately", r)
			continue
		}
		if got := r.HTTPStatus(); got != exp.status {
			t.Errorf("%q HTTPStatus() = %d, want %d", r, got, exp.status)
		}
		if got := r.GRPCCode(); got != exp.code {
			t.Errorf("%q GRPCCode() = %d, want %d", r, got, exp.code)
		}
	}
}

// TestOverloadIsNotRateLimiting pins the one status choice most likely to be
// "corrected" later. 429 means the client sent too many requests and is already
// Jul's rate-limiter status; overload is not the client's fault. The gRPC
// consequence follows: RESOURCE_EXHAUSTED maps back to 429 and would contradict
// the HTTP path, so overload is UNAVAILABLE.
func TestOverloadIsNotRateLimiting(t *testing.T) {
	if got := ReasonProxyOverloaded.HTTPStatus(); got != http.StatusServiceUnavailable {
		t.Errorf("overload status = %d, want 503", got)
	}
	if got := ReasonProxyOverloaded.GRPCCode(); got != GRPCCodeUnavailable {
		t.Errorf("overload gRPC code = %d, want UNAVAILABLE (%d)", got, GRPCCodeUnavailable)
	}
	if !ReasonProxyOverloaded.RetryAfter() {
		t.Error("overload should advertise Retry-After")
	}
	for _, r := range Reasons() {
		if r != ReasonProxyOverloaded && r.RetryAfter() {
			t.Errorf("%q advertises Retry-After; only overload knows the condition is load-shaped", r)
		}
	}
}

// TestUnavailableAndCircuitOpenShareAStatusButNotAReason is the whole point of
// having a taxonomy separate from the status code.
func TestUnavailableAndCircuitOpenShareAStatusButNotAReason(t *testing.T) {
	if ReasonUpstreamUnavailable.HTTPStatus() != ReasonCircuitOpen.HTTPStatus() {
		t.Fatal("the two should look identical to a client")
	}
	if ReasonUpstreamUnavailable == ReasonCircuitOpen {
		t.Fatal("the two must never look identical to an operator")
	}
}

func TestReasonForClassifiesSentinels(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want Reason
	}{
		{"no error", nil, ""},
		{"overloaded", ErrOverloaded, ReasonProxyOverloaded},
		{"retired generation", ErrRetired, ReasonProxyOverloaded},
		{"at capacity", ErrBackendAtCapacity, ReasonBackendAtCapacity},
		{"no backend", ErrNoAvailableBackend, ReasonUpstreamUnavailable},
		{"wrapped sentinel", fmt.Errorf("dial: %w", ErrNoAvailableBackend), ReasonUpstreamUnavailable},
		{"deadline", context.DeadlineExceeded, ReasonUpstreamTimeout},
		{"plain transport failure", errors.New("connection refused"), ReasonUpstreamConnectFailed},
		{"unknown authority", x509.UnknownAuthorityError{}, ReasonUpstreamTLSIdentity},
		{"hostname mismatch", x509.HostnameError{Host: "wrong"}, ReasonUpstreamTLSIdentity},
		{"invalid certificate", x509.CertificateInvalidError{}, ReasonUpstreamTLSIdentity},
		{"verification error", &tls.CertificateVerificationError{}, ReasonUpstreamTLSIdentity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasonFor(tc.err, context.Background()); got != tc.want {
				t.Fatalf("ReasonFor(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestReasonForClassifiesNetTimeout(t *testing.T) {
	if got := ReasonFor(timeoutError{}, context.Background()); got != ReasonUpstreamTimeout {
		t.Fatalf("net.Error timeout = %q, want %q", got, ReasonUpstreamTimeout)
	}
	if got := ReasonFor(notTimeoutError{}, context.Background()); got != ReasonUpstreamConnectFailed {
		t.Fatalf("non-timeout net.Error = %q, want %q", got, ReasonUpstreamConnectFailed)
	}
}

// TestCancellationDistinguishesTheClientFromOurOwnDeadline is the distinction
// the 499 change depends on.
//
// context.Canceled reaches the classifier from both directions, because the
// retry driver derives its context from the inbound one and cancellation
// propagates with the same sentinel. Without the inbound check, Jul's own
// deadline enforcement would be recorded as clients disconnecting — and a
// dashboard of client cancellations would silently be measuring Jul.
func TestCancellationDistinguishesTheClientFromOurOwnDeadline(t *testing.T) {
	clientGone, cancel := context.WithCancel(context.Background())
	cancel()

	if got := ReasonFor(context.Canceled, clientGone); got != ReasonClientCancelled {
		t.Errorf("cancelled inbound context = %q, want %q", got, ReasonClientCancelled)
	}
	if got := ReasonFor(context.Canceled, context.Background()); got != ReasonRetryDeadlineExhausted {
		t.Errorf("cancellation with a live client = %q, want %q", got, ReasonRetryDeadlineExhausted)
	}
	// A caller with no client context at all (the active health checker) must
	// not be reported as a client disconnecting.
	if got := ReasonFor(context.Canceled, nil); got != ReasonRetryDeadlineExhausted {
		t.Errorf("cancellation with no inbound context = %q, want %q", got, ReasonRetryDeadlineExhausted)
	}
}

// TestClientCancellationRecords499NotGatewayTimeout documents the intentional
// compatibility change. Recording 504 for a client that had already
// disconnected inflated "gateway timeout" with cases where nothing timed out,
// corrupting the dashboards this taxonomy exists to make trustworthy.
func TestClientCancellationRecords499NotGatewayTimeout(t *testing.T) {
	if got := ReasonClientCancelled.HTTPStatus(); got != 499 {
		t.Fatalf("client cancellation status = %d, want 499", got)
	}
	if ReasonClientCancelled.HTTPStatus() == http.StatusGatewayTimeout {
		t.Fatal("client cancellation must no longer be recorded as a gateway timeout")
	}
}

func TestReasonForStopUsesTheDriversVerdict(t *testing.T) {
	live := context.Background()
	gone, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name    string
		stop    StopReason
		err     error
		inbound context.Context
		want    Reason
	}{
		{"budget", StopBudget, errors.New("boom"), live, ReasonRetryBudgetExhausted},
		{"not retryable", StopNotRetryable, errors.New("boom"), live, ReasonRequestNotReplayable},
		{"response started", StopResponseStarted, errors.New("boom"), live, ReasonRequestNotReplayable},
		{"deadline", StopDeadline, errors.New("boom"), live, ReasonRetryDeadlineExhausted},
		{"cancelled by the client", StopCancelled, context.Canceled, gone, ReasonClientCancelled},
		{"cancelled by us", StopCancelled, context.Canceled, live, ReasonRetryDeadlineExhausted},
		{"no backend keeps the underlying why", StopNoBackend, ErrBackendAtCapacity, live, ReasonBackendAtCapacity},
		{"no backend with no error", StopNoBackend, nil, live, ReasonUpstreamUnavailable},
		{"attempts exhausted falls back to the error", StopAttempts, ErrNoAvailableBackend, live, ReasonUpstreamUnavailable},
		{"terminal error falls back to the error", StopTerminalError, errors.New("boom"), live, ReasonUpstreamConnectFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReasonForStop(tc.stop, tc.err, tc.inbound); got != tc.want {
				t.Fatalf("ReasonForStop(%q, %v) = %q, want %q", tc.stop, tc.err, got, tc.want)
			}
		})
	}
}

// TestRetrySuppressionKeepsTheBackendsAnswer pins that retry accounting never
// overwrites the status a backend actually produced. Reporting 503 because a
// budget was spent would hide a 500 the client needs to see.
func TestRetrySuppressionKeepsTheBackendsAnswer(t *testing.T) {
	for _, r := range []Reason{ReasonRetryBudgetExhausted, ReasonRequestNotReplayable} {
		if got := r.HTTPStatus(); got != StatusFromLastAttempt {
			t.Errorf("%q HTTPStatus() = %d, want the last attempt's own status", r, got)
		}
	}
}

func TestInvalidReasonIsNotAMember(t *testing.T) {
	for _, r := range []Reason{"", "backend_10_0_0_1", "whatever"} {
		if r.Valid() {
			t.Errorf("%q reported as a member of the closed set", r)
		}
	}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

type notTimeoutError struct{}

func (notTimeoutError) Error() string   { return "connection refused" }
func (notTimeoutError) Timeout() bool   { return false }
func (notTimeoutError) Temporary() bool { return false }

var (
	_ net.Error = timeoutError{}
	_ net.Error = notTimeoutError{}
)
