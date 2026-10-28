// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/background"
	"jul/internal/config"
	"jul/internal/logthrottle"
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
	inspectKey [32]byte
	samples    atomic.Pointer[decisionBuffer]
	mem        *memStore
	disk       *diskStore

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

	fillMu     sync.Mutex
	fills      map[string]*fillCall // in-flight cold-miss fills (#525)
	onFillLock func(outcome string)
	onRange    func(outcome string)

	// varyMu serializes the read-modify-write of a base resource's variant
	// membership stub. Publishing a variant and recording it in the stub must be
	// one step, or two concurrent variant writes lose one another's membership
	// and leave an entry that invalidation can no longer reach.
	varyMu sync.Mutex

	// genMu, genSeq, keyGen, and purgeGen assign monotonic per-key generations
	// across the memory and disk tiers so a slow disk lookup cannot resurrect a
	// deleted/replaced entry into memory and a slow write-through cannot
	// overwrite a newer disk entry. Entries in keyGen are reference-counted by
	// in-flight operations and tier residency (inMem/inDisk), and are reclaimed
	// in O(1) time once a key is idle and absent from both tiers.
	genMu                     sync.Mutex
	genSeq                    atomic.Uint64
	keyGen                    map[string]*keyGenState
	purgeGen                  uint64
	byHost                    map[string]map[string]struct{}
	byResourceID              map[string][2]string
	baseVariants              map[string]map[string]struct{}
	tagIndex                  map[string]map[string]map[string]struct{}
	baseIndexedTags           map[string][]string
	totalTagLinks             int
	fences                    []selectorFence
	fenceFloorGen             uint64
	hostFenceFloorGen         map[string]uint64
	incompleteFloorGen        uint64
	incompleteHostFloorGen    map[string]uint64
	evictedIncompleteHostGens map[string]map[uint64]struct{}
	latestFenceGen            atomic.Uint64
	incompleteFenceGen        atomic.Uint64
	startupHighWaterGen       atomic.Uint64
	originOpSeq               uint64
	inflightOps               map[uint64]uint64
	onBeforeReclaim           func(key string)
	onAfterFenceInstall       func(sel NormalizedSelector, fenceGen uint64)
	onAfterDiskPublication    func(key string, gen uint64)

	// observe, when set, receives one bounded outcome per revalidation
	// decision. It is installed by the composition root so the cache does not
	// depend on the observability package.
	observe func(outcome string)

	// capture bounds the bytes all in-flight captures hold at once (#505);
	// onCaptureSkip, when set, receives one bounded reason per response that
	// was streamed without being captured.
	capture       captureBudget
	onCaptureSkip func(reason string)
	onTagSkip     func(reason string)
	onInvalidate  func(selector, outcome string, memRemoved, diskRemoved int)
	// captureSkipLog bounds the debug line: the sizes and concurrency that
	// trigger a skip are chosen by clients and origins.
	captureSkipLog logthrottle.Limiter

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
		log:    logger,
		calls:  make(map[revalidateKey]*revalidateCall),
		fills:  make(map[string]*fillCall),
		keyGen: make(map[string]*keyGenState),
	}
	pol := policyFromConfig(cfg)
	if err := c.initInspection(); err != nil {
		return nil, err
	}
	c.setDecisionSampling(pol.DecisionSamples)
	c.policy.Store(&pol)
	c.mem = newMemStore(cfg.MemoryMaxSize.Bytes(), nil)
	c.mem.pinVictims = func(victims []*memItem) {
		c.genMu.Lock()
		c.pinVictimsLocked(victims)
		c.genMu.Unlock()
	}
	c.mem.onEvictItem = func(it *memItem) {
		c.recordKeyDecision(it.key, "memory_eviction")
		if c.disk != nil && !sensitiveVaryEntry(it.entry) && !c.disk.hasKey(it.key) && c.isGenCurrent(it.key, it.gen) && !c.isPublicationFenced(it.gen, it.key, it.entry, nil) {
			c.writeThroughObserved(it.key, it.entry, it.gen, it.gen, true, nil)
		}
		if it.pinnedState != nil {
			c.finishKeyOp(it.pinnedState)
		}
	}
	if cfg.DiskPath != "" {
		d, err := newDiskStore(cfg.DiskPath, cfg.DiskMaxSize.Bytes(), logger)
		if err != nil {
			return nil, err
		}
		d.onEviction = func(k string) { c.recordKeyDecision(k, "disk_eviction") }
		d.onSkip = c.captureSkipped
		d.checkGen = c.isGenCurrent
		d.canCommit = c.canCommitToDisk
		d.onKeyDiskState = c.setKeyDiskState
		d.onRehydrate = c.onDiskRehydrated
		c.disk = d
		d.mu.Lock()
		rehydrated := d.snapshotItemsLocked()
		d.mu.Unlock()
		c.onDiskRehydrated(rehydrated)
		if hw := d.highWaterMark(); hw > c.genSeq.Load() {
			c.genSeq.Store(hw)
		}
		c.startupHighWaterGen.Store(c.genSeq.Load())
	}
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

// SetCaptureSkipObserver installs a bounded counter for responses streamed
// without being captured for storage. fn receives only the package's reason
// constants ("oversize", "budget"). Call it once at startup, before the cache
// serves traffic.
func (c *Cache) SetCaptureSkipObserver(fn func(reason string)) {
	if c == nil {
		return
	}
	c.onCaptureSkip = fn
}

// SetTagSkipObserver installs a bounded counter for responses whose origin tag
// metadata was invalid, oversize, or exceeded the tag index limit (#621).
func (c *Cache) SetTagSkipObserver(fn func(reason string)) {
	if c == nil {
		return
	}
	c.onTagSkip = fn
}

func (c *Cache) tagSkipped(reason string) {
	if c != nil && c.onTagSkip != nil && reason != "" {
		c.onTagSkip(reason)
	}
}

// SetInvalidationObserver installs a bounded counter for cache invalidation
// operations and per-tier removed entries (#621).
func (c *Cache) SetInvalidationObserver(fn func(selector, outcome string, memRemoved, diskRemoved int)) {
	if c == nil {
		return
	}
	c.onInvalidate = fn
}

// newCaptureAccount binds one capture to the cache-wide in-flight budget.
func (c *Cache) newCaptureAccount() captureAccount {
	return captureAccount{budget: &c.capture, onSkip: c.captureSkipped}
}

func (c *Cache) captureSkipped(reason string) {
	if c.onCaptureSkip != nil {
		c.onCaptureSkip(reason)
	}
	if c.captureSkipLog.Allow(diskWriteFailureLogInterval) {
		c.log.Debug("cache: response streamed without capture; not stored",
			"reason", reason, "limit_bytes", c.Policy().MaxEntryBytes,
			"inflight_capture_bytes", c.capture.inflight.Load())
	}
}

