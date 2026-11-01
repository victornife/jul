// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestPurgeRetryPreservesFreshVaryPreviewAccounting(t *testing.T) {
	t.Parallel()

	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			t.Parallel()
			cfg := config.CacheConfig{
				MemoryMaxSize: config.Size(2 << 20),
				DefaultTTL:    config.Duration(5 * time.Minute),
				TagHeaders:    []string{"Cache-Tag"},
			}
			if disk {
				cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), config.Size(8<<20)
			}
			c := newTestCache(t, cfg)
			enCalls := 0
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=300")
				w.Header().Set("Vary", "Accept-Language")
				if r.Header.Get("Accept-Language") == "en" {
					enCalls++
					tag := "A"
					if enCalls > 1 {
						tag = "C"
					}
					w.Header().Set("Cache-Tag", tag)
					_, _ = fmt.Fprintf(w, "en-v%d", enCalls)
				} else {
					w.Header().Set("Cache-Tag", "B")
					_, _ = io.WriteString(w, "fr-v1")
				}
			}))
			serve := func(lang string) *httptest.ResponseRecorder {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/doc", nil)
				req.Header.Set("Accept-Language", lang)
				h.ServeHTTP(rr, req)
				return rr
			}
			serve("en")
			serve("fr")
			tagA, err := ParseSelector(Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"A"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.onAfterFenceInstall = func(NormalizedSelector, uint64) { cancel() }
			initial, err := c.InvalidateScope(ctx, tagA, InvalidateOptions{})
			if !errors.Is(err, context.Canceled) || initial.State != "canceled" || initial.FenceSequence == 0 {
				t.Fatalf("canceled purge: res=%+v err=%v", initial, err)
			}
			c.onAfterFenceInstall = nil
			fresh := serve("en")
			if fresh.Header().Get("X-Cache") != "MISS" || fresh.Body.String() != "en-v2" {
				t.Fatalf("fresh variant: X-Cache=%q body=%q", fresh.Header().Get("X-Cache"), fresh.Body.String())
			}
			exact, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/doc"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			before := c.PreviewScope(exact, PreviewOptions{})
			if before.MemoryEntries != 2 || before.MatchedRepresentations != 1 || before.MemoryBytes <= 0 {
				t.Fatalf("expected a fresh variant and its stub, excluding the fenced variant: %+v", before)
			}
			retry, err := c.InvalidateScope(context.Background(), tagA, InvalidateOptions{ReuseFenceGen: initial.FenceSequence})
			if err != nil || retry.State != "succeeded" {
				t.Fatalf("retry: res=%+v err=%v", retry, err)
			}
			after := c.PreviewScope(exact, PreviewOptions{})
			if before.MemoryEntries != after.MemoryEntries ||
				before.MatchedRepresentations != after.MatchedRepresentations ||
				before.MemoryBytes != after.MemoryBytes ||
				before.DiskEntries != after.DiskEntries ||
				before.DiskBytes != after.DiskBytes {
				t.Fatalf("retry changed accounting for protected fresh entries: before=%+v after=%+v", before, after)
			}
			hit := serve("en")
			if hit.Header().Get("X-Cache") != "HIT" || hit.Body.String() != "en-v2" {
				t.Fatalf("fresh data was not preserved: X-Cache=%q body=%q", hit.Header().Get("X-Cache"), hit.Body.String())
			}
		})
	}
}

