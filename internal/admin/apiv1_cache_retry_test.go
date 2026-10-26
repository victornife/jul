// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
)

type cancelPurgeAfterCommit struct {
	*cache.Cache
	cancelCount int32
	calls       atomic.Int32
}

func (p *cancelPurgeAfterCommit) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	if p.calls.Add(1) <= p.cancelCount {
		opCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		commit := opts.CommitFence
		opts.CommitFence = func(gen uint64, resources []string) error {
			if err := commit(gen, resources); err != nil {
				return err
			}
			cancel()
			return nil
		}
		return p.Cache.InvalidateScope(opCtx, sel, opts)
	}
	return p.Cache.InvalidateScope(ctx, sel, opts)
}

type tagRetryScenario struct {
	disk, pressure, restart, vary, ranges, stableTag, disableTags, query bool
}

func TestV1CachePurgeTagRetryPreservesOriginalResourceGroups(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		opts tagRetryScenario
	}{
		{"memory", tagRetryScenario{}},
		{"disk", tagRetryScenario{disk: true}},
		{"fence_eviction_memory", tagRetryScenario{pressure: true}},
		{"fence_eviction_disk", tagRetryScenario{disk: true, pressure: true}},
		{"restart", tagRetryScenario{disk: true, restart: true}},
		{"restart_vary", tagRetryScenario{disk: true, restart: true, vary: true}},
		{"vary", tagRetryScenario{vary: true}},
		{"range", tagRetryScenario{ranges: true}},
		{"vary_range_disk", tagRetryScenario{disk: true, vary: true, ranges: true}},
		{"tag_policy_disabled", tagRetryScenario{disk: true, stableTag: true, disableTags: true}},
		{"restart_tag_policy_disabled", tagRetryScenario{disk: true, restart: true, stableTag: true, disableTags: true}},
		{"query_secret_restart", tagRetryScenario{disk: true, restart: true, query: true}},
		{"stable_restart_control", tagRetryScenario{disk: true, restart: true, stableTag: true}},
		{"stable_vary_control", tagRetryScenario{vary: true, stableTag: true}},
		{"stable_range_control", tagRetryScenario{ranges: true, stableTag: true}},
		{"stable_vary_range_disk_control", tagRetryScenario{disk: true, vary: true, ranges: true, stableTag: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTagRetryScenario(t, tc.opts)
		})
	}
}

