// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"testing/iotest"
	"time"

	"jul/internal/config"
)

// descriptorsExhausted is the errno a dial returns when the process has no
// descriptors left.
func descriptorsExhausted() syscall.Errno {
	if runtime.GOOS == "windows" {
		return syscall.Errno(10024) // WSAEMFILE
	}
	return syscall.EMFILE
}

type deadlineOnlyContext struct {
	context.Context
	deadline time.Time
}

func (c deadlineOnlyContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestWatchInboundBodyAbsent(t *testing.T) {
	for _, body := range []io.ReadCloser{nil, http.NoBody} {
		req := &http.Request{Body: body}
		if got := WatchInboundBody(req); got != nil || req.Body != body {
			t.Fatalf("absent body was replaced: watcher=%v body=%v", got, req.Body)
		}
	}
}

func TestInboundBodyCleanRead(t *testing.T) {
	transportErr := errors.New("write tcp: broken pipe")
	var absent *InboundBody
	if got := absent.Attribute(transportErr); got != transportErr {
		t.Fatalf("absent watcher changed the transport error: %v", got)
	}

	body := io.NopCloser(strings.NewReader("fine"))
	req := &http.Request{Body: body}
	watch := WatchInboundBody(req)
	if watch == nil || req.Body != watch || watch.ReadCloser != body {
		t.Fatal("request body was not wrapped around the original reader")
	}
	if got := watch.Attribute(transportErr); got != transportErr {
		t.Fatalf("unread body changed the transport error: %v", got)
	}
	data, err := io.ReadAll(req.Body)
	if err != nil || string(data) != "fine" {
		t.Fatalf("read = %q, %v, want fine without error", data, err)
	}
	if got := watch.Attribute(transportErr); got != transportErr {
		t.Fatalf("clean body or EOF blamed the client: %v", got)
	}
	if got := watch.Attribute(nil); got != nil {
		t.Fatalf("successful transport acquired an error: %v", got)
	}
	if err := req.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInboundBodyAttribution(t *testing.T) {
	for _, cause := range []error{errors.New("invalid byte in chunk length"), &http.MaxBytesError{Limit: 1024}} {
		t.Run(cause.Error(), func(t *testing.T) {
			req := &http.Request{Body: io.NopCloser(iotest.ErrReader(cause))}
			watch := WatchInboundBody(req)
			if _, err := io.ReadAll(req.Body); err != cause {
				t.Fatalf("read error = %v, want %v", err, cause)
			}
			transportErr := errors.New("write tcp: broken pipe")
			attributed := watch.Attribute(transportErr)
			var bodyErr *ClientBodyError
			if !errors.As(attributed, &bodyErr) || bodyErr.Err != cause {
				t.Fatalf("attributed error = %v, want client body cause %v", attributed, cause)
			}
			if !errors.Is(attributed, cause) || attributed.Error() != "read inbound request body: "+cause.Error() {
				t.Fatalf("client body error lost its cause or message: %v", attributed)
			}
			wrapped := fmt.Errorf("transport attempt: %w", attributed)
			classification := ClassifyAttemptError(wrapped, nil, nil)
			if classification.Origin() != OriginClientRequest || classification.Health() != HealthNeutral || classification.Reason() != ReasonClientRequestBody {
				t.Fatalf("classification = {%q %q %d}, want client_request/client_request_body/neutral", classification.Origin(), classification.Reason(), classification.Health())
			}
			if got := ReasonFor(wrapped, nil); got != ReasonClientRequestBody {
				t.Fatalf("reason = %q, want client_request_body", got)
			}
			if got := watch.Attribute(nil); got != nil {
				t.Fatalf("body failure invented a successful transport error: %v", got)
			}
		})
	}
}

func TestInboundBodyRetainsFirstError(t *testing.T) {
	first := errors.New("first body failure")
	second := errors.New("second body failure")
	req := &http.Request{Body: io.NopCloser(iotest.ErrReader(first))}
	watch := WatchInboundBody(req)
	buffer := make([]byte, 4)
	if _, err := watch.Read(buffer); err != first {
		t.Fatalf("first read = %v", err)
	}
	watch.ReadCloser = io.NopCloser(iotest.ErrReader(second))
	if _, err := watch.Read(buffer); err != second {
		t.Fatalf("second read = %v", err)
	}
	watch.ReadCloser = io.NopCloser(strings.NewReader(""))
	if _, err := watch.Read(buffer); err != io.EOF {
		t.Fatalf("final read = %v, want EOF", err)
	}
	attributed := watch.Attribute(errors.New("transport failed"))
	if !errors.Is(attributed, first) || errors.Is(attributed, second) {
		t.Fatalf("later reads replaced the first failure: %v", attributed)
	}
}

func TestInboundBodyCancellationPrecedence(t *testing.T) {
	cause := &ClientBodyError{Err: errors.New("client upload failed")}
	client, cancel := context.WithCancel(context.Background())
	cancel()
	classification := ClassifyAttemptError(cause, client, nil)
	if classification.Origin() != OriginClientCancellation || classification.Health() != HealthNeutral || classification.Reason() != ReasonClientCancelled {
		t.Fatalf("cancelled upload classification = {%q %q %d}", classification.Origin(), classification.Reason(), classification.Health())
	}
	if got := ReasonFor(cause, client); got != ReasonClientCancelled {
		t.Fatalf("cancelled upload reason = %q, want client_cancelled", got)
	}
}

func TestAttemptFailureAttributionMatrix(t *testing.T) {
	clientCancelled, cancelClient := context.WithCancel(context.Background())
	cancelClient()

	clientDeadline, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()

	julAttempt, cancelAttempt := context.WithCancel(context.Background())
	cancelAttempt()

	tests := []struct {
		name    string
		err     error
		inbound context.Context
		attempt context.Context
		origin  FailureOrigin
		reason  Reason
		health  HealthConsequence
	}{
		{"success", nil, context.Background(), context.Background(), OriginSuccess, "", HealthSuccess},
		{"client cancellation", context.Canceled, clientCancelled, clientCancelled, OriginClientCancellation, ReasonClientCancelled, HealthNeutral},
		{"client deadline", context.DeadlineExceeded, clientDeadline, clientDeadline, OriginClientDeadline, ReasonClientDeadline, HealthNeutral},
		{"Jul retry deadline", context.Canceled, context.Background(), julAttempt, OriginJulTimeout, ReasonRetryDeadlineExhausted, HealthNeutral},
		{"backend identity", x509.HostnameError{Host: "wrong.internal"}, context.Background(), context.Background(), OriginBackendIdentity, ReasonUpstreamTLSIdentity, HealthNeutral},
		{"backend timeout", context.DeadlineExceeded, context.Background(), context.Background(), OriginBackendTransport, ReasonUpstreamTimeout, HealthFailure},
		{"backend transport", errors.New("connection reset"), context.Background(), context.Background(), OriginBackendTransport, ReasonUpstreamConnectFailed, HealthFailure},
		{"Jul out of descriptors", &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("socket", descriptorsExhausted())}, context.Background(), context.Background(), OriginJulPolicy, ReasonProxyOverloaded, HealthNeutral},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyAttemptError(tt.err, tt.inbound, tt.attempt)
			if got.Origin() != tt.origin || got.Reason() != tt.reason || got.Health() != tt.health {
				t.Fatalf("classification = {%q %q %d}, want {%q %q %d}", got.Origin(), got.Reason(), got.Health(), tt.origin, tt.reason, tt.health)
			}
		})
	}
}

