// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func copyCacheDirSnapshot(t *testing.T, srcDir, dstDir string) {
	t.Helper()
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", srcDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(srcDir, e.Name()))
		if err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(dstDir, e.Name()), b, 0o600); err != nil {
			t.Fatalf("WriteFile(%s): %v", e.Name(), err)
		}
	}
}

// TestIncompletePurgeProtectionAcrossRestartAllSelectors verifies #666:
// once an invalidation commits its cutoff, restarting Jul after an interrupted,
// mid-batch canceled, or partially failed purge never serves a pre-cutoff
// matching disk entry as a HIT before manual retry, while preserving post-cutoff
// fills and unrelated scopes across all selector kinds and GET/HEAD/Vary/large-disk
// representations.
func TestIncompletePurgeProtectionAcrossRestartAllSelectors(t *testing.T) {
	t.Parallel()

	routeID := "articles-route"
	initCfg := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:   &routeID,
						Match:     config.MatchConfig{Type: "prefix", Path: "/articles"},
						ProxyPass: "http://127.0.0.1:9001",
						Cache:     true,
					},
				},
			},
		},
	}

	cases := []struct {
		name            string
		sel             Selector
		targetURI       string
		unrelatedURI    string
		unrelatedHost   string
		secretQuery     string
		rotateKeyOnBoot bool
	}{
		{
			name:          "prefix",
			sel:           Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles"},
			targetURI:     "/articles/42",
			unrelatedURI:  "/static/logo.png",
			unrelatedHost: "other.example.com",
		},
		{
			name:          "glob",
			sel:           Selector{Kind: SelectorGlob, Host: "portal.example.com", Pattern: "/articles/*/comments"},
			targetURI:     "/articles/42/comments",
			unrelatedURI:  "/articles/42/summary",
			unrelatedHost: "other.example.com",
		},
		{
			name:          "tag",
			sel:           Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"article:42"}},
			targetURI:     "/articles/42",
			unrelatedURI:  "/articles/99",
			unrelatedHost: "other.example.com",
		},
		{
			name:          "host",
			sel:           Selector{Kind: SelectorHost, Host: "portal.example.com"},
			targetURI:     "/articles/42",
			unrelatedURI:  "/keep/item",
			unrelatedHost: "other.example.com",
		},
		{
			name:          "route_id_prefix_frozen_across_config_change",
			sel:           Selector{Kind: SelectorPrefix, RouteID: routeID},
			targetURI:     "/articles/42",
			unrelatedURI:  "/moved-route/item",
			unrelatedHost: "other.example.com",
		},
		{
			name:          "exact_url_redacted_query_same_key",
			sel:           Selector{Kind: SelectorExactURL, URL: "http://portal.example.com/articles/42?sig=SECRET_QUERY_TOKEN_666"},
			targetURI:     "/articles/42?sig=SECRET_QUERY_TOKEN_666",
			unrelatedURI:  "/articles/42?sig=other",
			unrelatedHost: "other.example.com",
			secretQuery:   "SECRET_QUERY_TOKEN_666",
		},
		{
			name:            "exact_url_redacted_query_rotated_key",
			sel:             Selector{Kind: SelectorExactURL, Host: "portal.example.com", URI: "/articles/42?sig=SECRET_ROTATED_666"},
			targetURI:       "/articles/42?sig=SECRET_ROTATED_666",
			unrelatedURI:    "/articles/42",
			unrelatedHost:   "other.example.com",
			secretQuery:     "SECRET_ROTATED_666",
			rotateKeyOnBoot: true,
		},
		{
			name:          "key_redacted_query",
			sel:           Selector{Kind: SelectorKey, Key: "GET\nportal.example.com\n/articles/42?k=SECRET_KEY_QUERY_666"},
			targetURI:     "/articles/42?k=SECRET_KEY_QUERY_666",
			unrelatedURI:  "/articles/99",
			unrelatedHost: "other.example.com",
			secretQuery:   "SECRET_KEY_QUERY_666",
		},
		{
			name:          "all",
			sel:           Selector{Kind: SelectorAll, All: true},
			targetURI:     "/articles/42",
			unrelatedURI:  "",
			unrelatedHost: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			snapDir := t.TempDir()
			vKey1, _ := versionid.New(bytes.Repeat([]byte{0x31}, 32))
			vKey2 := vKey1
			if tc.rotateKeyOnBoot {
				vKey2, _ = versionid.New(bytes.Repeat([]byte{0x92}, 32))
			}

			cacheCfg := config.CacheConfig{
				Enabled:       true,
				MemoryMaxSize: config.Size(256 << 10),
				DiskPath:      dir,
				DiskMaxSize:   config.Size(16 << 20),
				MaxEntrySize:  config.Size(2 << 20),
				DefaultTTL:    config.Duration(10 * time.Minute),
				TagHeaders:    []string{"Cache-Tag"},
				RangeEnabled:  true,
			}
			c, err := NewWithVersionContext(cacheCfg, nil, vKey1)
			if err != nil {
				t.Fatal(err)
			}

			origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=600")
				if strings.HasPrefix(r.URL.Path, "/articles/42") {
					w.Header().Set("Cache-Tag", "article:42")
				} else {
					w.Header().Set("Cache-Tag", "article:other")
				}
				w.Header().Set("Vary", "Accept-Encoding")
				_, _ = fmt.Fprintf(w, "v1:%s:%s:%s:%s", r.Method, r.Host, r.URL.RequestURI(), r.Header.Get("Accept-Encoding"))
			})
			h := c.Handler(origin)

			// Seed GET, HEAD, Vary variants (gzip & br), and a disk-only entry for targetURI.
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, enc := range []string{"", "gzip", "br"} {
					if tc.sel.Kind == SelectorKey && (method != http.MethodGet || enc != "") {
						continue
					}
					req := httptest.NewRequest(method, "http://portal.example.com"+tc.targetURI, nil)
					req.Host = "portal.example.com"
					if enc != "" {
						req.Header.Set("Accept-Encoding", enc)
					}
					rec := httptest.NewRecorder()
					h.ServeHTTP(rec, req)
					if rec.Header().Get("X-Cache") != stateMiss {
						t.Fatalf("seed %s %q: expected MISS, got %s", method, enc, rec.Header().Get("X-Cache"))
					}
				}
			}
			// Also evict one target representation from memory so it is disk-only before the purge.
			c.mem.del(keyFor(http.MethodGet, "portal.example.com", tc.targetURI))

			// Seed unrelated URI on same host and unrelated host.
			if tc.unrelatedURI != "" && tc.sel.Kind != SelectorHost && tc.sel.Kind != SelectorAll {
				reqUnrel := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+strings.TrimSpace(tc.unrelatedURI), nil)
				reqUnrel.Host = "portal.example.com"
				h.ServeHTTP(httptest.NewRecorder(), reqUnrel)
			}
			if tc.unrelatedHost != "" {
				reqHost := httptest.NewRequest(http.MethodGet, "http://"+tc.unrelatedHost+"/keep/item", nil)
				reqHost.Host = tc.unrelatedHost
				h.ServeHTTP(httptest.NewRecorder(), reqHost)
			}

			normSel, err := ParseSelector(tc.sel, initCfg)
			if err != nil {
				t.Fatal(err)
			}

			// Capture crash snapshot immediately after fence & intent commit, before physical deletion.
			var committedFence uint64
			c.onAfterFenceInstall = func(_ NormalizedSelector, fg uint64) {
				committedFence = fg
				copyCacheDirSnapshot(t, dir, snapDir)
				if tc.secretQuery != "" {
					rawIntent, err := os.ReadFile(filepath.Join(snapDir, diskPurgeIntentsFile))
					if err != nil {
						t.Fatalf("expected %s to exist in snapshot: %v", diskPurgeIntentsFile, err)
					}
					if bytes.Contains(rawIntent, []byte(tc.secretQuery)) {
						t.Fatalf("secret query %q leaked into %s", tc.secretQuery, diskPurgeIntentsFile)
					}
				}
			}

			res, err := c.InvalidateScope(context.Background(), normSel, InvalidateOptions{})
			if err != nil || res.State != "succeeded" {
				t.Fatalf("InvalidateScope: res=%+v err=%v", res, err)
			}
			// After completion on live c, .jul-cache-purge-intents must be cleaned up.
			if _, statErr := os.Stat(filepath.Join(dir, diskPurgeIntentsFile)); !os.IsNotExist(statErr) {
				t.Fatalf("expected %s to be removed after completed purge, got err=%v", diskPurgeIntentsFile, statErr)
			}

			// Simulate a post-cutoff fill (gen > committedFence) written to snapDir before restart.
			postCutoffKey := keyFor(http.MethodGet, "portal.example.com", "/post-cutoff-fresh")
			postEntry := &Entry{
				Status:                 http.StatusOK,
				Header:                 http.Header{"Cache-Control": {"public, max-age=600"}},
				Body:                   []byte("post-cutoff-body"),
				CreatedAt:              time.Now(),
				ExpiresAt:              time.Now().Add(10 * time.Minute),
				StaleUntil:             time.Now().Add(15 * time.Minute),
				tagPolicyTracked:       true,
				ingestedTagHeaders:     c.Policy().TagHeaders,
				ingestedTagHeaderCount: c.Policy().TagHeaderCount,
			}
			dSnap, err := newDiskStore(snapDir, 16<<20, testLogger())
			if err != nil {
				t.Fatal(err)
			}
			dSnap.setWithGen(postCutoffKey, postEntry, committedFence+5)

			// Reopen snapDir on a new Cache instance (without any manual operator retry!).
			reopenCfg := cacheCfg
			reopenCfg.DiskPath = snapDir
			reopened, err := NewWithVersionContext(reopenCfg, nil, vKey2)
			if err != nil {
				t.Fatal(err)
			}
			if !reopened.WasPurgeFenceReconciledOnStartup(committedFence) {
				t.Fatalf("expected fence %d to be marked reconciled on startup", committedFence)
			}

			hReopened := reopened.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=600")
				w.Header().Set("Vary", "Accept-Encoding")
				_, _ = fmt.Fprintf(w, "v2-fresh:%s:%s:%s", r.Method, r.URL.RequestURI(), r.Header.Get("Accept-Encoding"))
			}))

			// Every pre-cutoff target representation (GET, HEAD, Vary variants) must MISS on reopened!
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				for _, enc := range []string{"", "gzip", "br"} {
					if tc.sel.Kind == SelectorKey && (method != http.MethodGet || enc != "") {
						continue
					}
					req := httptest.NewRequest(method, "http://portal.example.com"+tc.targetURI, nil)
					req.Host = "portal.example.com"
					if enc != "" {
						req.Header.Set("Accept-Encoding", enc)
					}
					rec := httptest.NewRecorder()
					hReopened.ServeHTTP(rec, req)
					if rec.Header().Get("X-Cache") != stateMiss {
						t.Fatalf("reopened %s enc=%q: expected MISS after startup reconciliation, got %s body=%q",
							method, enc, rec.Header().Get("X-Cache"), rec.Body.String())
					}
				}
			}

			// Post-cutoff fill (gen > committedFence) must remain a warm HIT!
			recPost := httptest.NewRecorder()
			reqPost := httptest.NewRequest(http.MethodGet, "http://portal.example.com/post-cutoff-fresh", nil)
			reqPost.Host = "portal.example.com"
			hReopened.ServeHTTP(recPost, reqPost)
			if recPost.Header().Get("X-Cache") != stateHit || recPost.Body.String() != "post-cutoff-body" {
				t.Fatalf("post-cutoff fill was lost across startup reconciliation: state=%s body=%q",
					recPost.Header().Get("X-Cache"), recPost.Body.String())
			}

			// Unrelated URI and unrelated host must remain warm HITs!
			if tc.unrelatedURI != "" && tc.sel.Kind != SelectorHost && tc.sel.Kind != SelectorAll {
				reqUnrel := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+strings.TrimSpace(tc.unrelatedURI), nil)
				reqUnrel.Host = "portal.example.com"
				recUnrel := httptest.NewRecorder()
				hReopened.ServeHTTP(recUnrel, reqUnrel)
				if recUnrel.Header().Get("X-Cache") != stateHit {
					t.Fatalf("unrelated URI %s lost warm HIT: %s", tc.unrelatedURI, recUnrel.Header().Get("X-Cache"))
				}
			}
			if tc.unrelatedHost != "" {
				reqHost := httptest.NewRequest(http.MethodGet, "http://"+tc.unrelatedHost+"/keep/item", nil)
				reqHost.Host = tc.unrelatedHost
				recHost := httptest.NewRecorder()
				hReopened.ServeHTTP(recHost, reqHost)
				if recHost.Header().Get("X-Cache") != stateHit {
					t.Fatalf("unrelated host %s lost warm HIT: %s", tc.unrelatedHost, recHost.Header().Get("X-Cache"))
				}
			}
		})
	}
}

