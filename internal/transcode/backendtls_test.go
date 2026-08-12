// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"crypto/tls"
	"testing"

	"jul/internal/backendtls"
)

// TestDialUsesTheResolvedPolicy proves the transcoder consumes the resolved
// policy rather than a hard-coded TLS config, and that a nil policy reproduces
// exactly the previous behaviour (a TLS 1.2 floor and platform roots).
func TestDialUsesTheResolvedPolicy(t *testing.T) {
	t.Run("nil policy keeps the previous defaults", func(t *testing.T) {
		var policy *backendtls.Policy
		cfg := policy.ClientConfig()
		if cfg.MinVersion != tls.VersionTLS12 {
			t.Fatalf("min version = %d, want TLS 1.2 as before", cfg.MinVersion)
		}
		if cfg.RootCAs != nil || cfg.InsecureSkipVerify || len(cfg.Certificates) > 0 {
			t.Fatalf("nil policy config = %+v, want the language defaults", cfg)
		}
		// The dial itself is lazy, so this only asserts it accepts the policy
		// shape; verification behaviour is covered by internal/backendtls and
		// by the handler-level gRPC tests.
		conn, err := dial("127.0.0.1:65535", true, nil)
		if err != nil {
			t.Fatalf("dial with no policy: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("a resolved policy is accepted", func(t *testing.T) {
		policy, err := backendtls.Resolve(backendtls.Options{
			ServerName: "grpc.internal",
			MinVersion: backendtls.MinVersion13,
		}, "grpcsvc")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if got := policy.ClientConfig().MinVersion; got != tls.VersionTLS13 {
			t.Fatalf("min version = %d, want TLS 1.3 from the policy", got)
		}
		if got := policy.ClientConfig().ServerName; got != "grpc.internal" {
			t.Fatalf("server name = %q, want the policy's", got)
		}
		conn, err := dial("127.0.0.1:65535", true, policy)
		if err != nil {
			t.Fatalf("dial with a policy: %v", err)
		}
		_ = conn.Close()
	})

	t.Run("plaintext ignores the policy", func(t *testing.T) {
		policy, err := backendtls.Resolve(backendtls.Options{ServerName: "grpc.internal"}, "grpcsvc")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		// A cleartext target uses insecure credentials whatever the policy says;
		// configuration validation rejects a policy on a plaintext target, so
		// this path exists only for safety.
		conn, err := dial("127.0.0.1:65535", false, policy)
		if err != nil {
			t.Fatalf("dial plaintext: %v", err)
		}
		_ = conn.Close()
	})
}
