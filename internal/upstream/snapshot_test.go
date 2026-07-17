// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"testing"

	"jul/internal/config"
)

func TestPoolSnapshotPickUsesSnapshotBackends(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	snap := pool.Snapshot()
	pool.UpdateBackends([]config.UpstreamServer{
		{Address: "10.0.0.2:80", Weight: 1},
	})

	ctx := WithSnapshot(context.Background(), map[string]*PoolSnapshot{"api": snap})

	b, err := pool.PickCtx(ctx)
	if err != nil {
		t.Fatalf("PickCtx: %v", err)
	}
	if b.Address != "10.0.0.1:80" {
		t.Errorf("snapshot pick used backend %q, want 10.0.0.1:80", b.Address)
	}
	pool.Release(b)
}

func TestPoolSnapshotPickFallsBackToLivePool(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	b, err := pool.PickCtx(context.Background())
	if err != nil {
		t.Fatalf("PickCtx: %v", err)
	}
	if b.Address != "10.0.0.1:80" {
		t.Errorf("live pick used backend %q, want 10.0.0.1:80", b.Address)
	}
	pool.Release(b)
}

func TestPoolSnapshotBackendsCtxUsesSnapshot(t *testing.T) {
	pool, err := NewPool(config.UpstreamConfig{
		Name:     "api",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "10.0.0.1:80", Weight: 1}},
		MaxFails: 1,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	snap := pool.Snapshot()
	pool.UpdateBackends([]config.UpstreamServer{
		{Address: "10.0.0.2:80", Weight: 1},
	})

	ctx := WithSnapshot(context.Background(), map[string]*PoolSnapshot{"api": snap})
	if got := len(pool.BackendsCtx(ctx)); got != 1 {
		t.Fatalf("snapshot backends = %d, want 1", got)
	}
	if got := pool.BackendsCtx(ctx)[0].Address; got != "10.0.0.1:80" {
		t.Errorf("snapshot backend = %q, want 10.0.0.1:80", got)
	}
}