func runTagRetryScenario(t *testing.T, opts tagRetryScenario) {
	t.Helper()
	cfg := config.CacheConfig{
		Enabled: true, MemoryMaxSize: config.Size(2 << 20),
		DefaultTTL: config.Duration(5 * time.Minute), TagHeaders: []string{"Cache-Tag"},
		RangeEnabled: opts.ranges,
	}
	if opts.disk {
		cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), config.Size(8<<20)
	}
	c, err := cache.New(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	getCalls, headCalls := 0, 0
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		if opts.vary {
			w.Header().Set("Vary", "Accept")
		}
		if r.Method == http.MethodGet {
			getCalls++
			tag := "A"
			if getCalls > 1 && !opts.stableTag {
				tag = "C"
			}
			w.Header().Set("Cache-Tag", tag)
			w.Header().Set("X-Origin-Version", fmt.Sprintf("get-v%d", getCalls))
			_, _ = io.WriteString(w, "get-body")
			return
		}
		headCalls++
		w.Header().Set("Cache-Tag", "B")
		w.Header().Set("X-Origin-Version", fmt.Sprintf("head-v%d", headCalls))
		w.WriteHeader(http.StatusOK)
	})
	h := c.Handler(origin)
	const secret = "PRIVATE_QUERY_RETRY_621"
	target := "http://portal.example.com/article"
	if opts.query {
		target += "?token=" + secret
	}
	serve := func(method string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, target, nil)
		req.Header.Set("Accept", "text/plain")
		if opts.ranges && method == http.MethodGet {
			req.Header.Set("Range", "bytes=0-3")
		}
		h.ServeHTTP(rr, req)
		return rr
	}
	serve(http.MethodGet)
	serve(http.MethodHead)
	if warm := serve(http.MethodHead); warm.Header().Get("X-Cache") != "HIT" {
		t.Fatal("HEAD was not cached")
	}
	const token = "tag-retry-admin-token"
	const idemKey = "idem-tag-retry-original-targets"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache: &cancelPurgeAfterCommit{Cache: c, cancelCount: 1},
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{Cache: cfg}, nil
		},
	})
	req := adminapi.CachePurgeRequest{
		Selector: adminapi.CacheSelector{Kind: "tag", Host: "portal.example.com", Tags: []string{"A"}},
	}
	initialRR := postV1JSON(t, s, "/api/v1/cache/purge", token, idemKey, req)
	if initialRR.Code != http.StatusOK {
		t.Fatalf("initial purge: HTTP %d: %s", initialRR.Code, initialRR.Body.String())
	}
	initial := decodeInto[adminapi.CachePurgeResponse](t, initialRR)
	if initial.State != "canceled" || initial.RemainingEntries < 2 {
		t.Fatalf("expected cancellation before method-group deletion: %+v", initial)
	}
	s.purgeLedger.mu.Lock()
	ids := s.purgeLedger.byID[initial.OperationID].MatchedResourceIDs
	s.purgeLedger.mu.Unlock()
	if len(ids) != 1 || len(ids[0]) != 64 {
		t.Fatalf("original resource snapshot was not captured: %v", ids)
	}
	if opts.disk {
		data, err := os.ReadFile(filepath.Join(cfg.DiskPath, diskPurgeLedgerFilename))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), secret) {
			t.Fatal("durable resource snapshot leaked a raw query value")
		}
	}
	freshGet := serve(http.MethodGet)
	if freshGet.Header().Get("X-Cache") != "MISS" || freshGet.Header().Get("X-Origin-Version") != "get-v2" {
		t.Fatalf("GET was not replaced after the cutoff: X-Cache=%q version=%q",
			freshGet.Header().Get("X-Cache"), freshGet.Header().Get("X-Origin-Version"))
	}
	if opts.ranges && (freshGet.Code != http.StatusPartialContent || freshGet.Body.String() != "get-" || freshGet.Header().Get("Content-Range") != "bytes 0-3/8") {
		t.Fatalf("range representation changed: status=%d body=%q headers=%v", freshGet.Code, freshGet.Body.String(), freshGet.Header())
	}
	if opts.disableTags {
		cfg.TagHeaders = nil
		c.PrepareCacheUpdate(cfg).Commit()
	}
	var completes []func()
	if opts.pressure {
		for i := 0; i < cache.MaxActiveFences; i++ {
			sel, err := cache.ParseSelector(cache.Selector{
				Kind: cache.SelectorExactURL, Host: "portal.example.com", URI: fmt.Sprintf("/pressure/%d", i),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, complete := c.BeginSelectorFence(sel)
			completes = append(completes, complete)
		}
		t.Cleanup(func() {
			for _, complete := range completes {
				complete()
			}
		})
		if stats := c.IndexStats(); stats.ActiveFences != cache.MaxActiveFences {
			t.Fatalf("fence-cap setup did not saturate: %+v", stats)
		}
	}
	if opts.restart {
		c, err = cache.New(cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		h = c.Handler(origin)
		s = newTestServer(t, config.AdminConfig{Token: token}, Deps{
			Cache: c,
			LoadConfig: func() (*config.Config, error) {
				return &config.Config{Cache: cfg}, nil
			},
		})
	}
	retryRR := postV1JSON(t, s, "/api/v1/cache/purge", token, idemKey, req)
	if retryRR.Code != http.StatusOK {
		t.Fatalf("retry: HTTP %d: %s", retryRR.Code, retryRR.Body.String())
	}
	retry := decodeInto[adminapi.CachePurgeResponse](t, retryRR)
	if retry.State != "succeeded" || retry.InvalidationEffective != "effective" || retry.FenceSequence != initial.FenceSequence {
		t.Fatalf("retry did not complete at the original cutoff: %+v", retry)
	}
	if opts.pressure {
		completes[0]()
	}
	after := serve(http.MethodHead)
	if after.Header().Get("X-Cache") != "MISS" || after.Header().Get("X-Origin-Version") != "head-v2" {
		t.Fatalf("successful retry exposed a pre-cutoff sibling: X-Cache=%q version=%q", after.Header().Get("X-Cache"), after.Header().Get("X-Origin-Version"))
	}
	getHit := serve(http.MethodGet)
	if getHit.Header().Get("X-Cache") != "HIT" || getHit.Header().Get("X-Origin-Version") != "get-v2" {
		t.Fatalf("retry removed a post-cutoff GET: X-Cache=%q version=%q", getHit.Header().Get("X-Cache"), getHit.Header().Get("X-Origin-Version"))
	}
	s.purgeLedger.mu.Lock()
	retained := s.purgeLedger.byID[initial.OperationID].MatchedResourceIDs
	s.purgeLedger.mu.Unlock()
	if retained != nil {
		t.Fatal("successful operation retained its retry-only resource snapshot")
	}
}

func TestV1CachePurgeRejectsConflictingRetryIdentities(t *testing.T) {
	t.Parallel()

	for _, completed := range []bool{false, true} {
		for _, newerKey := range []bool{false, true} {
			t.Run(fmt.Sprintf("completed=%t/newer_key=%t", completed, newerKey), func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: config.Size(2 << 20), DefaultTTL: config.Duration(5 * time.Minute)}
				c, err := cache.New(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=300")
					_, _ = io.WriteString(w, "warm")
				}))
				warm := func(path string) {
					h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://portal.example.com/items/"+path, nil))
				}
				p := &cancelPurgeAfterCommit{Cache: c, cancelCount: 2}
				wantState := "canceled"
				if completed {
					p.cancelCount, wantState = 0, "succeeded"
				}
				const token = "retry-identity-admin-token"
				s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
					Cache: p,
					LoadConfig: func() (*config.Config, error) {
						return &config.Config{Cache: cfg}, nil
					},
				})
				req := adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "prefix", Host: "portal.example.com", Prefix: "/items"}}
				warm("early")
				rrA := postV1JSON(t, s, "/api/v1/cache/purge", token, "idem-retry-identity-a", req)
				a := decodeInto[adminapi.CachePurgeResponse](t, rrA)
				warm("later")
				rrB := postV1JSON(t, s, "/api/v1/cache/purge", token, "idem-retry-identity-b", req)
				b := decodeInto[adminapi.CachePurgeResponse](t, rrB)
				if rrA.Code != http.StatusOK || rrB.Code != http.StatusOK || a.State != wantState || b.State != wantState || a.FenceSequence >= b.FenceSequence {
					t.Fatalf("invalid operation setup: a=%+v b=%+v", a, b)
				}
				key, retryID := "idem-retry-identity-a", b.OperationID
				if newerKey {
					key, retryID = "idem-retry-identity-b", a.OperationID
				}
				req.RetryOperationID = retryID
				rr := postV1JSON(t, s, "/api/v1/cache/purge", token, key, req)
				if rr.Code != http.StatusConflict || p.calls.Load() != 2 {
					t.Fatalf("conflicting identities reached execution or replay: HTTP %d engine calls=%d body=%s", rr.Code, p.calls.Load(), rr.Body.String())
				}
				s.purgeLedger.mu.Lock()
				afterA, afterB := s.purgeLedger.byID[a.OperationID].Response, s.purgeLedger.byID[b.OperationID].Response
				s.purgeLedger.mu.Unlock()
				if afterA.FenceSequence != a.FenceSequence || afterB.FenceSequence != b.FenceSequence || afterA.State != wantState || afterB.State != wantState {
					t.Fatalf("conflicting request mutated operation records: a=%+v b=%+v", afterA, afterB)
				}
				req.RetryOperationID = a.OperationID
				controlRR := postV1JSON(t, s, "/api/v1/cache/purge", token, "idem-retry-identity-a", req)
				if controlRR.Code != http.StatusOK {
					t.Fatalf("matching identities were rejected: HTTP %d: %s", controlRR.Code, controlRR.Body.String())
				}
				control := decodeInto[adminapi.CachePurgeResponse](t, controlRR)
				if control.OperationID != a.OperationID || control.FenceSequence != a.FenceSequence {
					t.Fatalf("matching retry changed its boundary: %+v", control)
				}
			})
		}
	}
}

