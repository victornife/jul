// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package cache implements a two-tier (in-memory + disk overflow) HTTP response
// cache with size-bounded LRU eviction, Vary handling, conditional
// revalidation, and stale-while-revalidate support.
package cache

import (
	"maps"
	"net/http"
	"time"
)

// Entry is a cached HTTP response together with the freshness metadata needed
// to serve, revalidate, and key it.
//
// A published entry is IMMUTABLE. Once store or set hands an *Entry to a tier,
// no field of it — including the Header map, the Body slice, Vary and
// VaryValues — may be written again, because concurrent readers hold the same
// pointer and the disk tier may be encoding it. Code that needs to change
// timing or metadata clones the entry, mutates the clone, and replaces the
// stored pointer under the tier's own lock. Clone is the only supported way to
// produce a mutable copy.
type Entry struct {
	Status int
	Header http.Header
	Body   []byte

	CreatedAt  time.Time
	ExpiresAt  time.Time // end of freshness
	StaleUntil time.Time // end of stale-while-revalidate grace window

	ETag         string
	LastModified string

	// RequiresValidation records a response Cache-Control: no-cache. Such a
	// response MAY be stored, but every reuse needs a successful validation
	// first (RFC 9111 §5.2.2.4), however fresh the entry still looks.
	RequiresValidation bool
	// MustRevalidate records must-revalidate or proxy-revalidate. It forbids
	// serving the entry once stale without contacting the origin, and it
	// outranks both the global stale_while_revalidate/stale_if_error settings
	// and an explicit response stale-if-error.
	MustRevalidate bool
	// SharedAuthReuse records that the origin explicitly permitted a shared
	// cache to satisfy a request carrying Authorization from this response
	// (public, s-maxage or must-revalidate; RFC 9111 §3.5). Its zero value is
	// the safe one, so an entry written by an older Jul — which had no such
	// field — can never satisfy an authenticated request.
	SharedAuthReuse bool
	// StaleIfError is the origin's explicit stale-if-error window, and
	// HasStaleIfError distinguishes "not sent" from "sent as zero". An explicit
	// window replaces the global [cache] stale_if_error for this entry.
	StaleIfError    time.Duration
	HasStaleIfError bool

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
	// Variants is the stub's membership record: every variant storage key this
	// base resource owns. It is what makes unsafe-method invalidation complete —
	// deleting the stub alone would leave the hashed variant entries in the
	// store, reachable again as soon as a new stub with the same Vary appeared.
	//
	// Membership is authoritative on lookup: a variant key that is not listed
	// here is a miss even if the store still holds an entry under it. A stub
	// written by an older Jul has no Variants, so it fails closed — a one-time
	// miss per Vary URL after an upgrade, never a stale or orphaned hit.
	Variants []string
}

// maxVariantsPerResource caps one base resource's membership list. A pathological
// Vary (say, Vary: User-Agent) would otherwise grow the stub without bound,
// because the stub is a single entry the LRU sizes as one small object. Past the
// cap the oldest variants are dropped from the store together with their
// membership, so the list can never become a leak or an unbounded delete loop.
const maxVariantsPerResource = 64

// Clone returns a deep-enough copy of e for mutation: every reference field a
// reader could observe (headers and their value slices, body, Vary, VaryValues)
// is copied, so writing to the clone cannot be seen through the published
// pointer. It is called only at publication and update boundaries — never per
// cache hit — so the body copy is paid once per revalidation, not once per
// request.
func (e *Entry) Clone() *Entry {
	if e == nil {
		return nil
	}
	cp := *e
	cp.Header = cloneHeader(e.Header)
	if e.Body != nil {
		cp.Body = append([]byte(nil), e.Body...)
	}
	if e.Vary != nil {
		cp.Vary = append([]string(nil), e.Vary...)
	}
	if e.VaryValues != nil {
		cp.VaryValues = maps.Clone(e.VaryValues)
	}
	if e.Variants != nil {
		cp.Variants = append([]string(nil), e.Variants...)
	}
	return &cp
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

// ownsVariant reports whether vk is a variant this stub is responsible for.
// A stub with no membership record is a pre-#132 entry and owns nothing, so it
// fails closed rather than exposing a variant no longer accounted for.
func (e *Entry) ownsVariant(vk string) bool {
	for _, v := range e.Variants {
		if v == vk {
			return true
		}
	}
	return false
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
