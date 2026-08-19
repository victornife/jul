// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"jul/internal/config"
	"jul/internal/resilience"
)

// Scenario 8: a gRPC stream alive for hours.
//
// The risk in a long-lived stream is not the first frame, it is drift: an
// accounting bug that charges per frame instead of per stream is invisible on a
// unary call and unbounded on a stream that runs for hours. A multi-hour stream
// cannot be run in a test, so what is pinned is the property that would make one
// wrong — the count must not move as frames flow — plus survival of the policy
// reload such a stream is certain to outlive.
func TestScenarioLongLivedStreamAccounting(t *testing.T) {
	backend := startGRPCEcho(t)
	front, adm := startAdmittedGRPCFront(t, backend, &config.ResilienceConfig{MaxActiveRequests: 4})
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/echo.Echo/Bidi")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}

	for i := range 50 {
		if err := stream.SendMsg([]byte("frame")); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
		var got []byte
		if err := stream.RecvMsg(&got); err != nil {
			t.Fatalf("recv %d: %v", i, err)
		}
		if n := adm.Active(); n != 1 {
			t.Fatalf("active after %d frames = %d, want 1; the slot is charged per frame, not per stream", i+1, n)
		}
	}

	// A reload during the stream's life must not disturb it. Rebuilding
	// admission here would either lose the stream's slot or double-count it on
	// the next frame.
	pol, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 8})
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	adm.SetPolicy(pol)

	if n := adm.Active(); n != 1 {
		t.Fatalf("active across a policy reload = %d, want the stream's 1", n)
	}
	if err := stream.SendMsg([]byte("after-reload")); err != nil {
		t.Fatalf("send after reload: %v", err)
	}
	var got []byte
	if err := stream.RecvMsg(&got); err != nil {
		t.Fatalf("recv after reload: %v", err)
	}
	if string(got) != "after-reload" {
		t.Fatalf("echo after reload = %q", got)
	}
	if n := adm.Active(); n != 1 {
		t.Fatalf("active after the reload and another frame = %d, want 1", n)
	}

	_ = stream.CloseSend()
	var drain []byte
	for stream.RecvMsg(&drain) == nil {
	}
	waitAdmissionIdle(t, adm)
}
