// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"testing"
	"time"

	"jul/internal/config"
)

// TestMemStoreStats proves memStore.stats() reports live occupancy and counts
// an eviction forced by exceeding the byte cap.
func TestMemStoreStats(t *testing.T) {
	m := newMemStore(400, nil)
	m.set("a", &Entry{Body: make([]byte, 10)}) // Entry.Size() = len(Body) + 256 fixed overhead

	if bytes, maxBytes, entries, evictions := m.stats(); entries != 1 || evictions != 0 || maxBytes != 400 || bytes <= 0 {
		t.Fatalf("stats() after one set = (bytes=%d, max=%d, entries=%d, evictions=%d), want one entry and no evictions", bytes, maxBytes, entries, evictions)
	}

	// Exceeds the 400-byte cap, forcing "a" to be evicted.
	m.set("b", &Entry{Body: make([]byte, 10)})

	bytes, maxBytes, entries, evictions := m.stats()
	if entries != 1 || evictions != 1 || maxBytes != 400 || bytes <= 0 {
		t.Fatalf("stats() after overflow = (bytes=%d, max=%d, entries=%d, evictions=%d), want one entry and one eviction", bytes, maxBytes, entries, evictions)
	}
}

// TestDiskStoreStats proves diskStore.stats() reports live occupancy and
// counts an eviction forced by exceeding the byte cap.
func TestDiskStoreStats(t *testing.T) {
	d, err := newDiskStore(t.TempDir(), 700, testLogger())
	if err != nil {
		t.Fatalf("newDiskStore: %v", err)
	}
	d.set("a", &Entry{Status: 200, Body: make([]byte, 10), ExpiresAt: time.Now().Add(time.Hour)}) // gob-encoded, ~400 bytes

	if bytes, maxBytes, entries, evictions := d.stats(); entries != 1 || evictions != 0 || maxBytes != 700 || bytes <= 0 {
		t.Fatalf("stats() after one set = (bytes=%d, max=%d, entries=%d, evictions=%d), want one entry and no evictions", bytes, maxBytes, entries, evictions)
	}

	// Exceeds the 700-byte cap, forcing "a" to be evicted.
	d.set("b", &Entry{Status: 200, Body: make([]byte, 10), ExpiresAt: time.Now().Add(time.Hour)})

	bytes, maxBytes, entries, evictions := d.stats()
	if entries != 1 || evictions != 1 || maxBytes != 700 || bytes <= 0 {
		t.Fatalf("stats() after overflow = (bytes=%d, max=%d, entries=%d, evictions=%d), want one entry and one eviction", bytes, maxBytes, entries, evictions)
	}
}

// TestCacheStatsMemoryOnly proves Cache.Stats() reports the memory tier and a
// nil disk tier when no disk_path is configured.
func TestCacheStatsMemoryOnly(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: config.Size(1 << 20)})
	c.mem.set("a", &Entry{Body: make([]byte, 10)})

	mem, disk := c.Stats()
	if disk != nil {
		t.Fatalf("Stats() disk = %#v, want nil with no disk_path configured", disk)
	}
	if mem.Entries != 1 || mem.MaxBytes != (1<<20) {
		t.Fatalf("Stats() mem = %#v, want one entry with the configured max", mem)
	}
}

// TestCacheStatsMemoryAndDisk proves Cache.Stats() reports both tiers when a
// disk_path is configured.
func TestCacheStatsMemoryAndDisk(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{
		MemoryMaxSize: config.Size(1 << 20),
		DiskPath:      t.TempDir(),
		DiskMaxSize:   config.Size(1 << 20),
	})
	c.mem.set("a", &Entry{Body: make([]byte, 10)})
	c.disk.set("a", &Entry{Status: 200, Body: make([]byte, 10), ExpiresAt: time.Now().Add(time.Hour)})

	mem, disk := c.Stats()
	if disk == nil {
		t.Fatal("Stats() disk = nil, want a populated tier with disk_path configured")
	}
	if mem.Entries != 1 || disk.Entries != 1 || disk.MaxBytes != (1<<20) {
		t.Fatalf("Stats() = mem %#v disk %#v, want one entry in each tier", mem, disk)
	}
}

// TestCacheStatsNilCache proves Stats() on a nil *Cache (caching disabled)
// returns a zero memory tier and a nil disk tier rather than panicking.
func TestCacheStatsNilCache(t *testing.T) {
	var c *Cache
	mem, disk := c.Stats()
	if mem != (TierStats{}) || disk != nil {
		t.Fatalf("Stats() on nil cache = mem %#v disk %#v, want zero value and nil", mem, disk)
	}
}
