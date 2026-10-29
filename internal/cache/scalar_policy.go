// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"jul/internal/cachemeta"
	"time"

	"jul/internal/config"
)

// CachePolicy is the immutable snapshot of scalar cache behavior that
// hot-reloads independently of the memory/disk store objects (#92): the
// default freshness lifetime, the stale-while-revalidate and stale-if-error
// grace windows, the per-entry capture limit, and cold-miss fill locking (#525).
// Every cache decision loads exactly one snapshot, so it never mixes values
// from two different configurations.
type CachePolicy struct {
	DecisionSamples      bool
	DefaultTTL           time.Duration
	StaleWhileRevalidate time.Duration
	StaleIfError         time.Duration
	// MemoryMaxBytes is the in-memory tier cap and in-flight memory capture
	// budget ceiling (#505).
	MemoryMaxBytes int64
	// MaxEntryBytes caps the maximum cacheable response body size across both
	// tiers (#525 Part 2, ADR 0023). When [cache].max_entry_size is omitted or
	// zero, it defaults to MemoryMaxBytes.
	MaxEntryBytes int64
	// Lock enables per-key cold-miss fill coalescing (#525 Part 1).
	Lock bool
	// LockTimeout bounds how long a cold-miss waiter blocks for an active fill
	// leader before falling through to an uncached origin fetch.
	LockTimeout time.Duration
	// RangeEnabled opts the cache into Option A range-aware caching (#442, ADR 0024).
	RangeEnabled bool
	// MaxRanges bounds the number of byte-range specs accepted when RangeEnabled
	// is true (defaults to 1 when RangeEnabled is true).
	MaxRanges int
	// TagHeaderCount and TagHeaders hold up to 8 canonicalized trusted origin
	// response header names for grouping-tag ingestion (#621, ADR 0027). Using
	// a fixed array keeps CachePolicy comparable by value.
	TagHeaderCount int
	TagHeaders     [8]string
	// StripTagHeaders removes configured TagHeaders from downstream responses
	// and stored headers (defaults to true when TagHeaderCount > 0).
	StripTagHeaders bool

	// Route-scoped policy overrides and isolation (#623 / #672).
	RouteScope         string
	RouteEpoch         uint64
	MultiRouteOverlap  bool
	HasRouteDefaultTTL bool
	HasMaxTTL          bool
	MaxTTL             time.Duration
	HasRouteSWR        bool
	HasRouteSIE        bool
	BypassAnyCookie    bool
	BypassCookieCount  int
	BypassCookies      [config.MaxBypassCookies]string
	varyGuard          *varyTransformPolicy
}

// TagHeaderSlice returns the active canonicalized origin tag header names.
func (p CachePolicy) TagHeaderSlice() []string {
	if p.TagHeaderCount <= 0 {
		return nil
	}
	return p.TagHeaders[:p.TagHeaderCount]
}

// MemoryCapBytes returns the largest response body that may be captured in
// memory and stored in the memory tier under this policy.
func (p CachePolicy) MemoryCapBytes() int64 {
	if p.MemoryMaxBytes <= 0 {
		return p.MaxEntryBytes
	}
	if p.MaxEntryBytes > 0 && p.MaxEntryBytes < p.MemoryMaxBytes {
		return p.MaxEntryBytes
	}
	return p.MemoryMaxBytes
}

const defaultLockTimeout = 5 * time.Second

func policyFromConfig(cfg config.CacheConfig) CachePolicy {
	lockTimeout := cfg.LockTimeout.Std()
	if cfg.Lock && lockTimeout <= 0 {
		lockTimeout = defaultLockTimeout
	}
	memMax := cfg.MemoryMaxSize.Bytes()
	maxEntry := cfg.MaxEntrySize.Bytes()
	if maxEntry <= 0 {
		maxEntry = memMax
	}
	maxRanges := cfg.MaxRanges
	if cfg.RangeEnabled && maxRanges <= 0 {
		maxRanges = 1
	}
	canonTags := canonicalizeTagHeaders(cfg.TagHeaders)
	var tagArr [8]string
	tagCount := len(canonTags)
	if tagCount > len(tagArr) {
		tagCount = len(tagArr)
	}
	for i := 0; i < tagCount; i++ {
		tagArr[i] = canonTags[i]
	}
	return CachePolicy{
		DecisionSamples:      cfg.DecisionSamples,
		DefaultTTL:           cfg.DefaultTTL.Std(),
		StaleWhileRevalidate: cfg.StaleWhileRevalidate.Std(),
		StaleIfError:         cfg.StaleIfError.Std(),
		MemoryMaxBytes:       memMax,
		MaxEntryBytes:        maxEntry,
		Lock:                 cfg.Lock,
		LockTimeout:          lockTimeout,
		RangeEnabled:         cfg.RangeEnabled,
		MaxRanges:            maxRanges,
		TagHeaderCount:       tagCount,
		TagHeaders:           tagArr,
		StripTagHeaders:      cfg.StripTagHeadersEnabled(),
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
	p.c.commitPolicyAndSyncTags(pol)
	p.c.setDecisionSampling(pol.DecisionSamples)
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

// TierStats is one tier's live occupancy: current bytes, the configured cap,
// entry count, and cumulative LRU-capacity evictions since startup (not
// explicit invalidation).
type TierStats = cachemeta.TierStats

// Stats returns a snapshot of every configured tier's live occupancy
// (JUL-AUD-005): "cache is bounded" is otherwise an unfalsifiable claim
// against a running process. Read at scrape time rather than pushed from the
// request path, like the upstream resilience gauges. The disk tier is absent
// when no disk_path is configured, matching the tier's own optionality.
func (c *Cache) Stats() (mem TierStats, disk *TierStats) {
	if c == nil {
		return TierStats{}, nil
	}
	bytes, maxBytes, entries, evictions := c.mem.stats()
	mem = TierStats{Bytes: bytes, MaxBytes: maxBytes, Entries: entries, Evictions: evictions}
	if c.disk == nil {
		return mem, nil
	}
	dBytes, dMaxBytes, dEntries, dEvictions := c.disk.stats()
	return mem, &TierStats{Bytes: dBytes, MaxBytes: dMaxBytes, Entries: dEntries, Evictions: dEvictions}
}
