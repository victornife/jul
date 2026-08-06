// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"time"

	"jul/internal/config"
)

// PoolSnapshot is an immutable point-in-time view of a pool's backend set and
// selection parameters. It is embedded in request context by the factory's
// generation-scoped snapshot middleware so an in-flight request never observes
// backends introduced by a later reload (R5-05). Backends are shared pointers
// with the live pool so passive/active health state and in-flight counters
// remain coherent for the request's lifetime.
//
// Discovery-owned pools are special: their backend set is refreshed by a
// background refresher, so a snapshot for a dynamic pool delegates backend
// selection to the live pool at request time. This keeps in-flight requests
// on the converged backend view instead of freezing them to a stale seed.
type PoolSnapshot struct {
	key         PoolSnapshotKey
	strategy    string
	backends    []*Backend
	maxFails    int
	failTimeout time.Duration
	balancer    Balancer

	// pool is the live pool this snapshot was captured from. For dynamic pools
	// it is used to serve request-time backend views.
	pool *Pool
	// dynamic is true when the snapshot belongs to a discovery-owned pool.
	dynamic bool
}

// Pick selects an available backend from the snapshot, mirroring Pool.Pick.
func (s *PoolSnapshot) Pick() (*Backend, error) {
	return s.pickExcluding(nil)
}

// pick selects an available backend from the snapshot, mirroring Pool.Pick.
func (s *PoolSnapshot) pick() (*Backend, error) {
	return s.pickExcluding(nil)
}

// pickExcluding selects an available backend from the snapshot, skipping any
// backend whose stable identity is in excluded. It returns
// ErrNoAvailableBackend when every available backend is excluded. For dynamic
// pools it delegates to the live pool so discovery convergence is visible to
// each request.
func (s *PoolSnapshot) pickExcluding(excluded map[BackendIdentity]struct{}) (*Backend, error) {
	if s.dynamic {
		return s.pool.pickExcluding(excluded)
	}
	now := time.Now().UnixNano()
	avail := make([]*Backend, 0, len(s.backends))
	for _, b := range s.backends {
		if !b.available(now) {
			continue
		}
		if _, ok := excluded[b.Identity()]; ok {
			continue
		}
		avail = append(avail, b)
	}
	if len(avail) == 0 {
		return nil, ErrNoAvailableBackend
	}
	b := s.balancer.pick(avail)
	if b == nil {
		return nil, ErrNoAvailableBackend
	}
	b.acquire()
	return b, nil
}

// Backends returns the snapshot's backend set. The returned slice must not be
// modified by callers. For dynamic pools this returns the live pool's current
// backend set.
func (s *PoolSnapshot) Backends() []*Backend {
	if s.dynamic {
		return s.pool.Backends()
	}
	return s.backends
}

// Key returns the identity (name, scheme) of this snapshot.
func (s *PoolSnapshot) Key() PoolSnapshotKey { return s.key }

// Snapshot returns an immutable snapshot of the pool's current backend set and
// selection parameters. The snapshot owns a dedicated balancer instance so
// selection state advances per request and concurrent snapshots do not race
// on shared backend state.
func (p *Pool) Snapshot() *PoolSnapshot {
	return &PoolSnapshot{
		key:         PoolSnapshotKey{Name: p.name, Scheme: p.scheme},
		strategy:    p.strategy,
		backends:    p.Backends(),
		maxFails:    p.maxFails,
		failTimeout: p.failTimeout,
		balancer:    newBalancer(p.strategy),
		pool:        p,
		dynamic:     p.dynamic,
	}
}

// staticSnapshot returns a non-dynamic snapshot built from the supplied server
// list. It is used for build-time consumers (such as gRPC reflection) that
// need the candidate backend set rather than the live pool view (R9-06).
func (p *Pool) staticSnapshot(servers []config.UpstreamServer) *PoolSnapshot {
	return &PoolSnapshot{
		key:         PoolSnapshotKey{Name: p.name, Scheme: p.scheme},
		strategy:    p.strategy,
		backends:    buildBackends(servers, p.scheme),
		maxFails:    p.maxFails,
		failTimeout: p.failTimeout,
		balancer:    newBalancer(p.strategy),
		pool:        p,
		dynamic:     false,
	}
}

// PickCtx returns a backend from the generation-scoped snapshot in ctx when
// one exists for this pool, otherwise falls back to the live pool.
func (p *Pool) PickCtx(ctx context.Context) (*Backend, error) {
	return p.PickExcluding(ctx, nil)
}

// PickExcluding returns a backend from the generation-scoped snapshot or live
// pool, skipping any backend whose stable identity is in excluded. It is used
// by the proxy retry loop so a failed backend does not consume an attempt while
// an untried backend remains.
func (p *Pool) PickExcluding(ctx context.Context, excluded map[BackendIdentity]struct{}) (*Backend, error) {
	if snap := snapshotFrom(ctx, p.name, p.scheme); snap != nil {
		return snap.pickExcluding(excluded)
	}
	return p.pickExcluding(excluded)
}

// pickExcluding selects an available backend from the live pool, skipping any
// backend whose stable identity is in excluded.
func (p *Pool) pickExcluding(excluded map[BackendIdentity]struct{}) (*Backend, error) {
	now := time.Now().UnixNano()
	backends := *p.backends.Load()
	avail := make([]*Backend, 0, len(backends))
	for _, b := range backends {
		if !b.available(now) {
			continue
		}
		if _, ok := excluded[b.Identity()]; ok {
			continue
		}
		avail = append(avail, b)
	}
	if len(avail) == 0 {
		return nil, ErrNoAvailableBackend
	}
	b := p.balancer.pick(avail)
	if b == nil {
		return nil, ErrNoAvailableBackend
	}
	b.acquire()
	return b, nil
}

// BackendsCtx returns the backend set from the generation-scoped snapshot in
// ctx when one exists for this pool, otherwise falls back to the live pool.
func (p *Pool) BackendsCtx(ctx context.Context) []*Backend {
	if snap := snapshotFrom(ctx, p.name, p.scheme); snap != nil {
		return snap.Backends()
	}
	return p.Backends()
}

// poolSnapshotKey is the context key for the map of upstream snapshots.
type poolSnapshotKey struct{}

// SnapshotMap is the generation-scoped view of upstream pools, keyed by
// (name, scheme).
type SnapshotMap map[PoolSnapshotKey]*PoolSnapshot

// WithSnapshot returns ctx carrying the supplied upstream snapshots, keyed by
// (name, scheme).
func WithSnapshot(ctx context.Context, snaps SnapshotMap) context.Context {
	return context.WithValue(ctx, poolSnapshotKey{}, snaps)
}

// SnapshotsFrom returns the generation-scoped snapshot map stored in ctx, or
// nil. It lets the background-lease seam carry a request's generation view onto
// the context of work that continues after the request returns.
func SnapshotsFrom(ctx context.Context) SnapshotMap {
	m, _ := ctx.Value(poolSnapshotKey{}).(SnapshotMap)
	return m
}

// snapshotFrom returns the snapshot for (name, scheme) stored in ctx, or nil.
func snapshotFrom(ctx context.Context, name, scheme string) *PoolSnapshot {
	if m, ok := ctx.Value(poolSnapshotKey{}).(SnapshotMap); ok {
		return m[PoolSnapshotKey{Name: name, Scheme: scheme}]
	}
	return nil
}
