// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bytes"
	"jul/internal/config"
	"jul/internal/versionid"
)

func routeEpochFixture(t *testing.T, disk bool) (config.CacheConfig, *Cache, RouteCacheConfig, http.Handler) {
	t.Helper()
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DefaultTTL: config.Duration(time.Hour)}
	if disk {
		cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), 16<<20
	}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc := CompileRouteCacheConfig("retired-route", true, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("version-1"))
	})
	h := c.HandlerForRoute(origin, rc)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/retired", nil))
	return cfg, c, rc, origin
}

func TestRouteEpochCorruptionPopulatedStartup(t *testing.T) {
	for _, corrupt := range []string{"{broken", "null", `{"retired-route":18446744073709551615}`, strings.Repeat("x", maxDiskMetaBytes+1)} {
		cfg, _, _, _ := routeEpochFixture(t, true)
		if err := os.WriteFile(filepath.Join(cfg.DiskPath, diskRouteEpochsFile), []byte(corrupt), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.disk.items) != 0 {
			t.Fatal("corrupt route epochs retained reusable disk entries")
		}
		if len(c.keyGen) != 0 {
			t.Fatal("corrupt recovery rehydrated a stale disk snapshot")
		}
	}
}

func TestRouteEpochPruningNeverResurrects(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%t", disk), func(t *testing.T) {
			cfg, c, rc, _ := routeEpochFixture(t, disk)
			c.CommitRoutePolicies(nil)
			for i := 0; i < 1026; i++ {
				c.CommitRoutePolicies([]RouteCacheConfig{CompileRouteCacheConfig(fmt.Sprintf("churn-%d", i), true, nil)})
			}
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			if len(c.routeEpoch) > 1024+1 {
				t.Fatal("route epoch history grew beyond its bound")
			}
			assertFresh := func(c *Cache) {
				t.Helper()
				h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=3600")
					_, _ = w.Write([]byte("version-2"))
				}), rc)
				r := httptest.NewRecorder()
				h.ServeHTTP(r, httptest.NewRequest("GET", "http://example.com/retired", nil))
				if r.Body.String() != "version-2" {
					t.Fatalf("retired response resurrected: %s %q", r.Header().Get("X-Cache"), r.Body.String())
				}
			}
			if disk {
				restarted, err := New(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				restarted.CommitRoutePolicies([]RouteCacheConfig{rc})
				assertFresh(restarted)
			} else {
				assertFresh(c)
			}
		})
	}
}

func TestRouteEpochPersistenceFailureRetiresDiskAndRecovers(t *testing.T) {
	cfg, c, rc, _ := routeEpochFixture(t, true)
	c.disk.testPersistRouteEpochErr = errors.New("injected route epoch persistence failure")
	c.CommitRoutePolicies(nil)
	if !c.disk.routeEpochFailed.Load() || len(c.disk.items) != 0 {
		t.Fatal("failed persistence did not disable and retire disk tier")
	}
	// Ordinary reconciliation must not silently re-enable this tier.
	c.disk.reconcileInvalidation()
	if !c.disk.routeEpochFailed.Load() {
		t.Fatal("tombstone reconciliation cleared route failure")
	}
	sel, err := ParseSelector(Selector{Kind: SelectorAll, All: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := c.InspectScope(httptest.NewRequest("GET", "/", nil).Context(), sel)
	if err != nil || inspection.State != "partial" {
		t.Fatalf("disk failure not reported: %s %v", inspection.State, err)
	}
	if _, ok := c.disk.get(keyFor("GET", "example.com", "/retired")); ok {
		t.Fatal("failed epoch persistence allowed a disk hit")
	}
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write([]byte("version-2"))
	}), rc)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/new", nil))
	if len(c.disk.items) != 0 {
		t.Fatal("failed epoch persistence allowed disk publication")
	}
	restarted, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(restarted.disk.items) != 0 {
		t.Fatal("restart resurrected entries after failed persistence")
	}
	c.disk.testPersistRouteEpochErr = nil
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	if c.disk.routeEpochFailed.Load() {
		t.Fatal("successful durable commit did not recover disk tier")
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/recovered", nil))
	if len(c.disk.items) == 0 {
		t.Fatal("disk tier failed to recover")
	}
}