func TestPurgeWaitsForInFlightDiskPublication(t *testing.T) {
	t.Parallel()

	for _, kind := range []SelectorKind{SelectorExactURL, SelectorAll, SelectorTag} {
		for _, fresh := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fresh=%t", kind, fresh), func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{
					MemoryMaxSize: config.Size(4096), MaxEntrySize: config.Size(64 << 10),
					DiskPath: t.TempDir(), DiskMaxSize: config.Size(8 << 20),
					DefaultTTL: config.Duration(5 * time.Minute), TagHeaders: []string{"Cache-Tag"},
				}
				c := newTestCache(t, cfg)
				checked, allowRename := make(chan struct{}), make(chan struct{})
				published, allowPostcheck := make(chan struct{}), make(chan struct{})
				fillDone, purgeDone := make(chan struct{}), make(chan InvalidateResult, 1)
				releaseRename := sync.OnceFunc(func() { close(allowRename) })
				releasePostcheck := sync.OnceFunc(func() { close(allowPostcheck) })
				t.Cleanup(func() {
					releaseRename()
					releasePostcheck()
					<-fillDone
				})
				orig := c.disk.canCommit
				var checkedOnce sync.Once
				c.disk.canCommit = func(key string, gen, start uint64, hasStart bool, e *Entry, tags []string) bool {
					ok := orig(key, gen, start, hasStart, e, tags)
					if ok {
						checkedOnce.Do(func() {
							close(checked)
							<-allowRename
						})
					}
					return ok
				}
				var paused atomic.Bool
				c.onAfterDiskPublication = func(string, uint64) {
					if paused.CompareAndSwap(false, true) {
						close(published)
						<-allowPostcheck
					}
				}
				var calls atomic.Int64
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					n := calls.Add(1)
					w.Header().Set("Cache-Control", "public, max-age=300")
					w.Header().Set("Cache-Tag", "A")
					_, _ = io.WriteString(w, strings.Repeat(fmt.Sprint(n), 12288))
				}))
				serve := func() *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://portal.example.com/large", nil))
					return rr
				}
				go func() {
					defer close(fillDone)
					serve()
				}()
				<-checked
				raw := Selector{Kind: kind, Host: "portal.example.com"}
				switch kind {
				case SelectorAll:
					raw.Host, raw.All = "", true
				case SelectorExactURL:
					raw.URI = "/large"
				case SelectorTag:
					raw.Tags = []string{"A"}
				}
				sel, err := ParseSelector(raw, nil)
				if err != nil {
					t.Fatal(err)
				}
				committed := make(chan struct{})
				go func() {
					res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{
						CommitFence: func(_ uint64, _ []string, _ PurgeRecoveryIntent) error {
							close(committed)
							return nil
						},
					})
					if err != nil {
						t.Errorf("purge: %v", err)
					}
					purgeDone <- res
				}()
				<-committed
				c.genMu.Lock()
				installed := c.latestFenceGen.Load() > 0
				c.genMu.Unlock()
				if !installed {
					t.Fatal("fence was not installed")
				}
				releaseRename()
				<-published
				res := <-purgeDone
				if res.State != "succeeded" || res.InvalidationEffective != "effective" || res.DiskEntriesRemoved != 1 {
					t.Fatalf("in-flight disk publication was not cleaned up before acknowledgement: %+v", res)
				}
				key := keyFor(http.MethodGet, "portal.example.com", "/large")
				if c.disk.hasKey(key) {
					t.Fatal("purge relied on the still-paused writer to remove its stale file")
				}
				if fresh {
					rr := serve()
					if rr.Header().Get("X-Cache") != "MISS" || rr.Body.String() != strings.Repeat("2", 12288) {
						t.Fatalf("bad post-cutoff publication: X-Cache=%q body bytes=%d", rr.Header().Get("X-Cache"), rr.Body.Len())
					}
				}
				releasePostcheck()
				<-fillDone
				reopened := newTestCache(t, cfg)
				if fresh {
					assertCachedEntryBody(t, c, key, strings.Repeat("2", 12288))
					assertCachedEntryBody(t, reopened, key, strings.Repeat("2", 12288))
				} else if e, ok := reopened.get(key); ok {
					t.Fatalf("pre-cutoff publication recovered after successful cleanup: %d bytes", e.BodyLen())
				}
			})
		}
	}
}

