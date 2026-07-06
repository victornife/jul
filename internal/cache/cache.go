// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jul/internal/config"
	"jul/internal/tracing"
)

// Cache is a two-tier HTTP response cache. The memory tier overflows evicted
// entries to the disk tier (when configured).
type Cache struct {
	mem  *memStore
	disk *diskStore

	defaultTTL time.Duration
	swr        time.Duration
	// sif is stale-if-error: extra grace period to keep serving stale when
	// a background revalidation encounters an upstream error.
	sif      time.Duration
	maxEntry int64

	reMu     sync.Mutex
	inflight map[string]struct{} // keys with an in-flight background revalidation
}

// cacheableStatus lists response codes safe to cache by default.
var cacheableStatus = map[int]bool{
	http.StatusOK:                   true,
	http.StatusNonAuthoritativeInfo: true,
	http.StatusMovedPermanently:     true,
	http.StatusNotFound:             true,
	http.StatusGone:                 true,
}

// New builds a Cache from config. It returns (nil, nil) when caching is
// disabled so callers can treat a nil *Cache as "no caching". The logger (may be
// nil) receives operational warnings from the disk tier, such as foreign files
// found in the cache directory or a failed disk write.
func New(cfg config.CacheConfig, logger *slog.Logger) (*Cache, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	c := &Cache{
		defaultTTL: cfg.DefaultTTL.Std(),
		swr:        cfg.StaleWhileRevalidate.Std(),
		sif:        cfg.StaleIfError.Std(),
		maxEntry:   cfg.MemoryMaxSize.Bytes(),
		inflight:   make(map[string]struct{}),
	}
	if cfg.DiskPath != "" {
		d, err := newDiskStore(cfg.DiskPath, cfg.DiskMaxSize.Bytes(), logger)
		if err != nil {
			return nil, err
		}
		c.disk = d
	}
	c.mem = newMemStore(cfg.MemoryMaxSize.Bytes(), func(key string, e *Entry) {
		if c.disk != nil {
			c.disk.set(key, e)
		}
	})
	return c, nil
}

func (c *Cache) get(key string) (*Entry, bool) {
	if e, ok := c.mem.get(key); ok {
		return e, true
	}
	if c.disk != nil {
		if e, ok := c.disk.get(key); ok {
			c.mem.set(key, e) // promote
			return e, true
		}
	}
	return nil, false
}

func (c *Cache) set(key string, e *Entry) {
	c.mem.set(key, e)
}

// Delete removes a single key from both tiers.
func (c *Cache) Delete(key string) {
	c.mem.del(key)
	if c.disk != nil {
		c.disk.del(key)
	}
}

// Purge clears both tiers.
func (c *Cache) Purge() {
	c.mem.purge()
	if c.disk != nil {
		c.disk.purge()
	}
}

// key derives the primary cache key from method, host, and request URI.
func key(r *http.Request) string {
	return r.Method + "\n" + strings.ToLower(r.Host) + "\n" + r.URL.RequestURI()
}

// lookup resolves a request to a stored entry, following the per-URL Vary stub
// when present. It returns the entry, the effective storage key it lives under
// (the base key, or a variant key for Vary responses), and whether it was found.
func (c *Cache) lookup(base string, r *http.Request) (*Entry, string, bool) {
	e, ok := c.get(base)
	if !ok {
		return nil, base, false
	}
	if e.IsVaryStub {
		vk := variantKey(base, e.Vary, r)
		e, ok = c.get(vk)
		return e, vk, ok
	}
	return e, base, true
}

// store writes an entry to the cache. A response without Vary is stored directly
// under the base key. A Vary response is stored under a per-variant key, and a
// small stub is (re)written under the base key so future lookups can find the
// right variant; this lets multiple variants of one URL coexist instead of
// overwriting one another.
func (c *Cache) store(base string, r *http.Request, e *Entry) {
	if len(e.Vary) == 0 {
		c.set(base, e)
		return
	}
	c.set(variantKey(base, e.Vary, r), e)
	c.set(base, &Entry{IsVaryStub: true, Vary: append([]string(nil), e.Vary...)})
}

