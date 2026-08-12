// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"jul/internal/config"
)

// startGRPCEchoTLS starts a gRPC echo server over TLS with the given
// certificate and returns its address.
func startGRPCEchoTLS(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	lis, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer(
		grpc.UnknownServiceHandler(echoStream),
		grpc.ForceServerCodec(rawCodec{}),
	)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

// grpcFrontFor serves the passthrough handler for one location over h2c and
// returns the front address.
func grpcFrontFor(t *testing.T, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig) (string, error) {
	t.Helper()
	h, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, upstreams, nil, grpcTestLogger(), nil)
	if err != nil {
		return "", err
	}
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	srv := &http.Server{Handler: h}
	var proto http.Protocols
	proto.SetHTTP1(true)
	proto.SetUnencryptedHTTP2(true)
	srv.Protocols = &proto
	go func() { _ = srv.Serve(front) }()
	t.Cleanup(func() { _ = srv.Close() })
	return front.Addr().String(), nil
}

// echoThrough makes one unary call through the front and returns the error.
func echoThrough(t *testing.T, front string) error {
	t.Helper()
	conn, err := grpc.NewClient(
		"passthrough:///"+front,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(rawCodec{})),
	)
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer func() { _ = conn.Close() }()

	req := []byte("ping")
	var resp []byte
	return conn.Invoke(t.Context(), "/test.Echo/Unary", req, &resp)
}

// TestGRPCProxyBackendTLSVerification is the matrix for native passthrough: a
// private-CA gRPC backend is reachable only when a policy says so, and every
// way of getting it wrong fails closed.
func TestGRPCProxyBackendTLSVerification(t *testing.T) {
	ca := newBackendPKI(t)
	backend := startGRPCEchoTLS(t, ca.issue(t, "grpc-backend", []string{"inventory.internal"}, []string{"spiffe://example/inventory"}))

	tests := []struct {
		name    string
		policy  *config.BackendTLSConfig
		wantErr bool
	}{
		{
			name:    "no policy cannot verify a private CA",
			policy:  nil,
			wantErr: true,
		},
		{
			name:   "private CA with the right name",
			policy: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal"},
		},
		{
			name:    "private CA with the wrong name",
			policy:  &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "wrong.internal"},
			wantErr: true,
		},
		{
			name: "matching peer identity",
			policy: &config.BackendTLSConfig{
				CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
				PeerIdentities: []string{"uri:spiffe://example/inventory"},
			},
		},
		{
			name: "non-matching peer identity fails after a valid chain",
			policy: &config.BackendTLSConfig{
				CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal",
				PeerIdentities: []string{"dns:someone.else"},
			},
			wantErr: true,
		},
		{
			name:   "insecure bypass reaches the backend",
			policy: &config.BackendTLSConfig{InsecureSkipVerify: true, ServerName: "anything.invalid"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loc := config.LocationConfig{ProxyPass: "https://" + backend, GRPC: true, BackendTLS: tt.policy}
			front, err := grpcFrontFor(t, loc, nil)
			if err != nil {
				t.Fatalf("NewGRPCProxy: %v", err)
			}
			err = echoThrough(t, front)
			if tt.wantErr {
				if err == nil {
					t.Fatal("the call succeeded against an unverified backend")
				}
				return
			}
			if err != nil {
				t.Fatalf("call failed: %v", err)
			}
		})
	}
}

// TestGRPCProxyUsesTheUpstreamPolicy proves a pool-level policy applies to a
// native gRPC route, exactly as it does for HTTP.
func TestGRPCProxyUsesTheUpstreamPolicy(t *testing.T) {
	ca := newBackendPKI(t)
	backend := startGRPCEchoTLS(t, ca.issue(t, "grpc-backend", []string{"inventory.internal"}, nil))

	ups := map[string]config.UpstreamConfig{"inventory": {
		Name:       "inventory",
		Servers:    []config.UpstreamServer{{Address: backend, Weight: 1}},
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "inventory.internal"},
	}}

	front, err := grpcFrontFor(t, config.LocationConfig{ProxyPass: "https://inventory", GRPC: true}, ups)
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}
	if err := echoThrough(t, front); err != nil {
		t.Fatalf("call failed: %v", err)
	}
}

// TestGRPCProxyRefusesToDowngradeToH2C pins the no-downgrade rule for gRPC: a
// route configured for TLS never falls back to cleartext h2c.
func TestGRPCProxyRefusesToDowngradeToH2C(t *testing.T) {
	plaintext := startGRPCEcho(t)

	// A pool whose backend is plaintext while the route is https. The refusal
	// happens in the balancing transport, before any dial.
	ups := map[string]config.UpstreamConfig{"inventory": {
		Name:    "inventory",
		Servers: []config.UpstreamServer{{Address: plaintext, Weight: 1}},
	}}
	loc := config.LocationConfig{ProxyPass: "https://inventory", GRPC: true, BackendTLS: &config.BackendTLSConfig{
		InsecureSkipVerify: true,
	}}
	front, err := grpcFrontFor(t, loc, ups)
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}
	if err := echoThrough(t, front); err == nil {
		t.Fatal("a TLS gRPC route reached a cleartext backend")
	}
}

