// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"testing"
)

func TestRegistrySnapshotProjectsUnixNetworkSeparatelyFromAddress(t *testing.T) {
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	if _, err := r.For(context.Background(), upstreamCfg("local-app", "round_robin", "unix:/tmp/local-app.sock"), "http"); err != nil {
		t.Fatalf("For: %v", err)
	}
	r.Commit()
	defer r.CloseAll()

	snapshot := r.Snapshot()
	if len(snapshot) != 1 || len(snapshot[0].Backends) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	got := snapshot[0].Backends[0]
	if got.Network != NetworkUnix || got.Address != "/tmp/local-app.sock" {
		t.Fatalf("Unix backend status = %+v, want network=unix and normalized dial address", got)
	}
}
