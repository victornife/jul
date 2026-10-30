// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

func matcherFixture(value string, identified bool) (config.ServerConfig, config.LocationConfig) {
	id := "catalog-route"
	loc := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/", Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &value}}}}
	if identified {
		loc.RouteID = &id
	}
	return config.ServerConfig{Listen: ":8080", ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{loc}}, loc
}

func compileMatcher(srv config.ServerConfig, loc config.LocationConfig, versions *versionid.Context) RouteCacheConfig {
	return CompileRouteCacheConfig(RouteScope(srv, loc), true, loc.CachePolicy).WithMatcher(loc, versions)
}

func matcherRequest(c *Cache, rc RouteCacheConfig, path, body string) *httptest.ResponseRecorder {
	h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte(body))
	}), rc)
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "http://example.com"+path, nil))
	return r
}

func TestMatcherCompatibilityWarmReloadAndRestart(t *testing.T) {
	for _, identified := range []bool{false, true} {
		t.Run(fmt.Sprint(identified), func(t *testing.T) {
			cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20, DefaultTTL: config.Duration(time.Hour)}
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			key, _ := versionid.New(bytes.Repeat([]byte{0x73}, 32))
			srv, loc := matcherFixture("synthetic-secret-a", identified)
			rc := compileMatcher(srv, loc, key)
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			matcherRequest(c, rc, "/unchanged", "original")
			matcherRequest(c, rc, "/changed", "old-matcher")
			before := c.CurrentRouteEpoch(rc.Scope)
			for i := 0; i < 3; i++ {
				c.CommitRoutePolicies([]RouteCacheConfig{rc})
			}
			if c.CurrentRouteEpoch(rc.Scope) != before {
				t.Fatal("unchanged reload advanced epoch")
			}
			if r := matcherRequest(c, rc, "/unchanged", "unwanted-origin"); r.Header().Get("X-Cache") != "HIT" || r.Body.String() != "original" {
				t.Fatal("unchanged memory entry went cold")
			}
			c, err = New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			if r := matcherRequest(c, rc, "/unchanged", "unwanted-origin"); r.Header().Get("X-Cache") != "HIT" || r.Body.String() != "original" {
				t.Fatal("unchanged disk entry went cold on restart")
			}
			// Simulate a matcher edit while stopped, then a return to the old
			// predicate. Stable identity alone must never authorize the old body.
			srvB, locB := matcherFixture("synthetic-secret-b", identified)
			rcB := compileMatcher(srvB, locB, key)
			if rcB.Scope != rc.Scope {
				t.Fatal("predicate value changed the partition")
			}
			c, err = New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.CommitRoutePolicies([]RouteCacheConfig{rcB})
			if c.CurrentRouteEpoch(rc.Scope) <= before {
				t.Fatal("offline matcher edit did not advance epoch")
			}
			if r := matcherRequest(c, rcB, "/changed", "new-matcher"); r.Header().Get("X-Cache") != "MISS" || r.Body.String() != "new-matcher" {
				t.Fatal("offline matcher edit reused old content")
			}
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			if r := matcherRequest(c, rc, "/changed", "restored-matcher"); r.Header().Get("X-Cache") != "MISS" || r.Body.String() != "restored-matcher" {
				t.Fatal("matcher rollback resurrected an old epoch")
			}
		})
	}
}

