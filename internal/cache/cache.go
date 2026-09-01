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
	"sync/atomic"
	"time"

	"jul/internal/background"
	"jul/internal/config"
	"jul/internal/respwriter"
	"jul/internal/tracing"
)

// Cache is a two-tier HTTP response cache. The memory tier overflows evicted
// entries to the disk tier (when configured).
//
// Entries published to either tier are immutable; see Entry. Cache instances
// outlive handler generations: the composition root creates one Cache per
// process and every reload wraps the new handler tree with it, so cached data
// survives an ordinary configuration reload.
type Cache struct {
	mem  *memStore
	disk *diskStore

	// policy is the atomically-swappable scalar snapshot (#92): default TTL,
	// stale-while-revalidate, stale-if-error, and the per-entry capture limit.
	// Every request-path read loads exactly one snapshot via Policy() so a
	// single cache decision never mixes values from two configurations.
	policy atomic.Pointer[CachePolicy]
	// diskEvictionFailures counts disk files a capacity reduction failed to
	// remove (#92); see DiskEvictionFailures.
	diskEvictionFailures atomic.Int64

	log *slog.Logger

	reMu  sync.Mutex
	calls map[revalidateKey]*revalidateCall // in-flight revalidations

	// varyMu serializes the read-modify-write of a base resource's variant
	// membership stub. Publishing a variant and recording it in the stub must be
	// one step, or two concurrent variant writes lose one another's membership
	// and leave an entry that invalidation can no longer reach.
	varyMu sync.Mutex

	// observe, when set, receives one bounded outcome per revalidation
	// decision. It is installed by the composition root so the cache does not
	// depend on the observability package.
	observe func(outcome string)

	// now is the clock seam. Production leaves it nil and reads time.Now;
	// tests install a deterministic clock so freshness, Age and stale-window
	// assertions never depend on wall-clock timing.
	now func() time.Time
}

