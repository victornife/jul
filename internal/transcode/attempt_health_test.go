// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"jul/internal/upstream"
)

func TestGRPCAttemptAttributionMatrix(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelDeadline := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelDeadline()
	julDeadline, cancelJul := context.WithCancel(context.Background())
	cancelJul()

	tests := []struct {
		name    string
		err     error
		inbound context.Context
		attempt context.Context
		origin  upstream.FailureOrigin
		health  upstream.HealthConsequence
	}{
		{"caller cancellation", status.Error(codes.Canceled, "caller cancelled"), cancelled, cancelled, upstream.OriginClientCancellation, upstream.HealthNeutral},
		{"caller deadline", status.Error(codes.DeadlineExceeded, "deadline"), expired, expired, upstream.OriginClientDeadline, upstream.HealthNeutral},
		{"Jul deadline", status.Error(codes.DeadlineExceeded, "deadline"), context.Background(), julDeadline, upstream.OriginJulTimeout, upstream.HealthNeutral},
		{"backend unavailable", status.Error(codes.Unavailable, "unavailable"), context.Background(), context.Background(), upstream.OriginBackendProtocol, upstream.HealthFailure},
		{"backend deadline", status.Error(codes.DeadlineExceeded, "deadline"), context.Background(), context.Background(), upstream.OriginBackendProtocol, upstream.HealthFailure},
		{"application not found", status.Error(codes.NotFound, "missing"), context.Background(), context.Background(), upstream.OriginBackendApplication, upstream.HealthSuccess},
		{"backend application cancellation", status.Error(codes.Canceled, "server cancelled"), context.Background(), context.Background(), upstream.OriginBackendApplication, upstream.HealthSuccess},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyGRPCAttempt(tt.err, tt.inbound, tt.attempt)
			if got.Origin() != tt.origin || got.Health() != tt.health {
				t.Fatalf("classification = {%q %d}, want {%q %d}", got.Origin(), got.Health(), tt.origin, tt.health)
			}
		})
	}
}