type keyGenState struct {
	key                    string
	baseKey                string
	host                   string
	requestURI             string
	method                 string
	isVariant              bool
	isVaryStub             bool
	variants               []string
	tags                   []string
	retainedTags           []string
	ingestedTagHeaders     [8]string
	ingestedTagHeaderCount int
	bodyBytes              int64
	memBytes               int64
	diskBytes              int64
	createdAt              time.Time
	evidence               *publicationEvidence
	expiresAt              time.Time
	staleUntil             time.Time
	varyFields             []string
	gen                    uint64
	inflight               int32
	inMem                  bool
	inDisk                 bool
	indexed                bool
}

func (c *Cache) beginNextGenLocked(key string) (st *keyGenState, gen, prevGen uint64, hadPrev bool) {
	if c.keyGen == nil {
		c.keyGen = make(map[string]*keyGenState)
	}
	g := c.genSeq.Add(1)
	if existing, ok := c.keyGen[key]; ok {
		prevGen = existing.gen
		existing.gen = g
		existing.inflight++
		return existing, g, prevGen, true
	}
	st = &keyGenState{key: key, gen: g, inflight: 1}
	c.keyGen[key] = st
	return st, g, 0, false
}

func (c *Cache) beginReadGenLocked(key string) (st *keyGenState, preGen, prePurge uint64) {
	if c.keyGen == nil {
		c.keyGen = make(map[string]*keyGenState)
	}
	existing, ok := c.keyGen[key]
	if !ok {
		existing = &keyGenState{key: key}
		c.keyGen[key] = existing
	}
	existing.inflight++
	return existing, existing.gen, c.purgeGen
}

func (c *Cache) maybeReclaimLocked(st *keyGenState) {
	if st == nil {
		return
	}
	if cur, ok := c.keyGen[st.key]; ok && cur == st && st.inflight == 0 {
		st.inMem = c.mem != nil && c.mem.hasKey(st.key)
		if !st.inMem && !st.inDisk {
			c.unindexKeyEntryLocked(st)
			delete(c.keyGen, st.key)
		}
	}
}

func (c *Cache) finishKeyOp(st *keyGenState) {
	if st == nil {
		return
	}
	hook := c.onBeforeReclaim
	c.genMu.Lock()
	st.inflight--
	if st.inflight < 0 {
		st.inflight = 0
	}
	if hook == nil {
		c.maybeReclaimLocked(st)
		c.genMu.Unlock()
		return
	}
	c.genMu.Unlock()
	hook(st.key)
	c.genMu.Lock()
	c.maybeReclaimLocked(st)
	c.genMu.Unlock()
}

func (c *Cache) setKeyDiskState(key string, inDisk bool) {
	c.genMu.Lock()
	if st, ok := c.keyGen[key]; ok {
		st.inDisk = inDisk
		if !inDisk {
			c.maybeReclaimLocked(st)
		}
	}
	c.genMu.Unlock()
}

func (c *Cache) pinVictimsLocked(victims []*memItem) {
	for _, it := range victims {
		if st, ok := c.keyGen[it.key]; ok {
			if st.gen == it.gen && (c.mem == nil || !c.mem.hasKey(it.key)) {
				st.inMem = false
			}
			st.inflight++
			it.pinnedState = st
		}
	}
}

func (c *Cache) isGenCurrentLocked(key string, gen uint64) bool {
	if gen < c.purgeGen {
		return false
	}
	if st, ok := c.keyGen[key]; ok && st.gen != 0 {
		return st.gen == gen
	}
	return gen == 0 && c.purgeGen == 0
}

func (c *Cache) isGenCurrent(key string, gen uint64) bool {
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.isGenCurrentLocked(key, gen)
}

func (c *Cache) canCommitToDisk(key string, gen, startGen uint64, hasStartGen bool, e *Entry, extraTags []string) bool {
	c.genMu.Lock()
	defer c.genMu.Unlock()
	if !c.isGenCurrentLocked(key, gen) {
		return false
	}
	if e != nil && !e.IsVaryStub && e.tagPolicyTracked {
		if curPol := c.Policy(); curPol.TagHeaderCount > 0 && (e.ingestedTagHeaderCount != curPol.TagHeaderCount || e.ingestedTagHeaders != curPol.TagHeaders) {
			return false
		}
	}
	if hasStartGen && e != nil && c.isPublicationFencedLocked(startGen, key, e.Tags, extraTags) {
		return false
	}
	return true
}

func (c *Cache) hasStoredKey(key string) bool {
	if _, ok := c.mem.get(key); ok {
		return true
	}
	if c.disk != nil {
		return c.disk.hasKey(key)
	}
	return false
}

func (c *Cache) get(key string) (*Entry, bool) {
	if e, mgen, ok := c.mem.getWithGen(key); ok {
		if lf := c.incompleteFenceGen.Load(); lf > 0 && mgen <= lf && c.isResidentEntryFenced(mgen, key, e) {
			c.delOneIfGenAtMost(key, mgen)
		} else {
			return e, true
		}
	}
	if c.disk != nil {
		c.genMu.Lock()
		st, preGen, prePurge := c.beginReadGenLocked(key)
		c.genMu.Unlock()
		memCap := c.Policy().MemoryCapBytes()
		e, diskGen, ok := c.disk.getWithMemCapObserved(key, memCap, func() { c.recordKeyDecision(key, "disk_read_failed") })
		if !ok {
			c.finishKeyOp(st)
			return nil, false
		}
		c.genMu.Lock()
		curSt := c.keyGen[key]
		curGen := uint64(0)
		if curSt != nil {
			curGen = curSt.gen
		}
		tagPolicyOK := true
		if !e.IsVaryStub {
			if curPol := c.Policy(); curPol.TagHeaderCount > 0 {
				if !e.tagPolicyTracked || e.ingestedTagHeaderCount != curPol.TagHeaderCount || e.ingestedTagHeaders != curPol.TagHeaders {
					tagPolicyOK = false
				}
			}
		}
		stillValid := c.purgeGen == prePurge && curSt == st && curGen == preGen &&
			tagPolicyOK && c.isGenCurrentLocked(key, diskGen) && !c.isResidentEntryFencedLocked(diskGen, key, e.Tags)
		if !stillValid {
			c.genMu.Unlock()
			if !tagPolicyOK {
				c.delOneIfGenAtMost(key, diskGen)
			}
			c.finishKeyOp(st)
			return c.mem.get(key)
		}
		var victims []*memItem
		if e.diskBody == nil {
			victims = c.mem.putWithGen(key, e, diskGen) // promote memory-eligible entry under genMu
			st.inMem = c.mem.hasKey(key)
			if st.inMem {
				st.memBytes = e.Size() + int64(len(key))
			}
			c.pinVictimsLocked(victims)
		}
		c.genMu.Unlock()
		c.mem.flushVictims(victims)
		c.finishKeyOp(st)
		return e, true
	}
	return nil, false
}