func TestPurgeRetryRepairsDurableCleanupWithoutCatalogMatches(t *testing.T) {
	t.Parallel()

	for _, fault := range []string{"unlink", "manifest_and_unlink", "triple", "directory_sync"} {
		t.Run(fault, func(t *testing.T) {
			t.Parallel()
			cfg := config.CacheConfig{
				MemoryMaxSize: config.Size(2 << 20), DiskPath: t.TempDir(), DiskMaxSize: config.Size(8 << 20),
			}
			c := newTestCache(t, cfg)
			key := keyFor(http.MethodGet, "portal.example.com", "/durable")
			c.set(key, &Entry{Status: http.StatusOK, Body: []byte("pre-cutoff")})
			switch fault {
			case "unlink":
				c.disk.testRemoveFile = func(string) error { return errors.New("unlink EIO") }
			case "manifest_and_unlink", "triple":
				c.disk.testRemoveFile = func(string) error { return errors.New("unlink EIO") }
				c.disk.testPersistTombstoneErr = errors.New("manifest EIO")
				if fault == "triple" {
					c.disk.testTruncateFile = func(string, int64) error { return errors.New("truncate EIO") }
				}
			case "directory_sync":
				var calls int
				c.disk.testSyncDir = func(string) error {
					calls++
					if calls > 1 {
						return errors.New("directory sync EIO")
					}
					return nil
				}
			}
			sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/durable"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			first, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
			if err != nil || first.State == "succeeded" {
				t.Fatalf("injected failure was not surfaced: res=%+v err=%v", first, err)
			}
			stillFailed, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: first.FenceSequence})
			if err != nil || stillFailed.State == "succeeded" || stillFailed.InvalidationEffective == "effective" {
				t.Fatalf("retry ignored unresolved durable state: res=%+v err=%v", stillFailed, err)
			}
			c.disk.testRemoveFile = nil
			c.disk.testPersistTombstoneErr = nil
			c.disk.testTruncateFile = nil
			c.disk.testSyncDir = nil
			retry, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: first.FenceSequence})
			if err != nil || retry.State != "succeeded" || retry.InvalidationEffective != "effective" {
				t.Fatalf("retry did not repair durable state: res=%+v err=%v", retry, err)
			}
			if len(c.disk.tombstones) != 0 || c.disk.tombstoneDirty || c.disk.unrecoverableInvalidation.Load() {
				t.Fatalf("successful retry left durable cleanup pending: tombstones=%d dirty=%t unrecoverable=%t",
					len(c.disk.tombstones), c.disk.tombstoneDirty, c.disk.unrecoverableInvalidation.Load())
			}
			reopened := newTestCache(t, cfg)
			if e, ok := reopened.get(key); ok {
				t.Fatalf("successful retry resurrected %q", e.Body)
			}
		})
	}
}

func TestFencedVaryTransitionPreservesFreshRepresentations(t *testing.T) {
	t.Parallel()

	for _, tier := range []string{"memory", "disk", "disk_stream"} {
		for _, freshVary := range []string{"Accept", ""} {
			t.Run(tier+"/fresh_vary="+freshVary, func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{MemoryMaxSize: config.Size(2 << 20), DefaultTTL: config.Duration(5 * time.Minute)}
				bodyBytes := 64
				if tier != "memory" {
					cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), config.Size(8<<20)
				}
				if tier == "disk_stream" {
					cfg.MemoryMaxSize, cfg.MaxEntrySize, bodyBytes = config.Size(4096), config.Size(64<<10), 12288
				}
				c := newTestCache(t, cfg)
				entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				releaseOrigin := sync.OnceFunc(func() { close(release) })
				t.Cleanup(func() {
					releaseOrigin()
					<-done
				})
				var calls atomic.Int64
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					n := calls.Add(1)
					w.Header().Set("Cache-Control", "public, max-age=300")
					w.Header().Set("X-Origin-Version", fmt.Sprint(n))
					if n == 1 {
						w.Header().Set("Vary", "Accept-Language")
						close(entered)
						<-release
					} else if freshVary != "" {
						w.Header().Set("Vary", freshVary)
					}
					_, _ = io.WriteString(w, strings.Repeat(fmt.Sprint(n), bodyBytes))
				}))
				serve := func() *httptest.ResponseRecorder {
					rr := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/vary", nil)
					req.Header.Set("Accept", "text/plain")
					req.Header.Set("Accept-Language", "en")
					h.ServeHTTP(rr, req)
					return rr
				}
				go func() {
					defer close(done)
					serve()
				}()
				<-entered
				sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/vary"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				res, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
				if err != nil || res.State != "succeeded" {
					t.Fatalf("purge: res=%+v err=%v", res, err)
				}
				if fresh := serve(); fresh.Header().Get("X-Origin-Version") != "2" {
					t.Fatal("fresh origin response was not published")
				}
				if hit := serve(); hit.Header().Get("X-Cache") != "HIT" {
					t.Fatal("post-cutoff response was not cached")
				}
				releaseOrigin()
				<-done
				after := serve()
				if after.Header().Get("X-Cache") != "HIT" || after.Header().Get("X-Origin-Version") != "2" || calls.Load() != 2 {
					t.Fatalf("fenced old fill disturbed a newer layout: X-Cache=%q version=%q calls=%d",
						after.Header().Get("X-Cache"), after.Header().Get("X-Origin-Version"), calls.Load())
				}
			})
		}
	}

}