// variantKey derives a per-variant storage key from the base key and the
// request's values for the response's Vary header fields. Field names are
// lowercased and sorted so the key is independent of header order or case, and
// values are separated by control bytes that cannot appear in header values, so
// distinct variants never collide.
func variantKey(base string, vary []string, r *http.Request) string {
	fields := make([]string, len(vary))
	for i, f := range vary {
		fields[i] = strings.ToLower(strings.TrimSpace(f))
	}
	sort.Strings(fields)
	var b strings.Builder
	b.WriteString(base)
	for _, f := range fields {
		b.WriteByte(0x00)
		b.WriteString(f)
		b.WriteByte(0x1f)
		b.WriteString(r.Header.Get(f))
	}
	return b.String()
}

// Handler wraps next with cache lookup/store behavior. Only GET/HEAD responses
// that are cacheable per HTTP semantics are stored.
func (c *Cache) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		// Span over the lookup/decision only; the origin fetch on a miss keeps
		// its own (proxy) span. No-op unless the otel build wired a tracer.
		_, span := tracing.Active().Start(r.Context(), "cache.lookup")

		if requestNoStore(r) {
			span.SetString("cache.status", "BYPASS")
			span.End()
			w.Header().Set("X-Cache", "BYPASS")
			next.ServeHTTP(w, r)
			return
		}

		k := key(r)
		now := time.Now()
		if e, effKey, ok := c.lookup(k, r); ok && e.matchesVary(r) {
			if e.Fresh(now) {
				span.SetString("cache.status", "HIT")
				span.End()
				c.serve(w, r, e, "HIT", now)
				return
			}
			if e.ServableStale(now) {
				span.SetString("cache.status", "STALE")
				span.End()
				c.serve(w, r, e, "STALE", now)
				// Background refresh, deduplicated per variant so a burst of
				// stale hits triggers exactly one upstream revalidation.
				if c.beginRevalidate(effKey) {
					go func() {
						defer c.endRevalidate(effKey)
						c.revalidate(effKey, r, next, e)
					}()
				}
				return
			}
		}
		span.SetString("cache.status", "MISS")
		span.End()
		c.fetchAndStore(w, r, next, now)
	})
}

// fetchAndStore runs the upstream handler, streams the response to the client,
// and stores it if cacheable.
func (c *Cache) fetchAndStore(w http.ResponseWriter, r *http.Request, next http.Handler, now time.Time) {
	cw := &cacheWriter{ResponseWriter: w, limit: c.maxEntry}
	w.Header().Set("X-Cache", "MISS")
	next.ServeHTTP(cw, r)

	if cw.tooBig {
		return
	}
	if e := c.buildEntry(r, cw.status, w.Header(), cw.buf.Bytes(), now); e != nil {
		c.store(key(r), r, e)
	}
}

// beginRevalidate marks key as having an in-flight background revalidation,
// returning false if one is already running so callers skip launching a
// duplicate. This bounds a burst of concurrent stale hits to one upstream fetch.
func (c *Cache) beginRevalidate(key string) bool {
	c.reMu.Lock()
	defer c.reMu.Unlock()
	if _, ok := c.inflight[key]; ok {
		return false
	}
	c.inflight[key] = struct{}{}
	return true
}

// endRevalidate clears the in-flight marker for key.
func (c *Cache) endRevalidate(key string) {
	c.reMu.Lock()
	delete(c.inflight, key)
	c.reMu.Unlock()
}