func (c *Cache) set(key string, e *Entry) {
	c.setWithResult(key, e)
}

func (c *Cache) setWithResult(key string, e *Entry) bool {
	return c.setWithStartGen(key, e, 0, false)
}

func (c *Cache) setIfNotFenced(key string, e *Entry, startGen uint64, extraTags []string) (fenced bool) {
	_, fenced, _ = c.setCoreWithStartGen(key, e, startGen, true, extraTags)
	return fenced
}

func (c *Cache) setWithStartGen(key string, e *Entry, startGen uint64, hasStartGen bool) bool {
	stored, _, _ := c.setCoreWithStartGen(key, e, startGen, hasStartGen, nil)
	return stored
}

func (c *Cache) setCoreWithStartGen(key string, e *Entry, startGen uint64, hasStartGen bool, extraTags []string) (stored, fenced bool, publishedGen uint64) {
	if sensitiveVaryEntry(e) {
		return false, true, 0
	}
	c.genMu.Lock()
	if e != nil && !e.IsVaryStub {
		curPol := c.Policy()
		if e.tagPolicyTracked {
			if curPol.TagHeaderCount > 0 && (e.ingestedTagHeaderCount != curPol.TagHeaderCount || e.ingestedTagHeaders != curPol.TagHeaders) {
				c.genMu.Unlock()
				return false, true, 0
			}
		} else {
			e.ingestedTagHeaderCount = curPol.TagHeaderCount
			e.ingestedTagHeaders = curPol.TagHeaders
			e.tagPolicyTracked = true
		}
	}
	if hasStartGen && e != nil && c.isPublicationFencedLocked(startGen, key, e.Tags, extraTags) {
		c.genMu.Unlock()
		return false, true, 0
	}
	if e != nil && len(e.Tags) > 0 {
		if _, host, _, _, ok := parseBaseKeyParts(key); ok {
			if !c.canReserveTagLinksLocked(host, baseKeyOf(key), e.Tags) {
				c.genMu.Unlock()
				c.recordKeyDecision(key, "tag_capture_rejected")
				c.tagSkipped(TagSkipIndexLimit)
				return false, true, 0
			}
		}
	}
	st, gen, prevGen, hadPrev := c.beginNextGenLocked(key)
	var victims []*memItem
	if e != nil && e.diskBody != nil {
		c.mem.del(key)
		st.inMem = false
		st.memBytes = 0
	} else {
		victims = c.mem.putWithGen(key, e, gen)
		st.inMem = c.mem.hasKey(key)
		if st.inMem && e != nil {
			st.memBytes = e.Size() + int64(len(key))
		} else {
			st.memBytes = 0
		}
		c.pinVictimsLocked(victims)
	}
	if e != nil {
		c.indexKeyEntryLocked(st, e)
	}
	c.genMu.Unlock()
	c.mem.flushVictims(victims)
	if c.disk != nil {
		if !c.writeThroughObserved(key, e, gen, startGen, hasStartGen, extraTags) {
			if e != nil && e.diskBody != nil {
				c.genMu.Lock()
				if c.keyGen[key] == st && st.gen == gen {
					if hadPrev {
						st.gen = prevGen
					} else {
						st.gen = 0
					}
				}
				fencedAfterDisk := hasStartGen && c.isPublicationFencedLocked(startGen, key, e.Tags, extraTags)
				c.genMu.Unlock()
				if fencedAfterDisk {
					c.delOneIfGenAtMost(key, gen)
				}
				c.finishKeyOp(st)
				return false, fencedAfterDisk, 0
			}
			// Memory write-through failed, while memory now holds the new generation `gen`.
			// Remove or tombstone any older disk generation (< gen) for `key` so a
			// subsequent restart cannot rehydrate and serve the superseded disk value.
			c.disk.delIfOlderGen(key, gen)
		} else if e != nil {
			c.genMu.Lock()
			st.diskBytes = e.BodyLen()
			c.genMu.Unlock()
		}
	}
	if hasStartGen && e != nil {
		c.genMu.Lock()
		fencedAfter := c.isPublicationFencedLocked(startGen, key, e.Tags, extraTags)
		c.genMu.Unlock()
		if fencedAfter {
			c.delOneIfGenAtMost(key, gen)
			c.finishKeyOp(st)
			return false, true, 0
		}
	}
	c.finishKeyOp(st)
	return c.hasStoredKey(key), false, gen
}

// Delete removes a single key from both tiers. When the key holds a Vary stub,
// every variant the stub owns is removed with it: deleting the stub alone would
// strand the hashed variant entries in the store, where a later stub with the
// same Vary could make them reachable again.
func (c *Cache) Delete(key string) {
	c.deleteAtMostGen(key, math.MaxUint64)
}

func (c *Cache) deleteAtMostGen(key string, maxGen uint64) {
	c.varyMu.Lock()
	defer c.varyMu.Unlock()
	var extraVars []string
	c.genMu.Lock()
	if st := c.keyGen[key]; st != nil && st.gen <= maxGen {
		extraVars = append(extraVars, st.variants...)
		if c.baseVariants != nil {
			for vk := range c.baseVariants[key] {
				extraVars = append(extraVars, vk)
			}
		}
	}
	c.genMu.Unlock()
	if maxGen == math.MaxUint64 {
		if e, ok := c.get(key); ok && e.IsVaryStub {
			extraVars = append(extraVars, e.Variants...)
		}
	}
	seen := make(map[string]struct{}, len(extraVars))
	for _, vk := range extraVars {
		if _, ok := seen[vk]; ok {
			continue
		}
		seen[vk] = struct{}{}
		c.delOneIfGenAtMost(vk, maxGen)
	}
	c.delOneIfGenAtMost(key, maxGen)
}

func (c *Cache) delOne(key string) {
	c.delOneIfGenAtMost(key, math.MaxUint64)
}

func (c *Cache) delOneIfGenAtMost(key string, maxGen uint64) {
	c.genMu.Lock()
	if maxGen != math.MaxUint64 {
		if existing, ok := c.keyGen[key]; ok && existing.gen > maxGen {
			c.genMu.Unlock()
			// An older disk publication may outlive the catalog generation
			// that invalidated it. Preserve newer entries, not that old file.
			if c.disk != nil {
				c.disk.delIfOlderGen(key, maxGen+1)
			}
			return
		}
	}
	st, _, _, _ := c.beginNextGenLocked(key)
	c.mem.delIfGenAtMost(key, maxGen)
	st.inMem = c.mem.hasKey(key)
	if !st.inMem {
		st.memBytes = 0
	}
	c.genMu.Unlock()
	if c.disk != nil {
		if maxGen == math.MaxUint64 {
			c.disk.del(key)
		} else {
			c.disk.delIfOlderGen(key, maxGen+1)
		}
	}
	c.finishKeyOp(st)
}