func (c *Cache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// The X-Cache result values. The set is closed and small on purpose: it is
// reported in a response header AND used as the `state` label of
// jul_cache_events_total, so an unbounded or request-derived value would be a
// cardinality defect. Every value describes what actually happened to THIS
// request, not what the cache would like to have done.
const (
	// stateHit means the response came from a fresh stored entry.
	stateHit = "HIT"
	// stateMiss means the response came from the origin.
	stateMiss = "MISS"
	// stateStale means a stored entry was served past its freshness lifetime,
	// under stale-while-revalidate or stale-if-error.
	stateStale = "STALE"
	// stateRevalidated means the origin confirmed the stored entry during this
	// request and the confirmed copy was served.
	stateRevalidated = "REVALIDATED"
	// stateBypass means the cache was not consulted at all.
	stateBypass = "BYPASS"
)

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
	if logger == nil {
		logger = slog.Default()
	}
	c := &Cache{
		log:   logger,
		calls: make(map[revalidateKey]*revalidateCall),
	}
	pol := policyFromConfig(cfg)
	c.policy.Store(&pol)
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

// SetRevalidationObserver installs a bounded counter for background
// revalidation outcomes. The values passed to fn are the package's own outcome
// constants — never a cache key, URL, host, or error string — so they are safe
// as a metric label. Call it once at startup, before the cache serves traffic.
func (c *Cache) SetRevalidationObserver(fn func(outcome string)) {
	if c == nil {
		return
	}
	c.observe = fn
}

func (c *Cache) observeRevalidation(outcome revalidateOutcome) {
	if c.observe != nil {
		c.observe(string(outcome))
	}
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

// Delete removes a single key from both tiers. When the key holds a Vary stub,
// every variant the stub owns is removed with it: deleting the stub alone would
// strand the hashed variant entries in the store, where a later stub with the
// same Vary could make them reachable again.
func (c *Cache) Delete(key string) {
	c.varyMu.Lock()
	defer c.varyMu.Unlock()
	c.deleteLocked(key)
}

func (c *Cache) deleteLocked(key string) {
	if e, ok := c.get(key); ok && e.IsVaryStub {
		for _, vk := range e.Variants {
			c.delOne(vk)
		}
	}
	c.delOne(key)
}

func (c *Cache) delOne(key string) {
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
//
// It never includes a credential, a cookie or any other request header: the key
// space is exactly (method, host, target), and reuse restrictions are enforced
// by the stored entry's policy instead. Deriving a key from Authorization would
// turn a leak into a silent per-credential cache.
func key(r *http.Request) string {
	return keyFor(r.Method, r.Host, r.URL.RequestURI())
}

func keyFor(method, host, requestURI string) string {
	return method + "\n" + strings.ToLower(host) + "\n" + requestURI
}

// lookup resolves a request to a stored entry, following the per-URL Vary stub
// when present. It returns the entry, the effective storage key it lives under
// (the base key, or a variant key for Vary responses), and whether it was found.
//
// The stub's membership list is authoritative: a variant key the stub does not
// claim is a miss even when the store still holds something under it. That is
// what stops an invalidated variant from becoming reachable again, and what
// makes a pre-#132 stub (which records no membership) fail closed.
func (c *Cache) lookup(base string, r *http.Request) (*Entry, string, bool) {
	e, ok := c.get(base)
	if !ok {
		return nil, base, false
	}
	if e.IsVaryStub {
		vk := variantKey(base, e.Vary, r)
		if !e.ownsVariant(vk) {
			return nil, vk, false
		}
		e, ok = c.get(vk)
		return e, vk, ok
	}
	return e, base, true
}

// store writes an entry to the cache. A response without Vary is stored directly
// under the base key. A Vary response is stored under a per-variant key, and the
// stub under the base key records both the varied field names and every variant
// key the resource owns, so a later invalidation can remove all of them.
func (c *Cache) store(base string, r *http.Request, e *Entry) {
	if len(e.Vary) == 0 {
		c.set(base, e)
		return
	}
	vk := variantKey(base, e.Vary, r)

	c.varyMu.Lock()
	defer c.varyMu.Unlock()

	stub := c.stubFor(base, e.Vary)
	// The variant is published before the stub that points at it, so a lookup
	// following a freshly listed key always finds an entry there.
	c.set(vk, e)
	if evicted := stub.addVariant(vk); evicted != "" {
		c.delOne(evicted)
	}
	c.set(base, stub)
}

// stubFor returns the membership stub to publish for base. An existing stub with
// the same Vary field set is extended; a stub for a different Vary — the origin
// changed what it varies on — is replaced, and the variants it owned are deleted
// rather than left behind under keys nothing will ever look up again.
func (c *Cache) stubFor(base string, vary []string) *Entry {
	if old, ok := c.get(base); ok && old.IsVaryStub {
		if sameFields(old.Vary, vary) {
			return old.Clone()
		}
		for _, vk := range old.Variants {
			c.delOne(vk)
		}
	}
	return &Entry{IsVaryStub: true, Vary: append([]string(nil), vary...)}
}

// addVariant records vk in the stub, returning a variant key that must be
// deleted because the membership cap was reached. Membership is bounded so a
// pathological Vary cannot grow one entry without limit.
func (e *Entry) addVariant(vk string) (evicted string) {
	if e.ownsVariant(vk) {
		return ""
	}
	e.Variants = append(e.Variants, vk)
	if len(e.Variants) > maxVariantsPerResource {
		evicted = e.Variants[0]
		e.Variants = e.Variants[1:]
	}
	return evicted
}

// sameFields reports whether two Vary field lists name the same headers,
// ignoring order and case.
func sameFields(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	na := normalizeFields(a)
	nb := normalizeFields(b)
	for i := range na {
		if na[i] != nb[i] {
			return false
		}
	}
	return true
}

func normalizeFields(f []string) []string {
	out := make([]string, len(f))
	for i, v := range f {
		out[i] = strings.ToLower(strings.TrimSpace(v))
	}
	sort.Strings(out)
	return out
}

// variantKey derives a per-variant storage key from the base key and the
// request's values for the response's Vary header fields. Field names are
// lowercased and sorted so the key is independent of header order or case, and
// values are separated by control bytes that cannot appear in header values, so
// distinct variants never collide.
func variantKey(base string, vary []string, r *http.Request) string {
	fields := normalizeFields(vary)
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
// that are cacheable per HTTP semantics are stored; an unsafe method is
// forwarded and, when it succeeds, invalidates what it changed.
func (c *Cache) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			c.serveUnsafe(w, r, next)
			return
		}

		// Span over the lookup/decision only; the origin fetch on a miss keeps
		// its own (proxy) span. No-op unless the otel build wired a tracer.
		_, span := tracing.Active().Start(r.Context(), "cache.lookup")

		// A protocol upgrade leaves HTTP behind: the handler takes the connection
		// and speaks another protocol on it. There is no representation to look up
		// or store, and the handler must receive the untouched writer so it can
		// hijack. Only the cache is bypassed — authentication, rate limiting and
		// the WAF wrap outside this handler and still run.
		if isUpgradeRequest(r) {
			c.bypass(w, r, next, span)
			return
		}

		// Decision D05 (#107/#132): a request for a byte range bypasses the
		// cache entirely rather than being answered from a complete stored
		// representation. It runs BEFORE lookup so no cached full response can
		// substitute for the 206/416 the origin would produce, and before any
		// storage decision so a 206 never reaches the store.
		if isRangeRequest(r) {
			c.bypass(w, r, next, span)
			return
		}

		reqPolicy := parseRequestPolicy(r)
		if reqPolicy.NoStore {
			// Request no-store opts this exchange out of the cache. It is not a
			// purge: an entry another client stored stays exactly as it was.
			c.bypass(w, r, next, span)
			return
		}

		k := key(r)
		now := c.clock()
		if e, effKey, ok := c.lookup(k, r); ok && e.matchesVary(r) {
			switch reuseDecision(e, r, reqPolicy, now) {
			case reuseHit:
				span.SetString("cache.status", "HIT")
				span.End()
				c.serve(w, r, e, stateHit, now)
				return
			case reuseStale:
				span.SetString("cache.status", "STALE")
				span.End()
				c.serve(w, r, e, stateStale, now)
				// Start the background refresh before returning, so the lease
				// is taken while this request still holds its generation.
				c.startRevalidate(effKey, r, next, e)
				return
			case reuseValidate:
				span.SetString("cache.status", "VALIDATE")
				span.End()
				c.validateAndServe(w, r, next, e, effKey, now)
				return
			}
		}
		span.SetString("cache.status", "MISS")
		span.End()
		c.fetchAndStore(w, r, next, now)
	})
}

