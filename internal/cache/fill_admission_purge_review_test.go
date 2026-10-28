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

	"jul/internal/config"
)

func TestReviewPurgeBetweenFillAdmissionAndOriginStart(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			cfg := config.CacheConfig{
				MemoryMaxSize: 1 << 20,
				Lock:          true,
				LockTimeout:   config.Duration(30 * time.Second),
			}
			if disk {
				cfg.DiskPath = t.TempDir()
				cfg.DiskMaxSize = 8 << 20
			}
			c, _ := conformanceCache(t, cfg)
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			fk := fillCoordinationKey(key(req), key(req), req)
			call, leader := c.beginFill(fk)
			if !leader {
				t.Fatal("expected the first fill to be admitted")
			}
			defer c.endInflightOriginOp(call.opID)

			sel, err := ParseSelector(Selector{Kind: SelectorExactURL, Host: "example.test", URI: "/item"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := c.InvalidateScope(context.Background(), sel, InvalidateOptions{})
			if err != nil || result.FencedInflightOps != 1 {
				t.Fatalf("purge did not detach the admitted fill: result=%+v err=%v", result, err)
			}

			olderReady, olderRelease, olderDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "public,max-age=60")
				if calls.Add(1) == 1 {
					close(olderReady)
					<-olderRelease
					_, _ = w.Write([]byte("older"))
					return
				}
				_, _ = w.Write([]byte("newer"))
			})
			go func() {
				defer close(olderDone)
				c.leadFill(httptest.NewRecorder(), req, origin, fk, call, c.Policy())
			}()
			<-olderReady

			rr := httptest.NewRecorder()
			c.Handler(origin).ServeHTTP(rr, req)
			if rr.Body.String() != "newer" {
				t.Errorf("post-purge fill returned %q", rr.Body.String())
			}
			close(olderRelease)
			<-olderDone

			if entry, ok := c.get(key(req)); !ok || string(entry.Body) != "newer" {
				if !ok {
					t.Error("post-purge entry missing")
				} else {
					t.Errorf("detached pre-purge fill overwrote the post-purge entry with %q", entry.Body)
				}
			}
			if disk {
				restarted := newTestCache(t, cfg)
				if entry, ok := restarted.get(key(req)); !ok || string(entry.Body) != "newer" {
					if !ok {
						t.Error("post-purge disk entry missing after restart")
					} else {
						t.Errorf("detached pre-purge fill survived restart with %q", entry.Body)
					}
				}
			}
		})
	}
}
