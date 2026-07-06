// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package cache implements a two-tier (in-memory + disk overflow) HTTP response
// cache with size-bounded LRU eviction, Vary handling, conditional
// revalidation, and stale-while-revalidate support.
package cache

import (
	"net/http"
	"time"
)

// Entry is a cached HTTP response together with the freshness metadata needed
// to serve, revalidate, and key it.
type Entry struct {
	Status int
	Header http.Header
	Body   []byte

	CreatedAt  time.Time
	ExpiresAt  time.Time // end of freshness
	StaleUntil time.Time // end of stale-while-revalidate grace window

	ETag         string
	LastModified string

	// Vary records the response Vary header names and the request header
	// values they were stored against, so a differing request is a miss.
	Vary       []string
	VaryValues map[string]string

	// IsVaryStub marks a pointer entry stored under a URL's base key for a
	// response that carries a Vary header. It holds only the Vary field names so
	// a lookup can compute the per-variant key (base key plus the request's
	// values for those fields) where the real response is stored. A stub is
	// never served — it only redirects the lookup to the correct variant.
	IsVaryStub bool
}

// Size estimates the entry's memory footprint in bytes.
func (e *Entry) Size() int64 {
	n := int64(len(e.Body)) + 256 // body + fixed metadata overhead
	for k, vs := range e.Header {
		n += int64(len(k))
		for _, v := range vs {
			n += int64(len(v))
		}
	}
	return n
}

// Fresh reports whether the entry is within its freshness lifetime.
func (e *Entry) Fresh(now time.Time) bool {
	return now.Before(e.ExpiresAt)
}

// ServableStale reports whether the entry is expired but still within the
// stale-while-revalidate grace window.
func (e *Entry) ServableStale(now time.Time) bool {
	return !e.Fresh(now) && now.Before(e.StaleUntil)
}

// matchesVary reports whether req's varied header values match those the entry
// was stored against.
func (e *Entry) matchesVary(req *http.Request) bool {
	for _, name := range e.Vary {
		if name == "*" {
			return false // Vary: * is never reusable
		}
		if req.Header.Get(name) != e.VaryValues[name] {
			return false
		}
	}
	return true
}

// store is the storage tier interface implemented by the memory and disk tiers.
type store interface {
	get(key string) (*Entry, bool)
	set(key string, e *Entry)
	del(key string)
	purge()
}