// Purge clears both tiers.
func (c *Cache) Purge() {
	c.genMu.Lock()
	pGen := c.genSeq.Add(1)
	c.purgeGen = pGen
	c.fenceFloorGen = pGen
	c.hostFenceFloorGen = nil
	c.incompleteFloorGen = 0
	c.incompleteHostFloorGen = nil
	c.evictedIncompleteHostGens = nil
	c.fences = nil
	c.latestFenceGen.Store(pGen)
	c.incompleteFenceGen.Store(0)
	c.byHost = nil
	c.byResourceID = nil
	c.baseVariants = nil
	c.tagIndex = nil
	c.baseIndexedTags = nil
	c.totalTagLinks = 0
	c.keyGen = make(map[string]*keyGenState)
	c.mem.purge()
	c.genMu.Unlock()
	if c.disk != nil {
		_ = c.disk.recordHighWaterGen(pGen)
		c.disk.purge()
	}
}

// DiskWriteHealth reports the disk tier's cumulative write failures and
// whether its most recent write failed. ok is false when there is no disk
// tier (or c is nil).
func (c *Cache) DiskWriteHealth() (failures int64, failing, ok bool) {
	if c == nil || c.disk == nil {
		return 0, false, false
	}
	return c.disk.writeFails.Load(), c.disk.failing.Load(), true
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
	startGen := c.genSeq.Load()
	e, ok := c.get(base)
	if !ok {
		return nil, base, false
	}
	if sensitiveVaryEntry(e) {
		c.recordDecision(r, "vary_sensitive_rejected")
		c.cleanupSensitiveVary(base, e, startGen)
		return nil, base, false
	}
	if e.IsVaryStub {
		vk := variantKey(base, e.Vary, r)
		if !e.ownsVariant(vk) {
			return nil, vk, false
		}
		e, ok = c.get(vk)
		if ok && sensitiveVaryEntry(e) {
			c.recordDecision(r, "vary_sensitive_rejected")
			c.delOneIfGenAtMost(vk, startGen)
			return nil, base, false
		}
		return e, vk, ok
	}
	return e, base, true
}

// Cleanup is opportunistic and generation-bounded: an entry published after
// lookup began must not be deleted. At most one stub and 64 variants are
// touched; unvisited legacy files remain subject to normal eviction or purge.
func (c *Cache) cleanupSensitiveVary(base string, e *Entry, startGen uint64) {
	c.varyMu.Lock()
	defer c.varyMu.Unlock()
	if e.IsVaryStub {
		for i, variant := range e.Variants {
			if i == maxVariantsPerResource {
				break
			}
			c.delOneIfGenAtMost(variant, startGen)
		}
	}
	c.delOneIfGenAtMost(base, startGen)
}

// store writes an entry to the cache. A response without Vary is stored directly
// under the base key. A Vary response is stored under a per-variant key, and the
// stub under the base key records both the varied field names and every variant
// key the resource owns, so a later invalidation can remove all of them.
func (c *Cache) store(base string, r *http.Request, e *Entry) {
	c.storeCaptured(base, r, e, nil)
}

func (c *Cache) setCaptured(k string, e *Entry, stream *diskStreamCapture) bool {
	return c.setCapturedWithStartGen(k, e, stream, 0, false)
}

func (c *Cache) setCapturedWithStartGen(k string, e *Entry, stream *diskStreamCapture, startGen uint64, hasStartGen bool) bool {
	stored, _ := c.publishCapturedWithStartGen(k, e, stream, startGen, hasStartGen)
	return stored
}

// The returned generation belongs to this publication, not a later replacement,
// and is the upper bound for any membership-publication rollback.
func (c *Cache) publishCapturedWithStartGen(k string, e *Entry, stream *diskStreamCapture, startGen uint64, hasStartGen bool) (bool, uint64) {
	if sensitiveVaryEntry(e) {
		return false, 0
	}
	if stream == nil {
		stored, _, gen := c.setCoreWithStartGen(k, e, startGen, hasStartGen, nil)
		return stored, gen
	}
	if c.disk == nil {
		return false, 0
	}
	c.genMu.Lock()
	if e != nil && !e.IsVaryStub {
		curPol := c.Policy()
		if e.tagPolicyTracked {
			if curPol.TagHeaderCount > 0 && (e.ingestedTagHeaderCount != curPol.TagHeaderCount || e.ingestedTagHeaders != curPol.TagHeaders) {
				c.genMu.Unlock()
				return false, 0
			}
		} else {
			e.ingestedTagHeaderCount = curPol.TagHeaderCount
			e.ingestedTagHeaders = curPol.TagHeaders
			e.tagPolicyTracked = true
		}
	}
	if hasStartGen && e != nil && c.isPublicationFencedLocked(startGen, k, e.Tags, nil) {
		c.genMu.Unlock()
		return false, 0
	}
	if e != nil && len(e.Tags) > 0 {
		if _, host, _, _, ok := parseBaseKeyParts(k); ok {
			if !c.canReserveTagLinksLocked(host, baseKeyOf(k), e.Tags) {
				c.genMu.Unlock()
				c.recordKeyDecision(k, "tag_capture_rejected")
				c.tagSkipped(TagSkipIndexLimit)
				return false, 0
			}
		}
	}
	st, gen, prevGen, hadPrev := c.beginNextGenLocked(k)
	c.mem.del(k)
	st.inMem = false
	st.memBytes = 0
	if e != nil {
		c.indexKeyEntryLocked(st, e)
	}
	c.genMu.Unlock()
	blob, ok := c.disk.publishStreamWithGenFull(k, e, stream, gen, startGen, hasStartGen, nil)
	if !ok {
		c.genMu.Lock()
		if c.keyGen[k] == st && st.gen == gen {
			if hadPrev {
				st.gen = prevGen
			} else {
				st.gen = 0
			}
		}
		c.genMu.Unlock()
		c.disk.delIfOlderGen(k, gen)
		c.finishKeyOp(st)
		return false, 0
	}
	e.diskBody = blob
	c.genMu.Lock()
	st.diskBytes = blob.fileSize
	c.genMu.Unlock()
	if c.onAfterDiskPublication != nil {
		c.onAfterDiskPublication(k, gen)
	}
	if hasStartGen && c.isPublicationFenced(startGen, k, e, nil) {
		c.delOneIfGenAtMost(k, gen)
		c.finishKeyOp(st)
		return false, 0
	}
	c.finishKeyOp(st)
	return true, gen
}

func (c *Cache) storeCaptured(base string, r *http.Request, e *Entry, stream *diskStreamCapture) bool {
	return c.storeCapturedWithStartGen(base, r, e, stream, 0, false)
}