func TestMatcherProofPrivacyContextAndPersistence(t *testing.T) {
	keyA, _ := versionid.New(bytes.Repeat([]byte{0x41}, 32))
	keyB, _ := versionid.New(bytes.Repeat([]byte{0x42}, 32))
	srv, loc := matcherFixture("synthetic-private-predicate", true)
	a, b := compileMatcher(srv, loc, keyA), compileMatcher(srv, loc, keyB)
	if a.Scope != b.Scope || a.matcherProof == b.matcherProof {
		t.Fatal("key context must affect only compatibility")
	}
	if a.matcherProof == keyA.Digest("effective-config", []byte("synthetic-private-predicate")) {
		t.Fatal("matcher proof lacks its own domain")
	}
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies([]RouteCacheConfig{a})
	matcherRequest(c, a, "/item", "old-context")
	raw, err := os.ReadFile(filepath.Join(cfg.DiskPath, diskRouteEpochsFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("synthetic-private-predicate")) || !bytes.Contains(raw, []byte(a.matcherProof)) {
		t.Fatal("manifest must retain only the private compatibility proof")
	}
	c.disk.mu.Lock()
	items := c.disk.snapshotItemsLocked()
	c.disk.mu.Unlock()
	for _, item := range items {
		if strings.Contains(item.Key, a.matcherProof) {
			t.Fatal("compatibility proof entered a cache key")
		}
		rawEntry, err := os.ReadFile(c.disk.path(hashKey(item.Key)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(rawEntry, []byte(a.matcherProof)) || bytes.Contains(rawEntry, []byte("synthetic-private-predicate")) {
			t.Fatal("compatibility evidence entered representation metadata")
		}
	}
	c, err = New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies([]RouteCacheConfig{b})
	if r := matcherRequest(c, b, "/item", "new-context"); r.Body.String() != "new-context" || r.Header().Get("X-Cache") != "MISS" {
		t.Fatal("different key context authorized old cache")
	}
}

func TestMatcherIdentityReorderingAndUnrelatedInsertion(t *testing.T) {
	for _, identified := range []bool{false, true} {
		t.Run(fmt.Sprint(identified), func(t *testing.T) {
			key, _ := versionid.New(bytes.Repeat([]byte{0x73}, 32))
			srv, a := matcherFixture("a", identified)
			_, b := matcherFixture("b", identified)
			if identified {
				id := "other-route"
				b.RouteID = &id
			}
			srv.Locations = []config.LocationConfig{a, b}
			scopes := RouteScopes(srv)
			if scopes[0] == scopes[1] {
				t.Fatal("same-path routes collided")
			}
			for i, loc := range srv.Locations {
				if scopes[i] != RouteScope(srv, loc) {
					t.Fatal("batch/inspection scope drift")
				}
			}
			c, _ := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20}, nil)
			rcA, rcB := compileMatcher(srv, a, key), compileMatcher(srv, b, key)
			c.CommitRoutePolicies([]RouteCacheConfig{rcA, rcB})
			matcherRequest(c, rcA, "/shared", "a-original")
			matcherRequest(c, rcB, "/shared", "b-original")
			// An unrelated path must not shift anonymous same-path identities.
			x := config.LocationConfig{Match: config.MatchConfig{Path: "/unrelated", Methods: []string{"GET"}}}
			srv.Locations = []config.LocationConfig{x, a, b}
			if compileMatcher(srv, a, key).Scope != rcA.Scope || compileMatcher(srv, b, key).Scope != rcB.Scope {
				t.Fatal("unrelated insertion shifted identity")
			}
			srv.Locations = []config.LocationConfig{b, a}
			afterA, afterB := compileMatcher(srv, a, key), compileMatcher(srv, b, key)
			c.CommitRoutePolicies([]RouteCacheConfig{afterA, afterB})
			wantState, wantBody := "MISS", "a-new"
			if identified {
				wantState, wantBody = "HIT", "a-original"
			}
			if r := matcherRequest(c, afterA, "/shared", "a-new"); r.Header().Get("X-Cache") != wantState || r.Body.String() != wantBody {
				t.Fatalf("reorder reused wrong identity: %s %q", r.Header().Get("X-Cache"), r.Body.String())
			}
		})
	}
}

func TestMatcherProofTracksMatchingSemantics(t *testing.T) {
	srv, loc := matcherFixture("tenant", true)
	loc.Match.Methods = []string{"GET", "HEAD"}
	key, _ := versionid.New(bytes.Repeat([]byte{0x73}, 32))
	base := compileMatcher(srv, loc, key)
	for name, change := range map[string]func(*config.LocationConfig){
		"path":    func(l *config.LocationConfig) { l.Match.Path = "/other" },
		"type":    func(l *config.LocationConfig) { l.Match.Type = "exact" },
		"methods": func(l *config.LocationConfig) { l.Match.Methods = []string{"GET"} },
		"headers": func(l *config.LocationConfig) {
			v := "other"
			l.Match.Headers = []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &v}}
		},
		"query": func(l *config.LocationConfig) {
			v := "other"
			l.Match.Query = []config.QueryMatch{{Name: "audience", Op: "exact", Value: &v}}
		},
		"preflight": func(l *config.LocationConfig) { l.CORS = &config.CORSConfig{Enabled: true} },
	} {
		t.Run(name, func(t *testing.T) {
			next := loc
			change(&next)
			rc := compileMatcher(srv, next, key)
			if rc.Scope != base.Scope || rc.matcherProof == base.matcherProof {
				t.Fatal("matching change must affect compatibility, not explicit ownership")
			}
		})
	}
	for name, change := range map[string]func(*config.LocationConfig){
		"canonical-order":      func(l *config.LocationConfig) { l.Match.Methods = []string{"HEAD", "GET"} },
		"backend-owned-by-624": func(l *config.LocationConfig) { l.ProxyPass = "http://127.0.0.1:9999" },
		"policy-independent": func(l *config.LocationConfig) {
			l.CachePolicy = &config.LocationCachePolicy{MaxTTL: durPtr(time.Second)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			next := loc
			change(&next)
			rc := compileMatcher(srv, next, key)
			if rc.Scope != base.Scope || rc.matcherProof != base.matcherProof {
				t.Fatal("non-matching change invalidated matcher proof")
			}
		})
	}
}