// bypass forwards the request with the untouched writer and stores nothing.
func (c *Cache) bypass(w http.ResponseWriter, r *http.Request, next http.Handler, span tracing.Span) {
	span.SetString("cache.status", stateBypass)
	span.End()
	w.Header().Set("X-Cache", stateBypass)
	next.ServeHTTP(w, r)
}

// reuseKind is the decision taken for a request that matched a stored entry.
type reuseKind int

const (
	// reuseNone means the entry cannot answer this request at all; fetch fresh.
	reuseNone reuseKind = iota
	// reuseHit means the entry is fresh and reusable as it stands.
	reuseHit
	// reuseStale means the entry may be served stale while it refreshes.
	reuseStale
	// reuseValidate means the entry may only be reused after a successful
	// synchronous validation against the origin.
	reuseValidate
)

// reuseDecision is the single gate every reuse passes through. Concentrating the
// rules here — rather than spreading them over the serve paths — is what makes
// the shared-cache contract auditable.
func reuseDecision(e *Entry, r *http.Request, p requestPolicy, now time.Time) reuseKind {
	if r.Header.Get("Authorization") != "" && !e.SharedAuthReuse {
		// RFC 9111 §3.5 is not a freshness rule: no amount of revalidation makes
		// a response the origin never marked shareable usable for an
		// authenticated request. Fetch a response of this request's own.
		return reuseNone
	}

	mustValidate := p.MustValidate || e.RequiresValidation
	if !mustValidate {
		if e.Fresh(now) {
			return reuseHit
		}
		// must-revalidate / proxy-revalidate forbid serving a stale response
		// without contacting the origin (RFC 9111 §5.2.2.2, §5.2.2.8). Jul's
		// global stale_while_revalidate and stale_if_error settings never
		// override that, and neither does an explicit stale-if-error.
		if !e.MustRevalidate && e.ServableStale(now) {
			return reuseStale
		}
	}
	if e.ETag == "" && e.LastModified == "" {
		// Nothing to make a conditional request with. RFC 9111 §4.3.2: fetch a
		// complete new response rather than serve the stored one.
		return reuseNone
	}
	return reuseValidate
}

