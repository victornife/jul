// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"time"
)

// mergeAction is what a 304 merge decided to do with the stored representation.
type mergeAction int

const (
	// mergeReplace publishes the refreshed clone under the same key.
	mergeReplace mergeAction = iota
	// mergeDiscard drops the stored representation: the 304 changed something
	// that makes the entry unusable where it lives.
	mergeDiscard
)

// notModifiedExcluded are header fields a 304 must never contribute to the
// stored representation.
//
// Content-Length describes the 304 itself, which carries no body; copying it
// over would make the stored entry advertise a length its body does not have.
// The hop-by-hop fields are removed separately by removeHopByHop.
var notModifiedExcluded = []string{"Content-Length", "X-Cache"}

// merge304 folds a 304 Not Modified into the stored representation and returns
// a NEW entry (RFC 9111 §4.3.4). The stored entry is never written through: it
// is published and therefore immutable (#131), so the merge clones, updates the
// clone, and leaves replacement to the caller.
//
// The stored body and status are preserved — that is the point of a 304 — while
// every end-to-end metadata field the origin sent replaces its stored
// counterpart, and the freshness, validator and policy metadata are recomputed
// from the merged result. A stale entry that comes back from a 304 as a
// no-cache or must-revalidate entry must behave that way immediately.
func (c *Cache) merge304(stored *Entry, h304 http.Header, now time.Time) (*Entry, mergeAction) {
	merged := cloneHeader(stored.Header)
	// Warning is obsolete (RFC 9111 §5.5) and a warn-code attached to the old
	// response must not survive a refresh that may have invalidated it.
	merged.Del("Warning")

	update := cloneHeader(h304)
	removeHopByHop(update)
	for _, name := range notModifiedExcluded {
		update.Del(name)
	}
	for name, values := range update {
		merged[name] = append([]string(nil), values...)
	}

	// A 304 that changes Vary changes which key this representation belongs
	// under. Publishing it at the old key would leave an entry reachable
	// through a keying rule that no longer describes it, so the safe answer is
	// to discard: the caller re-fetches and stores it under the correct key.
	if !sameFields(stored.Vary, parseList(merged.Get("Vary"))) {
		return nil, mergeDiscard
	}

	p := parseResponsePolicy(merged)
	ttl, swr, ok := c.freshness(stored.Status, merged, p, now)
	if !ok {
		// The refreshed metadata says this must no longer be stored (no-store,
		// private, an expired Expires, a Set-Cookie added by the 304).
		return nil, mergeDiscard
	}

	// Only a 304 that actually carries origin timing restarts the age clock; a
	// 304 without Date or Age leaves the cache with no evidence about when the
	// origin generated its answer, so the safe reading is "just now".
	created := now
	if h304.Get("Date") != "" || h304.Get("Age") != "" {
		created = now.Add(-initialAge(merged, now))
	}

	refreshed := stored.Clone()
	refreshed.Header = merged
	refreshed.CreatedAt = created
	refreshed.ExpiresAt = created.Add(ttl)
	refreshed.StaleUntil = created.Add(ttl + swr)
	refreshed.ETag = merged.Get("ETag")
	refreshed.LastModified = merged.Get("Last-Modified")
	refreshed.RequiresValidation = p.NoCache
	refreshed.MustRevalidate = p.revalidationRequired()
	refreshed.SharedAuthReuse = p.sharedAuthReuse()
	refreshed.StaleIfError = p.SIE
	refreshed.HasStaleIfError = p.HasSIE
	return refreshed, mergeReplace
}
