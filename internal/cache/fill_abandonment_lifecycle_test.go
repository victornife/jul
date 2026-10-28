// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewAbandonedFillRetainsOriginOperationUntilReturn(t *testing.T) {
	for _, ranged := range []bool{false, true} {
		t.Run(fmt.Sprintf("range=%v", ranged), func(t *testing.T) {
			cfg := lockCfg(5 * time.Second)
			cfg.RangeEnabled, cfg.MaxRanges = true, 1
			c := newTestCache(t, cfg)
			ready, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusOK)
				if calls.Add(1) == 1 {
					close(ready)
					<-release
				}
				_, _ = w.Write([]byte("body"))
			})
			req := httptest.NewRequest(http.MethodGet, "http://example.test/item", nil)
			if ranged {
				req.Header.Set("Range", "bytes=0-2")
			}
			go func() {
				defer close(done)
				c.Handler(origin).ServeHTTP(httptest.NewRecorder(), req)
			}()
			<-ready
			if got := c.inflightFills(); got != 0 {
				t.Errorf("abandoned fill remains joinable: %d", got)
			}
			if got := c.IndexStats().InflightOriginOps; got != 1 {
				t.Errorf("origin operations during abandoned handler = %d, want 1", got)
			}
			close(release)
			<-done
			if got := c.IndexStats().InflightOriginOps; got != 0 {
				t.Errorf("origin operations after handler return = %d, want 0", got)
			}
		})
	}
}