func (c *Cache) storeCapturedWithStartGen(base string, r *http.Request, e *Entry, stream *diskStreamCapture, startGen uint64, hasStartGen bool) (stored bool) {
	defer func() {
		if stored {
			c.recordKeyDecision(base, "response_stored")
		} else {
			c.recordKeyDecision(base, "publication_failed_or_fenced")
		}
	}()
	if sensitiveVaryEntry(e) {
		c.recordDecision(r, "vary_sensitive_rejected")
		return false
	}
	if len(e.Vary) == 0 {
		return c.setCapturedWithStartGen(base, e, stream, startGen, hasStartGen)
	}
	vk := variantKey(base, e.Vary, r)

	c.varyMu.Lock()
	defer c.varyMu.Unlock()

	if hasStartGen && c.isPublicationFenced(startGen, base, e, nil) {
		return false
	}
	deleteGen := uint64(math.MaxUint64)
	if hasStartGen {
		deleteGen = startGen
	}
	stub := c.stubFor(base, e.Vary, deleteGen)
	// The variant is published before the stub that points at it, so a lookup
	// following a freshly listed key always finds an entry there.
	stored, variantGen := c.publishCapturedWithStartGen(vk, e, stream, startGen, hasStartGen)
	if !stored {
		return false
	}
	if evicted := stub.addVariant(vk); evicted != "" {
		c.delOneIfGenAtMost(evicted, deleteGen)
	}
	var varTagSets [][]string
	c.genMu.Lock()
	for _, mvk := range stub.Variants {
		if vst := c.keyGen[mvk]; vst != nil && len(vst.tags) > 0 {
			varTagSets = append(varTagSets, vst.tags)
		}
	}
	c.genMu.Unlock()
	stub.Tags = mergeTagSets(varTagSets...)
	stubStored, _, stubGen := c.setCoreWithStartGen(base, stub, startGen, hasStartGen, nil)
	if !stubStored {
		c.delOneIfGenAtMost(vk, variantGen)
		return false
	}
	if (e.diskBody != nil && c.disk != nil && (!c.disk.hasKey(base) || !c.disk.hasKey(vk))) ||
		!c.hasStoredKey(base) || !c.hasStoredKey(vk) {
		c.delOneIfGenAtMost(vk, variantGen)
		c.delOneIfGenAtMost(base, stubGen)
		return false
	}
	return true
}

// stubFor returns the membership stub to publish for base. An existing stub with
// the same Vary field set is extended; a stub for a different Vary — the origin
// changed what it varies on — is replaced, and the variants it owned are deleted
// up to maxGen rather than deleting representations newer than the origin work.
func (c *Cache) stubFor(base string, vary []string, maxGen uint64) *Entry {
	if old, ok := c.get(base); ok && old.IsVaryStub {
		if sameFields(old.Vary, vary) {
			return old.Clone()
		}
		for _, vk := range old.Variants {
			c.delOneIfGenAtMost(vk, maxGen)
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
// values are length-framed, so repeated field lines and their order cannot
// collapse into the first value or collide with another combination.
func variantKey(base string, vary []string, r *http.Request) string {
	fields := normalizeFields(vary)
	var b strings.Builder
	b.WriteString(base)
	for _, f := range fields {
		b.WriteByte(0x00)
		b.WriteString(f)
		b.WriteByte(0x1f)
		b.WriteString(varyRequestValue(r.Header, f))
	}
	return b.String()
}

// varyRequestValue encodes every line of a varied request header. The leading
// control byte and version make persisted entries from the old first-value
// encoding miss after upgrade, including entries for a single-valued request:
// an old entry may have been generated from additional values it did not key.
// Length framing distinguishes absent, empty, repeated and comma-containing
// values without relying on a separator inside an arbitrary field value.
func varyRequestValue(header http.Header, name string) string {
	values := header.Values(name)
	var b strings.Builder
	b.WriteString("\x00v2:")
	b.WriteString(strconv.Itoa(len(values)))
	for _, value := range values {
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(len(value)))
		b.WriteByte(':')
		b.WriteString(value)
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
			c.recordDecision(r, "upgrade_bypass")
			c.bypass(w, r, next, span)
			return
		}

		pol := c.Policy()

		// Decision D05 (#107/#132) & #442 Option A (ADR 0024): when range_enabled
		// is false (default), a request carrying Range or If-Range bypasses the
		// cache before lookup and storage. When range_enabled is true, Option A
		// evaluates Range/If-Range against complete cached representations and
		// coordinates complete-object fills on eligible cold range misses.
		if isRangeRequest(r) {
			if !pol.RangeEnabled {
				c.recordDecision(r, "range_bypass")
				c.bypass(w, r, next, span)
				return
			}
			c.handleRangeRequest(w, r, next, pol, span)
			return
		}

		reqPolicy := parseRequestPolicy(r)
		if reqPolicy.NoStore {
			c.recordDecision(r, "request_no_store")
			// Request no-store opts this exchange out of the cache. It is not a
			// purge: an entry another client stored stays exactly as it was.
			c.bypass(w, r, next, span)
			return
		}

		c.serveOrFillFull(w, r, next, reqPolicy, pol, span)
	})
}

func (c *Cache) serveOrFillFull(w http.ResponseWriter, r *http.Request, next http.Handler, reqPolicy requestPolicy, pol CachePolicy, span tracing.Span) {
	k := key(r)
	now := c.clock()
	effKey, served := c.serveCachedIfReusable(w, r, next, k, reqPolicy, now, span)
	if served {
		return
	}
	if pol.Lock {
		c.fillOrWait(w, r, next, k, effKey, reqPolicy, pol, span, effKey == k)
		return
	}
	span.SetString("cache.status", "MISS")
	span.End()
	c.fetchAndStoreWithPolicy(w, r, next, now, pol, nil)
}

func (c *Cache) serveCachedIfReusable(w http.ResponseWriter, r *http.Request, next http.Handler, base string, reqPolicy requestPolicy, now time.Time, span tracing.Span) (effKey string, served bool) {
	for attempt := 0; attempt < 2; attempt++ {
		var e *Entry
		var ok bool
		e, effKey, ok = c.lookup(base, r)
		if !ok || !e.matchesVary(r) {
			return effKey, false
		}
		switch reuseDecision(e, r, reqPolicy, now) {
		case reuseHit:
			if !c.serve(w, r, e, stateHit, now) {
				continue
			}
			span.SetString("cache.status", "HIT")
			span.End()
			return effKey, true
		case reuseStale:
			if !c.serve(w, r, e, stateStale, now) {
				continue
			}
			span.SetString("cache.status", "STALE")
			span.End()
			// Start the background refresh before returning, so the lease
			// is taken while this request still holds its generation.
			c.startRevalidate(effKey, r, next, e)
			return effKey, true
		case reuseValidate:
			c.recordDecision(r, "revalidation_required")
			span.SetString("cache.status", "VALIDATE")
			span.End()
			c.validateAndServe(w, r, next, e, effKey, now)
			return effKey, true
		default:
			if hasHeaderField(r.Header, "Authorization") && !e.SharedAuthReuse {
				c.recordDecision(r, "auth_reuse_restricted")
			}
			return effKey, false
		}
	}
	return effKey, false
}

