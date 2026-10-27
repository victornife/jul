// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"jul/internal/config"
)

func BenchmarkInspectionVariants(b *testing.B) {
	for _, variants := range []int{1, 64} {
		b.Run(fmt.Sprintf("256resources_%dvariants", variants), func(b *testing.B) {
			c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 64 << 20, DefaultTTL: config.Duration(time.Hour)}, testLogger())
			if err != nil {
				b.Fatal(err)
			}
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Vary", "X-Variant")
				_, _ = w.Write([]byte("x"))
			}))
			for i := 0; i < 256; i++ {
				for j := 0; j < variants; j++ {
					r := httptest.NewRequest("GET", fmt.Sprintf("http://example.com/%d", i), nil)
					r.Header.Set("X-Variant", fmt.Sprint(j))
					h.ServeHTTP(httptest.NewRecorder(), r)
				}
			}
			sel := inspectSelector(b, SelectorHost, "")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := c.InspectScope(context.Background(), sel); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// This measures maximum continuously admitted metadata-owner load, rather than
// a normal operator refresh rate. B/op includes background builder allocations.
func BenchmarkCacheHitWithInspectionLoad(b *testing.B) {
	for _, workers := range []int{0, 2} {
		b.Run(fmt.Sprint(workers), func(b *testing.B) {
			c, err := New(config.CacheConfig{Enabled: true, MemoryMaxSize: 64 << 20, DefaultTTL: config.Duration(time.Hour)}, testLogger())
			if err != nil {
				b.Fatal(err)
			}
			h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
			for i := 0; i < 2000; i++ {
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", fmt.Sprintf("http://example.com/%d", i), nil))
			}
			r := httptest.NewRequest("GET", "http://example.com/0", nil)
			ctx, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sel := inspectSelector(b, SelectorHost, "")
					for ctx.Err() == nil {
						_, _ = c.InspectScope(ctx, sel)
					}
				}()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.ServeHTTP(httptest.NewRecorder(), r)
			}
			b.StopTimer()
			cancel()
			wg.Wait()
		})
	}
}

func TestInspectionMaximumSampleRetentionBudget(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DecisionSamples: true})
	defer c.setDecisionSampling(false)
	// Host + path is the maximum admitted 512-byte display. Query backing
	// buffers are intentionally much larger and must not contribute to retention.
	path := "/" + strings.Repeat("a", 500)
	for i := 0; i < DecisionCapacity*2; i++ {
		r := httptest.NewRequest("GET", "http://example.com"+path+"?value="+strings.Repeat("s", 4096), nil)
		c.recordDecision(r, "fresh_hit")
	}
	buf := c.samples.Load()
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if len(buf.records) != DecisionCapacity {
		t.Fatal(len(buf.records))
	}
	// Round each cloned display string up to 64 bytes, conservatively above
	// allocator slack at these <=512-byte lengths, plus 64-byte HMAC identity.
	bound := uint64(cap(buf.records))*uint64(unsafe.Sizeof(CacheDecision{})) + uint64(unsafe.Sizeof(*buf)) + 1024
	for _, d := range buf.records {
		bound += uint64((len(d.Host)+63)/64*64 + (len(d.Path)+63)/64*64 + 64)
	}
	t.Logf("retained sample storage conservative bound=%d bytes; capacity=%d; record=%d", bound, cap(buf.records), unsafe.Sizeof(CacheDecision{}))
	if bound > 256<<10 {
		t.Fatalf("sample retention budget exceeded: %d", bound)
	}
}
