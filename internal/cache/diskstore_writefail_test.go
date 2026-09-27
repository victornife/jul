// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/logthrottle"
)

// A filesystem that rejects every store (full, removed, read-only) must not
// produce one log line per request: the warning is bounded to one per interval
// and carries the running failure count, and nothing is indexed.
func TestDiskStoreWriteFailuresAreBoundedInTheLog(t *testing.T) {
	dir := t.TempDir()
	var logs bytes.Buffer
	d, err := newDiskStore(dir, 1<<20, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 200; i++ {
		d.set(strconv.Itoa(i), &Entry{Status: 200, Body: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	}
	if n := strings.Count(logs.String(), "cache: disk write failed"); n != 1 {
		t.Fatalf("%d write-failure lines for 200 failures, want 1:\n%s", n, logs.String())
	}
	if !strings.Contains(logs.String(), "failed_writes=1") {
		t.Fatalf("first report does not carry the failure count: %s", logs.String())
	}
	if got := d.writeFails.Load(); got != 200 {
		t.Fatalf("counted %d failures, want 200", got)
	}
	if cur, _, entries, _ := d.stats(); cur != 0 || entries != 0 {
		t.Fatalf("failed writes were indexed: %d bytes, %d entries", cur, entries)
	}

	// The next report after the interval states how many were suppressed; a
	// fresh limiter stands in for the interval elapsing.
	d.writeFailLog = logthrottle.Limiter{}
	logs.Reset()
	d.set("late", &Entry{Status: 200, Body: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)})
	if !strings.Contains(logs.String(), "failed_writes=201") || !strings.Contains(logs.String(), "suppressed=199") {
		t.Fatalf("second report = %q, want failed_writes=201 suppressed=199", logs.String())
	}
}

// DiskWriteHealth distinguishes "writes are failing now" from "some writes
// failed earlier": the flag clears on the next successful store while the
// cumulative count is kept (#437).
func TestCacheDiskWriteHealthTracksFailureAndRecovery(t *testing.T) {
	var nilCache *Cache
	if _, _, ok := nilCache.DiskWriteHealth(); ok {
		t.Fatal("nil cache reported a disk tier")
	}
	memOnly, err := New(config.CacheConfig{Enabled: true}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := memOnly.DiskWriteHealth(); ok {
		t.Fatal("memory-only cache reported a disk tier")
	}

	dir := t.TempDir()
	c, err := New(config.CacheConfig{Enabled: true, DiskPath: dir}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	entry := func() *Entry { return &Entry{Status: 200, Body: []byte("x"), ExpiresAt: time.Now().Add(time.Hour)} }
	if n, failing, ok := c.DiskWriteHealth(); !ok || n != 0 || failing {
		t.Fatalf("fresh disk tier = (%d, %v, %v)", n, failing, ok)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	c.disk.set("a", entry())
	c.disk.set("b", entry())
	if n, failing, _ := c.DiskWriteHealth(); n != 2 || !failing {
		t.Fatalf("after failures = (%d, %v), want (2, true)", n, failing)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c.disk.set("c", entry())
	if n, failing, _ := c.DiskWriteHealth(); n != 2 || failing {
		t.Fatalf("after recovery = (%d, %v), want (2, false)", n, failing)
	}
}