func TestMatcherProofOnlyRouteRetirementPersists(t *testing.T) {
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20}
	srv, loc := matcherFixture("never-filled", true)
	rc := compileMatcher(srv, loc, nil)
	c, _ := New(cfg, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	// No response entry exists to reconstruct activity; the manifest alone
	// must preserve retirement and must not retain inactive matcher proofs.
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies(nil)
	if c.CurrentRouteEpoch(rc.Scope) == 0 || len(c.routeMatchers) != 0 {
		t.Fatal("proof-only route retirement was forgotten")
	}
	c, err = New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	if c.CurrentRouteEpoch(rc.Scope) == 0 {
		t.Fatal("restart reset retired ownership")
	}
}

func TestMatcherUnchangedReloadDoesNotRequireDiskWrite(t *testing.T) {
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20}
	srv, loc := matcherFixture("before", true)
	rc := compileMatcher(srv, loc, nil)
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	matcherRequest(c, rc, "/read-only", "warm")
	// Include the first commit after disk rehydration: equal modern manifest
	// evidence must not be rewritten just because the process restarted.
	c, err = New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.disk.testPersistRouteEpochErr = errors.New("epoch writes unavailable")
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	if c.disk.routeEpochFailed.Load() {
		t.Fatal("unchanged reload unnecessarily rewrote durable state")
	}
	if r := matcherRequest(c, rc, "/read-only", "unwanted"); r.Header().Get("X-Cache") != "HIT" || r.Body.String() != "warm" {
		t.Fatal("unchanged reload lost warm reuse")
	}
	srv, loc = matcherFixture("after", true)
	next := compileMatcher(srv, loc, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{next})
	if !c.disk.routeEpochFailed.Load() {
		t.Fatal("changed matcher ignored required durable write failure")
	}
	c.disk.testPersistRouteEpochErr = nil
	c.CommitRoutePolicies([]RouteCacheConfig{next})
	if c.disk.routeEpochFailed.Load() {
		t.Fatal("unchanged retry failed to repair earlier persistence failure")
	}
}

