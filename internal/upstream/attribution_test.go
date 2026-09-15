// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"testing"
	"time"

	"jul/internal/config"
)

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
