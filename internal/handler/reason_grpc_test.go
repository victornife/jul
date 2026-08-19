// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"testing"

	"google.golang.org/grpc/codes"

	"jul/internal/upstream"
)

// TestReasonGRPCCodesMatchTheCodesPackage ties the bare numbers in
// internal/upstream to their meaning.
//
// The taxonomy stores gRPC codes as integers so that package stays stdlib-only
// and a lean build does not pull in gRPC to know what a reason means. That is
// the right trade, but it moves the risk to a transcription error, so the
// numbers are asserted here — in a build where the codes package exists.
func TestReasonGRPCCodesMatchTheCodesPackage(t *testing.T) {
	if got := codes.Code(upstream.GRPCCodeUnavailable); got != codes.Unavailable {
		t.Errorf("GRPCCodeUnavailable = %d (%s), want %d (%s)", upstream.GRPCCodeUnavailable, got, codes.Unavailable, codes.Unavailable)
	}
	if got := codes.Code(upstream.GRPCCodeDeadlineExceeded); got != codes.DeadlineExceeded {
		t.Errorf("GRPCCodeDeadlineExceeded = %d (%s), want %d (%s)", upstream.GRPCCodeDeadlineExceeded, got, codes.DeadlineExceeded, codes.DeadlineExceeded)
	}
	if got := codes.Code(upstream.GRPCCodeCancelled); got != codes.Canceled {
		t.Errorf("GRPCCodeCancelled = %d (%s), want %d (%s)", upstream.GRPCCodeCancelled, got, codes.Canceled, codes.Canceled)
	}
}

// TestOverloadIsNotResourceExhausted pins the consequence the taxonomy calls
// out: RESOURCE_EXHAUSTED maps back to 429, which is Jul's rate-limiter status
// and blames the client for a condition that is not theirs. Overload on any
// gRPC surface is UNAVAILABLE so the two protocol surfaces agree.
func TestOverloadIsNotResourceExhausted(t *testing.T) {
	got := codes.Code(upstream.ReasonProxyOverloaded.GRPCCode())
	if got == codes.ResourceExhausted {
		t.Fatal("overload maps to RESOURCE_EXHAUSTED, which httpStatusFromCode turns back into 429 and contradicts the 503 the HTTP path returns")
	}
	if got != codes.Unavailable {
		t.Fatalf("overload gRPC code = %s, want %s", got, codes.Unavailable)
	}
}

// TestEveryReasonHasAKnownGRPCCode walks the closed set so a reason added
// without a gRPC mapping cannot reach a gRPC surface as code 0 (OK).
func TestEveryReasonHasAKnownGRPCCode(t *testing.T) {
	for _, r := range upstream.Reasons() {
		code := codes.Code(r.GRPCCode())
		if r.HTTPStatus() == upstream.StatusFromLastAttempt {
			// These two carry the final attempt's own code by design.
			if code != codes.OK {
				t.Errorf("%q defers to the last attempt for HTTP but names gRPC code %s", r, code)
			}
			continue
		}
		if code == codes.OK {
			t.Errorf("reason %q would reach a gRPC client as OK", r)
		}
	}
}