func TestMatcherCorruptManifestIncompleteCleanupCannotResurrect(t *testing.T) {
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20}
	srv, loc := matcherFixture("retired", true)
	rc := compileMatcher(srv, loc, nil)
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	matcherRequest(c, rc, "/cleanup-fault", "retired-body")
	c.CommitRoutePolicies(nil)
	// Model startup after the epoch manifest becomes corrupt. Cache files are
	// readable but unlink/truncate and tombstone persistence fail; epoch-manifest
	// writes remain possible. Recovery must not turn an unknown epoch into 0.
	d, err := newDiskStore(cfg.DiskPath, cfg.DiskMaxSize.Bytes(), c.log)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DiskPath, diskRouteEpochsFile), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("cache cleanup unavailable")
	d.testRemoveFile = func(string) error { return failure }
	d.testTruncateFile = func(string, int64) error { return failure }
	d.testPersistTombstoneErr = failure
	memoryCfg := cfg
	memoryCfg.DiskPath = ""
	recovered, err := New(memoryCfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	recovered.routeEpoch, recovered.routeMatchers = d.loadRouteEpochs()
	recovered.disk = d
	recovered.CommitRoutePolicies([]RouteCacheConfig{rc})
	if !d.routeEpochFailed.Load() || recovered.CurrentRouteEpoch(rc.Scope) == 0 {
		t.Fatal("incomplete recovery authorized a fresh zero-epoch proof")
	}
	// Actual restart after filesystem health returns. Files that survived the
	// failed cleanup must remain behind a durable recovery floor.
	restarted, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	restarted.CommitRoutePolicies([]RouteCacheConfig{rc})
	if r := matcherRequest(restarted, rc, "/cleanup-fault", "fresh-body"); r.Body.String() != "fresh-body" || r.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("cleanup failure resurrected retired content: %s %q", r.Header().Get("X-Cache"), r.Body.String())
	}
}

func TestDiskRouteEpochRejectsOverflowBeforeRecovery(t *testing.T) {
	tail, err := encodeDiskTrailerAndMetaWithKeyGen(keyFor("GET", "example.com", "/overflow"), 1, &Entry{RouteEpoch: math.MaxUint64}, 0)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "entry")
	if err := os.WriteFile(path, tail, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := readDiskEntryMeta(path); !errors.Is(err, errCorruptDiskEntry) {
		t.Fatalf("overflowing route epoch accepted: %v", err)
	}
}

func TestMatcherChangeFencesInFlightAndLeavesSiblingWarm(t *testing.T) {
	key, _ := versionid.New(bytes.Repeat([]byte{0x73}, 32))
	srv, loc := matcherFixture("before", true)
	rc := compileMatcher(srv, loc, key)
	sibling := CompileRouteCacheConfig("unrelated-sibling", true, nil)
	c, _ := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20}, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rc, sibling})
	matcherRequest(c, sibling, "/sibling", "sibling-original")
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("old-fill"))
	}), rc)
	go func() {
		defer close(done)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/inflight-matcher", nil))
	}()
	<-started
	srv, loc = matcherFixture("after", true)
	next := compileMatcher(srv, loc, key)
	c.CommitRoutePolicies([]RouteCacheConfig{next, sibling})
	close(release)
	<-done
	if r := matcherRequest(c, next, "/inflight-matcher", "new-fill"); r.Header().Get("X-Cache") != "MISS" || r.Body.String() != "new-fill" {
		t.Fatal("old matcher fill published after commit")
	}
	if r := matcherRequest(c, sibling, "/sibling", "unwanted"); r.Header().Get("X-Cache") != "HIT" || r.Body.String() != "sibling-original" {
		t.Fatal("matcher edit invalidated unrelated route")
	}
}

