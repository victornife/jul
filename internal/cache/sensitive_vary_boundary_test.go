// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

func TestSensitiveVaryPublicationBoundaries(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("Cookie", "session=fixture")
	entry := &Entry{Status: http.StatusOK, Vary: []string{"Cookie"}}
	base := key(req)

	if stored, fenced, _ := c.setCoreWithStartGen(base, entry, 0, false, nil); stored || !fenced {
		t.Fatal("direct publication accepted sensitive Vary")
	}
	if stored, _ := c.publishCapturedWithStartGen(base, entry, nil, 0, false); stored {
		t.Fatal("capture publication accepted sensitive Vary")
	}
	if c.storeCapturedWithStartGen(base, req, entry, nil, 0, false) {
		t.Fatal("Vary storage accepted sensitive Vary")
	}
	if c.captureMatchesRequest(entry, req) || validatedReusable(entry, req) {
		t.Fatal("sensitive Vary entry was accepted for reuse")
	}
	headers := http.Header{"Vary": {"Cookie"}, "Cache-Control": {"public, max-age=60"}}
	if c.buildEntryFromCapture(req, http.StatusOK, headers, []byte("body"), 4, false, time.Now(), c.Policy()) != nil {
		t.Fatal("capture builder accepted sensitive Vary")
	}
	if sensitiveVaryEntry(nil) {
		t.Fatal("nil entry must not be classified as sensitive")
	}
}

func TestSensitiveVaryLegacyInconsistentVariantIsRejected(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("Accept", "text/plain")
	base := key(req)
	variant := variantKey(base, []string{"Accept"}, req)
	c.set(base, &Entry{IsVaryStub: true, Vary: []string{"Accept"}, Variants: []string{variant}})
	c.mem.putWithGen(variant, &Entry{
		Status:     http.StatusOK,
		Body:       []byte("must-not-be-served"),
		VaryValues: map[string]string{"authorization": "fixture-marker"},
	}, 0)
	if _, effective, ok := c.lookup(base, req); ok || effective != base {
		t.Fatal("inconsistent legacy variant was accepted")
	}
}

func TestSensitiveVaryLegacyCleanupIsBounded(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20})
	base := key(httptest.NewRequest(http.MethodGet, "http://example.com/item", nil))
	stub := &Entry{IsVaryStub: true, Vary: []string{"Cookie"}}
	for i := 0; i <= maxVariantsPerResource; i++ {
		variant := fmt.Sprintf("%s\x00fixture\x1f%d", base, i)
		stub.Variants = append(stub.Variants, variant)
		c.mem.putWithGen(variant, &Entry{Status: http.StatusOK}, 0)
	}
	c.mem.putWithGen(base, stub, 0)
	c.cleanupSensitiveVary(base, stub, 0)
	if c.mem.hasKey(base) || c.mem.hasKey(stub.Variants[maxVariantsPerResource-1]) {
		t.Fatal("bounded cleanup did not remove the stub and first variants")
	}
	if !c.mem.hasKey(stub.Variants[maxVariantsPerResource]) {
		t.Fatal("cleanup exceeded its per-resource variant limit")
	}
}
