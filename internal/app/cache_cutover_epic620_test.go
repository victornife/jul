// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/adminapi"
	"jul/internal/background"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/rbac"
)

func strPtrTest(s string) *string { return &s }

// TestRouteBackendCutoverInvalidateAndRetainMatrix verifies #624 (CACHE-OPS-04):
//   - Under reload_policy = "retain" (default), changing a route's backend from
//     A to B preserves valid cached entries until expiry or explicit purge.
//   - Under reload_policy = "invalidate", changing a route's backend from A to B
//     invalidates the affected route across memory and disk (fresh hits, Range,
//     Vary, large disk entries) while keeping unrelated routes warm.
//   - Barrier-controlled concurrent pre-cutover cold fills, 304 Not Modified
//     revalidations, background SWR refreshes, and delayed old-handler admissions
//     cannot publish or serve pre-cutover A responses after Publish.
//   - Preview / Build(..., false) and Prepare + abortFn have zero invalidation
//     side effects.
//   - Config rollback A -> B -> A under reload_policy = "invalidate" does not
//     resurrect entries invalidated at A -> B.
//   - Partial post-Publish invalidation failure keeps B committed and records
//     degraded cutover status via LastCacheCutover() while fencing old entries.
func TestRouteBackendCutoverInvalidateAndRetainMatrix(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	var (
		app1V1Hits atomic.Int64
		app1V2Hits atomic.Int64
		app2Hits   atomic.Int64

		holdColdFill atomic.Bool
		coldEntered  = make(chan struct{}, 4)
		coldRelease  = make(chan struct{})

		hold304    atomic.Bool
		entered304 = make(chan struct{}, 4)
		release304 = make(chan struct{})

		holdSWR    atomic.Bool
		enteredSWR = make(chan struct{}, 4)
		releaseSWR = make(chan struct{})
	)

	largePayloadA := strings.Repeat("BACKEND-A-LARGE-CHUNK-", 4096) // ~90 KiB
	largePayloadB := strings.Repeat("BACKEND-B-LARGE-CHUNK-", 4096)

	originApp1V1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app1V1Hits.Add(1)
		switch r.URL.Path {
		case "/app-one/slow-fill":
			if holdColdFill.Load() {
				select {
				case coldEntered <- struct{}{}:
				default:
				}
				<-coldRelease
			}
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("X-Backend", "app1-v1")
			_, _ = w.Write([]byte("app1-v1-slow-fill"))
		case "/app-one/reval":
			if r.Header.Get("If-None-Match") == `"etag-a1"` {
				if hold304.Load() {
					select {
					case entered304 <- struct{}{}:
					default:
					}
					<-release304
				}
				w.Header().Set("ETag", `"etag-a1"`)
				w.Header().Set("Cache-Control", "public, max-age=300")
				w.Header().Set("X-Backend", "app1-v1-304")
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"etag-a1"`)
			w.Header().Set("Cache-Control", "public, max-age=1, must-revalidate")
			w.Header().Set("X-Backend", "app1-v1")
			_, _ = w.Write([]byte("app1-v1-reval-body"))
		case "/app-one/swr":
			if holdSWR.Load() {
				select {
				case enteredSWR <- struct{}{}:
				default:
				}
				<-releaseSWR
			}
			w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
			w.Header().Set("X-Backend", "app1-v1")
			_, _ = w.Write([]byte(fmt.Sprintf("app1-v1-swr-%d", app1V1Hits.Load())))
		case "/app-one/large":
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("X-Backend", "app1-v1")
			_, _ = w.Write([]byte(largePayloadA))
		default:
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("X-Backend", "app1-v1")
			_, _ = w.Write([]byte("app1-v1:" + r.URL.Path))
		}
	}))
	defer originApp1V1.Close()

	originApp1V2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		app1V2Hits.Add(1)
		switch r.URL.Path {
		case "/app-one/large":
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Vary", "Accept-Encoding")
			w.Header().Set("X-Backend", "app1-v2")
			_, _ = w.Write([]byte(largePayloadB))
		default:
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("X-Backend", "app1-v2")
			_, _ = w.Write([]byte("app1-v2:" + r.URL.Path))
		}
	}))
	defer originApp1V2.Close()

	originApp2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := app2Hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Header().Set("X-Backend", "app2")
		_, _ = w.Write([]byte(fmt.Sprintf("app2-hit-%d:%s", n, r.URL.Path)))
	}))
	defer originApp2.Close()

	factory, cleanup := minimalFactory(t)
	defer cleanup()
	c, err := cache.New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        16 << 20,
		DiskPath:             tmpDir,
		DiskMaxSize:          64 << 20,
		MaxEntrySize:         4 << 20,
		DefaultTTL:           config.Duration(5 * time.Minute),
		StaleWhileRevalidate: config.Duration(30 * time.Second),
		StaleIfError:         config.Duration(5 * time.Minute),
		Lock:                 true,
		LockTimeout:          config.Duration(5 * time.Second),
		RangeEnabled:         true,
		MaxRanges:            8,
	}, factory.Log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	factory.Cache = c

	makeConfig := func(app1Target, app2Target, app1ReloadPolicy string) *config.Config {
		return &config.Config{
			Cache: config.CacheConfig{
				Enabled:       true,
				MemoryMaxSize: 16 << 20,
				DiskPath:      tmpDir,
				DiskMaxSize:   64 << 20,
				MaxEntrySize:  4 << 20,
				DefaultTTL:    config.Duration(5 * time.Minute),
				Lock:          true,
				RangeEnabled:  true,
				ReloadPolicy:  "retain",
			},
			Servers: []config.ServerConfig{
				{
					Listen:      ":8080",
					ServerNames: []string{"portal.example.com"},
					Locations: []config.LocationConfig{
						{
							RouteID:   strPtrTest("route-app-one"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/app-one/"},
							ProxyPass: app1Target,
							Cache:     true,
							CachePolicy: &config.LocationCachePolicy{
								ReloadPolicy: app1ReloadPolicy,
							},
						},
						{
							RouteID:   strPtrTest("route-app-two"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/app-two/"},
							ProxyPass: app2Target,
							Cache:     true,
							CachePolicy: &config.LocationCachePolicy{
								ReloadPolicy: "retain",
							},
						},
					},
				},
			},
		}
	}

	bgCtx, bgCancel := context.WithCancel(context.Background())
	bgGroup := background.NewGroup(bgCtx, background.GroupOptions{
		Generation:   1,
		MaxOperation: 30 * time.Second,
	})
	defer func() {
		bgGroup.Cancel()
		bgCancel()
		_ = bgGroup.Wait(5 * time.Second)
	}()

	doReq := func(h http.Handler, path string, hdrs map[string]string) (*httptest.ResponseRecorder, string) {
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+path, nil)
		req = req.WithContext(background.WithLease(req.Context(), bgGroup))
		req.Host = "portal.example.com"
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec, rec.Body.String()
	}

	// 1. Control test: reload_policy = "retain" preserves cached entries across A -> B cutover.
	cfgRetainA := makeConfig(originApp1V1.URL, originApp2.URL, "retain")
	resRetainA, _, err := factory.Build(ctx, cfgRetainA, true)
	if err != nil {
		t.Fatalf("Build cfgRetainA: %v", err)
	}
	hRetainA := resRetainA[":8080"]

	if _, body := doReq(hRetainA, "/app-one/retain-check", nil); body != "app1-v1:/app-one/retain-check" {
		t.Fatalf("unexpected initial retain body: %q", body)
	}
	cfgRetainB := makeConfig(originApp1V2.URL, originApp2.URL, "retain")
	resRetainB, _, err := factory.Build(ctx, cfgRetainB, true)
	if err != nil {
		t.Fatalf("Build cfgRetainB: %v", err)
	}
	hRetainB := resRetainB[":8080"]
	// Under reload_policy = "retain", the cached entry from v1 remains warm!
	recRetain, bodyRetain := doReq(hRetainB, "/app-one/retain-check", nil)
	if bodyRetain != "app1-v1:/app-one/retain-check" || recRetain.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected retained HIT from app1-v1 under reload_policy=retain, got X-Cache=%q body=%q", recRetain.Header().Get("X-Cache"), bodyRetain)
	}

	// 2. Switch /app-one/ to reload_policy = "invalidate" on originApp1V1 and warm all entry classes.
	cfgInvA := makeConfig(originApp1V1.URL, originApp2.URL, "invalidate")
	resInvA, _, err := factory.Build(ctx, cfgInvA, true)
	if err != nil {
		t.Fatalf("Build cfgInvA: %v", err)
	}
	hInvA := resInvA[":8080"]

	// Warm fresh hit, Range/Vary large entry, 304 entry, SWR entry, and unrelated /app-two/ entry.
	if _, body := doReq(hInvA, "/app-one/item", nil); body != "app1-v1:/app-one/item" {
		t.Fatalf("warm /app-one/item: %q", body)
	}
	if _, body := doReq(hInvA, "/app-one/large", map[string]string{"Accept-Encoding": "gzip"}); body != largePayloadA {
		t.Fatalf("warm /app-one/large length=%d", len(body))
	}
	if _, body := doReq(hInvA, "/app-one/reval", nil); body != "app1-v1-reval-body" {
		t.Fatalf("warm /app-one/reval: %q", body)
	}
	if _, body := doReq(hInvA, "/app-one/swr", nil); !strings.HasPrefix(body, "app1-v1-swr-") {
		t.Fatalf("warm /app-one/swr: %q", body)
	}
	_, app2WarmBody := doReq(hInvA, "/app-two/keep", nil)

	// 3. Verify Build(..., false) and Prepare + abortFn perform ZERO invalidation!
	cfgInvB := makeConfig(originApp1V2.URL, originApp2.URL, "invalidate")
	if _, _, err := factory.Build(ctx, cfgInvB, false); err != nil {
		t.Fatalf("Build(..., false): %v", err)
	}
	_, _, commitAbort, abortFn, err := factory.Prepare(ctx, cfgInvB)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_ = commitAbort
	abortFn()

	recStillWarm, bodyStillWarm := doReq(hInvA, "/app-one/item", nil)
	if recStillWarm.Header().Get("X-Cache") != "HIT" || bodyStillWarm != "app1-v1:/app-one/item" {
		t.Fatalf("expected /app-one/item to remain HIT after Build(..., false) and Prepare+abortFn, got %q %q", recStillWarm.Header().Get("X-Cache"), bodyStillWarm)
	}

	// Wait 1.2s so /app-one/reval and /app-one/swr become stale before starting barrier races.
	time.Sleep(1200 * time.Millisecond)

	// 4. Barrier-controlled concurrent pre-cutover operations against A:
	//   (a) cold fill (/app-one/slow-fill)
	//   (b) 304 revalidation (/app-one/reval)
	//   (c) background SWR refresh (/app-one/swr)
	holdColdFill.Store(true)
	hold304.Store(true)
	holdSWR.Store(true)

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		doReq(hInvA, "/app-one/slow-fill", nil)
	}()
	go func() {
		defer wg.Done()
		doReq(hInvA, "/app-one/reval", nil)
	}()
	go func() {
		defer wg.Done()
		// Triggers background SWR refresh while returning stale response to caller.
		doReq(hInvA, "/app-one/swr", nil)
	}()

	<-coldEntered
	<-entered304
	<-enteredSWR

	// 5. Prepare and commit (Publish) cutover to B while A operations are in-flight!
	resInvB, _, commitB, _, err := factory.Prepare(ctx, cfgInvB)
	if err != nil {
		t.Fatalf("Prepare cfgInvB: %v", err)
	}
	_, _ = commitB()
	hInvB := resInvB[":8080"]

	cutoverResults, cutoverErr := factory.LastCacheCutover()
	if cutoverErr != nil || len(cutoverResults) != 1 || cutoverResults[0].InvalidationEffective != "effective" {
		t.Fatalf("expected 1 effective cutover result on Publish, got results=%+v err=%v", cutoverResults, cutoverErr)
	}

	// Release all pre-cutover A operations so they finish AFTER Publish!
	holdColdFill.Store(false)
	hold304.Store(false)
	holdSWR.Store(false)
	close(coldRelease)
	close(release304)
	close(releaseSWR)
	wg.Wait()
	// Give async SWR goroutine a brief moment to finish its write attempt.
	time.Sleep(50 * time.Millisecond)

	// 6. Verify delayed old-handler admission (request dispatched on hInvA AFTER Publish):
	// It must NOT read or write to the post-cutover cache!
	_, delayedBody := doReq(hInvA, "/app-one/delayed-old-handler", nil)
	if delayedBody != "app1-v1:/app-one/delayed-old-handler" {
		t.Fatalf("unexpected delayed old-handler origin body: %q", delayedBody)
	}
	// Now request /app-one/delayed-old-handler on hInvB: must be a MISS served by app1-v2!
	recAfterDelayed, bodyAfterDelayed := doReq(hInvB, "/app-one/delayed-old-handler", nil)
	if recAfterDelayed.Header().Get("X-Cache") != "MISS" || bodyAfterDelayed != "app1-v2:/app-one/delayed-old-handler" {
		t.Fatalf("delayed old-handler request poisoned post-cutover cache: X-Cache=%q body=%q", recAfterDelayed.Header().Get("X-Cache"), bodyAfterDelayed)
	}

	// 7. Verify all post-cutover requests on hInvB serve app1-v2 (never app1-v1), including:
	//   - /app-one/item (was warm hit on A)
	//   - /app-one/slow-fill (was in-flight cold fill on A)
	//   - /app-one/reval (was in-flight 304 revalidation on A)
	//   - /app-one/swr (was in-flight background SWR refresh on A)
	//   - /app-one/large with Range bytes=0-20 (was warm large disk/Vary entry on A)
	for _, p := range []string{"/app-one/item", "/app-one/slow-fill", "/app-one/reval", "/app-one/swr"} {
		rec, body := doReq(hInvB, p, nil)
		if body != "app1-v2:"+p {
			t.Fatalf("post-cutover %s served stale backend A representation: X-Cache=%q body=%q", p, rec.Header().Get("X-Cache"), body)
		}
	}
	recRange, bodyRange := doReq(hInvB, "/app-one/large", map[string]string{
		"Accept-Encoding": "gzip",
		"Range":           "bytes=0-20",
	})
	if recRange.Code != http.StatusPartialContent || bodyRange != largePayloadB[:21] {
		t.Fatalf("post-cutover Range request on /app-one/large failed: status=%d body=%q", recRange.Code, bodyRange)
	}

	// 8. Verify unrelated route /app-two/keep stayed warm (HIT)!
	recApp2, bodyApp2 := doReq(hInvB, "/app-two/keep", nil)
	if recApp2.Header().Get("X-Cache") != "HIT" || bodyApp2 != app2WarmBody {
		t.Fatalf("unrelated route /app-two/keep was evicted during /app-one/ cutover: X-Cache=%q body=%q", recApp2.Header().Get("X-Cache"), bodyApp2)
	}

	// 9. Verify config rollback A -> B -> A under reload_policy = "invalidate" does NOT resurrect
	// old A entries or keep B entries!
	app1V1BeforeRollback := app1V1Hits.Load()
	resRollbackA, _, err := factory.Build(ctx, cfgInvA, true)
	if err != nil {
		t.Fatalf("Build rollback cfgInvA: %v", err)
	}
	hRollbackA := resRollbackA[":8080"]
	recRoll, bodyRoll := doReq(hRollbackA, "/app-one/item", nil)
	if recRoll.Header().Get("X-Cache") != "MISS" || bodyRoll != "app1-v1:/app-one/item" || app1V1Hits.Load() <= app1V1BeforeRollback {
		t.Fatalf("expected fresh MISS to origin A after A -> B -> A rollback, got X-Cache=%q body=%q", recRoll.Header().Get("X-Cache"), bodyRoll)
	}

	// 10. Verify partial post-Publish invalidation failure keeps new config committed,
	// reports degraded cutover via LastCacheCutover(), and still fences old entries via RouteEpoch!
	if err := os.Chmod(tmpDir, 0o500); err != nil {
		t.Fatalf("chmod read-only: %v", err)
	}
	defer func() { _ = os.Chmod(tmpDir, 0o700) }()

	resFailB, _, err := factory.Build(ctx, cfgInvB, true)
	if err != nil {
		t.Fatalf("Build cfgInvB under read-only cache dir must still commit config: %v", err)
	}
	failResults, failErr := factory.LastCacheCutover()
	if failErr == nil || len(failResults) == 0 || failResults[0].InvalidationEffective == "effective" {
		t.Fatalf("expected LastCacheCutover to report degraded post-Publish invalidation, got results=%+v err=%v", failResults, failErr)
	}
	// Restore permissions before closing cache.
	_ = os.Chmod(tmpDir, 0o700)

	// Even though disk persistence failed during cutover, RouteEpoch advanced in memory so
	// hFailB serves app1-v2 and does not reuse pre-cutover A entries!
	_, bodyAfterPartialFail := doReq(resFailB[":8080"], "/app-one/item", nil)
	if bodyAfterPartialFail != "app1-v2:/app-one/item" {
		t.Fatalf("expected post-cutover handler to serve app1-v2 even after partial disk cutover failure, got %q", bodyAfterPartialFail)
	}

	// 11. Verify policy-only flip ("invalidate" -> "retain" -> "invalidate") WITHOUT backend change
	// preserves warm cache entries, and verify same-host cross-listener (:9090) and retained child
	// route (/app-one/static/) isolation during /app-one/ backend cutover!
	if _, body := doReq(resFailB[":8080"], "/app-one/policy-flip", nil); body != "app1-v2:/app-one/policy-flip" {
		t.Fatalf("seed /app-one/policy-flip: %q", body)
	}
	cfgFlipRetain := makeConfig(originApp1V2.URL, originApp2.URL, "retain")
	resFlipRetain, _, err := factory.Build(ctx, cfgFlipRetain, true)
	if err != nil {
		t.Fatalf("Build cfgFlipRetain: %v", err)
	}
	if rec, _ := doReq(resFlipRetain[":8080"], "/app-one/policy-flip", nil); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("policy-only flip invalidate->retain without backend change must keep entry warm, got %q", rec.Header().Get("X-Cache"))
	}
	cfgFlipInv := makeConfig(originApp1V2.URL, originApp2.URL, "invalidate")
	resFlipInv, _, err := factory.Build(ctx, cfgFlipInv, true)
	if err != nil {
		t.Fatalf("Build cfgFlipInv: %v", err)
	}
	if rec, _ := doReq(resFlipInv[":8080"], "/app-one/policy-flip", nil); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("policy-only flip retain->invalidate without backend change must keep entry warm, got %q", rec.Header().Get("X-Cache"))
	}

	// Same-host cross-listener (:8080 vs :9090) and retained child route (/app-one/static/) isolation.
	cfgMultiListenerA := &config.Config{
		Cache: cfgFlipInv.Cache,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:     strPtrTest("route-app-one-child"),
						Match:       config.MatchConfig{Type: "prefix", Path: "/app-one/static/"},
						ProxyPass:   originApp2.URL,
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "retain"},
					},
					{
						RouteID:     strPtrTest("route-app-one"),
						Match:       config.MatchConfig{Type: "prefix", Path: "/app-one/"},
						ProxyPass:   originApp1V1.URL,
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"},
					},
				},
			},
			{
				Listen:      ":9090",
				ServerNames: []string{"portal.example.com"},
				Locations: []config.LocationConfig{
					{
						RouteID:     strPtrTest("route-9090"),
						Match:       config.MatchConfig{Type: "prefix", Path: "/app-one/"},
						ProxyPass:   originApp2.URL,
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "retain"},
					},
				},
			},
		},
	}
	resMLA, _, err := factory.Build(ctx, cfgMultiListenerA, true)
	if err != nil {
		t.Fatalf("Build cfgMultiListenerA: %v", err)
	}
	_, _ = doReq(resMLA[":8080"], "/app-one/parent-item", nil)
	_, childBody := doReq(resMLA[":8080"], "/app-one/static/logo.svg", nil)
	_, port9090Body := doReq(resMLA[":9090"], "/app-one/parent-item", nil)

	cfgMultiListenerB := *cfgMultiListenerA
	cfgMultiListenerB.Servers = append([]config.ServerConfig(nil), cfgMultiListenerA.Servers...)
	cfgMultiListenerB.Servers[0].Locations = append([]config.LocationConfig(nil), cfgMultiListenerA.Servers[0].Locations...)
	cfgMultiListenerB.Servers[0].Locations[1].ProxyPass = originApp1V2.URL // only :8080 /app-one/ changes backend!
	resMLB, _, err := factory.Build(ctx, &cfgMultiListenerB, true)
	if err != nil {
		t.Fatalf("Build cfgMultiListenerB: %v", err)
	}
	if rec, body := doReq(resMLB[":8080"], "/app-one/parent-item", nil); rec.Header().Get("X-Cache") != "MISS" || body != "app1-v2:/app-one/parent-item" {
		t.Fatalf("expected :8080 /app-one/parent-item to be invalidated, got %q %q", rec.Header().Get("X-Cache"), body)
	}
	if rec, body := doReq(resMLB[":8080"], "/app-one/static/logo.svg", nil); rec.Header().Get("X-Cache") != "HIT" || body != childBody {
		t.Fatalf("retained child route /app-one/static/logo.svg must remain HIT, got %q %q", rec.Header().Get("X-Cache"), body)
	}
	if rec, body := doReq(resMLB[":9090"], "/app-one/parent-item", nil); rec.Header().Get("X-Cache") != "HIT" || body != port9090Body {
		t.Fatalf("retained route on listener :9090 must remain HIT, got %q %q", rec.Header().Get("X-Cache"), body)
	}

	// Wildcard/unnamed-host cutover (SelectorAll with cutoverRouteScopes) must NOT purge unrelated retained routes!
	cfgWildcardA := &config.Config{
		Cache: cfgFlipInv.Cache,
		Servers: []config.ServerConfig{
			{
				Listen:      ":8080",
				ServerNames: nil, // unnamed catch-all server -> SelectorAll with cutoverRouteScopes
				Locations: []config.LocationConfig{
					{
						RouteID:     strPtrTest("wc-inv"),
						Match:       config.MatchConfig{Type: "prefix", Path: "/app-one/"},
						ProxyPass:   originApp1V1.URL,
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "invalidate"},
					},
					{
						RouteID:     strPtrTest("wc-ret"),
						Match:       config.MatchConfig{Type: "prefix", Path: "/app-two/"},
						ProxyPass:   originApp2.URL,
						Cache:       true,
						CachePolicy: &config.LocationCachePolicy{ReloadPolicy: "retain"},
					},
				},
			},
		},
	}
	resWCA, _, err := factory.Build(ctx, cfgWildcardA, true)
	if err != nil {
		t.Fatalf("Build cfgWildcardA: %v", err)
	}
	_, _ = doReq(resWCA[":8080"], "/app-one/wc-item", nil)
	_, wcRetBody := doReq(resWCA[":8080"], "/app-two/wc-keep", nil)

	cfgWildcardB := *cfgWildcardA
	cfgWildcardB.Servers = append([]config.ServerConfig(nil), cfgWildcardA.Servers...)
	cfgWildcardB.Servers[0].Locations = append([]config.LocationConfig(nil), cfgWildcardA.Servers[0].Locations...)
	cfgWildcardB.Servers[0].Locations[0].ProxyPass = originApp1V2.URL
	resWCB, _, err := factory.Build(ctx, &cfgWildcardB, true)
	if err != nil {
		t.Fatalf("Build cfgWildcardB: %v", err)
	}
	if rec, body := doReq(resWCB[":8080"], "/app-one/wc-item", nil); rec.Header().Get("X-Cache") != "MISS" || body != "app1-v2:/app-one/wc-item" {
		t.Fatalf("expected wildcard /app-one/wc-item to be invalidated, got %q %q", rec.Header().Get("X-Cache"), body)
	}
	if rec, body := doReq(resWCB[":8080"], "/app-two/wc-keep", nil); rec.Header().Get("X-Cache") != "HIT" || body != wcRetBody {
		t.Fatalf("retained sibling route on unnamed/wildcard server must remain HIT, got %q %q", rec.Header().Get("X-Cache"), body)
	}
}

// TestEpic620PortalAndTwoApplicationsEndToEnd verifies the integrated #620
// acceptance criteria on a single jul instance hosting a portal route ("/")
// and two independent applications ("/app-one/" and "/app-two/"):
//   - #621: Scoped purge by host, prefix, exact_url, and tag (dry-run preview,
//     execution, broad-scope guardrail, and application isolation).
//   - #622: Read-only cache inspection + bounded recent decision sampling with
//     secret-safe query redaction.
//   - #623: Route-level cache policy overrides (default_ttl = 0s, max_ttl cap,
//     SWR/SIE overrides, bypass_cookies, bypass_any_cookie, reload_policy) and
//     shared-cache safety invariants (Set-Cookie, Authorization, Vary).
//   - #624 + #666 + #668: Explicit route/backend cutover invalidation for
//     /app-one/ while keeping / and /app-two/ warm, semantic diff/preview
//     cutover plan reporting, and legacy purge durability outcome reporting.
func TestEpic620PortalAndTwoApplicationsEndToEnd(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	var (
		portalHits  atomic.Int64
		app1V1Hits  atomic.Int64
		app1V2Hits  atomic.Int64
		app2Hits    atomic.Int64
		privateHits atomic.Int64
	)

	portalOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := portalHits.Add(1)
		// Origin omits Cache-Control max-age so route default_ttl = 1h applies (#623).
		w.Header().Set("Cache-Tag", "portal, release-v1")
		_, _ = w.Write([]byte(fmt.Sprintf("portal-home-v1-hit-%d", n)))
	}))
	defer portalOrigin.Close()

	app1OriginV1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := app1V1Hits.Add(1)
		if r.URL.Path == "/app-one/no-explicit-cc" {
			// Origin omits freshness headers; route default_ttl = 0s prevents heuristic storage (#623).
			_, _ = w.Write([]byte(fmt.Sprintf("app1-no-cc-%d", n)))
			return
		}
		if r.URL.Path == "/app-one/set-cookie-leak" {
			// Even with max-age=300, Set-Cookie must never be stored in shared cache (#623).
			w.Header().Set("Cache-Control", "public, max-age=300")
			w.Header().Set("Set-Cookie", "app1_session=secret123; Path=/")
			_, _ = w.Write([]byte(fmt.Sprintf("app1-set-cookie-%d", n)))
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("Cache-Tag", "app-one, tenant-alpha")
		_, _ = w.Write([]byte(fmt.Sprintf("app1-v1-hit-%d:%s", n, r.URL.Path)))
	}))
	defer app1OriginV1.Close()

	app1OriginV2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := app1V2Hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("Cache-Tag", "app-one, tenant-alpha")
		_, _ = w.Write([]byte(fmt.Sprintf("app1-v2-hit-%d:%s", n, r.URL.Path)))
	}))
	defer app1OriginV2.Close()

	app2Origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := app2Hits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=600")
		w.Header().Set("Cache-Tag", "app-two, tenant-beta")
		_, _ = w.Write([]byte(fmt.Sprintf("app2-hit-%d:%s", n, r.URL.Path)))
	}))
	defer app2Origin.Close()

	privateOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := privateHits.Add(1)
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = w.Write([]byte(fmt.Sprintf("private-hit-%d", n)))
	}))
	defer privateOrigin.Close()

	d0s := config.Duration(0)
	d1m := config.Duration(time.Minute)
	d2m := config.Duration(2 * time.Minute)
	d10m := config.Duration(10 * time.Minute)
	d1h := config.Duration(time.Hour)
	d24h := config.Duration(24 * time.Hour)
	d30s := config.Duration(30 * time.Second)
	d5m := config.Duration(5 * time.Minute)

	makePortalConfig := func(app1Target string) *config.Config {
		return &config.Config{
			Cache: config.CacheConfig{
				Enabled:              true,
				MemoryMaxSize:        16 << 20,
				DiskPath:             tmpDir,
				DiskMaxSize:          64 << 20,
				MaxEntrySize:         4 << 20,
				DefaultTTL:           config.Duration(5 * time.Minute),
				StaleWhileRevalidate: d30s,
				StaleIfError:         d5m,
				Lock:                 true,
				RangeEnabled:         true,
				TagHeaders:           []string{"Cache-Tag"},
				ReloadPolicy:         "retain",
				DecisionSamples:      true,
			},
			Servers: []config.ServerConfig{
				{
					Listen:      ":8080",
					ServerNames: []string{"portal.example.com"},
					Locations: []config.LocationConfig{
						{
							RouteID:   strPtrTest("route-app-one"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/app-one/"},
							ProxyPass: app1Target,
							Cache:     true,
							CachePolicy: &config.LocationCachePolicy{
								DefaultTTL:           &d0s,
								MaxTTL:               &d1m,
								StaleWhileRevalidate: &d30s,
								StaleIfError:         &d5m,
								BypassCookies:        []string{"app1_session"},
								ReloadPolicy:         "invalidate",
							},
						},
						{
							RouteID:   strPtrTest("route-app-two"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/app-two/"},
							ProxyPass: app2Origin.URL,
							Cache:     true,
							CachePolicy: &config.LocationCachePolicy{
								DefaultTTL:      &d2m,
								MaxTTL:          &d10m,
								BypassAnyCookie: true,
								ReloadPolicy:    "retain",
							},
						},
						{
							RouteID:   strPtrTest("route-private"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/private/"},
							ProxyPass: privateOrigin.URL,
							Cache:     false,
						},
						{
							RouteID:   strPtrTest("route-portal"),
							Match:     config.MatchConfig{Type: "prefix", Path: "/"},
							ProxyPass: portalOrigin.URL,
							Cache:     true,
							CachePolicy: &config.LocationCachePolicy{
								DefaultTTL:   &d1h,
								MaxTTL:       &d24h,
								ReloadPolicy: "retain",
							},
						},
					},
				},
			},
		}
	}

	factory, cleanup := minimalFactory(t)
	defer cleanup()
	stripTags := true
	c, err := cache.New(config.CacheConfig{
		Enabled:              true,
		MemoryMaxSize:        16 << 20,
		DiskPath:             tmpDir,
		DiskMaxSize:          64 << 20,
		MaxEntrySize:         4 << 20,
		DefaultTTL:           config.Duration(5 * time.Minute),
		StaleWhileRevalidate: d30s,
		StaleIfError:         d5m,
		Lock:                 true,
		RangeEnabled:         true,
		TagHeaders:           []string{"Cache-Tag"},
		StripTagHeaders:      &stripTags,
		DecisionSamples:      true,
	}, factory.Log)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	factory.Cache = c

	cfgV1 := makePortalConfig(app1OriginV1.URL)
	resV1, _, err := factory.Build(ctx, cfgV1, true)
	if err != nil {
		t.Fatalf("Build cfgV1: %v", err)
	}
	gw := resV1[":8080"]

	adminSrv := admin.New(config.AdminConfig{
		Enabled: true,
		Listen:  "127.0.0.1:0",
		Token:   "super-secret-admin-token-1234567890",
	}, factory.Log, admin.Deps{
		Cache: c,
		LoadConfig: func() (*config.Config, error) {
			return factory.liveConfig, nil
		},
	})
	adminTS := httptest.NewServer(adminSrv.Handler())
	defer adminTS.Close()

	gwGet := func(path string, cookie string) (*httptest.ResponseRecorder, string) {
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+path, nil)
		req.Host = "portal.example.com"
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		rec := httptest.NewRecorder()
		gw.ServeHTTP(rec, req)
		return rec, rec.Body.String()
	}

	// ── Step 1: Verify #623 per-route cache policies & safety invariants ─────
	// Portal ("/") caches using route default_ttl = 1h even when origin omits Cache-Control.
	recP1, bodyP1 := gwGet("/index.html", "")
	recP2, bodyP2 := gwGet("/index.html", "")
	if recP1.Header().Get("X-Cache") != "MISS" || recP2.Header().Get("X-Cache") != "HIT" || bodyP1 != bodyP2 {
		t.Fatalf("portal route default_ttl=1h failed: p1=%q p2=%q", recP1.Header().Get("X-Cache"), recP2.Header().Get("X-Cache"))
	}

	// App 1 ("/app-one/") requires explicit origin freshness because default_ttl = 0s.
	recNoCC1, _ := gwGet("/app-one/no-explicit-cc", "")
	recNoCC2, _ := gwGet("/app-one/no-explicit-cc", "")
	if recNoCC1.Header().Get("X-Cache") != "MISS" || recNoCC2.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected default_ttl=0s on /app-one/ to prevent caching when origin omits freshness")
	}

	// App 1 caches explicit max-age=600 responses, capped at route max_ttl=1m, and bypasses app1_session cookie.
	recA1, bodyA1 := gwGet("/app-one/dashboard?api_key=TOPSECRET123", "")
	recA2, bodyA2 := gwGet("/app-one/dashboard?api_key=TOPSECRET123", "")
	if recA1.Header().Get("X-Cache") != "MISS" || recA2.Header().Get("X-Cache") != "HIT" || bodyA1 != bodyA2 {
		t.Fatalf("app-one dashboard caching failed: a1=%q a2=%q", recA1.Header().Get("X-Cache"), recA2.Header().Get("X-Cache"))
	}
	// Named cookie app1_session bypasses lookup & storage, while unrelated cookie theme=dark hits cache!
	recBypassCookie, _ := gwGet("/app-one/dashboard?api_key=TOPSECRET123", "app1_session=user42")
	recAllowedCookie, _ := gwGet("/app-one/dashboard?api_key=TOPSECRET123", "theme=dark")
	if recBypassCookie.Header().Get("X-Cache") != "BYPASS" || recAllowedCookie.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("app-one bypass_cookies failed: session=%q theme=%q", recBypassCookie.Header().Get("X-Cache"), recAllowedCookie.Header().Get("X-Cache"))
	}
	// Set-Cookie response is never stored.
	_, bSC1 := gwGet("/app-one/set-cookie-leak", "")
	_, bSC2 := gwGet("/app-one/set-cookie-leak", "")
	if bSC1 == bSC2 {
		t.Fatalf("Set-Cookie response must never be cached")
	}

	// App 2 ("/app-two/") caches anonymous requests and bypasses ANY cookie (bypass_any_cookie = true).
	recB1, bodyB1 := gwGet("/app-two/catalog", "")
	recB2, bodyB2 := gwGet("/app-two/catalog", "")
	recBCookie, _ := gwGet("/app-two/catalog", "any_cookie=1")
	if recB1.Header().Get("X-Cache") != "MISS" || recB2.Header().Get("X-Cache") != "HIT" || bodyB1 != bodyB2 || recBCookie.Header().Get("X-Cache") != "BYPASS" {
		t.Fatalf("app-two caching/bypass_any_cookie failed: b1=%q b2=%q cookie=%q", recB1.Header().Get("X-Cache"), recB2.Header().Get("X-Cache"), recBCookie.Header().Get("X-Cache"))
	}

	// Private route ("/private/") has cache = false.
	_, bPriv1 := gwGet("/private/profile", "")
	_, bPriv2 := gwGet("/private/profile", "")
	if bPriv1 == bPriv2 {
		t.Fatalf("private route with cache=false must never cache")
	}

	// ── Step 2: Verify #622 read-only inspection & secret-safe decisions ────
	inspectReqBody, _ := json.Marshal(adminapi.CacheInspectRequest{
		Selector: adminapi.CacheSelector{
			Kind:   "prefix",
			Host:   "portal.example.com",
			Prefix: "/app-one/",
		},
	})
	inspectReq, _ := http.NewRequest(http.MethodPost, adminTS.URL+"/api/v1/cache/inspect", bytes.NewReader(inspectReqBody))
	inspectReq.Header.Set("Authorization", "Bearer super-secret-admin-token-1234567890")
	inspectReq.Header.Set("Content-Type", "application/json")
	inspectResp, err := http.DefaultClient.Do(inspectReq)
	if err != nil {
		t.Fatalf("POST /api/v1/cache/inspect: %v", err)
	}
	rawInspectBytes, _ := io.ReadAll(inspectResp.Body)
	_ = inspectResp.Body.Close()
	if inspectResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 from route cache inspection, got %d: %s", inspectResp.StatusCode, string(rawInspectBytes))
	}
	// Secret query string "TOPSECRET123" and cookie value "user42" must NEVER appear in inspection payload!
	if strings.Contains(string(rawInspectBytes), "TOPSECRET123") || strings.Contains(string(rawInspectBytes), "user42") {
		t.Fatalf("secret query or cookie leaked in cache inspection payload: %s", string(rawInspectBytes))
	}
	var routeInspect adminapi.CacheInspectResponse
	if err := json.Unmarshal(rawInspectBytes, &routeInspect); err != nil {
		t.Fatalf("unmarshal CacheInspectResponse: %v", err)
	}
	if routeInspect.Inspection.Policy.ReloadPolicy != "invalidate" {
		t.Fatalf("expected route inspection policy reload_policy=invalidate, got %q", routeInspect.Inspection.Policy.ReloadPolicy)
	}
	if routeInspect.Inspection.MatchedResources < 1 || len(routeInspect.Inspection.Decisions) == 0 {
		t.Fatalf("expected matched resources and recent decisions in route inspection, got resources=%d decisions=%d",
			routeInspect.Inspection.MatchedResources, len(routeInspect.Inspection.Decisions))
	}

	// ── Step 3: Verify #621 scoped purge (tag & prefix) isolates App 1 from App 2 & Portal ──
	// Dry-run preview for tag "app-one"
	tagSelector := adminapi.CacheSelector{
		Kind: "tag",
		Host: "portal.example.com",
		Tags: []string{"app-one"},
	}
	purgeBody, _ := json.Marshal(adminapi.CachePurgePreviewRequest{
		Selector:       tagSelector,
		IncludeSamples: true,
	})
	pReq, _ := http.NewRequest(http.MethodPost, adminTS.URL+"/api/v1/cache/purge/preview", bytes.NewReader(purgeBody))
	pReq.Header.Set("Authorization", "Bearer super-secret-admin-token-1234567890")
	pReq.Header.Set("Content-Type", "application/json")
	pResp, err := http.DefaultClient.Do(pReq)
	if err != nil {
		t.Fatalf("POST /api/v1/cache/purge/preview: %v", err)
	}
	rawPrevBytes, _ := io.ReadAll(pResp.Body)
	_ = pResp.Body.Close()
	if pResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on tag preview, got %d: %s", pResp.StatusCode, string(rawPrevBytes))
	}
	var prevResp adminapi.CachePurgePreviewResponse
	if err := json.Unmarshal(rawPrevBytes, &prevResp); err != nil {
		t.Fatalf("unmarshal CachePurgePreviewResponse: %v", err)
	}
	// Dry-run preview must leave /app-one/dashboard warm!
	if rec, _ := gwGet("/app-one/dashboard?api_key=TOPSECRET123", ""); rec.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("dry-run preview must not evict /app-one/dashboard")
	}

	// Execute tag purge for "app-one"
	execBody, _ := json.Marshal(adminapi.CachePurgeRequest{
		Selector:     tagSelector,
		PreviewToken: prevResp.PreviewToken,
	})
	eReq, _ := http.NewRequest(http.MethodPost, adminTS.URL+"/api/v1/cache/purge", bytes.NewReader(execBody))
	eReq.Header.Set("Authorization", "Bearer super-secret-admin-token-1234567890")
	eReq.Header.Set("Content-Type", "application/json")
	eResp, err := http.DefaultClient.Do(eReq)
	if err != nil {
		t.Fatalf("POST /api/v1/cache/purge: %v", err)
	}
	rawExecBytes, _ := io.ReadAll(eResp.Body)
	_ = eResp.Body.Close()
	if eResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on tag execute, got %d: %s", eResp.StatusCode, string(rawExecBytes))
	}

	// /app-one/dashboard is now MISS, while /app-two/catalog and /index.html (Portal) remain HIT!
	if rec, _ := gwGet("/app-one/dashboard?api_key=TOPSECRET123", ""); rec.Header().Get("X-Cache") != "MISS" {
		t.Fatalf("expected /app-one/dashboard to be MISS after tag=app-one purge")
	}
	if rec, body := gwGet("/app-two/catalog", ""); rec.Header().Get("X-Cache") != "HIT" || body != bodyB1 {
		t.Fatalf("expected /app-two/catalog to remain HIT after tag=app-one purge")
	}
	if rec, body := gwGet("/index.html", ""); rec.Header().Get("X-Cache") != "HIT" || body != bodyP1 {
		t.Fatalf("expected portal /index.html to remain HIT after tag=app-one purge")
	}

	// ── Step 4: Verify #624 backend cutover of /app-one/ from V1 to V2 ──────
	cfgV2 := makePortalConfig(app1OriginV2.URL)
	// Verify semantic diff / preview reports CacheCutoverPlan for /app-one/!
	diff := admin.DiffConfigs(cfgV1, cfgV2)
	if diff.CacheCutover == nil || diff.CacheCutover.Disposition != "invalidate_on_publish" {
		t.Fatalf("expected DiffConfigs to report CacheCutover.Disposition=invalidate_on_publish, got %+v", diff.CacheCutover)
	}
	if len(diff.CacheCutover.InvalidatedScopes) != 1 || diff.CacheCutover.InvalidatedScopes[0] != "prefix portal.example.com/app-one/" {
		t.Fatalf("unexpected InvalidatedScopes in DiffConfigs: %+v", diff.CacheCutover.InvalidatedScopes)
	}

	// Commit (Publish) cfgV2
	resV2, _, err := factory.Build(ctx, cfgV2, true)
	if err != nil {
		t.Fatalf("Build cfgV2: %v", err)
	}
	gw = resV2[":8080"]

	// /app-one/dashboard now serves from app1-v2, while /app-two/catalog and /index.html remain warm HITs!
	recAfterCutover, bodyAfterCutover := gwGet("/app-one/dashboard?api_key=TOPSECRET123", "")
	if recAfterCutover.Header().Get("X-Cache") != "MISS" || !strings.HasPrefix(bodyAfterCutover, "app1-v2-hit-") {
		t.Fatalf("expected /app-one/dashboard to cut over to app1-v2 on Publish, got X-Cache=%q body=%q", recAfterCutover.Header().Get("X-Cache"), bodyAfterCutover)
	}
	if rec, body := gwGet("/app-two/catalog", ""); rec.Header().Get("X-Cache") != "HIT" || body != bodyB1 {
		t.Fatalf("expected /app-two/catalog to stay warm across /app-one/ backend cutover")
	}
	if rec, body := gwGet("/index.html", ""); rec.Header().Get("X-Cache") != "HIT" || body != bodyP1 {
		t.Fatalf("expected portal /index.html to stay warm across /app-one/ backend cutover")
	}
	_ = rbac.RoleAdmin
}