func TestPolicyIDIncludesEveryReuseField(t *testing.T) {
	// New semantic fields cannot silently lose publication fencing or policy
	// tightening. Only these three documented non-freshness fields are ignored.
	ignored := map[string]bool{"DecisionSamples": true, "MultiRouteOverlap": true, "varyGuard": true}
	base := policyID(CachePolicy{})
	typeOf := reflect.TypeFor[CachePolicy]()
	for i := range typeOf.NumField() {
		name := typeOf.Field(i).Name
		if name == "varyGuard" {
			continue
		}
		p := CachePolicy{}
		f := reflect.ValueOf(&p).Elem().Field(i)
		switch f.Kind() {
		case reflect.Bool:
			f.SetBool(true)
		case reflect.String:
			f.SetString("different")
		case reflect.Int, reflect.Int64:
			f.SetInt(1)
		case reflect.Uint64:
			f.SetUint(1)
		case reflect.Array:
			f.Index(0).SetString("different")
		default:
			t.Fatalf("unclassified policy field %s", name)
		}
		if same := policyID(p) == base; same != ignored[name] {
			t.Errorf("policy field %s ignored=%t, want %t", name, same, ignored[name])
		}
	}
	if policyID(CachePolicy{varyGuard: newProxyVaryTransformPolicy([]string{"X-Test"}, true)}) != base {
		t.Fatal("Vary guard changed freshness evidence")
	}
	// String boundaries must remain unambiguous.
	a, b := CachePolicy{}, CachePolicy{}
	a.BypassCookies[0], a.BypassCookies[1] = "ab", "c"
	b.BypassCookies[0], b.BypassCookies[1] = "a", "bc"
	if policyID(a) == policyID(b) {
		t.Fatal("ambiguous policy string encoding")
	}
}

func BenchmarkRoutePolicyHTTPHit(b *testing.B) {
	for _, cookies := range []bool{false, true} {
		b.Run(fmt.Sprintf("cookies=%t", cookies), func(b *testing.B) {
			c := benchCache()
			rc := CompileRouteCacheConfig("ordinary-route", false, &config.LocationCachePolicy{BypassCookies: []string{"session_id"}}).WithProxyVaryGuard(nil, true)
			c.CommitRoutePolicies([]RouteCacheConfig{rc})
			h := c.HandlerForRoute(benchNext, rc)
			r := httptest.NewRequest("GET", "http://example.com/public", nil)
			if cookies {
				r.Header.Set("Cookie", "theme=dark")
			}
			h.ServeHTTP(httptest.NewRecorder(), r)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				if rec.Header().Get("X-Cache") != "HIT" {
					b.Fatal("expected HIT")
				}
			}
		})
	}
}

func BenchmarkRoutePolicyManyRoutes(b *testing.B) {
	for _, count := range []int{10, 1000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			c := benchCache()
			srv := config.ServerConfig{Listen: ":8080"}
			for i := 0; i < count; i++ {
				srv.Locations = append(srv.Locations, config.LocationConfig{Match: config.MatchConfig{Path: fmt.Sprintf("/%d", i), Methods: []string{"GET"}}})
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				routes := make([]RouteCacheConfig, count)
				scopes := RouteScopes(srv)
				for i, loc := range srv.Locations {
					routes[i] = CompileRouteCacheConfig(scopes[i], true, nil).WithMatcher(loc, nil)
				}
				c.CommitRoutePolicies(routes)
			}
		})
	}
}

func TestRouteEpochCompactionFencesActiveFill(t *testing.T) {
	_, c, _, _ := routeEpochFixture(t, false)
	rc := CompileRouteCacheConfig("active-during-compaction", true, nil)
	c.CommitRoutePolicies([]RouteCacheConfig{rc})
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = fmt.Fprintf(w, "fill-%d", n)
	}), rc)
	go func() {
		defer close(finished)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/inflight", nil))
	}()
	<-started
	for i := 0; i < 1027; i++ {
		c.CommitRoutePolicies([]RouteCacheConfig{rc, CompileRouteCacheConfig(fmt.Sprintf("retire-%d", i), true, nil)})
	}
	if c.CurrentRouteEpoch(rc.Scope) == 0 {
		t.Fatal("compaction did not advance active-route compatibility")
	}
	close(release)
	<-finished
	r := httptest.NewRecorder()
	h.ServeHTTP(r, httptest.NewRequest("GET", "http://example.com/inflight", nil))
	if r.Body.String() != "fill-2" {
		t.Fatalf("pre-compaction fill published late: %q", r.Body.String())
	}
}

