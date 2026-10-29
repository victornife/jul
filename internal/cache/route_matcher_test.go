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
