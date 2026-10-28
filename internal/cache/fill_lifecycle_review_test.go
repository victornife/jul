// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewRangeFillAdmissionPurgeOrdering(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			cfg := lockCfg(5 * time.Second)
			cfg.RangeEnabled, cfg.MaxRanges = true, 1
			if disk {
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = 8 << 20
			}
			c, _ := conformanceCache(t, cfg)
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			req.Header.Set("Range", "bytes=0-2")
			fk := fillCoordinationKey(key(req), key(req), req)
			call, leader := c.beginFill(fk)
			if !leader {
				t.Fatal("expected the first range fill to be admitted")
			}
			defer c.endInflightOriginOp(call.opID)
			defer c.endFill(fk, call)

			sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
			if err != nil || result.FencedInflightOps != 1 {
				t.Fatalf("purge did not detach the admitted range fill: result=%+v err=%v", result, err)
			}

			ready, release := make(chan struct{}), make(chan struct{})
			resultCh := make(chan *Entry, 1)
			var calls atomic.Int32
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public,max-age=60")
				if calls.Add(1) == 1 {
					close(ready)
					<-release
					_, _ = w.Write([]byte("older"))
					return
				}
				_, _ = w.Write([]byte("newer"))
			})
			go func() {
				resultCh <- c.performRangeFillWithStartGen(req, origin, key(req), c.Policy(), call.startGen, nil)
			}()
			<-ready

			plainReq := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			rr := httptest.NewRecorder()
			c.Handler(origin).ServeHTTP(rr, plainReq)
			if rr.Body.String() != "newer" {
				t.Errorf("post-purge fill returned %q", rr.Body.String())
			}
			close(release)
			if entry := <-resultCh; entry != nil {
				t.Errorf("detached range fill returned a reusable entry %q", entry.Body)
			}
			if entry, ok := c.get(key(req)); !ok || string(entry.Body) != "newer" {
				if !ok {
					t.Error("post-purge entry missing")
				} else {
					t.Errorf("detached range fill replaced the post-purge entry with %q", entry.Body)
				}
			}
			if disk {
				restarted := newTestCache(t, cfg)
				if entry, ok := restarted.get(key(req)); !ok || string(entry.Body) != "newer" {
					if !ok {
						t.Error("post-purge disk entry missing after restart")
					} else {
						t.Errorf("detached range fill survived restart with %q", entry.Body)
					}
				}
			}
		})
	}
}

func TestReviewFillAdmissionReleasesOriginOperation(t *testing.T) {
	for _, ranged := range []bool{false, true} {
		for _, outcome := range []string{"stored", "uncacheable", "canceled", "panic"} {
			t.Run(fmt.Sprintf("range=%v/outcome=%s", ranged, outcome), func(t *testing.T) {
				cfg := lockCfg(5 * time.Second)
				cfg.RangeEnabled, cfg.MaxRanges = true, 1
				c := newTestCache(t, cfg)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil).WithContext(ctx)
				if ranged {
					req.Header.Set("Range", "bytes=0-2")
				}
				origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					switch outcome {
					case "uncacheable":
						w.Header().Set("Cache-Control", "no-store")
					case "canceled":
						cancel()
					case "panic":
						panic("fixture")
					default:
						w.Header().Set("Cache-Control", "public,max-age=60")
					}
					_, _ = w.Write([]byte("body"))
				})
				func() {
					defer func() {
						recovered := recover()
						if outcome == "panic" && recovered == nil {
							t.Error("expected origin panic")
						} else if outcome != "panic" && recovered != nil {
							t.Errorf("unexpected panic: %v", recovered)
						}
					}()
					c.Handler(origin).ServeHTTP(httptest.NewRecorder(), req)
				}()
				if got := c.inflightFills(); got != 0 {
					t.Errorf("stranded fill calls: %d", got)
				}
				if got := c.IndexStats().InflightOriginOps; got != 0 {
					t.Errorf("stranded origin operations: %d", got)
				}
			})
		}
	}
}