// revalidate refreshes a stale entry in the background using a conditional
// request when possible.
func (c *Cache) revalidate(effKey string, orig *http.Request, next http.Handler, stale *Entry) {
	r := orig.Clone(context.Background())
	r.Body = http.NoBody
	if stale.ETag != "" {
		r.Header.Set("If-None-Match", stale.ETag)
	}
	if stale.LastModified != "" {
		r.Header.Set("If-Modified-Since", stale.LastModified)
	}

	rec := &recorder{header: http.Header{}, limit: c.maxEntry}
	next.ServeHTTP(rec, r)
	now := time.Now()

	if rec.status == http.StatusNotModified {
		// Upstream confirms freshness: extend the stored entry's lifetime under
		// the same (variant) key it already lives at.
		if ttl, swr, ok := c.freshness(stale.Status, stale.Header, now); ok {
			refreshed := *stale
			refreshed.CreatedAt = now
			refreshed.ExpiresAt = now.Add(ttl)
			refreshed.StaleUntil = now.Add(ttl + swr)
			c.set(effKey, &refreshed)
		}
		return
	}
	if rec.status >= 500 {
		// Upstream error during revalidation: extend stale-if-error window
		// so the stale entry remains servable while the backend recovers.
		if c.sif > 0 {
			stale.StaleUntil = now.Add(c.sif)
			c.set(effKey, stale)
		}
		return
	}
	if rec.tooBig {
		return
	}
	if e := c.buildEntry(orig, rec.status, rec.header, rec.body.Bytes(), now); e != nil {
		c.store(key(orig), orig, e)
	}
}

// buildEntry constructs a cacheable Entry, or nil if the response must not be
// stored.
func (c *Cache) buildEntry(r *http.Request, status int, h http.Header, body []byte, now time.Time) *Entry {
	ttl, swr, ok := c.freshness(status, h, now)
	if !ok {
		return nil
	}
	// Responses to authorized requests are private unless explicitly public.
	if r.Header.Get("Authorization") != "" && !ccHasDirective(h.Get("Cache-Control"), "public") {
		return nil
	}

	stored := cloneHeader(h)
	removeHopByHop(stored)
	stored.Del("X-Cache")

	e := &Entry{
		Status:       status,
		Header:       stored,
		Body:         append([]byte(nil), body...),
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
		StaleUntil:   now.Add(ttl + swr),
		ETag:         h.Get("ETag"),
		LastModified: h.Get("Last-Modified"),
	}
	if vary := parseList(h.Get("Vary")); len(vary) > 0 {
		for _, name := range vary {
			if name == "*" {
				// Vary: * means the response is not reusable for any other
				// request; do not store it.
				return nil
			}
		}
		e.Vary = vary
		e.VaryValues = make(map[string]string, len(vary))
		for _, name := range vary {
			e.VaryValues[name] = r.Header.Get(name)
		}
	}
	return e
}

// serve writes a cached entry to the client, honoring conditional requests.
func (c *Cache) serve(w http.ResponseWriter, r *http.Request, e *Entry, state string, now time.Time) {
	h := w.Header()
	for k, vs := range e.Header {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	age := int(now.Sub(e.CreatedAt).Seconds())
	if age < 0 {
		age = 0
	}
	h.Set("Age", strconv.Itoa(age))
	h.Set("X-Cache", state)

	if notModified(r, e) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.Body)
	}
}

// freshness decides whether a response is cacheable and for how long.
func (c *Cache) freshness(status int, h http.Header, now time.Time) (ttl, swr time.Duration, ok bool) {
	if !cacheableStatus[status] {
		return 0, 0, false
	}
	cc := parseCacheControl(h.Get("Cache-Control"))
	if _, no := cc["no-store"]; no {
		return 0, 0, false
	}
	if _, priv := cc["private"]; priv {
		return 0, 0, false
	}
	if h.Get("Set-Cookie") != "" {
		return 0, 0, false
	}

	ttl = c.defaultTTL
	if v, set := cc["s-maxage"]; set {
		ttl = secs(v)
	} else if v, set := cc["max-age"]; set {
		ttl = secs(v)
	} else if exp := h.Get("Expires"); exp != "" {
		if t, err := http.ParseTime(exp); err == nil {
			ttl = t.Sub(now)
		}
	}
	if ttl <= 0 {
		return 0, 0, false
	}

	swr = c.swr
	if v, set := cc["stale-while-revalidate"]; set {
		swr = secs(v)
	}
	return ttl, swr, true
}
