// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
)

type measuredPurge struct {
	*cache.Cache
	commit, cutoff time.Duration
}

func (p *measuredPurge) InvalidateScope(ctx context.Context, sel cache.NormalizedSelector, opts cache.InvalidateOptions) (cache.InvalidateResult, error) {
	start := time.Now()
	commit := opts.CommitFence
	opts.CommitFence = func(gen uint64, ids []string, intent cache.PurgeRecoveryIntent) error {
		t0 := time.Now()
		err := commit(gen, ids, intent) // Actual marshal + atomicfile.Write + fsync + rename.
		p.commit = time.Since(t0)
		p.cutoff = time.Since(start)
		return err
	}
	return p.Cache.InvalidateScope(ctx, sel, opts)
}

// Opt-in acceptance on declared local hardware, not a portable production SLO.
// Budgets are fixed before measurement: 2s cutoff, 30s admin operation,
// 10ms warm-hit p95 and 1s miss/publication p95 with real disk and ledger I/O.
func TestMeasure667DurableAdminBudgets(t *testing.T) {
	if os.Getenv("RUN_667_DURABLE_PROFILE") != "1" {
		t.Skip("set RUN_667_DURABLE_PROFILE=1 for real 1k/200k disk+ledger profiles")
	}
	for _, n := range []int{1000, 200000} {
		for sample := 1; sample <= 3; sample++ {
			t.Run(fmt.Sprintf("disk_%d_sample_%d", n, sample), func(t *testing.T) {
				cfg := config.CacheConfig{Enabled: true, MemoryMaxSize: 512 << 20, DiskPath: t.TempDir(), DiskMaxSize: 1 << 30, DefaultTTL: config.Duration(time.Hour), TagHeaders: []string{"Cache-Tag"}}
				c, err := cache.New(cfg, nil)
				if err != nil {
					t.Fatal(err)
				}
				h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Cache-Control", "public, max-age=3600")
					if strings.HasPrefix(r.URL.Path, "/article/") {
						if i, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/article/")); err == nil && i%10 == 0 {
							w.Header().Set("Cache-Tag", "profile")
						}
						if strings.HasSuffix(r.URL.Path, "00") {
							w.Header().Set("Vary", "Accept-Language")
						}
					}
					_, _ = w.Write([]byte("body"))
				}))
				request := func(host, uri string) {
					h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://"+host+uri, nil))
				}
				for i := 0; i < n; i++ {
					uri := "/article/" + strconv.Itoa(i)
					request("purge.example.com", uri)
					if i%20 == 0 {
						h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("HEAD", "http://purge.example.com"+uri, nil))
					}
					if strings.HasSuffix(uri, "00") {
						req := httptest.NewRequest("GET", "http://purge.example.com"+uri, nil)
						req.Header.Set("Accept-Language", "es")
						h.ServeHTTP(httptest.NewRecorder(), req)
					}
				}
				entries, err := os.ReadDir(cfg.DiskPath)
				if err != nil {
					t.Fatal(err)
				}
				diskCount := 0
				for _, e := range entries {
					if len(e.Name()) == 64 {
						diskCount++
					}
				}
				if diskCount < n {
					t.Fatalf("expected at least %d real disk entries, got %d", n, diskCount)
				}
				request("keep.example.com", "/warm")
				sel, err := cache.ParseSelector(cache.Selector{Kind: cache.SelectorHost, Host: "purge.example.com"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := c.PreviewScope(sel, cache.PreviewOptions{}); got.DiskEntries == 0 {
					t.Fatal("profile did not create real disk entries")
				}
				p := &measuredPurge{Cache: c}
				s := newTestServer(t, config.AdminConfig{}, Deps{Cache: p, LoadConfig: func() (*config.Config, error) { return &config.Config{Cache: cfg}, nil }})
				var stop atomic.Bool
				var wg sync.WaitGroup
				var hit, publication []time.Duration
				ready := make(chan struct{}, 2)
				for worker := range 2 {
					wg.Add(1)
					go func() {
						defer wg.Done()
						ready <- struct{}{}
						for i := 0; !stop.Load(); i++ {
							t0 := time.Now()
							if worker == 0 {
								request("keep.example.com", "/warm")
								hit = append(hit, time.Since(t0))
							} else {
								request("keep.example.com", "/publication/"+strconv.Itoa(i))
								publication = append(publication, time.Since(t0))
							}
							time.Sleep(100 * time.Microsecond)
						}
					}()
				}
				<-ready
				<-ready
				runtime.GC()
				start := time.Now()
				rr := postV1JSON(t, s, "/api/v1/cache/purge", "", "", adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "purge.example.com"}, ConfirmBroad: true, TimeoutMS: 30000})
				total := time.Since(start)
				stop.Store(true)
				wg.Wait()
				if rr.Code != 200 {
					t.Fatalf("purge status=%d %s", rr.Code, rr.Body.String())
				}
				result := decodeInto[adminapi.CachePurgeResponse](t, rr)
				if result.State != "succeeded" || result.InvalidatedResources != n || result.DiskEntriesRemoved != diskCount {
					t.Fatalf("unexpected profile result: %+v", result)
				}
				p95 := func(samples []time.Duration) time.Duration {
					if len(samples) == 0 {
						t.Fatal("no concurrent samples")
					}
					slices.Sort(samples)
					return samples[(len(samples)-1)*95/100]
				}
				hit95, pub95 := p95(hit), p95(publication)
				t.Logf("entries=%d sample=%d cutoff=%s ledger_commit=%s total=%s hit_p95=%s publication_p95=%s hit_samples=%d publication_samples=%d", n, sample, p.cutoff, p.commit, total, hit95, pub95, len(hit), len(publication))
				if p.cutoff > 2*time.Second || total > 30*time.Second || hit95 > 10*time.Millisecond || pub95 > time.Second {
					t.Fatal("declared local latency budget exceeded")
				}
			})
		}
	}
}