func TestV1CachePurgeRejectsInvalidDurableResourceSnapshots(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		ids  []string
	}{
		{"short", []string{"not-a-resource-id"}},
		{"non_hex", []string{strings.Repeat("z", 64)}},
		{"non_canonical", []string{strings.Repeat("A", 64)}},
		{"oversized", make([]string, cache.MaxPurgeScanKeys+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			data, err := json.Marshal([]*cachePurgeRecord{{
				OperationID: "invalid-resource-snapshot", MatchedResourceIDs: tc.ids,
				Response: adminapi.CachePurgeResponse{State: "canceled", Terminal: true},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, diskPurgeLedgerFilename), data, 0o600); err != nil {
				t.Fatal(err)
			}
			eng := &countingScopedPurger{}
			const token = "invalid-snapshot-admin-token"
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
				Cache: eng,
				LoadConfig: func() (*config.Config, error) {
					return &config.Config{Cache: config.CacheConfig{Enabled: true, DiskPath: dir}}, nil
				},
			})
			rr := postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{
				Selector: adminapi.CacheSelector{Kind: "exact_url", Host: "portal.example.com", URI: "/article"},
			})
			if rr.Code != http.StatusServiceUnavailable || eng.executions.Load() != 0 {
				t.Fatalf("invalid durable snapshot was accepted: HTTP %d executions=%d", rr.Code, eng.executions.Load())
			}
		})
	}
}