func (c *Cache) serveDownstreamWithStrip(w http.ResponseWriter, r *http.Request, next http.Handler) {
	pol := c.Policy()
	if !pol.StripTagHeaders || pol.TagHeaderCount == 0 {
		next.ServeHTTP(w, r)
		return
	}
	tagHeaders := pol.TagHeaderSlice()
	sw := &statusWriter{
		ResponseWriter:  w,
		stripTagHeaders: true,
		tagHeaders:      tagHeaders,
	}
	next.ServeHTTP(respwriter.Wrap(sw, w), r)
	if !sw.wroteHeader && !sw.hijacked {
		stripConfiguredTagHeaders(w.Header(), tagHeaders)
	}
}

// bypass forwards the request with the untouched writer and stores nothing.
func (c *Cache) bypass(w http.ResponseWriter, r *http.Request, next http.Handler, span tracing.Span) {
	span.SetString("cache.status", stateBypass)
	span.End()
	w.Header().Set("X-Cache", stateBypass)
	c.serveDownstreamWithStrip(w, r, next)
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
	if hasHeaderField(r.Header, "Authorization") && !e.SharedAuthReuse {
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
		c.serveDownstreamWithStrip(w, r, next)
		return
	}
	pol := c.Policy()
	tagHeaders := pol.TagHeaderSlice()
	strip := pol.StripTagHeaders && pol.TagHeaderCount > 0
	sw := &statusWriter{
		ResponseWriter:  w,
		stripTagHeaders: strip,
		tagHeaders:      tagHeaders,
	}
	next.ServeHTTP(respwriter.Wrap(sw, w), r)
	if !sw.wroteHeader && !sw.hijacked && strip {
		stripConfiguredTagHeaders(w.Header(), tagHeaders)
	}
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
	c.fetchAndStoreWithPolicy(w, r, next, now, c.Policy(), nil)
}

