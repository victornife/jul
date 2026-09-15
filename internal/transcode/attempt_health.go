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
	c := upstream.ClassifyAttemptError(err, inbound, attempt)
	if c.Origin() != upstream.OriginBackendTransport {
		return c
	}

	code := status.Code(err)
	if !isBackendFailure(code) {
		return upstream.BackendApplicationResult()
	}
	reason := upstream.ReasonUpstreamConnectFailed
	if code == codes.DeadlineExceeded {
		reason = upstream.ReasonUpstreamTimeout
	}
	return upstream.BackendProtocolFailure(reason)
}