func TestMatcherManifestLegacyMissingAndCorruption(t *testing.T) {
	for _, state := range []string{"legacy", "missing", "bad-proof", "future", "unknown-field"} {
		t.Run(state, func(t *testing.T) {
			cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: t.TempDir(), DiskMaxSize: 16 << 20}
			c, _ := New(cfg, nil)
			srv, loc := matcherFixture("test", true)
			rc := compileMatcher(srv, loc, nil)
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			matcherRequest(c, rc, "/manifest", "old")
			path := filepath.Join(cfg.DiskPath, diskRouteEpochsFile)
			switch state {
			case "legacy":
				raw, _ := json.Marshal(c.routeEpoch)
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "bad-proof":
				raw := fmt.Sprintf(`{"version":1,"epochs":{},"matchers":{%q:"not-a-proof"}}`, rc.Scope)
				if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			case "future":
				if err := os.WriteFile(path, []byte(`{"version":2,"epochs":{}}`), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unknown-field":
				if err := os.WriteFile(path, []byte(`{"version":1,"epochs":{},"unexpected":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := New(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			if r := matcherRequest(c, rc, "/manifest", "fresh"); r.Body.String() != "fresh" || r.Header().Get("X-Cache") != "MISS" {
				t.Fatal("unverifiable metadata reused content")
			}
			if _, err := c.InspectScope(context.Background(), NormalizedSelector{Kind: SelectorAll}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIndependent673OldSelectedHandlerCannotPublishIntoReplacementEpoch(t *testing.T) {
	for _, identified := range []bool{false, true} {
		for _, withDisk := range []bool{false, true} {
			name := fmt.Sprintf("identified=%t/disk=%t", identified, withDisk)
			t.Run(name, func(t *testing.T) {
				cfg := config.CacheConfig{
					Enabled:       true,
					MemoryMaxSize: 4 << 20,
					DefaultTTL:    config.Duration(time.Hour),
					Lock:          true,
					RangeEnabled:  true,
				}
				if withDisk {
					cfg.DiskPath = t.TempDir()
					cfg.DiskMaxSize = 16 << 20
				}
				c, err := New(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				key, _ := versionid.New(bytes.Repeat([]byte{0x73}, 32))
				srvA, locA := matcherFixture("tenant-a", identified)
				rcA := compileMatcher(srvA, locA, key)
				c.CommitRoutePolicies([]RouteCacheConfig{rcA})
				hA := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=3600")
					_, _ = w.Write([]byte("tenant-a"))
				}), rcA)

				// 1. A request selects the route matching X-Tenant: tenant-a, then
				// pauses before entering the cache.
				selected, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
				oldRec := httptest.NewRecorder()
				go func() {
					defer close(done)
					req := httptest.NewRequest(http.MethodGet, "http://example.com/shared", nil)
					req.Header.Set("X-Tenant", "tenant-a")
					close(selected)
					<-resume
					hA.ServeHTTP(oldRec, req)
				}()
				<-selected

				// 2. Reload changes that route's matcher to tenant-b.
				srvB, locB := matcherFixture("tenant-b", identified)
				rcB := compileMatcher(srvB, locB, key)
				c.CommitRoutePolicies([]RouteCacheConfig{rcB})
				hB := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=3600")
					_, _ = w.Write([]byte("tenant-b"))
				}), rcB)

				// 3. The old request resumes and finishes its request, but must not
				// acquire the replacement matcher's cache privileges.
				close(resume)
				<-done
				if oldRec.Code != http.StatusOK || oldRec.Body.String() != "tenant-a" {
					t.Fatalf("old handler response = %d %q, want 200 tenant-a", oldRec.Code, oldRec.Body.String())
				}
				// Even if a caller constructs a handler from the stale rcA struct
				// after reload or issues a Range request on hA, it cannot publish.
				matcherRequest(c, rcA, "/shared", "tenant-a")
				rangeReq := httptest.NewRequest(http.MethodGet, "http://example.com/shared", nil)
				rangeReq.Header.Set("Range", "bytes=0-3")
				hA.ServeHTTP(httptest.NewRecorder(), rangeReq)

				// 4. A new tenant-B request must miss (fetching "tenant-b") and then hit "tenant-b".
				for i, wantState := range []string{"MISS", "HIT"} {
					reqB := httptest.NewRequest(http.MethodGet, "http://example.com/shared", nil)
					reqB.Header.Set("X-Tenant", "tenant-b")
					recB := httptest.NewRecorder()
					hB.ServeHTTP(recB, reqB)
					if recB.Header().Get("X-Cache") != wantState || recB.Body.String() != "tenant-b" {
						t.Fatalf("step %d: state=%s body=%q, want %s tenant-b", i, recB.Header().Get("X-Cache"), recB.Body.String(), wantState)
					}
				}

				if withDisk {
					restarted, err := New(cfg, nil)
					if err != nil {
						t.Fatal(err)
					}
					restarted.CommitRoutePolicies([]RouteCacheConfig{rcB})
					if r := matcherRequest(restarted, rcB, "/shared", "unwanted-origin"); r.Header().Get("X-Cache") != "HIT" || r.Body.String() != "tenant-b" {
						t.Fatalf("after restart: state=%s body=%q, want HIT tenant-b", r.Header().Get("X-Cache"), r.Body.String())
					}
				}

				// 5. ABA transition (tenant-b -> tenant-a -> tenant-b): an old
				// handler from the first tenant-b generation must not publish into
				// the third generation's epoch even though the matcher proof returns
				// to tenant-b.
				c.CommitRoutePolicies([]RouteCacheConfig{rcA})
				c.CommitRoutePolicies([]RouteCacheConfig{rcB})
				recABA := httptest.NewRecorder()
				hB.ServeHTTP(recABA, httptest.NewRequest(http.MethodGet, "http://example.com/aba", nil))
				if r := matcherRequest(c, rcB, "/aba", "fresh-b-epoch"); r.Header().Get("X-Cache") != "MISS" || r.Body.String() != "fresh-b-epoch" {
					t.Fatalf("ABA old handler published into later epoch: state=%s body=%q", r.Header().Get("X-Cache"), r.Body.String())
				}
			})
		}
	}
}

func TestIndependent673InitialBindingBeyond4096Routes(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 16 << 20,
		DefaultTTL:    config.Duration(time.Hour),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := versionid.New(bytes.Repeat([]byte{0x42}, 32))

	// Abandoned candidate before initial commit is discarded cleanly.
	abandonedLoc := config.LocationConfig{
		Match:     config.MatchConfig{Type: "prefix", Path: "/abandoned"},
		ProxyPass: "http://127.0.0.1:8080",
		Cache:     true,
	}
	abandonedRC := CompileRouteCacheConfig("route:abandoned", true, nil).WithMatcher(abandonedLoc, key)
	abandonedHandler := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("abandoned"))
	}), abandonedRC)
	c.DiscardPendingRouteHandlers()

	const totalRoutes = 4097
	routes := make([]RouteCacheConfig, totalRoutes)
	handlers := make([]http.Handler, totalRoutes)
	for i := range totalRoutes {
		p := fmt.Sprintf("/r/%d", i)
		loc := config.LocationConfig{
			Match:     config.MatchConfig{Type: "prefix", Path: p},
			ProxyPass: "http://127.0.0.1:8080",
			Cache:     true,
		}
		rc := CompileRouteCacheConfig(fmt.Sprintf("route:%d", i), true, nil).WithMatcher(loc, key)
		routes[i] = rc
		body := fmt.Sprintf("body-%d", i)
		handlers[i] = c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte(body))
		}), rc)
	}
	c.CommitRoutePolicies(routes)

	for _, idx := range []int{0, 1, 2048, totalRoutes - 2, totalRoutes - 1} {
		target := fmt.Sprintf("http://example.com/r/%d", idx)
		wantBody := fmt.Sprintf("body-%d", idx)
		for step, wantState := range []string{stateMiss, stateHit} {
			rec := httptest.NewRecorder()
			handlers[idx].ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
			if rec.Header().Get("X-Cache") != wantState || rec.Body.String() != wantBody {
				t.Fatalf("route %d step %d: state=%s body=%q, want %s %q",
					idx, step, rec.Header().Get("X-Cache"), rec.Body.String(), wantState, wantBody)
			}
		}
	}

	// Verify the discarded abandoned candidate handler never acquired cache privileges.
	for range 2 {
		rec := httptest.NewRecorder()
		abandonedHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/abandoned", nil))
		if rec.Header().Get("X-Cache") != stateMiss {
			t.Fatalf("abandoned handler state=%s, want MISS", rec.Header().Get("X-Cache"))
		}
	}

	// Verify an aborted staged candidate (BeginRouteCandidate -> HandlerForRoute -> DiscardPendingRouteHandlers)
	// for an ALREADY-ACTIVE compatible route (route:0) cannot acquire cache privileges either.
	c.BeginRouteCandidate()
	abortedCompatible := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("aborted-candidate-body"))
	}), routes[0])
	c.DiscardPendingRouteHandlers()
	for range 2 {
		rec := httptest.NewRecorder()
		abortedCompatible.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/r/0-aborted", nil))
		if rec.Header().Get("X-Cache") != stateMiss {
			t.Fatalf("aborted compatible candidate handler state=%s, want MISS", rec.Header().Get("X-Cache"))
		}
	}
}

func TestIndependent673GenerationCompactionPreservesActiveHandler(t *testing.T) {
	c, err := New(config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: 8 << 20,
		DefaultTTL:    config.Duration(time.Hour),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := versionid.New(bytes.Repeat([]byte{0x55}, 32))

	keepLoc := config.LocationConfig{
		Match:     config.MatchConfig{Type: "prefix", Path: "/keep"},
		ProxyPass: "http://127.0.0.1:8080",
		Cache:     true,
	}
	compileKeep := func() RouteCacheConfig {
		rc := CompileRouteCacheConfig("route:keep", true, nil).WithMatcher(keepLoc, key)
		rc.Epoch = c.CurrentRouteEpoch(rc.Scope)
		return rc
	}
	originKeep := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("keep-body"))
	})

	// Follow the factory's construction-before-commit order across 1,030 reloads
	// with distinct temporary routes, crossing both epoch and matcher-generation compaction.
	var keepHandler http.Handler
	var retiredFirstHandler http.Handler
	var retiredFirstRC RouteCacheConfig
	for i := range 1030 {
		rcKeep := compileKeep()
		keepHandler = c.HandlerForRoute(originKeep, rcKeep)

		tmpLoc := config.LocationConfig{
			Match:     config.MatchConfig{Type: "prefix", Path: fmt.Sprintf("/tmp/%d", i)},
			ProxyPass: "http://127.0.0.1:8080",
			Cache:     true,
		}
		rcTmp := CompileRouteCacheConfig(fmt.Sprintf("route:tmp:%d", i), true, nil).WithMatcher(tmpLoc, key)
		rcTmp.Epoch = c.CurrentRouteEpoch(rcTmp.Scope)
		tmpHandler := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = fmt.Fprintf(w, "tmp-%d", i)
		}), rcTmp)
		if i == 0 {
			retiredFirstHandler = tmpHandler
			retiredFirstRC = rcTmp
		}

		c.CommitRoutePolicies([]RouteCacheConfig{rcKeep, rcTmp})
	}

	// After 1,030 reloads (past 1,026 compaction threshold), the unchanged route's
	// handler must still cache and hit subsequent responses.
	for step, wantState := range []string{stateMiss, stateHit, stateHit} {
		rec := httptest.NewRecorder()
		keepHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/keep/item", nil))
		if rec.Header().Get("X-Cache") != wantState || rec.Body.String() != "keep-body" {
			t.Fatalf("keep route step %d after compaction: state=%s body=%q, want %s keep-body",
				step, rec.Header().Get("X-Cache"), rec.Body.String(), wantState)
		}
	}

	// Re-adding the pruned route:tmp:0 after compaction must assign a fresh generation
	// above the compacted floor so retiredFirstHandler from reload 0 remains fenced.
	rcKeep := compileKeep()
	keepHandler = c.HandlerForRoute(originKeep, rcKeep)
	readdedRC := retiredFirstRC
	readdedRC.Epoch = c.CurrentRouteEpoch(readdedRC.Scope)
	readdedHandler := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("tmp-0-readded"))
	}), readdedRC)
	c.CommitRoutePolicies([]RouteCacheConfig{rcKeep, readdedRC})

	// Old handler from reload 0 must not publish into the re-added route's epoch.
	retiredFirstHandler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://example.com/tmp/0", nil))
	for step, wantState := range []string{stateMiss, stateHit} {
		rec := httptest.NewRecorder()
		readdedHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://example.com/tmp/0", nil))
		if rec.Header().Get("X-Cache") != wantState || rec.Body.String() != "tmp-0-readded" {
			t.Fatalf("readded route step %d: state=%s body=%q, want %s tmp-0-readded",
				step, rec.Header().Get("X-Cache"), rec.Body.String(), wantState)
		}
	}
}
