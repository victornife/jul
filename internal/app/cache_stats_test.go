// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"testing"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestCacheStatsNilCacheDisabled(t *testing.T) {
	if got := cacheStats(nil); got != nil {
		t.Fatalf("cacheStats(nil) = %#v, want nil", got)
	}
}

func TestCacheStatsMemoryOnly(t *testing.T) {
	c, err := cache.New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
	}, nil)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	got := cacheStats(c)
	if len(got) != 1 || got[0].Tier != "memory" {
		t.Fatalf("cacheStats(memory-only) = %#v, want exactly one memory tier entry", got)
	}
}

func TestCacheStatsMemoryAndDisk(t *testing.T) {
	c, err := cache.New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(1 << 20),
		DiskPath:      t.TempDir(),
		DiskMaxSize:   config.Size(1 << 20),
	}, nil)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	got := cacheStats(c)
	if len(got) != 2 || got[0].Tier != "memory" || got[1].Tier != "disk" {
		t.Fatalf("cacheStats(memory+disk) = %#v, want memory then disk tier entries", got)
	}
}