// serveUnsafe forwards a request whose method the cache never stores, and
// invalidates the representations a successful state-changing request made
// obsolete (RFC 9111 §4.4).
func (c *Cache) serveUnsafe(w http.ResponseWriter, r *http.Request, next http.Handler) {
	if !isUnsafeMethod(r.Method) {
		// OPTIONS and TRACE change nothing; CONNECT never has a representation.
		next.ServeHTTP(w, r)
		return
	}
	sw := &statusWriter{ResponseWriter: w}
	next.ServeHTTP(respwriter.Wrap(sw, w), r)
	if !invalidatingStatus(sw.status) {
		// A failed, hijacked, timed-out or canceled request changed nothing the
		// cache can prove, so it invalidates nothing.
		return
	}
	c.invalidateFor(r, w.Header())
}

// fetchAndStore runs the upstream handler, streams the response to the client,
// and stores it if cacheable. The handler receives a capability-transparent
// wrapper, so enabling the cache neither removes nor invents an optional
// ResponseWriter interface.
func (c *Cache) fetchAndStore(w http.ResponseWriter, r *http.Request, next http.Handler, now time.Time) {
	cw := &cacheWriter{ResponseWriter: w, limit: c.Policy().MaxEntryBytes}
	w.Header().Set("X-Cache", stateMiss)
	// Captured after X-Cache is set, so that field cancels out of the stored
	// entry the same way any other outer-layer pre-set field does (#332),
	// rather than needing its own denylist entry in buildEntry.
	cw.entrySnapshot = cloneHeader(w.Header())
	next.ServeHTTP(respwriter.Wrap(cw, w), r)

	if !cw.storable() {
		return
	}
	// cw.snapshot, not w.Header(): layers outside the cache (compression in
	// particular) mutate the shared header map after cacheWriter.WriteHeader
	// returns, and cw.buf only ever holds the bytes the handler itself wrote.
	// Reading w.Header() here would pair a header from one layer with a body
	// captured at another (#326). cw.snapshot is also already the multiset
	// difference against cw.entrySnapshot, so a field an outer layer pre-set
	// before the handler ran — X-Request-ID, the X-Cache line just above — is
	// not in it at all (#332).
	h := cw.snapshot
	if h == nil {
		h = w.Header()
	}
	if e := c.buildEntry(r, cw.status, h, cw.buf.Bytes(), now); e != nil {
		c.store(key(r), r, e)
	}
}

// startRevalidate launches the background refresh of a stale entry. It must be
// called from the originating request's ServeHTTP, before it returns:
//
//  1. it acquires the handler generation's background lease first, so the
//     generation cannot retire — and close the gRPC connections, plugin
//     runtimes and static roots the captured next handler uses — while the
//     refresh runs. Acquisition fails cleanly on a retiring generation or when
//     no lease is installed, in which case no refresh starts and the stale
//     entry simply expires normally;
//  2. it deduplicates per (effective key, generation), so a burst of concurrent
//     stale hits produces exactly one origin request;
//  3. it clones the request onto the leased context here, not in the goroutine,
//     because the originating *http.Request must not be touched after
//     ServeHTTP returns.
func (c *Cache) startRevalidate(effKey string, r *http.Request, next http.Handler, stale *Entry) {
	ctx, release, ok := background.Acquire(r.Context(), background.OpCacheRevalidate)
	if !ok {
		c.observeRevalidation(outcomeNoLease)
		c.log.Debug("cache: background revalidation not started",
			"operation", background.OpCacheRevalidate.String(), "reason", "no generation lease available")
		return
	}
	gen, _ := background.Generation(r.Context())
	k := revalidateKey{key: effKey, gen: gen}

	call, leader := c.beginRevalidate(k)
	if !leader {
		release()
		c.observeRevalidation(outcomeDeduplicated)
		return
	}

	req := conditionalRequest(r.Clone(ctx), stale)

	go func() {
		defer release()
		c.revalidate(ctx, k, req, next, stale, call)
	}()
}

