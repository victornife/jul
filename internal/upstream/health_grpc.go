// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package upstream

import (
	"context"
	"crypto/tls"
	"net"

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
func doProbeGRPCHealth(ctx context.Context, b *Backend, service string, policy *backendtls.Policy, authority string) bool {
	var creds credentials.TransportCredentials
	if b.Scheme() == "https" {
		tlsCfg := &tls.Config{}
		if policy != nil {
			tlsCfg = policy.ClientConfig()
		}
		if authority != "" && tlsCfg.ServerName == "" {
			tlsCfg.ServerName = b.URL.Hostname()
		}
		creds = credentials.NewTLS(tlsCfg)
		if authority != "" {
			creds = probeRoutingCredentials{TransportCredentials: creds, serverName: tlsCfg.ServerName}
		}
	} else {
		creds = insecure.NewCredentials()
	}

	dialOpts := []grpc.DialOption{grpc.WithTransportCredentials(creds)}
	if authority != "" {
		dialOpts = append(dialOpts, grpc.WithAuthority(authority))
	}
	conn, err := grpc.NewClient("passthrough:///"+b.Address, dialOpts...)
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

// probeRoutingCredentials separates HTTP/2 routing authority from TLS identity.
// The embedded credentials retain the explicit live-traffic ServerName and do
// the full handshake/verification. Only the informational override is cleared
// so grpc.WithAuthority does not reject an intentionally different route name.
// No certificate checks are disabled, and Clone retains the same boundary.
type probeRoutingCredentials struct {
	credentials.TransportCredentials
	serverName string
}

func (c probeRoutingCredentials) Info() credentials.ProtocolInfo {
	info := c.TransportCredentials.Info()
	return credentials.ProtocolInfo{SecurityProtocol: info.SecurityProtocol}
}
func (c probeRoutingCredentials) Clone() credentials.TransportCredentials {
	return probeRoutingCredentials{TransportCredentials: c.TransportCredentials.Clone(), serverName: c.serverName}
}

func (c probeRoutingCredentials) ClientHandshake(ctx context.Context, _ string, conn net.Conn) (net.Conn, credentials.AuthInfo, error) {
	// grpc-go uses its supplied authority as the TLS name even when the
	// tls.Config has ServerName. Freeze the pool's trust name at this seam.
	return c.TransportCredentials.ClientHandshake(ctx, c.serverName, conn)
}