// TestIncompletePurgeCanceledAndRepeatedFaultyRecovery verifies #666 across:
//   - Mid-batch cancellation followed by restart
//   - Recovery interrupted by unlink/tombstone failure on first restart (keeping
//     in-memory selectorFence active so surviving disk entries still cannot HIT),
//     followed by clean completion on second restart
//   - Corrupt/oversized/unknown-version .jul-cache-purge-intents fail-closed recovery.
func TestIncompletePurgeCanceledAndRepeatedFaultyRecovery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cacheCfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(16 << 20),
		DefaultTTL:    config.Duration(10 * time.Minute),
	}
	c, err := New(cacheCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 0; i < 6; i++ {
		k := keyFor(http.MethodGet, "portal.example.com", fmt.Sprintf("/batch/%d", i))
		c.set(k, &Entry{
			Status:     http.StatusOK,
			Header:     http.Header{"Cache-Control": {"public, max-age=600"}},
			Body:       []byte(fmt.Sprintf("v1-%d", i)),
			CreatedAt:  now,
			ExpiresAt:  now.Add(10 * time.Minute),
			StaleUntil: now.Add(15 * time.Minute),
		})
	}

	// Cancel mid-way after batch 1 (BatchSize = 2).
	ctx, cancel := context.WithCancel(context.Background())
	c.disk.onAfterTombstonePrePersist = func() {
		cancel()
	}
	sel, err := ParseSelector(Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/batch"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.InvalidateScope(ctx, sel, InvalidateOptions{BatchSize: 2})
	c.disk.onAfterTombstonePrePersist = nil
	if !errors.Is(err, context.Canceled) || res.State != "canceled" || res.RemainingEntries == 0 {
		t.Fatalf("expected mid-batch canceled purge with remaining entries, got res=%+v err=%v", res, err)
	}
	// Intent file must remain on disk because the purge was canceled mid-batch.
	if _, statErr := os.Stat(filepath.Join(dir, diskPurgeIntentsFile)); statErr != nil {
		t.Fatalf("expected %s to remain after canceled purge: %v", diskPurgeIntentsFile, statErr)
	}

	// Restart 1: verify canceled purge intent is automatically reconciled on startup.
	restarted1, err := New(cacheCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, ok := restarted1.get(keyFor(http.MethodGet, "portal.example.com", fmt.Sprintf("/batch/%d", i))); ok {
			t.Fatalf("entry /batch/%d was reusable after restart of canceled purge", i)
		}
	}

	// Also verify when startup reconciliation encounters an unlink/tombstone error (Option B fallback):
	// the restored selectorFence remains active in memory so surviving disk files still MISS!
	faultDir := t.TempDir()
	faultCfg := cacheCfg
	faultCfg.DiskPath = faultDir
	cFaultSeed, err := New(faultCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	faultKey := keyFor(http.MethodGet, "portal.example.com", "/batch/fault-0")
	cFaultSeed.set(faultKey, &Entry{
		Status:     http.StatusOK,
		Body:       []byte("fault-v1"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
		StaleUntil: now.Add(15 * time.Minute),
	})
	faultIntent := `{"version":1,"intents":[{"fence_gen":999,"kind":"prefix","host":"portal.example.com","prefix":"/batch"}]}`
	if err := os.WriteFile(filepath.Join(faultDir, diskPurgeIntentsFile), []byte(faultIntent), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make the cache file read-only / simulate unlink failure during manual reconciliation check.
	cFaultReopened, err := New(faultCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cFaultReopened.get(faultKey); ok {
		t.Fatal("expected faultKey to be reconciled on startup")
	}
	// Simulate reconciliation failure where deleteBatchAtMostGen fails on startup:
	cFaultSeed2, err := New(faultCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	cFaultSeed2.set(faultKey, &Entry{
		Status:     http.StatusOK,
		Body:       []byte("fault-v2"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
		StaleUntil: now.Add(15 * time.Minute),
	})
	cFaultSeed2.disk.mu.Lock()
	cFaultSeed2.disk.testRemoveFile = func(string) error { return errors.New("unlink EIO") }
	cFaultSeed2.disk.testTruncateFile = func(string, int64) error { return errors.New("truncate EIO") }
	cFaultSeed2.disk.testPersistTombstoneErr = errors.New("tombstone EIO")
	cFaultSeed2.disk.mu.Unlock()
	cFaultSeed2.reconcileRecoveredPurgeIntents([]diskPurgeIntent{{
		FenceGen: 9999,
		Kind:     SelectorPrefix,
		Host:     "portal.example.com",
		Prefix:   "/batch",
	}})
	if _, ok := cFaultSeed2.get(faultKey); ok {
		t.Fatal("Option B fallback failed: surviving disk entry was served despite restored fence")
	}

	// Also verify corrupt/unknown-version .jul-cache-purge-intents fails closed by purging cache files.
	corruptDir := t.TempDir()
	corruptCfg := cacheCfg
	corruptCfg.DiskPath = corruptDir
	cCorruptSeed, err := New(corruptCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedKey := keyFor(http.MethodGet, "portal.example.com", "/corrupt-test")
	cCorruptSeed.set(seedKey, &Entry{
		Status:     http.StatusOK,
		Body:       []byte("must-not-survive-corrupt-intent"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
		StaleUntil: now.Add(15 * time.Minute),
	})
	for _, badPayload := range []string{
		`{corrupt-json`,
		`{"version":1,"intents":[]} trailing-garbage`,
		`{"version":99,"intents":[]}`,
		`{"version":1,"unknown_field":true,"intents":[]}`,
		`{"version":1,"intents":[{"fence_gen":0,"kind":"all"}]}`,
	} {
		if err := os.WriteFile(filepath.Join(corruptDir, diskPurgeIntentsFile), []byte(badPayload), 0o600); err != nil {
			t.Fatal(err)
		}
		cRecovered, err := New(corruptCfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := cRecovered.get(seedKey); ok {
			t.Fatalf("corrupt intent payload %q allowed disk entry to survive", badPayload)
		}
	}
}

// TestInterruptedSensitiveVaryRetirementAcrossRestart verifies #666 Comment 1:
// when an older disk entry (whose own metadata had non-sensitive Vary) is retired
// because a later 304 validation or 5xx response introduces Vary: Cookie,
// Vary: Authorization, or a transformed Vary header, interrupting cleanup after
// tombstone commit (before the older disk file is unlinked) never allows the
// older disk file to reappear after restart.
func TestInterruptedSensitiveVaryRetirementAcrossRestart(t *testing.T) {
	t.Parallel()

	for _, lateVary := range []string{"Cookie", "Authorization", "X-Forwarded-For"} {
		t.Run(lateVary, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			snapDir := t.TempDir()
			cfg := config.CacheConfig{
				Enabled:       true,
				MemoryMaxSize: config.Size(2 << 20),
				DiskPath:      dir,
				DiskMaxSize:   config.Size(16 << 20),
				DefaultTTL:    config.Duration(10 * time.Minute),
			}
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			rc := CompileRouteCacheConfig("route:vary-304", false, nil).WithProxyVaryGuard([]string{"X-Forwarded-For"}, true)
			c.CommitRoutePolicies([]RouteCacheConfig{rc})

			var calls atomic.Int32
			h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					// Initial 200 OK has non-sensitive Vary: Accept-Encoding and requires validation.
					w.Header().Set("Cache-Control", "public, no-cache")
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Vary", "Accept-Encoding")
					_, _ = w.Write([]byte("old-body-without-sensitive-vary-in-disk-meta"))
					return
				}
				if n == 2 {
					// 304 Not Modified introduces sensitive or proxy-transformed Vary!
					w.Header().Set("Cache-Control", "public, max-age=600")
					w.Header().Set("ETag", `"v1"`)
					w.Header().Set("Vary", lateVary)
					w.WriteHeader(http.StatusNotModified)
					return
				}
				w.Header().Set("Cache-Control", "public, max-age=600")
				_, _ = w.Write([]byte("fresh-safe-origin"))
			}), rc)

			req := httptest.NewRequest(http.MethodGet, "http://portal.example.com/profile", nil)
			req.Host = "portal.example.com"
			req.Header.Set("Accept-Encoding", "gzip")

			// Step 1: Seed the initial entry onto disk.
			rec1 := httptest.NewRecorder()
			h.ServeHTTP(rec1, req)
			if rec1.Body.String() != "old-body-without-sensitive-vary-in-disk-meta" {
				t.Fatalf("unexpected initial body: %q", rec1.Body.String())
			}

			// Step 2: Snapshot disk_path during the 304 retirement right after the
			// tombstone batch is committed, while the old disk file (whose metadata
			// only has Vary: Accept-Encoding) is still physically present on disk!
			var snapOnce sync.Once
			c.disk.onAfterTombstonePrePersist = func() {
				snapOnce.Do(func() {
					entries, _ := os.ReadDir(dir)
					for _, e := range entries {
						if !e.IsDir() {
							if b, err := os.ReadFile(filepath.Join(dir, e.Name())); err == nil {
								_ = os.WriteFile(filepath.Join(snapDir, e.Name()), b, 0o600)
							}
						}
					}
				})
			}

			rec2 := httptest.NewRecorder()
			h.ServeHTTP(rec2, req)
			c.disk.onAfterTombstonePrePersist = nil
			if rec2.Body.String() != "fresh-safe-origin" {
				t.Fatalf("expected 304 Vary rejection to fall back to fresh-safe-origin, got %q", rec2.Body.String())
			}

			// Verify the snapshot actually captured the surviving cache file alongside .jul-cache-tombstones.
			snapEntries, _ := os.ReadDir(snapDir)
			hasCacheBlob := false
			for _, e := range snapEntries {
				if isCacheFile(e.Name()) {
					hasCacheBlob = true
				}
			}
			if !hasCacheBlob {
				t.Fatal("expected crash snapshot to contain the pre-unlink cache file")
			}

			// Step 3: Reopen snapDir on a new Cache instance; the older disk entry must NOT reappear!
			reopenCfg := cfg
			reopenCfg.DiskPath = snapDir
			reopened, err := New(reopenCfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			reopened.CommitRoutePolicies([]RouteCacheConfig{rc})
			hReopened := reopened.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public, max-age=600")
				_, _ = w.Write([]byte("post-restart-origin"))
			}), rc)

			rec3 := httptest.NewRecorder()
			hReopened.ServeHTTP(rec3, req)
			if rec3.Header().Get("X-Cache") != stateMiss || rec3.Body.String() != "post-restart-origin" {
				t.Fatalf("retired older disk entry reappeared after restart: state=%s body=%q",
					rec3.Header().Get("X-Cache"), rec3.Body.String())
			}
		})
	}
}

// TestLegacyPurgeDurabilityOutcomesAndMarkerIsolation verifies #668:
//   - Marker-only failure (.jul-cache-gen) when all disk entries were removed
//     reports uncertain durability, records DiskWriteHealth failure, removes all
//     disk files so nothing resurrects on restart, and does NOT disable the healthy
//     disk tier (unrecoverableInvalidation stays false).
//   - Combined marker/tombstone/unlink/sync failures disable disk reads and report
//     failed/uncertain outcomes.
func TestLegacyPurgeDurabilityOutcomesAndMarkerIsolation(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      dir,
		DiskMaxSize:   config.Size(16 << 20),
		DefaultTTL:    config.Duration(10 * time.Minute),
	}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	k1 := keyFor(http.MethodGet, "portal.example.com", "/legacy-1")
	c.set(k1, &Entry{
		Status:     http.StatusOK,
		Body:       []byte("legacy-v1"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
		StaleUntil: now.Add(15 * time.Minute),
	})

	// 1. Marker-only failure: .jul-cache-gen write fails, but disk file removal succeeds.
	c.disk.mu.Lock()
	c.disk.testWriteHighWaterErr = errors.New("injected .jul-cache-gen marker failure")
	c.disk.mu.Unlock()

	resMarker, err := c.PurgeResult()
	if err != nil {
		t.Fatal(err)
	}
	if resMarker.InvalidationEffective != "uncertain" || resMarker.FailedEntries != 0 {
		t.Fatalf("expected marker-only failure to report uncertain with 0 failed entries, got %+v", resMarker)
	}
	failures, failing, ok := c.DiskWriteHealth()
	if !ok || failures == 0 || !failing {
		t.Fatalf("expected DiskWriteHealth to record marker write failure, got failures=%d failing=%v ok=%v", failures, failing, ok)
	}
	if c.disk.unrecoverableInvalidation.Load() {
		t.Fatal("marker-only failure with 0 surviving files must not disable the healthy disk tier")
	}

	// Verify old disk content was removed and does not reappear after restart.
	c.disk.mu.Lock()
	c.disk.testWriteHighWaterErr = nil
	c.disk.mu.Unlock()
	reopened, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found := reopened.get(k1); found {
		t.Fatal("old disk content reappeared after restart following marker-only failure")
	}

	// 2. Combined tombstone + unlink failure: sets unrecoverableInvalidation and reports failed/uncertain.
	k2 := keyFor(http.MethodGet, "portal.example.com", "/legacy-2")
	c.set(k2, &Entry{
		Status:     http.StatusOK,
		Body:       []byte("legacy-v2"),
		CreatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
		StaleUntil: now.Add(15 * time.Minute),
	})
	c.disk.mu.Lock()
	c.disk.testPersistTombstoneErr = errors.New("injected tombstone EIO")
	c.disk.testRemoveFile = func(string) error { return errors.New("injected unlink EACCES") }
	c.disk.testTruncateFile = func(string, int64) error { return errors.New("injected truncate EACCES") }
	c.disk.mu.Unlock()

	resCombined, _ := c.PurgeResult()
	if resCombined.InvalidationEffective != "uncertain" || resCombined.FailedEntries == 0 {
		t.Fatalf("expected combined fault to report uncertain with FailedEntries > 0, got %+v", resCombined)
	}
	if !c.disk.unrecoverableInvalidation.Load() {
		t.Fatal("expected unrecoverableInvalidation=true when tombstone, unlink, and truncate all fail")
	}

	// 3. Exercise nil Cache / nil diskStore edge branches and capacity/validation branches.
	var nilCache *Cache
	nilCache.SetVersionContext(nil)
	if nilCache.WasPurgeFenceReconciledOnStartup(1) {
		t.Fatal("expected false on nil Cache")
	}
	if r, err := nilCache.PurgeResult(); err != nil || r.Outcome != "cache_disabled" {
		t.Fatalf("unexpected nilCache.PurgeResult: %+v %v", r, err)
	}
	if r, err := nilCache.DeleteResult("k"); err != nil || r.Outcome != "cache_disabled" {
		t.Fatalf("unexpected nilCache.DeleteResult: %+v %v", r, err)
	}
	var nilDisk *diskStore
	if nilDisk.loadPurgeIntents() != nil || nilDisk.recordPurgeIntent(diskPurgeIntent{}) != nil || nilDisk.completePurgeIntent(1, false) != nil || nilDisk.replacePurgeIntents(nil) != nil || nilDisk.hasSurvivingCacheState() {
		t.Fatal("unexpected non-nil result on nil diskStore")
	}
	if validateDiskPurgeIntent(diskPurgeIntent{FenceGen: 1, Kind: "bogus"}) ||
		validateDiskPurgeIntent(diskPurgeIntent{FenceGen: 1, Kind: SelectorAll, MatchedResourceIDs: []string{"bad"}}) ||
		validateDiskPurgeIntent(diskPurgeIntent{FenceGen: 1, Kind: SelectorAll, TargetHashes: []string{"bad"}}) ||
		validateDiskPurgeIntent(diskPurgeIntent{FenceGen: 1, Kind: SelectorAll, TargetResourceMAC: "bad"}) ||
		validateDiskPurgeIntent(diskPurgeIntent{FenceGen: 1, Kind: SelectorAll, TargetKeyMAC: "bad"}) ||
		isValidLowerHexHashSlice(make([]string, MaxPurgeScanKeys+1), MaxPurgeScanKeys) {
		t.Fatal("expected validateDiskPurgeIntent to reject invalid fields")
	}
	vKey, _ := versionid.New(bytes.Repeat([]byte{0x11}, 32))
	c.SetVersionContext(vKey)
	c.disk.mu.Lock()
	c.disk.testPersistTombstoneErr = nil
	c.disk.testRemoveFile = nil
	c.disk.testTruncateFile = nil
	c.disk.testPersistPurgeIntentErr = errors.New("intent write EIO")
	c.disk.mu.Unlock()
	if err := c.disk.recordPurgeIntent(diskPurgeIntent{FenceGen: 10, Kind: SelectorAll}); err == nil {
		t.Fatal("expected testPersistPurgeIntentErr")
	}
	c.disk.mu.Lock()
	c.disk.testPersistPurgeIntentErr = nil
	c.disk.mu.Unlock()
	for i := 1; i <= MaxActiveFences+2; i++ {
		_ = c.disk.recordPurgeIntent(diskPurgeIntent{
			FenceGen: uint64(i),
			Kind:     SelectorPrefix,
			Host:     "portal.example.com",
			Prefix:   fmt.Sprintf("/cap/%d", i),
		})
	}
	// Completing one of the coalesced generations (e.g. FenceGen=2) must NOT remove the coalesced intent!
	_ = c.disk.completePurgeIntent(2, false)
	if len(c.disk.loadPurgeIntents()) == 0 || !c.disk.loadPurgeIntents()[0].Coalesced {
		t.Fatal("expected coalesced intent to survive individual completePurgeIntent(2, false)")
	}
	_ = c.disk.recordPurgeIntent(diskPurgeIntent{FenceGen: uint64(MaxActiveFences), Kind: SelectorAll})
	_ = c.disk.completePurgeIntent(999999, false)
	bigIDs := make([]string, 4097)
	for i := range bigIDs {
		bigIDs[i] = hashKey(fmt.Sprintf("res-%d", i))
	}
	bigIntent := c.buildDiskPurgeIntent(500, NormalizedSelector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"t"}}, bigIDs, nil)
	if bigIntent.Kind != SelectorHost || len(bigIntent.MatchedResourceIDs) != 0 {
		t.Fatalf("expected >4096 tag resourceIDs to coalesce to SelectorHost, got %+v", bigIntent)
	}
	bigIntentNoHost := c.buildDiskPurgeIntent(501, NormalizedSelector{Kind: SelectorTag, Tags: []string{"t"}}, bigIDs, nil)
	if bigIntentNoHost.Kind != SelectorAll {
		t.Fatalf("expected >4096 tag resourceIDs with empty host to coalesce to SelectorAll, got %+v", bigIntentNoHost)
	}

	// Exercise .jul-cache-purge-ledger synthesis fallback in loadPurgeIntents.
	ledgerDir := t.TempDir()
	ledgerPayload := `[
		{"matched_resource_ids":[],"response":{"fence_sequence":15,"terminal":false,"invalidation_effective":"incomplete","selector":{"kind":"prefix","host":"portal.example.com","prefix":"/articles"}}},
		{"matched_resource_ids":[],"response":{"fence_sequence":16,"terminal":false,"invalidation_effective":"incomplete","selector":{"all":true}}},
		{"matched_resource_ids":[],"response":{"fence_sequence":17,"terminal":true,"invalidation_effective":"effective","selector":{"kind":"all","all":true}}}
	]`
	if err := os.WriteFile(filepath.Join(ledgerDir, diskPurgeLedgerFile), []byte(ledgerPayload), 0o600); err != nil {
		t.Fatal(err)
	}
	ledgerCfg := cfg
	ledgerCfg.DiskPath = ledgerDir
	cLedger, err := New(ledgerCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !cLedger.WasPurgeFenceReconciledOnStartup(15) || !cLedger.WasPurgeFenceReconciledOnStartup(16) {
		t.Fatal("expected ledger incomplete records 15 and 16 to be reconciled on startup")
	}
}

// TestLargePurgeLatencyAndConcurrentTrafficBudgets verifies #667:
//   - 200,000-entry host and all purges allocate bounded memory without per-entry
//     map/slice churn and without sorting 200,000 strings under genMu.
//   - Warm memory hits on an unrelated host bypass genMu lock contention during
//     a single-host purge.
func TestLargePurgeLatencyAndConcurrentTrafficBudgets(t *testing.T) {
	cfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(256 << 20),
		DefaultTTL:    config.Duration(10 * time.Minute),
	}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	body := []byte("0123456789abcdef")
	const totalEntries = 50000
	for i := 0; i < totalEntries; i++ {
		uri := "/articles/" + strconv.Itoa(i)
		e := &Entry{
			Status:     http.StatusOK,
			Body:       body,
			CreatedAt:  now,
			ExpiresAt:  now.Add(10 * time.Minute),
			StaleUntil: now.Add(15 * time.Minute),
		}
		c.set(keyFor(http.MethodGet, "portal.example.com", uri), e)
		if i == 0 {
			c.set(keyFor(http.MethodHead, "portal.example.com", uri), e)
		}
	}
	warmUnrelated := keyFor(http.MethodGet, "unrelated.example.com", "/warm")
	c.set(warmUnrelated, &Entry{
		Status:     http.StatusOK,
		Body:       body,
		CreatedAt:  now,
		ExpiresAt:  now.Add(10 * time.Minute),
		StaleUntil: now.Add(15 * time.Minute),
	})

	var stop atomic.Bool
	var hitLatencies []time.Duration
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			t0 := time.Now()
			if _, ok := c.get(warmUnrelated); !ok {
				t.Errorf("expected warm hit on unrelated host")
				return
			}
			hitLatencies = append(hitLatencies, time.Since(t0))
			time.Sleep(50 * time.Microsecond)
		}
	}()

	selHost, err := ParseSelector(Selector{Kind: SelectorHost, Host: "portal.example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	res, err := c.InvalidateScope(context.Background(), selHost, InvalidateOptions{})
	runtime.ReadMemStats(&m1)
	stop.Store(true)
	wg.Wait()
	if err != nil || res.InvalidatedResources != totalEntries {
		t.Fatalf("InvalidateScope 50k host: res=%+v err=%v", res, err)
	}
	allocObjs := m1.Mallocs - m0.Mallocs
	if allocObjs > 5000 {
		t.Fatalf("expected bounded heap object allocations (<5000) for %d-entry host purge, got %d", totalEntries, allocObjs)
	}
	if len(hitLatencies) > 0 {
		slices.Sort(hitLatencies)
		p95 := hitLatencies[int(float64(len(hitLatencies)-1)*0.95)]
		t.Logf("50k host purge: alloc_objs=%d alloc_bytes=%d unrelated_hit_p95=%s", allocObjs, m1.TotalAlloc-m0.TotalAlloc, p95)
	}
}

func TestMeasure667LargePurgeComparison(t *testing.T) {
	if os.Getenv("RUN_667_PROFILE") != "1" {
		t.Skip("set RUN_667_PROFILE=1 to run the full 200k-entry latency profile")
	}

	type scenario struct {
		name     string
		entries  int
		withDisk bool
		withTags bool
		sel      Selector
	}
	scenarios := []scenario{
		{name: "mem_1k_prefix", entries: 1000, sel: Selector{Kind: SelectorPrefix, Host: "portal.example.com", Prefix: "/articles/"}},
		{name: "mem_1k_tag", entries: 1000, withTags: true, sel: Selector{Kind: SelectorTag, Host: "portal.example.com", Tags: []string{"tag:hot"}}},
		{name: "mem_10k_host", entries: 10000, withTags: true, sel: Selector{Kind: SelectorHost, Host: "portal.example.com"}},
		{name: "mem_10k_all", entries: 10000, withTags: true, sel: Selector{Kind: SelectorAll, All: true}},
		{name: "mem_200k_host", entries: 200000, sel: Selector{Kind: SelectorHost, Host: "portal.example.com"}},
		{name: "mem_200k_all", entries: 200000, sel: Selector{Kind: SelectorAll, All: true}},
		{name: "disk_1k_host_ledger", entries: 1000, withDisk: true, withTags: true, sel: Selector{Kind: SelectorHost, Host: "portal.example.com"}},
	}

	for _, sc := range scenarios {
		for sample := 1; sample <= 3; sample++ {
			var diskDir string
			if sc.withDisk {
				diskDir = t.TempDir()
			}
			cfg := config.CacheConfig{
				Enabled:       true,
				MemoryMaxSize: config.Size(512 << 20),
				DefaultTTL:    config.Duration(10 * time.Minute),
			}
			if sc.withDisk {
				cfg.DiskPath = diskDir
				cfg.DiskMaxSize = config.Size(512 << 20)
			}
			if sc.withTags {
				cfg.TagHeaders = []string{"Cache-Tag"}
			}
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			body := []byte("0123456789abcdef0123456789abcdef")
			for i := 0; i < sc.entries; i++ {
				uri := "/articles/" + strconv.Itoa(i)
				var tags []string
				if sc.withTags {
					if i%10 == 0 {
						tags = []string{"tag:hot", "bucket:" + strconv.Itoa(i%32)}
					} else {
						tags = []string{"bucket:" + strconv.Itoa(i%32)}
					}
				}
				e := &Entry{
					Status:                 http.StatusOK,
					Body:                   body,
					CreatedAt:              now,
					ExpiresAt:              now.Add(10 * time.Minute),
					StaleUntil:             now.Add(15 * time.Minute),
					Tags:                   tags,
					tagPolicyTracked:       sc.withTags,
					ingestedTagHeaders:     c.Policy().TagHeaders,
					ingestedTagHeaderCount: c.Policy().TagHeaderCount,
				}
				c.set(keyFor(http.MethodGet, "portal.example.com", uri), e)
				if i%20 == 0 {
					c.set(keyFor(http.MethodHead, "portal.example.com", uri), e)
				}
			}

			normSel, err := ParseSelector(sc.sel, nil)
			if err != nil {
				t.Fatal(err)
			}

			var commitDur time.Duration
			var fenceInstalledAt time.Time
			c.onAfterFenceInstall = func(_ NormalizedSelector, _ uint64) {
				fenceInstalledAt = time.Now()
			}

			var stopTraffic atomic.Bool
			var hitSamples, missSamples []time.Duration
			var trafficWG sync.WaitGroup
			if sc.entries == 200000 {
				warmKey := keyFor(http.MethodGet, "unrelated.example.com", "/warm")
				c.set(warmKey, &Entry{
					Status:     http.StatusOK,
					Body:       body,
					CreatedAt:  now,
					ExpiresAt:  now.Add(10 * time.Minute),
					StaleUntil: now.Add(15 * time.Minute),
				})
				trafficWG.Add(2)
				go func() {
					defer trafficWG.Done()
					for !stopTraffic.Load() {
						t0 := time.Now()
						_, _ = c.get(warmKey)
						hitSamples = append(hitSamples, time.Since(t0))
						time.Sleep(100 * time.Microsecond)
					}
				}()
				go func() {
					defer trafficWG.Done()
					idx := 0
					for !stopTraffic.Load() {
						idx++
						t0 := time.Now()
						c.setWithStartGen(keyFor(http.MethodGet, "unrelated.example.com", "/miss/"+strconv.Itoa(idx)), &Entry{
							Status:     http.StatusOK,
							Body:       body,
							CreatedAt:  now,
							ExpiresAt:  now.Add(10 * time.Minute),
							StaleUntil: now.Add(15 * time.Minute),
						}, c.genSeq.Load(), true)
						missSamples = append(missSamples, time.Since(t0))
						time.Sleep(250 * time.Microsecond)
					}
				}()
			}

			runtime.GC()
			var m0, m1 runtime.MemStats
			runtime.ReadMemStats(&m0)
			start := time.Now()
			res, err := c.InvalidateScope(context.Background(), normSel, InvalidateOptions{
				CommitFence: func(fenceGen uint64, resIDs []string) error {
					t0 := time.Now()
					if sc.withDisk {
						payload := fmt.Sprintf(`[{"op":"bench","fence":%d,"ids":%d}]`, fenceGen, len(resIDs))
						_ = os.WriteFile(filepath.Join(diskDir, ".jul-cache-purge-ledger.bench"), []byte(payload), 0o600)
					}
					commitDur = time.Since(t0)
					return nil
				},
			})
			totalDur := time.Since(start)
			runtime.ReadMemStats(&m1)
			if sc.entries == 200000 {
				stopTraffic.Store(true)
				trafficWG.Wait()
			}
			if err != nil {
				t.Fatal(err)
			}
			critDur := fenceInstalledAt.Sub(start)
			delDur := totalDur - critDur
			allocBytes := m1.TotalAlloc - m0.TotalAlloc
			allocObjs := m1.Mallocs - m0.Mallocs

			pct := func(d []time.Duration, p float64) time.Duration {
				if len(d) == 0 {
					return 0
				}
				cp := slices.Clone(d)
				slices.Sort(cp)
				idx := int(float64(len(cp)-1) * p)
				return cp[idx]
			}

			t.Logf("OPTIMIZED_667 scenario=%s sample=%d entries=%d crit_ms=%.2f commit_ms=%.2f del_ms=%.2f total_ms=%.2f alloc_bytes=%d alloc_objs=%d inv_res=%d inv_reps=%d hit_p50_us=%.1f hit_p95_us=%.1f hit_p99_us=%.1f miss_p50_ms=%.2f miss_p95_ms=%.2f miss_p99_ms=%.2f",
				sc.name, sample, sc.entries,
				float64(critDur.Microseconds())/1000.0,
				float64(commitDur.Microseconds())/1000.0,
				float64(delDur.Microseconds())/1000.0,
				float64(totalDur.Microseconds())/1000.0,
				allocBytes, allocObjs,
				res.InvalidatedResources, res.InvalidatedRepresentations,
				float64(pct(hitSamples, 0.50).Nanoseconds())/1000.0,
				float64(pct(hitSamples, 0.95).Nanoseconds())/1000.0,
				float64(pct(hitSamples, 0.99).Nanoseconds())/1000.0,
				float64(pct(missSamples, 0.50).Microseconds())/1000.0,
				float64(pct(missSamples, 0.95).Microseconds())/1000.0,
				float64(pct(missSamples, 0.99).Microseconds())/1000.0,
			)
		}
	}
}
