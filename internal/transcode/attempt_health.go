// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"jul/internal/upstream"
)

// classifyGRPCAttempt attributes a gRPC error before the shared pool is
// mutated. Context and TLS ownership are decided by upstream first. Only a
// backend-owned result is then interpreted through the deliberately small
// application/protocol status policy already used by the transcoder.
func classifyGRPCAttempt(err error, inbound, attempt context.Context) upstream.AttemptClassification {
	// grpc status errors do not consistently unwrap to the context sentinels.
	// Normalize only the lifecycle codes so immutable deadline and preserved
	// context ownership participate in the shared classifier.
	classifiedErr := err
	switch status.Code(err) {
	case codes.Canceled:
		classifiedErr = context.Canceled
	case codes.DeadlineExceeded:
		classifiedErr = context.DeadlineExceeded
	}
	c := upstream.ClassifyAttemptError(classifiedErr, inbound, attempt)
	if c.Origin() != upstream.OriginBackendTransport {
		return c
	}

	code := status.Code(err)
	if code == codes.Canceled {
		// A bare cancellation with no independently canceled owner is ambiguous.
		// It cannot be used either to poison or to heal shared backend state.
		return upstream.JulPolicyFailure("")
	}
	if !isBackendFailure(code) {
		return upstream.BackendApplicationResult()
	}
	reason := upstream.ReasonUpstreamConnectFailed
	if code == codes.DeadlineExceeded {
		reason = upstream.ReasonUpstreamTimeout
	}
	return upstream.BackendProtocolFailure(reason)
}