func TestV1CachePurgeOperationRetryRejectsUnboundIdempotencyKey(t *testing.T) {
	t.Parallel()

	for _, keyed := range []bool{false, true} {
		for _, completed := range []bool{false, true} {
			t.Run(fmt.Sprintf("keyed=%t/completed=%t", keyed, completed), func(t *testing.T) {
				t.Parallel()
				cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: config.Size(2 << 20), DefaultTTL: config.Duration(5 * time.Minute)}
				c, err := cache.New(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=300")
					_, _ = io.WriteString(w, "warm")
				}))
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://portal.example.com/a", nil))
				p := &cancelPurgeAfterCommit{Cache: c, cancelCount: 1}
				if completed {
					p.cancelCount = 0
				}
				const token = "unbound-retry-key-admin-token"
				const freshKey = "idem-unbound-retry-key"
				s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
					Cache: p,
					LoadConfig: func() (*config.Config, error) {
						return &config.Config{Cache: cfg}, nil
					},
				})
				originalKey := ""
				if keyed {
					originalKey = "idem-original-retry-key"
				}
				req := adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "exact_url", Host: "portal.example.com", URI: "/a"}}
				initialRR := postV1JSON(t, s, "/api/v1/cache/purge", token, originalKey, req)
				if initialRR.Code != http.StatusOK {
					t.Fatalf("initial operation: HTTP %d: %s", initialRR.Code, initialRR.Body.String())
				}
				initial := decodeInto[adminapi.CachePurgeResponse](t, initialRR)
				req.RetryOperationID = initial.OperationID
				rejected := postV1JSON(t, s, "/api/v1/cache/purge", token, freshKey, req)
				if rejected.Code != http.StatusBadRequest || p.calls.Load() != 1 {
					t.Fatalf("unbound key was accepted or reached execution: HTTP %d calls=%d", rejected.Code, p.calls.Load())
				}
				s.purgeLedger.mu.Lock()
				record := s.purgeLedger.byID[initial.OperationID].Response
				s.purgeLedger.mu.Unlock()
				if record.State != initial.State || record.FenceSequence != initial.FenceSequence {
					t.Fatalf("rejected retry mutated its operation: %+v", record)
				}
				validRR := postV1JSON(t, s, "/api/v1/cache/purge", token, originalKey, req)
				if validRR.Code != http.StatusOK {
					t.Fatalf("retry with original or omitted key failed: HTTP %d: %s", validRR.Code, validRR.Body.String())
				}
				valid := decodeInto[adminapi.CachePurgeResponse](t, validRR)
				if valid.State != "succeeded" || valid.OperationID != initial.OperationID || valid.FenceSequence != initial.FenceSequence {
					t.Fatalf("valid retry changed its identity or cutoff: %+v", valid)
				}
				req.RetryOperationID, req.Selector.URI = "", "/different"
				newRR := postV1JSON(t, s, "/api/v1/cache/purge", token, freshKey, req)
				if newRR.Code != http.StatusOK {
					t.Fatalf("rejected request consumed its unused key: HTTP %d: %s", newRR.Code, newRR.Body.String())
				}
				newOp := decodeInto[adminapi.CachePurgeResponse](t, newRR)
				if newOp.OperationID == initial.OperationID {
					t.Fatal("fresh idempotency key was rebound to the retried operation")
				}
				req.Selector.URI = "/yet-another"
				conflict := postV1JSON(t, s, "/api/v1/cache/purge", token, freshKey, req)
				if conflict.Code != http.StatusConflict {
					t.Fatalf("accepted new operation did not bind its key: HTTP %d", conflict.Code)
				}
			})
		}
	}
}