// conditionalRequest turns req into the conditional GET that validates stale.
// The client's own conditional headers are replaced, not merged: the cache is
// asking whether ITS stored representation is still current, and a client
// If-None-Match would otherwise let the origin answer a different question.
func conditionalRequest(req *http.Request, stale *Entry) *http.Request {
	req.Body = http.NoBody
	req.Header.Del("If-None-Match")
	req.Header.Del("If-Modified-Since")
	// RFC 9110 §13.1.2 precedence: an entity tag is the strong validator and is
	// preferred whenever the origin supplied one; Last-Modified is the fallback.
	if stale.ETag != "" {
		req.Header.Set("If-None-Match", stale.ETag)
	} else if stale.LastModified != "" {
		req.Header.Set("If-Modified-Since", stale.LastModified)
	}
	return req
}

// revalidate refreshes a stale entry using a conditional request. It runs on the
// leased background context: independent of the client that triggered it, but
// canceled by process shutdown, generation retirement, and the lease's bounded
// operation deadline.
//
// It never mutates the published stale entry. Every update publishes a clone and
// replaces the stored pointer under the tier's own lock. The deferred cleanup
// removes the call state and releases every waiter on all paths, including a
// panic in the downstream handler.
func (c *Cache) revalidate(ctx context.Context, k revalidateKey, req *http.Request, next http.Handler, stale *Entry, call *revalidateCall) {
	defer func() {
		// Ordering matters: drop the call state first so a later stale hit can
		// start a fresh refresh the instant a waiter is released.
		c.endRevalidate(k, call)
		if v := recover(); v != nil {
			call.finish(nil, outcomePanic, errRevalidatePanic)
			c.observeRevalidation(outcomePanic)
			// The panic value is unbounded, request-influenced data; log only
			// that it happened, consistent with the request-path recoverer.
			c.log.Error("cache: background revalidation panicked",
				"operation", background.OpCacheRevalidate.String(), "generation", k.gen)
			return
		}
		// A leader that returned without publishing an outcome (an impossible
		// path today) must still release its waiters rather than strand them.
		call.finish(nil, outcomeCanceled, context.Canceled)
	}()

	rec := &recorder{header: http.Header{}, limit: c.Policy().MaxEntryBytes}
	next.ServeHTTP(rec, req)
	now := c.clock()

	// The handler may have returned early because the operation was canceled.
	// A canceled refresh must not be mistaken for an origin outage, so it never
	// extends the stale-if-error window.
	if err := ctx.Err(); err != nil {
		call.finish(nil, outcomeCanceled, err)
		c.observeRevalidation(outcomeCanceled)
		c.log.Debug("cache: background revalidation canceled",
			"operation", background.OpCacheRevalidate.String(), "generation", k.gen, "reason", err.Error())
		return
	}

	switch {
	case rec.status == http.StatusNotModified:
		// Upstream confirms freshness: republish a merged clone under the same
		// (variant) key it already lives at.
		refreshed, action := c.merge304(stale, rec.header, now)
		switch action {
		case mergeReplace:
			c.set(k.key, refreshed)
			call.finish(refreshed, outcomeNotModified, nil)
			c.observeRevalidation(outcomeNotModified)
		case mergeDiscard:
			// The 304 changed the representation's keying or made it
			// unstorable; dropping it is the only safe outcome.
			c.Delete(k.key)
			call.finish(nil, outcomeUncacheable, nil)
			c.observeRevalidation(outcomeUncacheable)
		}

	case rec.status >= 500:
		// Upstream error during revalidation: extend the stale-if-error window
		// so the entry stays servable while the backend recovers. The published
		// entry is replaced by a clone, never edited in place.
		if sie := c.staleOnErrorWindow(stale); sie > 0 {
			refreshed := stale.Clone()
			refreshed.StaleUntil = now.Add(sie)
			c.set(k.key, refreshed)
			call.finish(refreshed, outcomeOriginError, nil)
		} else {
			call.finish(nil, outcomeOriginError, nil)
		}
		c.observeRevalidation(outcomeOriginError)

	case !rec.storable():
		call.finish(nil, outcomeUncacheable, nil)
		c.observeRevalidation(outcomeUncacheable)

	default:
		e := c.buildEntry(req, rec.status, rec.header, rec.body.Bytes(), now)
		if e == nil {
			call.finish(nil, outcomeUncacheable, nil)
			c.observeRevalidation(outcomeUncacheable)
			return
		}
		c.store(key(req), req, e)
		call.finish(e, outcomeStored, nil)
		c.observeRevalidation(outcomeStored)
	}
}