func (c *Cache) fetchAndStoreWithPolicy(w http.ResponseWriter, r *http.Request, next http.Handler, now time.Time, pol CachePolicy, onAbandon func()) bool {
	opID, startGen := c.beginInflightOriginOp()
	defer c.endInflightOriginOp(opID)

	var tagSkipReason string
	cw := &cacheWriter{
		ResponseWriter:  w,
		limit:           pol.MaxEntryBytes,
		memLimit:        pol.MemoryCapBytes(),
		memBudgetLimit:  pol.MemoryMaxBytes,
		disk:            c.disk,
		acct:            c.newCaptureAccount(),
		onAbandon:       onAbandon,
		tagHeaders:      pol.TagHeaderSlice(),
		stripTagHeaders: pol.StripTagHeaders,
	}
	cw.acct.onFailure = func() { c.recordDecision(r, "disk_capture_failed") }
	cw.onDecision = func(reason string) { c.recordDecision(r, reason) }
	cw.acct.onSkip = func(reason string) { c.captureSkipped(reason); c.recordDecision(r, "capture_"+reason) }
	cw.headerCheck = func(code int, h http.Header) bool {
		if pol.TagHeaderCount > 0 {
			if _, _, skip := extractOriginTags(h, pol.TagHeaderSlice()); skip != "" {
				tagSkipReason = skip
				return false
			}
		}
		reason := c.storageReason(r, code, h, c.clock(), pol)
		if reason != "" {
			c.recordDecision(r, reason)
		}
		return reason == ""
	}
	defer cw.releaseCapture()
	w.Header().Set("X-Cache", stateMiss)
	// Captured after X-Cache is set, so that field cancels out of the stored
	// entry the same way any other outer-layer pre-set field does (#332),
	// rather than needing its own denylist entry in buildEntry.
	cw.entrySnapshot = cloneHeader(w.Header())
	next.ServeHTTP(respwriter.Wrap(cw, w), r)
	if !cw.wroteHeader && !cw.hijacked {
		cw.snapshot = headerDifference(w.Header(), cw.entrySnapshot)
		if cw.headerCheck != nil && !cw.headerCheck(http.StatusOK, cw.snapshot) {
			cw.dropCapture()
		}
		if cw.stripTagHeaders && len(cw.tagHeaders) > 0 {
			stripOriginTagHeadersFromResponse(w.Header(), cw.entrySnapshot, cw.tagHeaders)
		}
	}

	if tagSkipReason != "" {
		c.recordDecision(r, "tag_capture_rejected")
		c.tagSkipped(tagSkipReason)
		return false
	}
	if !cw.storable() || cw.writeErr != nil || r.Context().Err() != nil {
		c.recordDecision(r, "capture_not_stored")
		return false
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
	if e := c.buildEntryFromCapture(r, cw.status, h, cw.buf.Bytes(), cw.bodyLen, cw.diskStream != nil, now, pol); e != nil {
		stored := c.storeCapturedWithStartGen(key(r), r, e, cw.diskStream, startGen, true)
		return stored
	}
	return false
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

	origReq := r.Clone(ctx)
	req := conditionalRequest(r.Clone(ctx), stale)
	opID, startGen := c.beginInflightOriginOp()

	go func() {
		defer release()
		defer c.endInflightOriginOp(opID)
		c.revalidate(ctx, k, req, origReq, next, stale, call, startGen)
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
	req.Header.Del("Range")
	req.Header.Del("If-Range")
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
func (c *Cache) revalidate(ctx context.Context, k revalidateKey, req, origReq *http.Request, next http.Handler, stale *Entry, call *revalidateCall, startGen uint64) {
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

	pol := c.Policy()
	rec := c.newRecorder(pol)
	c.observeRecorder(rec, req)
	defer rec.releaseCapture()
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
		refreshed, action, reason := c.merge304Decision(stale, rec.header, now)
		if reason != "" {
			c.recordDecision(req, reason)
		}
		switch action {
		case mergeReplace:
			if c.setIfNotFenced(k.key, refreshed, startGen, stale.Tags) {
				c.delOneIfGenAtMost(k.key, startGen)
				call.finish(nil, outcomeUncacheable, nil)
				c.observeRevalidation(outcomeUncacheable)
				return
			}
			call.finish(refreshed, outcomeNotModified, nil)
			c.observeRevalidation(outcomeNotModified)
		case mergeDiscard:
			// The 304 changed the representation's keying or made it
			// unstorable; dropping the pre-validation generation is the only safe outcome.
			c.deleteAtMostGen(k.key, startGen)
			call.finish(nil, outcomeUncacheable, nil)
			c.observeRevalidation(outcomeUncacheable)
		}

	case rec.status >= 500:
		// Upstream error during revalidation: extend the stale-if-error window
		// so the entry stays servable while the backend recovers. The published
		// entry is replaced by a clone, never edited in place.
		if sie := c.staleOnErrorWindow(stale); sie > 0 {
			c.recordDecision(req, "stale_if_error")
			refreshed := stale.Clone()
			refreshed.StaleUntil = now.Add(sie)
			markStaleIfError(refreshed)
			if !c.setIfNotFenced(k.key, refreshed, startGen, stale.Tags) {
				c.recordDecision(req, "stale_if_error_extended")
				call.finish(refreshed, outcomeOriginError, nil)
				c.observeRevalidation(outcomeOriginError)
				return
			}
		}
		call.finish(nil, outcomeOriginError, nil)
		c.observeRevalidation(outcomeOriginError)

	case sensitiveVaryHeader(rec.header):
		c.recordDecision(origReq, "vary_sensitive_rejected")
		c.deleteAtMostGen(k.key, startGen)
		call.finish(nil, outcomeUncacheable, nil)
		c.observeRevalidation(outcomeUncacheable)

	case !rec.storable():
		c.recordDecision(req, "capture_not_stored")
		call.finish(nil, outcomeUncacheable, nil)
		c.observeRevalidation(outcomeUncacheable)

	default:
		e := c.buildEntryFromCapture(req, rec.status, rec.header, rec.body.Bytes(), rec.bodyLen, rec.diskStream != nil, now, pol)
		if !c.captureMatchesRequest(e, origReq) || !c.storeCapturedWithStartGen(key(req), req, e, rec.diskStream, startGen, true) {
			call.finish(nil, outcomeUncacheable, nil)
			c.observeRevalidation(outcomeUncacheable)
			return
		}
		call.finish(e, outcomeStored, nil)
		c.observeRevalidation(outcomeStored)
	}
}

func (c *Cache) newRecorder(pol CachePolicy) *recorder {
	return &recorder{
		header:          http.Header{},
		limit:           pol.MaxEntryBytes,
		memLimit:        pol.MemoryCapBytes(),
		memBudgetLimit:  pol.MemoryMaxBytes,
		disk:            c.disk,
		acct:            c.newCaptureAccount(),
		tagHeaders:      pol.TagHeaderSlice(),
		stripTagHeaders: pol.StripTagHeaders,
	}
}

// buildEntry constructs a cacheable Entry, or nil if the response must not be
// stored.
func (c *Cache) buildEntry(r *http.Request, status int, h http.Header, body []byte, now time.Time) *Entry {
	return c.buildEntryWithPolicy(r, status, h, body, now, c.Policy())
}

// storageReason is the capture admission owner; callers record its actual
// rejection without reconstructing policy from a later missing cache entry.
func (c *Cache) storageReason(r *http.Request, status int, h http.Header, now time.Time, pol CachePolicy) string {
	p := parseResponsePolicy(h)
	if _, _, ok := c.freshnessWithPolicy(status, h, p, now, pol); !ok {
		switch {
		case p.NoStore:
			return "response_no_store"
		case p.Private:
			return "response_private"
		case hasHeaderField(h, "Set-Cookie"):
			return "response_set_cookie"
		default:
			return "response_policy_rejected"
		}
	}
	if hasHeaderField(r.Header, "Authorization") && !p.sharedAuthStorable() {
		return "auth_storage_restricted"
	}
	if sensitiveVaryHeader(h) {
		return "vary_sensitive_rejected"
	}
	for _, name := range varyFields(h) {
		if name == "*" {
			return "vary_star"
		}
	}
	return ""
}

func (c *Cache) buildEntryWithPolicy(r *http.Request, status int, h http.Header, body []byte, now time.Time, pol CachePolicy) *Entry {
	return c.buildEntryFromCapture(r, status, h, body, int64(len(body)), false, now, pol)
}

func (c *Cache) buildEntryFromCapture(r *http.Request, status int, h http.Header, body []byte, bodyLen int64, streamBacked bool, now time.Time, pol CachePolicy) *Entry {
	if sensitiveVaryHeader(h) {
		c.recordDecision(r, "vary_sensitive_rejected")
		return nil
	}
	if r.Method != http.MethodHead {
		if cl := declaredLength(h); cl >= 0 && bodyLen != cl {
			c.recordDecision(r, "capture_incomplete")
			return nil
		}
	}
	var parsedTags []string
	if pol.TagHeaderCount > 0 {
		var skipReason string
		parsedTags, _, skipReason = extractOriginTags(h, pol.TagHeaderSlice())
		if skipReason != "" {
			c.recordDecision(r, "tag_capture_rejected")
			c.tagSkipped(skipReason)
			return nil
		}
	}
	p := parseResponsePolicy(h)
	ttl, swr, ok := c.freshnessWithPolicy(status, h, p, now, pol)
	if !ok {
		c.recordDecision(r, c.storageReason(r, status, h, now, pol))
		return nil
	}
	// A response produced FOR an authenticated request enters the shared cache
	// only with the origin's explicit permission. See sharedAuthStorable for why
	// this is stricter than the §3.5 reuse rule.
	if hasHeaderField(r.Header, "Authorization") && !p.sharedAuthStorable() {
		c.recordDecision(r, "auth_storage_restricted")
		return nil
	}

	stored := cloneHeader(h)
	removeHopByHop(stored)
	if pol.StripTagHeaders && pol.TagHeaderCount > 0 {
		stripConfiguredTagHeaders(stored, pol.TagHeaderSlice())
	}

	// created is when the origin generated the representation, not when Jul
	// received it: RFC 9111 §4.2.3 corrected initial age. Without it a response
	// that spent time in an upstream cache would be served for its full TTL
	// again here, and the Age header Jul reports would understate its real age.
	created := now.Add(-initialAge(h, now))
	if !hasHeaderField(stored, "Date") {
		// RFC 9110 §6.6.1: a caching recipient with a clock MUST append a Date
		// header field with the time the message was received when one is absent.
		stored.Set("Date", now.UTC().Format(http.TimeFormat))
	}

	var copiedBody []byte
	if !streamBacked {
		copiedBody = append([]byte(nil), body...)
	}

	e := &Entry{
		Status:                 status,
		Header:                 stored,
		Body:                   copiedBody,
		CreatedAt:              created,
		ExpiresAt:              created.Add(ttl),
		StaleUntil:             created.Add(ttl + swr),
		ETag:                   h.Get("ETag"),
		LastModified:           h.Get("Last-Modified"),
		RequiresValidation:     p.NoCache,
		MustRevalidate:         p.revalidationRequired(),
		SharedAuthReuse:        p.sharedAuthReuse(),
		StaleIfError:           p.SIE,
		HasStaleIfError:        p.HasSIE,
		Tags:                   parsedTags,
		ingestedTagHeaderCount: pol.TagHeaderCount,
		ingestedTagHeaders:     pol.TagHeaders,
		tagPolicyTracked:       true,
	}
	if vary := varyFields(h); len(vary) > 0 {
		for _, name := range vary {
			if name == "*" {
				c.recordDecision(r, "vary_star")
				// Vary: * means the response is not reusable for any other
				// request; do not store it.
				return nil
			}
		}
		e.Vary = vary
		e.VaryValues = make(map[string]string, len(vary))
		for _, name := range vary {
			e.VaryValues[name] = varyRequestValue(r.Header, name)
		}
	}
	e.Evidence = c.entryEvidence(status, h, p, pol, now, ttl, swr)
	return e
}

// initialAge is RFC 9111 §4.2.3 corrected initial age, clamped to be
// non-negative. A clock skewed the other way (a Date in the future, an Age
// larger than the response has existed) must not make an entry look younger
// than the moment it arrived.
func initialAge(h http.Header, now time.Time) time.Duration {
	var apparent time.Duration
	for _, value := range h.Values("Date") {
		if d, err := http.ParseTime(value); err == nil && now.Sub(d) > apparent {
			apparent = now.Sub(d)
		}
	}
	age := apparent
	if value := parseAge(h.Values("Age")); value > age {
		age = value
	}
	return age
}

// serve writes a cached entry to the client, honoring conditional requests.
// For disk-backed entries it acquires a reader lease before committing headers
// and streams the body in fixed 32 KiB chunks without loading the whole object
// into memory.
func (c *Cache) serve(w http.ResponseWriter, r *http.Request, e *Entry, state string, now time.Time) bool {
	modified := !notModified(r, e)
	var bodyReader BodyReader
	if modified && r.Method != http.MethodHead && e.diskBody != nil {
		var err error
		bodyReader, err = e.OpenBody()
		if err != nil {
			c.recordDecision(r, "disk_read_failed")
			return false
		}
		defer func() { _ = bodyReader.Close() }()
	}

	age := int(now.Sub(e.CreatedAt).Seconds())
	if age < 0 {
		age = 0
	}
	// Every successful reuse path, including conditional and range responses,
	// passes here after acquiring any required disk reader.
	c.recordReuseDecision(r, e, state)

	if modified && r.Method == http.MethodGet && e.Status == http.StatusOK {
		if spec, isRange := rangeSpecFromRequest(r); isRange {
			if e.BodyLen() == 0 {
				// RFC 9110 §14.1.2 & §14.2: a zero-length representation has no
				// addressable byte offsets (and a non-zero suffix-length such as
				// bytes=-1 is satisfiable with 0 bytes, which cannot be formatted
				// as a valid byte-range-resp first-last/0). Ignore Range for an
				// empty representation and serve the complete 0-byte 200 OK.
				c.observeRange(rangeOutcomeHit)
			} else if hasHeaderField(r.Header, "If-Range") && !ifRangeMatches(r, e) {
				c.observeRange(rangeOutcomeIfRangeFull)
			} else {
				c.serveRangeResponse(w, e, spec, bodyReader, state, age)
				return true
			}
		}
	}

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
	if pol := c.Policy(); pol.StripTagHeaders && pol.TagHeaderCount > 0 {
		stripConfiguredTagHeaders(h, pol.TagHeaderSlice())
	}
	h.Set("Age", strconv.Itoa(age))
	h.Set("X-Cache", state)

	if !modified {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		if bodyReader != nil {
			_, _ = copyWithPooledBuffer(w, bodyReader)
		} else {
			_, _ = w.Write(e.Body)
		}
	}
	return true
}

// freshness decides whether a response is cacheable and for how long.
//
// TTL precedence is s-maxage → max-age → Expires → [cache] default_ttl. The
// first explicit directive wins; a directive present with an unusable argument
// resolves to zero, which makes the response uncacheable rather than silently
// falling through to a longer lifetime.
func (c *Cache) freshness(status int, h http.Header, p responsePolicy, now time.Time) (ttl, swr time.Duration, ok bool) {
	return c.freshnessWithPolicy(status, h, p, now, c.Policy())
}

func (c *Cache) freshnessWithPolicy(status int, h http.Header, p responsePolicy, now time.Time, pol CachePolicy) (ttl, swr time.Duration, ok bool) {
	ttl, swr, _, ok = c.freshnessDecision(status, h, p, now, pol)
	return ttl, swr, ok
}

func (c *Cache) freshnessDecision(status int, h http.Header, p responsePolicy, now time.Time, pol CachePolicy) (ttl, swr time.Duration, source string, ok bool) {
	if !cacheableStatus[status] {
		return 0, 0, "", false
	}
	if len(h.Values("ETag")) > 1 || len(h.Values("Last-Modified")) > 1 {
		// Validators are singleton fields. Selecting the first of conflicting
		// values could validate a different representation on a later 304.
		return 0, 0, "", false
	}
	if p.NoStore {
		return 0, 0, "", false
	}
	if p.Private {
		// Jul is a shared cache; a private response belongs to one user agent.
		return 0, 0, "", false
	}
	if hasHeaderField(h, "Set-Cookie") {
		// Conservative shared-cache rule: a Set-Cookie is per-client state, and
		// replaying it to another client is a session-fixation vector. Origins
		// that genuinely want a cookie-bearing response shared must say so with
		// an explicit directive, which Jul does not currently honor for this
		// case (recorded as a limitation in docs/cache.md).
		return 0, 0, "", false
	}

	source = "gateway_fallback"
	ttl = pol.DefaultTTL
	switch {
	case p.HasSMaxAge:
		ttl = p.SMaxAge
		source = "origin_s_maxage"
	case p.HasMaxAge:
		ttl = p.MaxAge
		source = "origin_max_age"
	case !p.Targeted:
		if expires := h.Values("Expires"); len(expires) > 0 {
			if len(expires) != 1 {
				return 0, 0, "", false
			}
			expiry, valid := parseCacheDate(expires[0], now)
			if !valid {
				return 0, 0, "", false
			}
			// Expires is absolute, so it is measured against the origin's own
			// Date when there is one; using Jul's clock instead would fold
			// clock skew into the lifetime.
			base := now
			foundDate := false
			for _, value := range h.Values("Date") {
				if d, err := http.ParseTime(value); err == nil && (!foundDate || d.After(base)) {
					base, foundDate = d, true
				}
			}
			ttl = expiry.Sub(base)
			source = "origin_expires"
		}
	}
	if ttl <= 0 && !p.NoCache {
		return 0, 0, "", false
	}

	if p.NoCache {
		// A no-cache response is storable, and its stored copy is still worth
		// keeping even with no usable lifetime: every reuse revalidates, and a
		// 304 then saves the body. Give it the configured default so it is
		// retained and aged like anything else.
		if ttl <= 0 {
			ttl = pol.DefaultTTL
			source = "gateway_fallback_no_cache"
		}
		if ttl <= 0 {
			return 0, 0, "", false
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
	if p.Targeted {
		source = "targeted_" + source
	}
	return ttl, swr, source, true
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
