// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"time"
)

// PoolSnapshot is an immutable point-in-time view of a pool's backend set and
// selection parameters. It is embedded in request context by the factory's
// generation-scoped snapshot middleware so an in-flight request never observes
// backends introduced by a later reload (R5-05). Backends are shared pointers
// with the live pool so passive/active health state and in-flight counters
// remain coherent for the request's lifetime.
type PoolSnapshot struct {
	name        string
	strategy    string
	backends    []*Backend
	maxFails    int
	failTimeout time.Duration
}

// pick selects an available backend from the snapshot, mirroring Pool.Pick.
func (s *PoolSnapshot) pick() (*Backend, error) {
	now := time.Now().UnixNano()
	avail := make([]*Backend, 0, len(s.backends))
	for _, b := range s.backends {
		if b.available(now) {
			avail = append(avail, b)
		}
	}
	if len(avail) == 0 {
		return nil, ErrNoAvailableBackend
	}
	b := newBalancer(s.strategy).pick(avail)
	if b == nil {
		return nil, ErrNoAvailableBackend
	}
	b.acquire()
	return b, nil
}

// Backends returns the snapshot's backend set. The returned slice must not be
// modified by callers.
func (s *PoolSnapshot) Backends() []*Backend { return s.backends }

// Snapshot returns an immutable snapshot of the pool's current backend set and
// selection parameters.
func (p *Pool) Snapshot() *PoolSnapshot {
	return &PoolSnapshot{
		name:        p.name,
		strategy:    p.strategy,
		backends:    p.Backends(),
		maxFails:    p.maxFails,
		failTimeout: p.failTimeout,
	}
}

// PickCtx returns a backend from the generation-scoped snapshot in ctx when
// one exists for this pool, otherwise falls back to the live pool.
func (p *Pool) PickCtx(ctx context.Context) (*Backend, error) {
	if snap := snapshotFrom(ctx, p.name); snap != nil {
		return snap.pick()
	}
	return p.Pick()
}

// BackendsCtx returns the backend set from the generation-scoped snapshot in
// ctx when one exists for this pool, otherwise falls back to the live pool.
func (p *Pool) BackendsCtx(ctx context.Context) []*Backend {
	if snap := snapshotFrom(ctx, p.name); snap != nil {
		return snap.backends
	}
	return p.Backends()
}

// poolSnapshotKey is the context key for the map of upstream name -> snapshot.
type poolSnapshotKey struct{}

// WithSnapshot returns ctx carrying the supplied upstream snapshots, keyed by
// upstream name.
func WithSnapshot(ctx context.Context, snaps map[string]*PoolSnapshot) context.Context {
	return context.WithValue(ctx, poolSnapshotKey{}, snaps)
}

// snapshotFrom returns the snapshot for name stored in ctx, or nil.
func snapshotFrom(ctx context.Context, name string) *PoolSnapshot {
	if m, ok := ctx.Value(poolSnapshotKey{}).(map[string]*PoolSnapshot); ok {
		return m[name]
	}
	return nil
}