func TestRouteScopeNeverHashesPredicateValues(t *testing.T) {
	for _, withID := range []bool{false, true} {
		id, value := "public-route", "guessable-predicate-a"
		loc := config.LocationConfig{Match: config.MatchConfig{Path: "/", Headers: []config.HeaderMatch{{Name: "X-Role", Op: "exact", Value: &value}}}}
		if withID {
			loc.RouteID = &id
		}
		srv := config.ServerConfig{Listen: ":8080", ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{loc}}
		a := RouteScope(srv, loc)
		other := "guessable-predicate-b"
		loc.Match.Headers = []config.HeaderMatch{{Name: "X-Role", Op: "exact", Value: &other}}
		srv.Locations[0] = loc
		if b := RouteScope(srv, loc); a != b {
			t.Fatal("predicate values affected persistent partition")
		}
		c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20}, nil)
		if err != nil {
			t.Fatal(err)
		}
		rc := CompileRouteCacheConfig(a, true, nil).WithMatcher(loc, nil)
		c.CommitRoutePolicies([]RouteCacheConfig{rc})
		before := c.CurrentRouteEpoch(a)
		c.CommitRoutePolicies([]RouteCacheConfig{rc})
		if c.CurrentRouteEpoch(a) != before {
			t.Fatal("unchanged predicate reload invalidated compatible ownership")
		}
	}
}

func TestRouteVaryMembershipBoundedUnderRouteChurn(t *testing.T) {
	_, c, _, origin := routeEpochFixture(t, false)
	for i := 0; i < maxVariantsPerResource*3; i++ {
		rc := CompileRouteCacheConfig(fmt.Sprintf("slot-%d", i), true, nil)
		c.CommitRoutePolicies([]RouteCacheConfig{rc})
		c.HandlerForRoute(origin, rc).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com/churn", nil))
	}
	stub, ok := c.get(keyFor("GET", "example.com", "/churn"))
	if !ok || !stub.IsVaryStub {
		t.Fatal("missing route stub")
	}
	if len(stub.RouteVary) > maxVariantsPerResource {
		t.Fatalf("unbounded route membership: %d routes for %d variants", len(stub.RouteVary), len(stub.Variants))
	}
}

// A real second process proves stable private compatibility evidence preserves
// warm ordinary and predicate routes with the same durable version context.
func TestRouteScopeRestartCompatibility(t *testing.T) {
	dir := os.Getenv("JUL_ROUTE_SCOPE_TEST_DIR")
	child := dir != ""
	if !child {
		dir = t.TempDir()
	}
	cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 4 << 20, DiskPath: dir, DiskMaxSize: 16 << 20, DefaultTTL: config.Duration(time.Hour)}
	c, err := New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	static := config.LocationConfig{Match: config.MatchConfig{Path: "/static"}}
	predicate := config.LocationConfig{Match: config.MatchConfig{Path: "/predicate", Methods: []string{"GET"}}}
	srv := config.ServerConfig{Listen: ":8080", Locations: []config.LocationConfig{static, predicate}}
	mode := os.Getenv("JUL_ROUTE_SCOPE_TEST_MODE")
	keyByte := byte(0x73)
	if child && mode == "changed-key" {
		keyByte = 0x74
	}
	if child && mode == "changed-matcher" {
		predicate.Match.Methods = []string{"GET", "HEAD"}
		srv.Locations[1] = predicate
	}
	versions, _ := versionid.New(bytes.Repeat([]byte{keyByte}, 32))
	routes := []RouteCacheConfig{CompileRouteCacheConfig(RouteScope(srv, static), true, nil).WithMatcher(static, versions), CompileRouteCacheConfig(RouteScope(srv, predicate), true, nil).WithMatcher(predicate, versions)}
	c.CommitRoutePolicies(routes)
	for i, path := range []string{"/static", "/predicate"} {
		body := "version-1"
		if child {
			body = "version-2"
		}
		h := c.HandlerForRoute(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Cache-Control", "public, max-age=3600")
			_, _ = w.Write([]byte(body))
		}), routes[i])
		r := httptest.NewRecorder()
		h.ServeHTTP(r, httptest.NewRequest("GET", "http://example.com"+path, nil))
		want := "version-1"
		miss := child && (mode == "changed-key" || (mode == "changed-matcher" && i == 1))
		if miss {
			want = "version-2"
		}
		if child {
			state := "HIT"
			if miss {
				state = "MISS"
			}
			if r.Header().Get("X-Cache") != state {
				t.Fatalf("restart mode %s %s: got %s want %s", mode, path, r.Header().Get("X-Cache"), state)
			}
		}
		if r.Body.String() != want {
			t.Fatalf("restart %s: got %q want %q", path, r.Body.String(), want)
		}
	}
	if !child {
		for _, mode := range []string{"unchanged", "changed-matcher", "changed-key"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRouteScopeRestartCompatibility$", "-test.timeout=15s")
			cmd.Env = append(os.Environ(), "JUL_ROUTE_SCOPE_TEST_DIR="+dir, "JUL_ROUTE_SCOPE_TEST_MODE="+mode)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("restart %s: %v\n%s", mode, err, out)
			}
		}
	}
}
