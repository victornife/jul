// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/adminclient"
	"jul/internal/cache"
	"jul/internal/cachemeta"
	"jul/internal/config"
	"jul/internal/rbac"
)

type inspectionFake struct{ fail bool }

func (*inspectionFake) Purge()        {}
func (*inspectionFake) Delete(string) {}
func (f *inspectionFake) InspectScope(ctx context.Context, _ cache.NormalizedSelector) (cache.Inspection, error) {
	if f.fail {
		return cache.Inspection{}, errors.New("SECRET_INTERNAL_ERROR")
	}
	if err := ctx.Err(); err != nil {
		return cache.Inspection{}, err
	}
	d := cache.Inspection{State: "available", EvidenceState: "disabled", CountAccuracy: "exact", MatchedResources: 5, Resources: []cache.SafeResourceSummary{}, Decisions: []cache.CacheDecision{}, Warnings: []string{}}
	for i := 0; i < 5; i++ {
		d.Resources = append(d.Resources, cache.SafeResourceSummary{Host: "example.com", Path: fmt.Sprint("/", i), ResourceID: fmt.Sprint(i), Details: []cache.RepresentationMetadata{}})
	}
	return d, nil
}

func TestInspectionAPIPermissionsCursorIsolationAndRevocation(t *testing.T) {
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com", "other.example.com"}}}}
	fake := &inspectionFake{}
	s := newTestServer(t, config.AdminConfig{}, Deps{Cache: fake, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	const reader = "reader-token-32-chars-inspection-000"
	const reader2 = "reader2-token-32-chars-inspection-00"
	const ops = "operator-token-32-chars-inspection"
	makePolicy := func(disabled bool) *rbac.Policy {
		p, err := rbac.Build(true, rbac.RoleAdmin, map[string][]string{"inspect": {"cache:inspect"}}, []rbac.PrincipalDef{
			{Name: "root", Role: rbac.RoleAdmin, Token: "admin-inspection-token-32-chars-root"},
			{Name: "reader", Role: "inspect", Token: reader, Disabled: disabled}, {Name: "reader2", Role: "inspect", Token: reader2}, {Name: "ops", Role: rbac.RoleOperator, Token: ops},
		}, "", time.Now())
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	s.UpdatePolicy(makePolicy(false))
	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}, PageSize: 2}
	call := func(tok string, r adminapi.CacheInspectRequest) *httptest.ResponseRecorder {
		return postV1JSON(t, s, "/api/v1/cache/inspect", tok, "", r)
	}
	if rr := call("", req); rr.Code != 401 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := call(ops, req); rr.Code != 403 || strings.Contains(rr.Body.String(), "example.com") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if rr := postV1JSON(t, s, "/api/v1/cache/purge", reader, "", adminapi.CachePurgeRequest{Selector: req.Selector}); rr.Code != 403 {
		t.Fatal("inspection implied purge", rr.Code)
	}
	rr := call(reader, req)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	first := decodeInto[adminapi.CacheInspectResponse](t, rr)
	if first.NextCursor == "" || len(first.Inspection.Resources) != 2 {
		t.Fatal(first)
	}
	req.Cursor = first.NextCursor
	if rr = call(reader, req); rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	page := decodeInto[adminapi.CacheInspectResponse](t, rr)
	if page.Inspection.Resources[0].Path != "/2" {
		t.Fatal("pagination skipped or duplicated rows")
	}
	if rr = call(reader2, req); rr.Code != 400 {
		t.Fatal("cross principal reuse", rr.Code)
	}
	altered := req
	altered.Selector.Host = "other.example.com"
	if rr = call(reader, altered); rr.Code != 400 {
		t.Fatal("scope tamper", rr.Code)
	}
	altered = req
	parts := strings.Split(altered.Cursor, ".")
	parts[1] = "3"
	altered.Cursor = strings.Join(parts, ".")
	if rr = call(reader, altered); rr.Code != 400 {
		t.Fatal("offset tamper", rr.Code)
	}
	altered = req
	altered.Cursor = "bogus"
	if rr = call(reader, altered); rr.Code != 400 {
		t.Fatal(rr.Code)
	}
	for _, cursor := range []string{"a.x.c", "a.0.c", "a.999.c"} {
		altered.Cursor = cursor
		if rr = call(reader, altered); rr.Code != 400 {
			t.Fatal(rr.Code)
		}
	}
	s.inspectionMu.Lock()
	for _, p := range s.inspectionSnapshots {
		p.expires = time.Now().Add(-time.Second)
	}
	s.inspectionMu.Unlock()
	if rr = call(reader, req); rr.Code != 400 {
		t.Fatal("expiry", rr.Code)
	}
	req.Cursor = ""
	rr = call(reader, req)
	first = decodeInto[adminapi.CacheInspectResponse](t, rr)
	req.Cursor = first.NextCursor
	cfg.Cache.DefaultTTL = config.Duration(time.Minute)
	if rr = call(reader, req); rr.Code != 400 {
		t.Fatal("reload binding", rr.Code)
	}
	s.UpdatePolicy(makePolicy(true))
	if rr = call(reader, req); rr.Code != 401 {
		t.Fatal("revocation", rr.Code)
	}
	for _, role := range []string{rbac.RoleViewer, rbac.RoleOperator, rbac.RoleAuditor} {
		if rbac.RoleHas(role, rbac.CacheInspect) {
			t.Fatalf("default widened: %s", role)
		}
	}
	if !rbac.RoleHas(rbac.RoleAdmin, rbac.CacheInspect) || !rbac.Matches("cache:*", rbac.CacheInspect) {
		t.Fatal("wildcard compatibility")
	}
}

func TestInspectionAPIStatesAdmissionInvalidAndErrors(t *testing.T) {
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}}}}
	f := &inspectionFake{}
	const token = "inspection-legacy-token-32-chars-aa"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: f, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}, PageSize: 2}
	call := func() *httptest.ResponseRecorder { return postV1JSON(t, s, "/api/v1/cache/inspect", token, "", req) }
	f.fail = true
	if rr := call(); rr.Code != 503 || strings.Contains(rr.Body.String(), "SECRET_INTERNAL_ERROR") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	f.fail = false
	s.inspectionBuilders.Store(2)
	if rr := call(); rr.Code != 429 || rr.Header().Get("Retry-After") != "1" {
		t.Fatal(rr.Code)
	}
	s.inspectionBuilders.Store(0)
	s.inspectionSnapshots = make(map[string]*inspectionSnapshot)
	for i := 0; i < maxInspectionSnapshots; i++ {
		s.inspectionSnapshots[fmt.Sprint(i)] = &inspectionSnapshot{}
	}
	if rr := call(); rr.Code != 429 {
		t.Fatal("retention admission", rr.Code)
	}
	s.inspectionSnapshots = nil
	req.PageSize = 51
	if rr := call(); rr.Code != 400 {
		t.Fatal(rr.Code)
	}
	req.PageSize = 0
	for _, sel := range []adminapi.CacheSelector{{Kind: "all", All: true}, {Kind: "host", Host: "unknown.example.com"}, {Kind: "prefix", Host: "example.com", Prefix: "/../x"}} {
		old := req.Selector
		req.Selector = sel
		if rr := call(); rr.Code != 400 {
			t.Fatal(rr.Code)
		}
		req.Selector = old
	}
	req.Selector = adminapi.CacheSelector{Kind: "exact_url", Host: "example.com", URI: "/a?token=SECRET_QUERY"}
	rr := call()
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "SECRET_QUERY") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	cfg.Cache.Enabled = false
	rr = call()
	if rr.Code != 200 || decodeInto[adminapi.CacheInspectResponse](t, rr).Inspection.State != "disabled" {
		t.Fatal(rr.Code, rr.Body.String())
	}
	cfg.Cache.Enabled = true
	s.deps.Cache = nil
	rr = call()
	if rr.Code != 200 || decodeInto[adminapi.CacheInspectResponse](t, rr).Inspection.State != "unavailable" {
		t.Fatal(rr.Code, rr.Body.String())
	}
	open := newTestServer(t, config.AdminConfig{}, Deps{Cache: f, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	if rr = postV1JSON(t, open, "/api/v1/cache/inspect", "", "", req); rr.Code != 401 {
		t.Fatal("unauthenticated inspection", rr.Code)
	}
}

func TestInspectionCanceledBuildersNeverRetainSnapshots(t *testing.T) {
	for _, cancelAt := range []int{2, 3} {
		t.Run(fmt.Sprint(cancelAt), func(t *testing.T) {
			cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}}}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			const token = "inspection-legacy-token-32-chars-aa"
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: &inspectionFake{}, LoadConfig: func() (*config.Config, error) {
				calls++
				if calls == cancelAt {
					cancel()
				}
				return cfg, nil
			}})
			calls = 0
			raw, _ := json.Marshal(adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}, PageSize: 1})
			r := httptest.NewRequest(http.MethodPost, "/api/v1/cache/inspect", bytes.NewReader(raw)).WithContext(ctx)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, r)
			s.inspectionMu.Lock()
			n := len(s.inspectionSnapshots)
			s.inspectionMu.Unlock()
			if n != 0 || ctx.Err() == nil {
				t.Fatalf("canceled capture retained %d snapshots, config calls=%d", n, calls)
			}
		})
	}
}