func TestDeadlinePrecedenceDoesNotDependOnErrPublication(t *testing.T) {
	past := time.Now().Add(-time.Second)
	live := context.Background()

	client := deadlineOnlyContext{Context: live, deadline: past}
	got := ClassifyAttemptError(context.DeadlineExceeded, client, client)
	if got.Origin() != OriginClientDeadline || got.Health() != HealthNeutral {
		t.Fatalf("unpublished client deadline = {%q %d}, want client/neutral", got.Origin(), got.Health())
	}

	attempt := deadlineOnlyContext{Context: live, deadline: past}
	got = ClassifyAttemptError(context.DeadlineExceeded, live, attempt)
	if got.Origin() != OriginJulTimeout || got.Health() != HealthNeutral {
		t.Fatalf("unpublished Jul deadline = {%q %d}, want Jul/neutral", got.Origin(), got.Health())
	}
}

func TestProtocolClassificationPreservesLifecycleOwnership(t *testing.T) {
	client, cancel := context.WithCancel(context.Background())
	cancel()
	got := ClassifyProtocolError(context.Canceled, client, client)
	if got.Origin() != OriginClientCancellation || got.Health() != HealthNeutral {
		t.Fatalf("client protocol cancellation = {%q %d}, want client/neutral", got.Origin(), got.Health())
	}

	got = ClassifyProtocolError(errors.New("malformed response"), context.Background(), context.Background())
	if got.Origin() != OriginBackendProtocol || got.Health() != HealthFailure {
		t.Fatalf("backend protocol error = {%q %d}, want backend_protocol/failure", got.Origin(), got.Health())
	}
}