// buildEntry constructs a cacheable Entry, or nil if the response must not be
// stored.
func (c *Cache) buildEntry(r *http.Request, status int, h http.Header, body []byte, now time.Time) *Entry {
	p := parseResponsePolicy(h)
	ttl, swr, ok := c.freshness(status, h, p, now)
	if !ok {
		return nil
	}
	// A response produced FOR an authenticated request enters the shared cache
	// only with the origin's explicit permission. See sharedAuthStorable for why
	// this is stricter than the §3.5 reuse rule.
	if r.Header.Get("Authorization") != "" && !p.sharedAuthStorable() {
		return nil
	}

	stored := cloneHeader(h)
	removeHopByHop(stored)

	// created is when the origin generated the representation, not when Jul
	// received it: RFC 9111 §4.2.3 corrected initial age. Without it a response
	// that spent time in an upstream cache would be served for its full TTL
	// again here, and the Age header Jul reports would understate its real age.
	created := now.Add(-initialAge(h, now))

	e := &Entry{
		Status:             status,
		Header:             stored,
		Body:               append([]byte(nil), body...),
		CreatedAt:          created,
		ExpiresAt:          created.Add(ttl),
		StaleUntil:         created.Add(ttl + swr),
		ETag:               h.Get("ETag"),
		LastModified:       h.Get("Last-Modified"),
		RequiresValidation: p.NoCache,
		MustRevalidate:     p.revalidationRequired(),
		SharedAuthReuse:    p.sharedAuthReuse(),
		StaleIfError:       p.SIE,
		HasStaleIfError:    p.HasSIE,
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

// initialAge is RFC 9111 §4.2.3 corrected initial age, clamped to be
// non-negative. A clock skewed the other way (a Date in the future, an Age
// larger than the response has existed) must not make an entry look younger
// than the moment it arrived.
func initialAge(h http.Header, now time.Time) time.Duration {
	var apparent time.Duration
	if d, err := http.ParseTime(h.Get("Date")); err == nil {
		apparent = now.Sub(d)
	}
	if apparent < 0 {
		apparent = 0
	}
	age := apparent
	if v := strings.TrimSpace(h.Get("Age")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			if n > maxDeltaSeconds {
				n = maxDeltaSeconds
			}
			if d := time.Duration(n) * time.Second; d > age {
				age = d
			}
		}
	}
	return age
}

// serve writes a cached entry to the client, honoring conditional requests.
func (c *Cache) serve(w http.ResponseWriter, r *http.Request, e *Entry, state string, now time.Time) {
	h := w.Header()
	// Set on the first value, Add on the rest: a stored multi-value field is
	// still reproduced in full, but a stored field cannot stack beside a value
	// an outer layer already put on this same map before serve ran. The
	// entry-side rule in cacheWriter.WriteHeader already keeps a pre-set field
	// like X-Request-ID out of e.Header entirely (#332); this is the second,
	// independent half — belt and braces, not a substitute for it.
	for k, vs := range e.Header {
		for i, v := range vs {
			if i == 0 {
				h.Set(k, v)
			} else {
				h.Add(k, v)
			}
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
//
// TTL precedence is s-maxage → max-age → Expires → [cache] default_ttl. The
// first explicit directive wins; a directive present with an unusable argument
// resolves to zero, which makes the response uncacheable rather than silently
// falling through to a longer lifetime.
func (c *Cache) freshness(status int, h http.Header, p responsePolicy, now time.Time) (ttl, swr time.Duration, ok bool) {
	pol := c.Policy()
	if !cacheableStatus[status] {
		return 0, 0, false
	}
	if p.NoStore {
		return 0, 0, false
	}
	if p.Private {
		// Jul is a shared cache; a private response belongs to one user agent.
		return 0, 0, false
	}
	if h.Get("Set-Cookie") != "" {
		// Conservative shared-cache rule: a Set-Cookie is per-client state, and
		// replaying it to another client is a session-fixation vector. Origins
		// that genuinely want a cookie-bearing response shared must say so with
		// an explicit directive, which Jul does not currently honor for this
		// case (recorded as a limitation in docs/cache.md).
		return 0, 0, false
	}

	ttl = pol.DefaultTTL
	switch {
	case p.HasSMaxAge:
		ttl = p.SMaxAge
	case p.HasMaxAge:
		ttl = p.MaxAge
	default:
		if exp := h.Get("Expires"); exp != "" {
			t, err := http.ParseTime(exp)
			if err != nil {
				// RFC 9111 §5.3: an unparseable Expires means "already expired".
				return 0, 0, false
			}
			// Expires is absolute, so it is measured against the origin's own
			// Date when there is one; using Jul's clock instead would fold
			// clock skew into the lifetime.
			base := now
			if d, err := http.ParseTime(h.Get("Date")); err == nil {
				base = d
			}
			ttl = t.Sub(base)
		}
	}
	if ttl <= 0 && !p.NoCache {
		return 0, 0, false
	}

	if p.NoCache {
		// A no-cache response is storable, and its stored copy is still worth
		// keeping even with no usable lifetime: every reuse revalidates, and a
		// 304 then saves the body. Give it the configured default so it is
		// retained and aged like anything else.
		if ttl <= 0 {
			ttl = pol.DefaultTTL
		}
		if ttl <= 0 {
			return 0, 0, false
		}
	}

	swr = pol.StaleWhileRevalidate
	if p.HasSWR {
		swr = p.SWR
	}
	if p.revalidationRequired() || p.NoCache {
		// Neither a must-revalidate response nor a no-cache response may ever be
		// served without contacting the origin, so it has no stale window at all.
		swr = 0
	}
	return ttl, swr, true
}

// staleOnErrorWindow is how long an entry may keep being served after the origin
// failed to revalidate it.
//
// The precedence is the origin's, then Jul's:
//   - must-revalidate/proxy-revalidate forbid stale reuse outright (RFC 9111
//     §5.2.2.2), and neither the global setting nor RFC 5861's stale-if-error
//     may override that explicit prohibition;
//   - an explicit response stale-if-error replaces the global setting, in both
//     directions — a longer window, or an explicit zero that disables it;
//   - Jul's global [cache] stale_if_error is a default for responses that said
//     nothing. It is not permission to serve a no-cache response unvalidated,
//     so a no-cache entry gets a window only when the origin asked for one.
func (c *Cache) staleOnErrorWindow(e *Entry) time.Duration {
	if e.MustRevalidate {
		return 0
	}
	if e.HasStaleIfError {
		return e.StaleIfError
	}
	if e.RequiresValidation {
		return 0
	}
	return c.Policy().StaleIfError
}