type expiryInspectionFake struct {
	inspectionFake
	expiry time.Time
}

func (f *expiryInspectionFake) InspectScope(ctx context.Context, s cache.NormalizedSelector) (cache.Inspection, error) {
	d, e := f.inspectionFake.InspectScope(ctx, s)
	d.EvidenceState = "observed"
	d.Decisions = []cache.CacheDecision{{Reason: "fresh_hit", ObservedAt: f.expiry.Add(-time.Minute), ExpiresAt: f.expiry}}
	return d, e
}
func TestInspectionSnapshotsDoNotExtendDecisionTTL(t *testing.T) {
	const token = "inspection-legacy-token-32-chars-aa"
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}}}}
	fake := &expiryInspectionFake{expiry: time.Now().Add(time.Second)}
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: fake, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}, PageSize: 1}
	rr := postV1JSON(t, s, "/api/v1/cache/inspect", token, "", req)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	v := decodeInto[adminapi.CacheInspectResponse](t, rr)
	expiry, err := time.Parse(time.RFC3339Nano, v.ExpiresAt)
	if err != nil || expiry.After(fake.expiry) {
		t.Fatal("sample TTL extended", v.ExpiresAt, fake.expiry)
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		s.inspectionMu.Lock()
		n := len(s.inspectionSnapshots)
		s.inspectionMu.Unlock()
		if n == 0 {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("snapshot not reclaimed without traffic")
		case <-tick.C:
		}
	}
	req.Cursor = v.NextCursor
	rr = postV1JSON(t, s, "/api/v1/cache/inspect", token, "", req)
	if rr.Code != 400 {
		t.Fatal("expired decision returned", rr.Code, rr.Body.String())
	}
}

