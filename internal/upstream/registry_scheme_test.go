// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"testing"
)

// TestRegistryKeysPoolsByNameAndScheme verifies that the registry treats
// (name, scheme) as the pool identity, so the same upstream name can coexist
// with different schemes and each is managed independently (R7-04).
func TestRegistryKeysPoolsByNameAndScheme(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	httpPool, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	if err != nil {
		t.Fatalf("For http: %v", err)
	}
	httpsPool, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:443"), "https")
	if err != nil {
		t.Fatalf("For https: %v", err)
	}
	r.Commit()

	if httpPool == httpsPool {
		t.Fatal("http and https pools for the same name must be distinct")
	}
	if !closed(httpPool.Done()) && !closed(httpsPool.Done()) {
		// Both live, good.
	}

	// A later build that drops the https reference closes only the https pool.
	r.Begin()
	_, err = r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	if err != nil {
		t.Fatalf("For http second build: %v", err)
	}
	r.Commit()

	if closed(httpPool.Done()) {
		t.Error("http pool should still be live")
	}
	if !closed(httpsPool.Done()) {
		t.Error("https pool should have been closed when its reference was dropped")
	}
}

// TestRegistryReusesPoolWithSameNameAndScheme confirms that unchanged
// (name, scheme) entries still reuse the running pool across reloads.
func TestRegistryReusesPoolWithSameNameAndScheme(t *testing.T) {
	r := NewRegistry(RegistryOptions{})

	r.Begin()
	p1, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "https")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()

	r.Begin()
	p2, err := r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80", "10.0.0.2:80"), "https")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()

	if p1 != p2 {
		t.Fatal("same (name, scheme) with unchanged shape should reuse pool")
	}
}

// TestRegistrySnapshotPoolByScheme verifies SnapshotPool distinguishes scheme.
func TestRegistrySnapshotPoolByScheme(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	_, _ = r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:80"), "http")
	_, _ = r.For(context.Background(), upstreamCfg("api", "round_robin", "10.0.0.1:443"), "https")
	r.Commit()

	if r.SnapshotPool("api", "http") == nil {
		t.Error("expected http snapshot")
	}
	if r.SnapshotPool("api", "https") == nil {
		t.Error("expected https snapshot")
	}
	if r.SnapshotPool("api", "h2c") != nil {
		t.Error("did not expect h2c snapshot")
	}
}
