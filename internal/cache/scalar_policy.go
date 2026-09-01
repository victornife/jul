// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"time"

	"jul/internal/config"
)

// CachePolicy is the immutable snapshot of scalar cache behavior that
// hot-reloads independently of the memory/disk store objects (#92): the
// default freshness lifetime, the stale-while-revalidate and stale-if-error
// grace windows, and the per-entry capture limit. Every cache decision loads
// exactly one snapshot, so it never mixes values from two different
// configurations.
type CachePolicy struct {
	DefaultTTL           time.Duration
	StaleWhileRevalidate time.Duration
	StaleIfError         time.Duration
	// MaxEntryBytes caps how much of one response body fetchAndStore/the
	// revalidation recorder buffers for storage. It is deliberately coupled to
	// MemoryMaxSize: the schema has no separate per-entry limit field, and
	// decoupling it would be a new capability, not a hot-reload of an existing
	// one (#92 scope item 5 — characterized, not changed).
	MaxEntryBytes int64
}

func policyFromConfig(cfg config.CacheConfig) CachePolicy {
	return CachePolicy{
		DefaultTTL:           cfg.DefaultTTL.Std(),
		StaleWhileRevalidate: cfg.StaleWhileRevalidate.Std(),
		StaleIfError:         cfg.StaleIfError.Std(),
		MaxEntryBytes:        cfg.MemoryMaxSize.Bytes(),
	}
}

// Policy returns the currently active scalar policy snapshot.
func (c *Cache) Policy() CachePolicy {
	if c == nil {
		return CachePolicy{}
	}
	if p := c.policy.Load(); p != nil {
		return *p
	}
	return CachePolicy{}
}

// PreparedCacheUpdate is a built, not-yet-applied scalar-policy/capacity
// candidate (#92). Building it never mutates the live cache; Commit installs
// the policy and performs any capacity-driven eviction in one step, and is
// the only operation that may evict memory entries or delete disk files. Not
// calling Commit — the candidate simply going out of scope on a pre-Publish
// failure — is the abort: there is nothing to release.
type PreparedCacheUpdate struct {
	c       *Cache
	policy  CachePolicy
	memMax  int64
	diskMax int64
}

// PrepareCacheUpdate builds a candidate scalar-policy/capacity update from
// cfg without touching the live cache. cfg's scalar fields are already
// validated to canonical values by configuration validation before a
// candidate reaches this seam, so building can never fail. c may be nil (the
// cache is disabled); PrepareCacheUpdate then returns nil, and Commit on a
// nil *PreparedCacheUpdate is a safe no-op.
func (c *Cache) PrepareCacheUpdate(cfg config.CacheConfig) *PreparedCacheUpdate {
	if c == nil {
		return nil
	}
	return &PreparedCacheUpdate{
		c:       c,
		policy:  policyFromConfig(cfg),
		memMax:  cfg.MemoryMaxSize.Bytes(),
		diskMax: cfg.DiskMaxSize.Bytes(),
	}
}

// Commit installs the candidate policy with one atomic store, then resizes
// the memory tier and (if configured) the disk tier to the candidate
// capacity. It never fails: a disk-eviction removal failure is recorded
// through the cache's own bounded failure counter and a log line rather than
// returned, matching the reload transaction's no-fail Publish contract.
// Increasing a cap never evicts or deletes; decreasing one does, in strict
// LRU order. Calling Commit on a nil *PreparedCacheUpdate is a safe no-op.
func (p *PreparedCacheUpdate) Commit() {
	if p == nil {
		return
	}
	pol := p.policy
	p.c.policy.Store(&pol)
	p.c.mem.Resize(p.memMax)
	if p.c.disk != nil {
		if _, _, failed := p.c.disk.Resize(p.diskMax); failed > 0 {
			p.c.diskEvictionFailures.Add(int64(failed))
			if p.c.log != nil {
				p.c.log.Warn("cache: disk capacity reduction could not fully enforce the new limit",
					"failed_removals", failed)
			}
		}
	}
}

// DiskEvictionFailures reports the number of disk cache files a capacity
// reduction failed to remove since startup. A non-zero value means actual
// disk usage may still exceed the configured cap (#92): the failure is
// surfaced here rather than silently claiming the limit is enforced.
func (c *Cache) DiskEvictionFailures() int64 {
	if c == nil {
		return 0
	}
	return c.diskEvictionFailures.Load()
}