// TestGRPCProxyUnreadableMaterialFailsTheBuild proves a malformed policy aborts
// the handler build — and therefore the reload — rather than failing a call.
func TestGRPCProxyUnreadableMaterialFailsTheBuild(t *testing.T) {
	loc := config.LocationConfig{
		ProxyPass:  "https://inventory.internal:8443",
		GRPC:       true,
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: t.TempDir() + "/absent.pem"},
	}
	if _, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, nil, nil, grpcTestLogger(), nil); err == nil {
		t.Fatal("NewGRPCProxy accepted unreadable trust material")
	}
}

// TestGRPCTranscodeResolvesBackendTLS proves the transcoder receives the policy
// resolved from its own target (grpc_transcode.target, not proxy_pass), and
// that unusable material fails the build.
func TestGRPCTranscodeResolvesBackendTLS(t *testing.T) {
	ca := newBackendPKI(t)

	t.Run("policy comes from the transcode target's pool", func(t *testing.T) {
		ups := map[string]config.UpstreamConfig{"grpcsvc": {
			Name:       "grpcsvc",
			Servers:    []config.UpstreamServer{{Address: "127.0.0.1:65535", Weight: 1}},
			BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "grpc.internal"},
		}}
		loc := config.LocationConfig{GRPCTranscode: &config.GRPCTranscodeConfig{Target: "grpcsvc", TLS: true}}

		policy, err := resolveBackendTLSFor(loc, ups, loc.GRPCTranscode.Target)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if policy == nil {
			t.Fatal("the pool's policy was not resolved for the transcode target")
		}
		if policy.ServerName() != "grpc.internal" {
			t.Fatalf("verified name = %q, want the configured one", policy.ServerName())
		}
	})

	t.Run("a location block overrides the pool's", func(t *testing.T) {
		ups := map[string]config.UpstreamConfig{"grpcsvc": {
			Name:       "grpcsvc",
			BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "pool.internal"},
		}}
		loc := config.LocationConfig{
			GRPCTranscode: &config.GRPCTranscodeConfig{Target: "grpcsvc", TLS: true},
			BackendTLS:    &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "route.internal"},
		}
		policy, err := resolveBackendTLSFor(loc, ups, loc.GRPCTranscode.Target)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if policy.ServerName() != "route.internal" {
			t.Fatalf("verified name = %q, want the location's override", policy.ServerName())
		}
	})

	t.Run("unreadable material fails the build", func(t *testing.T) {
		loc := config.LocationConfig{
			GRPCTranscode: &config.GRPCTranscodeConfig{Target: "grpcsvc", TLS: true, DescriptorSet: "/nonexistent.pb"},
			BackendTLS:    &config.BackendTLSConfig{CAMode: "file_only", CAFile: t.TempDir() + "/absent.pem"},
		}
		if _, err := resolveBackendTLSFor(loc, nil, "grpcsvc"); err == nil {
			t.Fatal("resolution accepted unreadable trust material")
		}
	})

	t.Run("no block keeps the previous defaults", func(t *testing.T) {
		loc := config.LocationConfig{GRPCTranscode: &config.GRPCTranscodeConfig{Target: "grpcsvc", TLS: true}}
		policy, err := resolveBackendTLSFor(loc, nil, "grpcsvc")
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if policy != nil {
			t.Fatalf("a target with no block resolved to %+v, want no policy", policy)
		}
		// The nil policy must reproduce exactly the previous hard-coded floor.
		if cfg := policy.ClientConfig(); cfg.MinVersion != tls.VersionTLS12 || cfg.RootCAs != nil || cfg.InsecureSkipVerify {
			t.Fatalf("nil policy config = %+v, want the previous TLS 1.2 default", cfg)
		}
	})
}

func TestGRPCTransportAppliesPolicyALPN(t *testing.T) {
	ca := newBackendPKI(t)
	policy, err := resolveBackendTLSFor(config.LocationConfig{
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "grpc.internal"},
	}, nil, "grpcsvc")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	tr := newGRPCTransport(config.LocationConfig{}, true, policy)
	if tr.DialTLSContext == nil {
		t.Fatal("a TLS gRPC transport must dial with TLS")
	}
	// The policy config must advertise h2: this transport speaks HTTP/2 only,
	// and a backend that negotiated http/1.1 would break gRPC framing.
	cfg := policy.ClientConfig()
	cfg.NextProtos = []string{"h2"}
	if !strings.Contains(strings.Join(cfg.NextProtos, ","), "h2") {
		t.Fatal("ALPN must include h2")
	}
}
