// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

func TestTransientStaleSharesImmutableBody(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 32 << 20})
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	entry := &Entry{
		Status:          http.StatusOK,
		Body:            make([]byte, 16<<20),
		HasStaleIfError: true,
		StaleIfError:    time.Minute,
		Evidence:        &publicationEvidence{},
	}
	c.set(key(req), entry)
	transient := c.transientStaleOnError(entry, key(req), c.genSeq.Load(), time.Now(), req)
	if transient == nil || &transient.Body[0] != &entry.Body[0] {
		t.Fatal("transient stale reuse copied the immutable body")
	}
	if transient.Evidence == entry.Evidence || entry.Evidence.Flags != 0 {
		t.Fatal("transient stale evidence mutated the published entry")
	}
}