func TestClientCancellationNeverTripsSharedBackendHealth(t *testing.T) {
	for _, maxFails := range []int{1, 3} {
		t.Run(fmt.Sprintf("max_fails_%d", maxFails), func(t *testing.T) {
			p := pool(t, "round_robin", config.UpstreamServer{Address: "healthy:80", Weight: 1})
			p.setCircuitLimits(circuitParams{maxFails: maxFails, failTimeout: time.Hour, halfOpenProbes: 1})
			b := p.Backends()[0]

			for i := 0; i < maxFails+2; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				at, err := p.Pick()
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				p.RecordAttempt(at, ClassifyAttemptError(context.Canceled, ctx, ctx))
				p.Release(at.Backend)
			}
			if b.FailCount() != 0 || !b.Available() {
				t.Fatalf("client cancellations changed backend health: fails=%d available=%t", b.FailCount(), b.Available())
			}

			// Paired control: genuine backend transport failures must still trip
			// at this exact threshold.
			for i := 0; i < maxFails; i++ {
				at, err := p.Pick()
				if err != nil {
					t.Fatal(err)
				}
				tripped := p.RecordAttempt(at, ClassifyAttemptError(errors.New("connection reset"), context.Background(), context.Background()))
				p.Release(at.Backend)
				if tripped != (i == maxFails-1) {
					t.Fatalf("failure %d tripped=%t, want %t", i+1, tripped, i == maxFails-1)
				}
			}
			if b.Available() {
				t.Fatal("backend remained available after genuine transport failures reached the threshold")
			}
		})
	}
}

func TestNeutralHalfOpenAttemptReturnsProbeSlot(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "healthy:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	b := p.Backends()[0]
	clock := fakeBackendClock(b)

	failed := admitOn(t, b)
	p.RecordAttempt(failed, BackendProtocolFailure(ReasonUpstreamConnectFailed))
	clock.advance(time.Second)

	probe, err := p.Pick()
	if err != nil {
		t.Fatalf("pick half-open probe: %v", err)
	}
	if !probe.adm.probe {
		t.Fatal("recovery attempt was not admitted as a probe")
	}
	p.RecordAttempt(probe, ClientCancellationResult())
	p.Release(probe.Backend)

	next, err := p.Pick()
	if err != nil {
		t.Fatalf("neutral result did not return the half-open slot: %v", err)
	}
	if !next.adm.probe {
		t.Fatal("returned slot was not reusable as a half-open probe")
	}
	p.RecordAttempt(next, SuccessfulAttempt())
	p.Release(next.Backend)
	if !b.Available() {
		t.Fatal("successful replacement probe did not close the circuit")
	}
}

func TestBackendApplicationResultClosesHalfOpenCircuit(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "healthy:80", Weight: 1})
	p.setCircuitLimits(circuitParams{maxFails: 1, failTimeout: time.Second, halfOpenProbes: 1})
	b := p.Backends()[0]
	clock := fakeBackendClock(b)
	p.RecordAttempt(admitOn(t, b), BackendProtocolFailure(ReasonUpstreamConnectFailed))
	clock.advance(time.Second)

	probe, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	if !p.RecordAttempt(probe, BackendApplicationResult()) {
		t.Fatal("application response did not report half-open recovery")
	}
	p.Release(probe.Backend)
	if !b.Available() {
		t.Fatal("application response did not return the backend to rotation")
	}
}