func TestInspectionRoutePathIntersection(t *testing.T) {
	id := "app-route"
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, MemoryMaxSize: 1 << 20, DefaultTTL: config.Duration(time.Minute)}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{{RouteID: &id, Cache: true, Match: config.MatchConfig{Type: "prefix", Path: "/app"}}}}}}
	c, err := cache.New(cfg.Cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	for _, path := range []string{"/app/a", "/other/b"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://example.com"+path, nil))
	}
	const token = "inspection-legacy-token-32-chars-aa"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: c, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", RouteID: id}}
	rr := postV1JSON(t, s, "/api/v1/cache/inspect", token, "", req)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	v := decodeInto[adminapi.CacheInspectResponse](t, rr)
	if len(v.Inspection.Resources) != 1 || v.Inspection.Resources[0].Path != "/app/a" {
		t.Fatal("route widened", rr.Body.String())
	}
	cfg.Servers[0].Locations[0].Cache = false
	rr = postV1JSON(t, s, "/api/v1/cache/inspect", token, "", req)
	if rr.Code != 200 || decodeInto[adminapi.CacheInspectResponse](t, rr).RouteCacheState != "disabled_retained_entries_may_exist" {
		t.Fatal("disabled route policy not disclosed", rr.Code, rr.Body.String())
	}
	cfg.Servers[0].Locations[0].Match.Type = "regex"
	rr = postV1JSON(t, s, "/api/v1/cache/inspect", token, "", req)
	if rr.Code != 400 {
		t.Fatal("unsupported regex widened", rr.Code)
	}
}

func TestInspectionConfiguredIPv6Port(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{ServerNames: []string{"[::1]"}}}}
	sel, e := cache.ParseInspectionSelector(cache.Selector{Kind: cache.SelectorHost, Host: "[::1]:8080"}, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if !configuredInspectionScope(sel, cfg) {
		t.Error("configured literal IPv6 host rejected when request authority has explicit port")
	}
}
func TestInspectionConfiguredNormalizedName(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{ServerNames: []string{" Example.COM. "}}}}
	sel, e := cache.ParseInspectionSelector(cache.Selector{Kind: cache.SelectorHost, Host: "example.com"}, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if !configuredInspectionScope(sel, cfg) {
		t.Error("router-normalized configured name rejected")
	}
}