func TestPurgeRetryRepairsPinnedReaderManifest(t *testing.T) {
	t.Parallel()

	cfg := config.CacheConfig{
		MemoryMaxSize: config.Size(1024), MaxEntrySize: config.Size(64 << 10),
		DiskPath: t.TempDir(), DiskMaxSize: config.Size(8 << 20),
	}
	c := newTestCache(t, cfg)
	key := keyFor(http.MethodGet, "portal.example.com", "/leased")
	payload := strings.Repeat("leased", 2048)
	c.set(key, &Entry{Status: http.StatusOK, Body: []byte(payload)})
	entry, _, ok := c.disk.getWithMemCapGen(key, 0)
	if !ok {
		t.Fatal("disk-backed entry was not stored")
	}
	reader, err := entry.OpenBody()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	c.disk.testRenameFile = func(string, string) error { return errors.New("canonical file is pinned") }
	c.disk.testPersistTombstoneErr = errors.New("manifest EIO")
	c.disk.testTruncateFile = func(string, int64) error { return errors.New("truncate EIO") }
	sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/leased"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
	if err != nil || first.InvalidationEffective != "uncertain" {
		t.Fatalf("pinned manifest fault was not surfaced: res=%+v err=%v", first, err)
	}
	c.disk.testPersistTombstoneErr = nil
	retry, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{ReuseFenceGen: first.FenceSequence})
	if err != nil || retry.State != "succeeded" || retry.InvalidationEffective != "effective" {
		t.Fatalf("durable retirement still required the admitted reader to finish: res=%+v err=%v", retry, err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != payload {
		t.Fatalf("repair damaged an admitted reader: bytes=%d err=%v", len(body), err)
	}
	c.disk.testRenameFile, c.disk.testTruncateFile = nil, nil
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := newTestCache(t, cfg)
	if _, ok := reopened.get(key); ok {
		t.Fatal("retired canonical file recovered after its reader finished")
	}
}

func TestConditionalCleanupChecksPhysicalDiskGeneration(t *testing.T) {
	t.Parallel()

	for _, fresh := range []bool{false, true} {
		t.Run(fmt.Sprintf("fresh=%t", fresh), func(t *testing.T) {
			t.Parallel()
			cfg := config.CacheConfig{MemoryMaxSize: config.Size(2 << 20), DiskPath: t.TempDir(), DiskMaxSize: config.Size(8 << 20)}
			c := newTestCache(t, cfg)
			key := keyFor(http.MethodGet, "portal.example.com", "/physical")
			c.set(key, &Entry{Status: http.StatusOK, Body: []byte("old")})
			oldGen := c.genSeq.Load()
			if fresh {
				c.set(key, &Entry{Status: http.StatusOK, Body: []byte("fresh")})
			} else {
				c.genMu.Lock()
				c.keyGen[key].gen = c.genSeq.Add(1)
				c.mem.del(key)
				c.keyGen[key].inMem = false
				c.genMu.Unlock()
			}
			c.delOneIfGenAtMost(key, oldGen)
			reopened := newTestCache(t, cfg)
			if fresh {
				assertCachedEntryBody(t, c, key, "fresh")
				assertCachedEntryBody(t, reopened, key, "fresh")
			} else if _, ok := reopened.get(key); ok {
				t.Fatal("advanced catalog generation hid an old disk file from cleanup")
			}
		})
	}
}
