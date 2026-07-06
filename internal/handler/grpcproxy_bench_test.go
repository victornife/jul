// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"testing"
)

// BenchmarkGRPCPassthroughUnary measures a unary gRPC call forwarded end to end
// through the passthrough proxy over h2c (client -> proxy -> backend).
func BenchmarkGRPCPassthroughUnary(b *testing.B) {
	backend := startGRPCEcho(b)
	front, _ := startGRPCProxyFront(b, backend)
	conn := dialGRPC(b, front)

	ctx := context.Background()
	req := []byte("hello gRPC")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var reply []byte
		if err := conn.Invoke(ctx, "/echo.Echo/Unary", req, &reply); err != nil {
			b.Fatalf("invoke: %v", err)
		}
	}
}

// BenchmarkGRPCDirectUnary is the baseline: the same unary call straight to the
// backend with no proxy in the path. The delta against
// BenchmarkGRPCPassthroughUnary is the passthrough proxy's per-call overhead.
func BenchmarkGRPCDirectUnary(b *testing.B) {
	backend := startGRPCEcho(b)
	conn := dialGRPC(b, backend)

	ctx := context.Background()
	req := []byte("hello gRPC")

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var reply []byte
		if err := conn.Invoke(ctx, "/echo.Echo/Unary", req, &reply); err != nil {
			b.Fatalf("invoke: %v", err)
		}
	}
}