func TestInspectionWideVaryClientPagination(t *testing.T) {
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, MemoryMaxSize: 64 << 20, DefaultTTL: config.Duration(time.Minute)}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}}}}
	c, err := cache.New(cfg.Cache, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 16)
	for i := range names {
		names[i] = fmt.Sprintf("X-%02d-", i) + strings.Repeat("A", 123)
	}
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Vary", strings.Join(names, ", "))
		w.Write([]byte("x"))
	}))
	for i := 0; i < 50; i++ {
		for j := 0; j < 32; j++ {
			r := httptest.NewRequest("GET", fmt.Sprintf("http://example.com/%d", i), nil)
			r.Header.Set(names[0], fmt.Sprint(j))
			h.ServeHTTP(httptest.NewRecorder(), r)
		}
	}
	sel, _ := cache.ParseInspectionSelector(cache.Selector{Kind: cache.SelectorHost, Host: "example.com"}, cfg)
	data, err := c.InspectScope(context.Background(), sel)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(data)
	t.Logf("inspection encoded bytes=%d resources=%d reps=%d", len(b), len(data.Resources), data.MatchedRepresentations)
	const token = "inspection-legacy-token-32-chars-aa"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: c, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	ts := httptest.NewServer(s.routes())
	defer ts.Close()
	client, err := adminclient.New(adminclient.Config{Endpoint: ts.URL, Token: token})
	if err != nil {
		t.Fatal(err)
	}

	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}, PageSize: 50}
	seen := make(map[string]bool)
	pages := 0
	for {
		result, _, err := client.CacheInspect(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(result)
		if len(encoded)+1 > maxInspectionPageBytes {
			t.Fatalf("page bytes=%d", len(encoded)+1)
		}
		if pages == 0 && (len(result.Inspection.Resources) >= 50 || result.NextCursor == "") {
			t.Fatal("wide first page did not paginate")
		}
		for _, r := range result.Inspection.Resources {
			if seen[r.ResourceID] {
				t.Fatal("repeated resource")
			}
			seen[r.ResourceID] = true
		}
		pages++
		if result.NextCursor == "" {
			break
		}
		if pages > 50 {
			t.Fatal("pagination did not terminate")
		}
		req.Cursor = result.NextCursor
	}
	if len(seen) != 50 {
		t.Fatalf("got %d resources in %d pages", len(seen), pages)
	}
}

