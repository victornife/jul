// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

func TestUnrelatedTagPurgeDoesNotSuppressRetirement(t *testing.T) {
	for _, mode := range []string{"purge", "failed-commit", "begin-fence"} {
		for _, status := range []int{http.StatusForbidden, http.StatusNotModified} {
			for _, disk := range []bool{false, true} {
				t.Run(mode+"/"+strconv.Itoa(status)+"/disk="+strconv.FormatBool(disk), func(t *testing.T) {
					cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
					if disk {
						cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), 8<<20
					}
					c, clock := conformanceCache(t, cfg)
					group := testLease(t, 66)
					started, release := make(chan struct{}), make(chan struct{})
					h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
						w.Header().Set("ETag", `"fixture"`)
						if r.URL.Path == "/tagged" {
							w.Header().Set("Cache-Tag", "target")
						}
						if r.Header.Get("If-None-Match") != "" {
							close(started)
							<-release
							if status == http.StatusNotModified {
								w.Header().Set("Cache-Control", "no-store")
							}
							w.WriteHeader(status)
							return
						}
						_, _ = w.Write([]byte(r.URL.Path))
					}))
					tagged := leased(httptest.NewRequest(http.MethodGet, "http://example.test/tagged", nil), group)
					untagged := leased(httptest.NewRequest(http.MethodGet, "http://example.test/untagged", nil), group)
					h.ServeHTTP(httptest.NewRecorder(), tagged)
					h.ServeHTTP(httptest.NewRecorder(), untagged)
					clock.advance(2 * time.Second)
					h.ServeHTTP(httptest.NewRecorder(), untagged)
					<-started
					sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"target"}}, nil)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "begin-fence" {
						_, complete, err := c.BeginSelectorFenceWithError(sel)
						if err != nil {
							t.Fatal(err)
						}
						defer complete()
					} else {
						var opts InvalidateOptions
						if mode == "failed-commit" {
							opts.CommitFence = func(uint64, []string) error { return errors.New("fixture") }
						}
						_, err = c.InvalidateScope(context.Background(), sel, opts)
						if (mode == "failed-commit" && !errors.Is(err, ErrFenceCommit)) || (mode == "purge" && err != nil) {
							t.Fatalf("purge error = %v", err)
						}
					}
					close(release)
					waitLeaseReleased(t, group)
					if e, ok := c.get(key(untagged)); ok {
						t.Fatalf("untagged denial did not retire %q", e.Body)
					}
					if disk {
						restarted := newTestCache(t, cfg)
						if e, ok := restarted.get(key(untagged)); ok {
							t.Fatalf("untagged denial survived restart: %q", e.Body)
						}
					}
				})
			}
		}
	}
}

func TestTagSnapshotFencesEveryVariantValidator(t *testing.T) {
	for _, mode := range []string{"purge", "failed-commit", "begin-fence"} {
		for _, disk := range []bool{false, true} {
			t.Run(mode+"/disk="+strconv.FormatBool(disk), func(t *testing.T) {
				cfg := config.CacheConfig{MemoryMaxSize: 1 << 20, TagHeaders: []string{"Cache-Tag"}}
				if disk {
					cfg.DiskPath, cfg.DiskMaxSize = t.TempDir(), 8<<20
				}
				c, clock := conformanceCache(t, cfg)
				group := testLease(t, 67)
				enStarted, frStarted := make(chan struct{}), make(chan struct{})
				enRelease, frRelease := make(chan struct{}), make(chan struct{})
				var afterFence atomic.Bool
				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					language := r.Header.Get("Accept-Language")
					w.Header().Set("Cache-Control", "public, max-age=1, stale-while-revalidate=60")
					w.Header().Set("ETag", `"fixture"`)
					w.Header().Set("Vary", "Accept-Language")
					if language == "en" {
						w.Header().Set("Cache-Tag", "target")
					} else {
						w.Header().Set("Cache-Tag", "other")
					}
					if r.Header.Get("If-None-Match") != "" {
						if language == "en" {
							close(enStarted)
							<-enRelease
						} else {
							close(frStarted)
							<-frRelease
						}
						w.WriteHeader(http.StatusForbidden)
						return
					}
					if afterFence.Load() {
						_, _ = w.Write([]byte("after-fence"))
					} else {
						_, _ = w.Write([]byte(language))
					}
				})
				h := c.Handler(origin)
				request := func(language string) *http.Request {
					r := leased(httptest.NewRequest(http.MethodGet, "http://example.test/item", nil), group)
					r.Header.Set("Accept-Language", language)
					return r
				}
				en, fr := request("en"), request("fr")
				h.ServeHTTP(httptest.NewRecorder(), en)
				h.ServeHTTP(httptest.NewRecorder(), fr)
				stub, ok := c.get(key(en))
				if !ok {
					t.Fatal("variant stub missing")
				}
				enKey, frKey := variantKey(key(en), stub.Vary, en), variantKey(key(fr), stub.Vary, fr)
				clock.advance(2 * time.Second)
				h.ServeHTTP(httptest.NewRecorder(), en)
				h.ServeHTTP(httptest.NewRecorder(), fr)
				<-enStarted
				<-frStarted
				c.delOne(enKey)
				sel, err := ParseSelector(Selector{Kind: SelectorTag, Host: "example.test", Tags: []string{"target"}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "begin-fence" {
					_, complete, err := c.BeginSelectorFenceWithError(sel)
					if err != nil {
						t.Fatal(err)
					}
					defer complete()
				} else {
					var opts InvalidateOptions
					if mode == "failed-commit" {
						opts.CommitFence = func(uint64, []string) error { return errors.New("fixture") }
					}
					_, err = c.InvalidateScope(context.Background(), sel, opts)
					if (mode == "failed-commit" && !errors.Is(err, ErrFenceCommit)) || (mode == "purge" && err != nil) {
						t.Fatalf("purge error = %v", err)
					}
				}
				afterFence.Store(true)
				c.fetchAndStore(httptest.NewRecorder(), fr, origin, c.clock())
				close(frRelease)
				close(enRelease)
				waitLeaseReleased(t, group)
				entry, ok := c.get(frKey)
				if !ok || string(entry.Body) != "after-fence" {
					t.Fatalf("old variant validator removed post-fence entry: found=%v", ok)
				}
				if disk {
					restarted := newTestCache(t, cfg)
					if entry, ok := restarted.get(frKey); !ok || string(entry.Body) != "after-fence" {
						t.Fatal("post-fence variant did not survive restart")
					}
				}
			})
		}
	}
}
