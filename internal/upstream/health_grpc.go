// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package upstream

import (
	"context"
	"crypto/tls"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"jul/internal/backendtls"
)

func init() {
	grpcHealthProbe = doProbeGRPCHealth
}

// doProbeGRPCHealth implements grpcHealthProbe for a build with the "grpc" tag.
//
// Trust reuses exactly the same decision live gRPC traffic makes (#427 §44):
// the backend is TLS iff its scheme is "https" (matching NewGRPCProxy's own
// tlsBackend test), and a non-nil policy supplies the resolved CA/SNI/mTLS
// material; a nil policy on a TLS backend keeps Go's default system-root
// verification rather than downgrading to plaintext. A non-TLS backend always
// uses insecure transport credentials (h2c) — never InsecureSkipVerify, which
// would silently disable identity verification on a backend that is supposed
// to have it.
//
// The connection is short-lived by design (#427 §48-49): opened, used for
// exactly one RPC, and closed before returning. A benchmark comparing this
// against a persistent per-backend ClientConn (health_grpc_bench_test.go)
// measured the short-lived dial+handshake+RPC+close round trip at roughly
// 1.2ms/835 allocs versus ~0.4ms/163 allocs for a reused connection on a local
// h2c backend — a few tenths of a millisecond of extra cost against the
// default 5s probe interval, and none of the extra lifecycle ownership
// (reload/discovery-churn/shutdown retirement) a cached connection would need.
// The simpler model was kept rather than building a second connection cache
// ahead of Wave 3's dedicated resource-ownership hardening (#428).
func doProbeGRPCHealth(ctx context.Context, b *Backend, service string, policy *backendtls.Policy) bool {
	var creds credentials.TransportCredentials
	if b.Scheme() == "https" {
		tlsCfg := &tls.Config{}
		if policy != nil {
			tlsCfg = policy.ClientConfig()
		}
		creds = credentials.NewTLS(tlsCfg)
	} else {
		creds = insecure.NewCredentials()
	}

	conn, err := grpc.NewClient("passthrough:///"+b.Address, grpc.WithTransportCredentials(creds))
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()

	client := healthpb.NewHealthClient(conn)
	resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{Service: service})
	if err != nil {
		// Covers RPC error, deadline exceeded, connection failure, TLS
		// failure, SERVICE_UNKNOWN/UNIMPLEMENTED (surfaced as a gRPC status
		// error) and any malformed response the generated client rejects.
		return false
	}
	return resp.GetStatus() == healthpb.HealthCheckResponse_SERVING
}