func TestResolveInspectionRoutePolicyRouterShadowingAndPrefixOverlap(t *testing.T) {
	maxTTL := config.Duration(45 * time.Second)
	cfg := &config.Config{
		Cache: config.CacheConfig{
			Enabled:    true,
			DefaultTTL: config.Duration(5 * time.Minute),
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"example.com"},
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://root:8080",
						Cache:     true,
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://api:8080",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							MaxTTL:        &maxTTL,
							BypassCookies: []string{"SID", "auth"},
						},
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/api-v2"},
						ProxyPass: "http://apiv2:8080",
						Cache:     false,
					},
				},
			},
		},
	}
	base := cachemeta.EffectivePolicy{DefaultTTLSeconds: 300}

	// 1. Exact URL under /api/items shadows catch-all "/" and resolves /api policy unambiguously.
	selExact, err := cache.ParseInspectionSelector(cache.Selector{
		Kind: cache.SelectorExactURL,
		URL:  "http://example.com/api/items",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pol, src, state, warns := resolveInspectionRoutePolicy(selExact, cfg, base)
	if src != "route_override_with_global_inheritance" || state != "enabled" || len(warns) != 0 {
		t.Fatalf("exact URL under /api: src=%q state=%q warns=%v", src, state, warns)
	}
	if pol.MaxTTLSeconds == nil || *pol.MaxTTLSeconds != 45 || pol.DefaultTTLSeconds != 45 {
		t.Fatalf("expected MaxTTL=45 and capped DefaultTTL=45, got %+v", pol)
	}
	if len(pol.BypassCookies) != 2 || pol.BypassCookies[0] != "auth" || pol.BypassCookies[1] != "sid" {
		t.Fatalf("expected normalized sorted BypassCookies [auth sid], got %v", pol.BypassCookies)
	}

	// 2. Prefix selector "/api" does NOT overlap "/api-v2" (segment boundary) and shadows "/".
	selPrefixAPI, err := cache.ParseInspectionSelector(cache.Selector{
		Kind:   cache.SelectorPrefix,
		Host:   "example.com",
		Prefix: "/api",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	polAPI, srcAPI, _, warnsAPI := resolveInspectionRoutePolicy(selPrefixAPI, cfg, base)
	if srcAPI != "route_override_with_global_inheritance" || len(warnsAPI) != 0 || polAPI.MaxTTLSeconds == nil || *polAPI.MaxTTLSeconds != 45 {
		t.Fatalf("prefix /api: src=%q warns=%v pol=%+v", srcAPI, warnsAPI, polAPI)
	}

	// 3. Prefix selector "/apix" IS routed to prefix location "/api" by the router and shadows "/".
	selPrefixAPIX, err := cache.ParseInspectionSelector(cache.Selector{
		Kind:   cache.SelectorPrefix,
		Host:   "example.com",
		Prefix: "/apix",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	polAPIX, srcAPIX, _, warnsAPIX := resolveInspectionRoutePolicy(selPrefixAPIX, cfg, base)
	if srcAPIX != "route_override_with_global_inheritance" || len(warnsAPIX) != 0 || polAPIX.MaxTTLSeconds == nil || *polAPIX.MaxTTLSeconds != 45 {
		t.Fatalf("prefix /apix: src=%q warns=%v pol=%+v", srcAPIX, warnsAPIX, polAPIX)
	}

	// 4. Exercise hasSegmentPrefix, exact/regex tiers, SelectorKey targetPath extraction, and route_id resolution.
	if !hasSegmentPrefix("/a/b", "/") || !hasSegmentPrefix("/a/b", "/a/") || hasSegmentPrefix("/b", "/a") {
		t.Fatal("unexpected hasSegmentPrefix result")
	}
	routeExactID := "exact-id"
	routeNoOverrideID := "no-override-id"
	cfg2 := &config.Config{
		Cache: cfg.Cache,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"other.example.com"},
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://other:8080"},
				},
			},
			{
				Listen:      ":8080",
				ServerNames: []string{"example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:     &routeExactID,
						Match:       config.MatchConfig{Type: "exact", Path: "/api/exact"},
						ProxyPass:   "http://exact:8080",
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{MaxTTL: &maxTTL},
					},
					{
						RouteID:   &routeNoOverrideID,
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://api:8080",
						Cache:     false,
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
						ProxyPass: "http://api2:8080",
						Cache:     false,
					},
					{
						Match:     config.MatchConfig{Type: "regex", Path: `^/api/.*`},
						ProxyPass: "http://regex:8080",
						Cache:     false,
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://root1:8080",
						Cache:     true,
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://root2:8080",
						Cache:     false,
					},
				},
			},
		},
	}
	selKey, err := cache.ParseInspectionSelector(cache.Selector{
		Kind: cache.SelectorKey,
		Key:  "GET\nexample.com\n/api/exact?x=1",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, srcKey, stateKey, _ := resolveInspectionRoutePolicy(selKey, cfg2, base)
	if srcKey != "route_override_with_global_inheritance" || stateKey != "enabled" {
		t.Fatalf("SelectorKey exact route: src=%q state=%q", srcKey, stateKey)
	}
	selByID, err := cache.ParseInspectionSelector(cache.Selector{
		Kind:    cache.SelectorHost,
		Host:    "example.com",
		RouteID: routeNoOverrideID,
	}, cfg2)
	if err != nil {
		t.Fatal(err)
	}
	_, srcByID, stateByID, _ := resolveInspectionRoutePolicy(selByID, cfg2, base)
	if srcByID != "global_inherited_origin_precedence" || stateByID != "disabled_retained_entries_may_exist" {
		t.Fatalf("RouteID no override: src=%q state=%q", srcByID, stateByID)
	}
	// Prefix selector on root-only server exercises rootShadowed without Tier-2 shadow.
	selRootPrefix, err := cache.ParseInspectionSelector(cache.Selector{
		Kind:   cache.SelectorPrefix,
		Host:   "other.example.com",
		Prefix: "/anything",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, srcRoot, _, _ := resolveInspectionRoutePolicy(selRootPrefix, cfg2, base)
	if srcRoot != "global_inherited_origin_precedence" {
		t.Fatalf("root-only server prefix: src=%q", srcRoot)
	}
}

func TestResolveInspectionRoutePolicyRegexAndFallbackSemantics(t *testing.T) {
	cap60 := config.Duration(60 * time.Second)
	cfg := &config.Config{
		Cache: config.CacheConfig{
			Enabled:    true,
			DefaultTTL: config.Duration(5 * time.Minute),
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"example.com"},
				Locations: []config.LocationConfig{
					{
						Match:     config.MatchConfig{Type: "regex", Path: `^/assets/.*`},
						ProxyPass: "http://assets:8080",
						Cache:     true,
						CachePolicy: &config.LocationCachePolicy{
							MaxTTL: &cap60,
						},
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						ProxyPass: "http://app:8080",
						Cache:     false,
					},
				},
			},
		},
	}
	base := cachemeta.EffectivePolicy{DefaultTTLSeconds: 300}

	// Verify against the actual router via testRoute.
	rtAccount := testRoute(cfg, routeTestRequest{Host: "example.com", Path: "/account"})
	if !rtAccount.Matched || rtAccount.Match != "/" || rtAccount.Cache {
		t.Fatalf("expected router to select cache-disabled '/' for /account, got matched=%v match=%q cache=%v",
			rtAccount.Matched, rtAccount.Match, rtAccount.Cache)
	}

	// 1. Inspecting /account via ExactURL or Key must report the '/' fallback (cache disabled, no regex TTL cap).
	for _, tc := range []cache.Selector{
		{Kind: cache.SelectorExactURL, URL: "http://example.com/account"},
		{Kind: cache.SelectorExactURL, Host: "example.com", URI: "/%61ccount?tab=1"},
		{Kind: cache.SelectorKey, Key: "GET\nexample.com\n/account"},
	} {
		sel, err := cache.ParseInspectionSelector(tc, nil)
		if err != nil {
			t.Fatal(err)
		}
		pol, src, state, warns := resolveInspectionRoutePolicy(sel, cfg, base)
		if state != "disabled_retained_entries_may_exist" || src != "global_inherited_origin_precedence" || len(warns) != 0 {
			t.Fatalf("selector %+v: state=%q src=%q warns=%v", tc, state, src, warns)
		}
		if pol.MaxTTLSeconds != nil || pol.DefaultTTLSeconds != 300 {
			t.Fatalf("selector %+v reported regex TTL cap on /account: %+v", tc, pol)
		}
	}

	// 2. Inspecting /assets/bundle.js via ExactURL selects the regex route with its 60s TTL cap.
	selAsset, err := cache.ParseInspectionSelector(cache.Selector{
		Kind: cache.SelectorExactURL,
		URL:  "http://example.com/assets/bundle.js",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	polAsset, srcAsset, stateAsset, warnsAsset := resolveInspectionRoutePolicy(selAsset, cfg, base)
	if stateAsset != "enabled" || srcAsset != "route_override_with_global_inheritance" || len(warnsAsset) != 0 {
		t.Fatalf("asset URL: state=%q src=%q warns=%v", stateAsset, srcAsset, warnsAsset)
	}
	if polAsset.MaxTTLSeconds == nil || *polAsset.MaxTTLSeconds != 60 || polAsset.DefaultTTLSeconds != 60 {
		t.Fatalf("asset URL policy: %+v", polAsset)
	}

	// 3. Prefix/host selectors with an unshadowed regex route report uncertainty rather than asserting the regex policy.
	cfgRegexOnly := &config.Config{
		Cache: cfg.Cache,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"example.com"},
				Locations:   cfg.Servers[0].Locations[:1],
			},
		},
	}
	for _, selIn := range []cache.Selector{
		{Kind: cache.SelectorPrefix, Host: "example.com", Prefix: "/account"},
		{Kind: cache.SelectorHost, Host: "example.com"},
	} {
		sel, err := cache.ParseInspectionSelector(selIn, nil)
		if err != nil {
			t.Fatal(err)
		}
		polUncertain, srcUncertain, _, warnsUncertain := resolveInspectionRoutePolicy(sel, cfgRegexOnly, base)
		if srcUncertain != "ambiguous_multiple_routes_use_route_id" || len(warnsUncertain) == 0 || polUncertain.MaxTTLSeconds != nil {
			t.Fatalf("expected uncertainty for %+v on regex-only server, got src=%q warns=%v pol=%+v",
				selIn, srcUncertain, warnsUncertain, polUncertain)
		}
	}
}
