// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"fmt"
	"jul/internal/cachemeta"
	"jul/internal/config"
	"maps"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Operational bounds for scoped cache preview, invalidation, and selector
// fences (#621, ADR 0027 §3–§5).
const (
	MaxPreviewScanEntries = 10000
	MaxPurgeScanKeys      = 100000
	MaxActiveFences       = 256
	BroadScopeResourceThr = 100
)

// ErrBroadConfirmRequired is returned when an invalidation selects a broad
// scope (host, all, >= 100 matched resources, or truncated) and
// InvalidateOptions.RequireBroadConfirm is true.
var ErrBroadConfirmRequired = errors.New("broad cache invalidation (>= 100 matched resources, truncated scan, host, or all) requires confirm_broad = true")

// ErrFenceCommit reports that durable invalidation intent could not be
// committed. Resident entries are unchanged, but earlier origin publications
// remain fenced because the write may have reached durable storage.
var ErrFenceCommit = errors.New("cache invalidation intent commit failed")

// ErrRetrySnapshotUnavailable prevents a tag retry from forgetting its
// original resource group when neither durable nor retained ownership exists.
var ErrRetrySnapshotUnavailable = errors.New("original tag purge resource snapshot is unavailable; submit a fresh host or all purge with confirm_broad=true and a new idempotency key")

type selectorFence struct {
	gen              uint64
	sel              NormalizedSelector
	active           bool
	remaining        int
	matchedResources map[string]struct{}
}

// SafeResourceSummary is the secret-safe per-resource metadata summary shared
// between #621 previews and #622 inspection (ADR 0027 §6.1). Raw query strings
// and Vary request-header values are never exposed here.
type SafeResourceSummary = cachemeta.SafeResourceSummary

// PreviewOptions configures a non-destructive cache invalidation preview.
type PreviewOptions struct {
	MaxScanEntries int
	MaxSampleItems int
}

// PurgePreview is the result of a non-destructive PreviewScope call.
type PurgePreview struct {
	Selector               NormalizedSelector
	MatchedResources       int
	MatchedRepresentations int
	MemoryEntries          int
	DiskEntries            int
	MatchedBytes           int64
	MemoryBytes            int64
	DiskBytes              int64
	InflightFillsMatched   int
	InflightTagUnknown     bool
	CountAccuracy          string // "exact" or "lower_bound_truncated"
	Truncated              bool
	ScanLimit              int
	ScannedEntries         int
	CacheSequence          uint64
	AsOf                   time.Time
	BroadScope             bool
	TagIngestionEnabled    bool
	Samples                []SafeResourceSummary
}

// InvalidateOptions configures a scoped cache invalidation execution.
type InvalidateOptions struct {
	MaxScanKeys     int
	PreviewSequence uint64
	ReuseFenceGen   uint64
	// ReuseResourceIDs preserves original tag ownership across retries.
	// Nil means unavailable; an empty non-nil slice means zero original targets.
	ReuseResourceIDs    []string
	CrossBootRetry      bool
	BatchSize           int
	RequireBroadConfirm bool
	// CommitFence persists the operation's cutoff before its fence is installed.
	// It runs under genMu so publications cannot cross that boundary, and must
	// not call back into the cache. An error leaves resident entries unchanged
	// and conservatively fences earlier origin work against an uncertain write.
	// Resource IDs are opaque hashes, never raw request URIs. A nil slice means
	// no snapshot is required; an empty non-nil slice records zero tag targets.
	CommitFence func(fenceGen uint64, resourceIDs []string) error
}

// StartupHighWaterGen returns the durable generation high-water mark recovered
// at cache startup from .jul-cache-gen, .jul-cache-purge-ledger, and surviving
// disk entries.
func (c *Cache) StartupHighWaterGen() uint64 {
	if c == nil {
		return 0
	}
	return c.startupHighWaterGen.Load()
}

// InvalidateResult reports the outcome of a scoped cache invalidation.
type InvalidateResult struct {
	Selector                   NormalizedSelector
	State                      string // "succeeded", "partial", "failed", "canceled"
	Outcome                    string // "succeeded", "succeeded_no_matches", "partial_failure", "failed", "canceled", "cache_disabled"
	InvalidationEffective      string // "effective", "incomplete", "uncertain"
	FenceSequence              uint64
	CacheSequenceBefore        uint64
	StateChangedSincePreview   bool
	InvalidatedResources       int
	InvalidatedRepresentations int
	MemoryEntriesRemoved       int
	DiskEntriesRemoved         int
	ActiveReadersRetired       int
	FencedInflightOps          int
	SkippedEntries             int
	FailedEntries              int
	RemainingEntries           int
	StartedAt                  time.Time
	CompletedAt                time.Time
	RecoveryGuidance           string
}

// IndexStats exposes live secondary-index and fence counts so tests and
// benchmarks can prove bounded growth and complete reclamation (ADR 0027 §3).
type IndexStats struct {
	KeyGenEntries     int
	HostsIndexed      int
	BaseKeysIndexed   int
	TagLinks          int
	VariantGroups     int
	ActiveFences      int
	FenceFloorGen     uint64
	InflightOriginOps int
}

// IndexStats returns the current catalog, tag-index, and selector-fence counts.
func (c *Cache) IndexStats() IndexStats {
	if c == nil {
		return IndexStats{}
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	baseCount := 0
	for _, m := range c.byHost {
		baseCount += len(m)
	}
	return IndexStats{
		KeyGenEntries:     len(c.keyGen),
		HostsIndexed:      len(c.byHost),
		BaseKeysIndexed:   baseCount,
		TagLinks:          c.totalTagLinks,
		VariantGroups:     len(c.baseVariants),
		ActiveFences:      len(c.fences),
		FenceFloorGen:     c.fenceFloorGen,
		InflightOriginOps: len(c.inflightOps),
	}
}

func baseKeyOf(key string) string {
	if nul := strings.IndexByte(key, 0x00); nul >= 0 {
		return key[:nul]
	}
	return key
}

func resourceKeyOf(host, reqURI string) string {
	return host + "\n" + reqURI
}

func resourceKeyFromBaseKey(baseKey string) (host, reqURI, resKey string, ok bool) {
	_, host, reqURI, _, ok = parseBaseKeyParts(baseKey)
	if !ok {
		return "", "", "", false
	}
	return host, reqURI, resourceKeyOf(host, reqURI), true
}

func (c *Cache) beginInflightOriginOp() (opID, startGen uint64) {
	if c == nil {
		return 0, 0
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	if c.inflightOps == nil {
		c.inflightOps = make(map[uint64]uint64)
	}
	c.originOpSeq++
	opID = c.originOpSeq
	startGen = c.genSeq.Add(1)
	c.inflightOps[opID] = startGen
	return opID, startGen
}

func (c *Cache) endInflightOriginOp(opID uint64) {
	if c == nil || opID == 0 {
		return
	}
	c.genMu.Lock()
	delete(c.inflightOps, opID)
	c.reclaimFencesLocked()
	c.genMu.Unlock()
}

func (c *Cache) minInflightStartGenLocked() uint64 {
	if len(c.inflightOps) == 0 {
		return math.MaxUint64
	}
	minGen := uint64(math.MaxUint64)
	for _, g := range c.inflightOps {
		if g < minGen {
			minGen = g
		}
	}
	return minGen
}

func (c *Cache) enforceFenceCapLocked() {
	// Coalesce completed fences with identical selector digests first so
	// repeated purges for the same selector do not consume distinct slots.
	if len(c.fences) > 1 {
		dedup := make([]selectorFence, 0, len(c.fences))
		completedIdxByDigest := make(map[[32]byte]int, len(c.fences))
		for _, f := range c.fences {
			if !f.active && f.remaining == 0 {
				d := f.sel.Digest()
				if prevIdx, exists := completedIdxByDigest[d]; exists {
					if f.gen > dedup[prevIdx].gen {
						dedup[prevIdx].gen = f.gen
					}
					if len(f.matchedResources) > 0 {
						if dedup[prevIdx].matchedResources == nil {
							dedup[prevIdx].matchedResources = make(map[string]struct{}, len(f.matchedResources))
						}
						for rk := range f.matchedResources {
							dedup[prevIdx].matchedResources[rk] = struct{}{}
						}
					}
					continue
				}
				completedIdxByDigest[d] = len(dedup)
			}
			dedup = append(dedup, f)
		}
		c.fences = dedup
	}

	for len(c.fences) > MaxActiveFences {
		// Always evict completed fences (!f.active && f.remaining == 0) before
		// touching any active or incomplete fence.
		evictIdx := -1
		for i, f := range c.fences {
			if !f.active && f.remaining == 0 {
				evictIdx = i
				break
			}
		}
		if evictIdx < 0 {
			evictIdx = 0
		}
		evicted := c.fences[evictIdx]
		c.fences = append(c.fences[:evictIdx], c.fences[evictIdx+1:]...)
		if !evicted.active && evicted.remaining == 0 && len(evicted.sel.cutoverRouteScopes) > 0 {
			continue
		}
		evictHost := evicted.sel.Host
		if evictHost != "" && evicted.sel.Kind != SelectorAll {
			if c.hostFenceFloorGen == nil {
				c.hostFenceFloorGen = make(map[string]uint64)
			}
			if evicted.gen > c.hostFenceFloorGen[evictHost] {
				c.hostFenceFloorGen[evictHost] = evicted.gen
			}
			if evicted.active || evicted.remaining > 0 {
				if c.incompleteHostFloorGen == nil {
					c.incompleteHostFloorGen = make(map[string]uint64)
				}
				if c.evictedIncompleteHostGens == nil {
					c.evictedIncompleteHostGens = make(map[string]map[uint64]struct{})
				}
				if c.evictedIncompleteHostGens[evictHost] == nil {
					c.evictedIncompleteHostGens[evictHost] = make(map[uint64]struct{})
				}
				c.evictedIncompleteHostGens[evictHost][evicted.gen] = struct{}{}
				if evicted.gen > c.incompleteHostFloorGen[evictHost] {
					c.incompleteHostFloorGen[evictHost] = evicted.gen
				}
			}
		} else {
			if evicted.gen > c.fenceFloorGen {
				c.fenceFloorGen = evicted.gen
			}
			if (evicted.active || evicted.remaining > 0) && evicted.gen > c.incompleteFloorGen {
				c.incompleteFloorGen = evicted.gen
			}
		}
	}
	maxGen := c.fenceFloorGen
	for _, hg := range c.hostFenceFloorGen {
		if hg > maxGen {
			maxGen = hg
		}
	}
	incompleteMax := c.incompleteFloorGen
	for _, hg := range c.incompleteHostFloorGen {
		if hg > incompleteMax {
			incompleteMax = hg
		}
	}
	for _, f := range c.fences {
		if f.gen > maxGen {
			maxGen = f.gen
		}
		if (f.active || f.remaining > 0) && f.gen > incompleteMax {
			incompleteMax = f.gen
		}
	}
	c.updateFenceAtomicsLocked(maxGen, incompleteMax)
}

func (c *Cache) updateFenceAtomicsLocked(maxGen, incompleteMax uint64) {
	c.latestFenceGen.Store(maxGen)
	c.incompleteFenceGen.Store(incompleteMax)
	if incompleteMax == 0 || c.incompleteFloorGen > 0 || len(c.incompleteHostFloorGen) > 1 {
		c.incompleteFenceHost.Store(nil)
		return
	}
	singleHost := ""
	for h, hg := range c.incompleteHostFloorGen {
		if hg > 0 {
			if h == "" || (singleHost != "" && singleHost != h) {
				c.incompleteFenceHost.Store(nil)
				return
			}
			singleHost = h
		}
	}
	for i := range c.fences {
		f := &c.fences[i]
		if !f.active && f.remaining == 0 {
			continue
		}
		if f.sel.Kind == SelectorAll || f.sel.Host == "" || f.sel.cutoverMatchAnyPort {
			c.incompleteFenceHost.Store(nil)
			return
		}
		if singleHost != "" && singleHost != f.sel.Host {
			c.incompleteFenceHost.Store(nil)
			return
		}
		singleHost = f.sel.Host
	}
	if singleHost != "" {
		hCopy := singleHost
		c.incompleteFenceHost.Store(&hCopy)
	} else {
		c.incompleteFenceHost.Store(nil)
	}
}

func (c *Cache) clearEvictedIncompleteFenceLocked(host string, fenceGen uint64) {
	if host == "" || c.evictedIncompleteHostGens == nil {
		return
	}
	gm := c.evictedIncompleteHostGens[host]
	if gm == nil {
		return
	}
	delete(gm, fenceGen)
	if len(gm) == 0 {
		delete(c.evictedIncompleteHostGens, host)
		if len(c.evictedIncompleteHostGens) == 0 {
			c.evictedIncompleteHostGens = nil
		}
		if c.incompleteHostFloorGen != nil {
			delete(c.incompleteHostFloorGen, host)
			if len(c.incompleteHostFloorGen) == 0 {
				c.incompleteHostFloorGen = nil
			}
		}
		return
	}
	maxG := uint64(0)
	for g := range gm {
		if g > maxG {
			maxG = g
		}
	}
	if c.incompleteHostFloorGen != nil {
		c.incompleteHostFloorGen[host] = maxG
	}
}

func (c *Cache) reclaimFencesLocked() {
	minInflight := c.minInflightStartGenLocked()
	if minInflight == math.MaxUint64 {
		c.hostFenceFloorGen = nil
	} else if len(c.hostFenceFloorGen) > 0 {
		for h, hg := range c.hostFenceFloorGen {
			if minInflight > hg {
				delete(c.hostFenceFloorGen, h)
			}
		}
		if len(c.hostFenceFloorGen) == 0 {
			c.hostFenceFloorGen = nil
		}
	}
	if len(c.fences) == 0 {
		maxGen := c.fenceFloorGen
		for _, hg := range c.hostFenceFloorGen {
			if hg > maxGen {
				maxGen = hg
			}
		}
		incompleteMax := c.incompleteFloorGen
		for _, hg := range c.incompleteHostFloorGen {
			if hg > incompleteMax {
				incompleteMax = hg
			}
		}
		c.updateFenceAtomicsLocked(maxGen, incompleteMax)
		return
	}
	kept := c.fences[:0]
	for _, f := range c.fences {
		if f.active || f.remaining > 0 || minInflight <= f.gen {
			kept = append(kept, f)
		}
	}
	c.fences = kept
	c.enforceFenceCapLocked()
}

func routeScopeFromVariantKey(key string) string {
	idx := strings.Index(key, "\x00route\x1f")
	if idx < 0 {
		return ""
	}
	rest := key[idx+len("\x00route\x1f"):]
	if end := strings.IndexByte(rest, '\x00'); end >= 0 {
		return rest[:end]
	}
	return rest
}

func (c *Cache) keyRouteScopeLocked(key string) string {
	if s := routeScopeFromVariantKey(key); s != "" {
		return s
	}
	if st := c.keyGen[key]; st != nil && st.routeScope != "" {
		return st.routeScope
	}
	return ""
}

func (c *Cache) matchesAnyFenceLocked(entryGen uint64, key, host, reqURI string, ok bool, tags, extraTags []string, onlyIncomplete bool) bool {
	if len(c.fences) == 0 {
		return false
	}
	baseKey := baseKeyOf(key)
	resKey := ""
	if ok {
		resKey = resourceKeyOf(host, reqURI)
	}
	var keyRouteScope string
	var keyRouteScopeResolved bool
	for i := range c.fences {
		f := &c.fences[i]
		if entryGen > f.gen || (onlyIncomplete && !f.active && f.remaining == 0) {
			continue
		}
		if len(f.sel.cutoverRouteScopes) > 0 {
			if !keyRouteScopeResolved {
				keyRouteScope = c.keyRouteScopeLocked(key)
				keyRouteScopeResolved = true
			}
			if keyRouteScope != "" && !f.sel.matchesCutoverRoute(keyRouteScope) {
				continue
			}
		}
		if f.sel.Kind == SelectorKey {
			if key == f.sel.Key || baseKey == f.sel.Key {
				return true
			}
			continue
		}
		if !ok {
			continue
		}
		if f.matchedResources != nil {
			if _, matched := f.matchedResources[resKey]; matched {
				return true
			}
		}
		var resTags []string
		if c.baseIndexedTags != nil {
			resTags = c.baseIndexedTags[resKey]
		}
		effTags := mergeBaseTagSets(tags, extraTags, resTags)
		if f.sel.MatchesResource(host, reqURI, effTags) {
			return true
		}
	}
	return false
}

// isPublicationFencedLocked reports whether an origin fill, revalidation, or
// memory-to-disk overflow whose origin work started at entryGen is barred from
// publishing because a matching invalidation fence was installed at >= entryGen
// (ADR 0027 §3.2).
func (c *Cache) isPublicationFencedLocked(entryGen uint64, key string, tags, extraTags []string) bool {
	if entryGen < c.purgeGen {
		return true
	}
	if c.fenceFloorGen > 0 && entryGen <= c.fenceFloorGen {
		return true
	}
	_, host, reqURI, _, ok := parseBaseKeyParts(key)
	if ok && c.hostFenceFloorGen != nil {
		if hf := c.hostFenceFloorGen[host]; hf > 0 && entryGen <= hf {
			return true
		}
		if bare := stripSelectorHostPort(host); bare != host {
			if hf := c.hostFenceFloorGen[bare]; hf > 0 && entryGen <= hf {
				return true
			}
		}
	}
	return c.matchesAnyFenceLocked(entryGen, key, host, reqURI, ok, tags, extraTags, false)
}

func (c *Cache) isPublicationFenced(entryGen uint64, key string, e *Entry, extraTags []string) bool {
	if c == nil {
		return false
	}
	var tags []string
	if e != nil {
		tags = e.Tags
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.isPublicationFencedLocked(entryGen, key, tags, extraTags)
}

func (c *Cache) isEntryFenced(entryGen uint64, key string, e *Entry) bool {
	return c.isPublicationFenced(entryGen, key, e, nil)
}

// isResidentEntryFencedLocked reports whether an already-stored entry at
// entryGen is fenced by an actively executing or incomplete/canceled purge.
// Completed purges (!f.active && f.remaining == 0) have already removed their
// resident entries and therefore do not require lock acquisition on warm hits.
func (c *Cache) isResidentEntryFencedLocked(entryGen uint64, key string, tags []string) bool {
	if entryGen < c.purgeGen {
		return true
	}
	if c.incompleteFloorGen > 0 && entryGen <= c.incompleteFloorGen {
		return true
	}
	_, host, reqURI, _, ok := parseBaseKeyParts(key)
	if ok && c.incompleteHostFloorGen != nil {
		if ihf := c.incompleteHostFloorGen[host]; ihf > 0 && entryGen <= ihf {
			return true
		}
		if bare := stripSelectorHostPort(host); bare != host {
			if ihf := c.incompleteHostFloorGen[bare]; ihf > 0 && entryGen <= ihf {
				return true
			}
		}
	}
	return c.matchesAnyFenceLocked(entryGen, key, host, reqURI, ok, tags, nil, true)
}

func (c *Cache) isResidentEntryFenced(entryGen uint64, key string, e *Entry) bool {
	if c == nil {
		return false
	}
	var tags []string
	if e != nil {
		tags = e.Tags
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.isResidentEntryFencedLocked(entryGen, key, tags)
}

func (c *Cache) canReserveTagLinksLocked(host, baseKey string, candidateTags []string) bool {
	if len(candidateTags) == 0 {
		return true
	}
	_, _, resKey, ok := resourceKeyFromBaseKey(baseKey)
	if !ok {
		return true
	}
	existing := c.baseIndexedTags[resKey]
	merged := mergeBaseTagSets(existing, candidateTags)
	added := len(merged) - len(existing)
	if added <= 0 {
		return true
	}
	return c.totalTagLinks+added <= MaxTotalTagLinks
}

func (c *Cache) indexKeyEntryLocked(st *keyGenState, e *Entry) {
	if st == nil || e == nil {
		return
	}
	method, host, reqURI, isVar, ok := parseBaseKeyParts(st.key)
	if !ok {
		return
	}
	pol := c.Policy()
	st.method = method
	st.host = host
	st.requestURI = reqURI
	st.isVariant = isVar
	st.baseKey = baseKeyOf(st.key)
	st.isVaryStub = e.IsVaryStub
	st.variants = append([]string(nil), e.Variants...)
	if !e.IsVaryStub {
		st.retainedTags = append([]string(nil), e.Tags...)
		if e.tagPolicyTracked {
			st.ingestedTagHeaderCount = e.ingestedTagHeaderCount
			st.ingestedTagHeaders = e.ingestedTagHeaders
		} else {
			st.ingestedTagHeaderCount = 0
			st.ingestedTagHeaders = [8]string{}
		}
	}
	if pol.TagHeaderCount > 0 && !e.IsVaryStub &&
		st.ingestedTagHeaderCount == pol.TagHeaderCount &&
		st.ingestedTagHeaders == pol.TagHeaders {
		st.tags = append([]string(nil), st.retainedTags...)
	} else {
		st.tags = nil
	}
	st.bodyBytes = e.BodyLen()
	st.createdAt = e.CreatedAt
	st.evidence = e.Evidence
	st.expiresAt = e.ExpiresAt
	st.staleUntil = e.StaleUntil
	st.varyFields = append([]string(nil), e.Vary...)
	st.routeScope = e.RouteScope
	st.indexed = true

	if c.byHost == nil {
		c.byHost = make(map[string]map[string]struct{})
	}
	hm := c.byHost[host]
	if hm == nil {
		hm = make(map[string]struct{})
		c.byHost[host] = hm
	}
	hm[st.baseKey] = struct{}{}
	if method == http.MethodGet || method == http.MethodHead {
		if c.byResourceID == nil {
			c.byResourceID = make(map[string][2]string)
		}
		if st.resourceID == "" {
			if nl := strings.IndexByte(st.baseKey, '\n'); nl > 0 {
				st.resourceID = hashKey(st.baseKey[nl+1:])
			} else {
				st.resourceID = hashKey(resourceKeyOf(host, reqURI))
			}
		}
		bases := c.byResourceID[st.resourceID]
		if method == http.MethodGet {
			bases[0] = st.baseKey
		} else {
			bases[1] = st.baseKey
		}
		c.byResourceID[st.resourceID] = bases
	}

	if isVar {
		if c.baseVariants == nil {
			c.baseVariants = make(map[string]map[string]struct{})
		}
		vm := c.baseVariants[st.baseKey]
		if vm == nil {
			vm = make(map[string]struct{})
			c.baseVariants[st.baseKey] = vm
		}
		vm[st.key] = struct{}{}
	}
	if len(st.tags) > 0 || len(c.baseIndexedTags) > 0 {
		c.refreshBaseTagsLocked(host, st.baseKey)
	}
}

func (c *Cache) unindexKeyEntryLocked(st *keyGenState) {
	if st == nil || !st.indexed {
		return
	}
	st.indexed = false
	host := st.host
	baseKey := st.baseKey
	if st.isVariant && c.baseVariants != nil {
		if vm := c.baseVariants[baseKey]; vm != nil {
			delete(vm, st.key)
			if len(vm) == 0 {
				delete(c.baseVariants, baseKey)
			}
		}
		if bst := c.keyGen[baseKey]; bst != nil && len(bst.variants) > 0 {
			kept := bst.variants[:0]
			for _, vk := range bst.variants {
				if vk != st.key {
					kept = append(kept, vk)
				}
			}
			bst.variants = kept
		}
	}
	if len(st.tags) > 0 || len(c.baseIndexedTags) > 0 {
		c.refreshBaseTagsLocked(host, baseKey)
	}

	hasBase := false
	if bst, ok := c.keyGen[baseKey]; ok && bst != st && (bst.inMem || bst.inDisk || bst.inflight > 0) {
		hasBase = true
	}
	hasVars := c.baseVariants != nil && len(c.baseVariants[baseKey]) > 0
	if !hasBase && !hasVars && c.byHost != nil {
		if hm := c.byHost[host]; hm != nil {
			delete(hm, baseKey)
			if len(hm) == 0 {
				delete(c.byHost, host)
			}
			id := st.resourceID
			if id == "" {
				if nl := strings.IndexByte(baseKey, '\n'); nl > 0 {
					id = hashKey(baseKey[nl+1:])
				} else {
					id = hashKey(resourceKeyOf(host, st.requestURI))
				}
			}
			if bases, ok := c.byResourceID[id]; ok {
				for i, bk := range bases {
					if bk == baseKey {
						bases[i] = ""
					}
				}
				if bases[0] == "" && bases[1] == "" {
					delete(c.byResourceID, id)
				} else {
					c.byResourceID[id] = bases
				}
			}
		}
	}

}

func resourceIDsOf(resources map[string]struct{}) []string {
	ids := make([]string, 0, len(resources))
	for resource := range resources {
		ids = append(ids, hashKey(resource))
	}
	sort.Strings(ids)
	return ids
}

func (c *Cache) resourceBaseKeysLocked(ids []string, maxScan int) (keys []string, truncated bool, total int) {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for _, bk := range c.byResourceID[id] {
			if bk == "" {
				continue
			}
			total++
			if maxScan > 0 && len(keys) >= maxScan {
				return keys, true, total
			}
			keys = append(keys, bk)
		}
	}
	sort.Strings(keys)
	return keys, false, total
}

func (c *Cache) refreshBaseTagsLocked(host, baseKey string) {
	if host == "" || baseKey == "" {
		return
	}
	_, reqURI, resKey, ok := resourceKeyFromBaseKey(baseKey)
	if !ok {
		return
	}
	var sets [][]string
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		bk := keyFor(method, host, reqURI)
		if bst, exists := c.keyGen[bk]; exists && bst.indexed && !bst.isVaryStub && (bst.inMem || bst.inDisk || bst.inflight > 0) && len(bst.tags) > 0 {
			sets = append(sets, bst.tags)
		}
		if c.baseVariants != nil {
			for vk := range c.baseVariants[bk] {
				if vst, exists := c.keyGen[vk]; exists && vst.indexed && (vst.inMem || vst.inDisk || vst.inflight > 0) && len(vst.tags) > 0 {
					sets = append(sets, vst.tags)
				}
			}
		}
	}
	desired := mergeBaseTagSets(sets...)
	old := c.baseIndexedTags[resKey]

	// Remove tags no longer present on resKey.
	for _, t := range old {
		if !containsSortedOrLinear(desired, t) {
			if c.tagIndex != nil {
				if tm := c.tagIndex[host]; tm != nil {
					if bm := tm[t]; bm != nil {
						if _, had := bm[resKey]; had {
							delete(bm, resKey)
							c.totalTagLinks--
							if c.totalTagLinks < 0 {
								c.totalTagLinks = 0
							}
						}
						if len(bm) == 0 {
							delete(tm, t)
						}
					}
					if len(tm) == 0 {
						delete(c.tagIndex, host)
					}
				}
			}
		}
	}

	if len(desired) == 0 {
		if c.baseIndexedTags != nil {
			delete(c.baseIndexedTags, resKey)
		}
		return
	}

	if c.tagIndex == nil {
		c.tagIndex = make(map[string]map[string]map[string]struct{})
	}
	if c.baseIndexedTags == nil {
		c.baseIndexedTags = make(map[string][]string)
	}
	tm := c.tagIndex[host]
	if tm == nil {
		tm = make(map[string]map[string]struct{})
		c.tagIndex[host] = tm
	}
	var indexed []string
	for _, t := range desired {
		bm := tm[t]
		if bm == nil {
			if c.totalTagLinks >= MaxTotalTagLinks {
				continue
			}
			bm = make(map[string]struct{})
			tm[t] = bm
		}
		if _, exists := bm[resKey]; !exists {
			if c.totalTagLinks >= MaxTotalTagLinks {
				continue
			}
			bm[resKey] = struct{}{}
			c.totalTagLinks++
		}
		indexed = append(indexed, t)
	}
	if len(indexed) == 0 {
		delete(c.baseIndexedTags, resKey)
		if len(tm) == 0 {
			delete(c.tagIndex, host)
		}
	} else {
		c.baseIndexedTags[resKey] = indexed
	}
}

func (c *Cache) isPolicyPublicationFenced(startGen uint64, e *Entry) bool {
	if c == nil || e == nil {
		return false
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.isPolicyPublicationFencedLocked(startGen, e)
}

func (c *Cache) isPolicyPublicationFencedLocked(startGen uint64, e *Entry) bool {
	if e == nil {
		return false
	}
	if e.RouteEpoch == incompatibleRouteEpoch {
		return true
	}
	if e.IsVaryStub || e.Evidence == nil {
		return false
	}
	if e.RouteScope != "" {
		if c.routePolicyCommitted {
			curDigest, active := c.routePolicyDigest[e.RouteScope]
			if !active || e.RouteConfigDigest != curDigest {
				return true
			}
		}
		floor := max(c.routePolicyFloor[e.RouteScope], c.routePolicyFloor[routeEpochFloorKey])
		if c.globalPolicyFloorGen > floor {
			floor = c.globalPolicyFloorGen
		}
		if floor > 0 && startGen <= floor {
			curDigest, active := c.routePolicyDigest[e.RouteScope]
			if !active || e.RouteConfigDigest != curDigest {
				return true
			}
			if c.globalPolicyFloorGen > 0 && startGen <= c.globalPolicyFloorGen {
				rc := c.routeConfigs[e.RouteScope]
				if e.Evidence.PolicyID != policyID(c.effectivePolicy(&rc)) {
					return true
				}
			}
		}
		return false
	}
	if c.globalPolicyFloorGen > 0 && startGen <= c.globalPolicyFloorGen {
		return e.Evidence.PolicyID != policyID(c.Policy())
	}
	return false
}

func (c *Cache) commitPolicyAndSyncTags(newPol CachePolicy) {
	if c == nil {
		return
	}
	var retireBases []string
	c.genMu.Lock()
	prevPol := c.Policy()
	if policyID(prevPol) != policyID(newPol) {
		c.globalPolicyFloorGen = c.genSeq.Add(1)
	}
	c.policy.Store(&newPol)
	if newPol.TagHeaderCount == 0 {
		c.tagIndex = nil
		c.baseIndexedTags = nil
		c.totalTagLinks = 0
		for _, st := range c.keyGen {
			st.tags = nil
		}
		c.genMu.Unlock()
		return
	}
	if prevPol.TagHeaderCount == newPol.TagHeaderCount && prevPol.TagHeaders == newPol.TagHeaders {
		c.genMu.Unlock()
		return
	}
	c.tagIndex = nil
	c.baseIndexedTags = nil
	c.totalTagLinks = 0
	seenRetire := make(map[string]struct{})
	for _, st := range c.keyGen {
		if st == nil || !st.indexed || st.isVaryStub || (!st.inMem && !st.inDisk) {
			continue
		}
		if st.ingestedTagHeaderCount == newPol.TagHeaderCount && st.ingestedTagHeaders == newPol.TagHeaders {
			st.tags = append([]string(nil), st.retainedTags...)
			c.refreshBaseTagsLocked(st.host, st.baseKey)
		} else {
			st.tags = nil
			if _, ok := seenRetire[st.baseKey]; !ok {
				seenRetire[st.baseKey] = struct{}{}
				retireBases = append(retireBases, st.baseKey)
			}
		}
	}
	c.genMu.Unlock()
	for _, bk := range retireBases {
		c.deleteAtMostGen(bk, math.MaxUint64)
	}
}

func (c *Cache) onDiskRehydrated(items []diskItemSnapshot) {
	if c == nil {
		return
	}
	var retireKeys []string
	c.genMu.Lock()
	if c.keyGen == nil {
		c.keyGen = make(map[string]*keyGenState)
	}
	if c.routeEpoch == nil {
		c.routeEpoch = make(map[string]uint64)
	}
	if c.routePolicyDigest == nil {
		c.routePolicyDigest = make(map[string][16]byte)
	}
	pol := c.Policy()
	diskSeen := make(map[string]struct{}, len(items))
	seenRetire := make(map[string]struct{})
	for _, it := range items {
		if it.Key == "" {
			continue
		}
		if it.Gen > c.genSeq.Load() {
			c.genSeq.Store(it.Gen)
		}
		if !it.IsVaryStub && pol.TagHeaderCount > 0 {
			if !it.TagPolicyTracked || it.TagHeaderCount != pol.TagHeaderCount || it.TagHeaders != pol.TagHeaders {
				for _, rk := range []string{it.Key, baseKeyOf(it.Key)} {
					if _, ok := seenRetire[rk]; !ok {
						seenRetire[rk] = struct{}{}
						retireKeys = append(retireKeys, rk)
					}
				}
				continue
			}
		}
		diskSeen[it.Key] = struct{}{}
		st, ok := c.keyGen[it.Key]
		if !ok {
			st = &keyGenState{key: it.Key, gen: it.Gen}
			c.keyGen[it.Key] = st
		} else if it.Gen > st.gen {
			st.gen = it.Gen
		}
		if it.RouteScope != "" {
			if it.RouteEpoch > c.routeEpoch[it.RouteScope] {
				c.routeEpoch[it.RouteScope] = it.RouteEpoch
			}
			if _, exists := c.routePolicyDigest[it.RouteScope]; !exists {
				c.routePolicyDigest[it.RouteScope] = it.RouteConfigDigest
			}
		}
		st.inDisk = true
		st.diskBytes = it.FileSize
		e := &Entry{
			IsVaryStub:             it.IsVaryStub,
			Variants:               it.Variants,
			Tags:                   it.Tags,
			CreatedAt:              it.CreatedAt,
			Evidence:               it.Evidence,
			ExpiresAt:              it.ExpiresAt,
			StaleUntil:             it.StaleUntil,
			Vary:                   it.VaryFields,
			RouteScope:             it.RouteScope,
			tagPolicyTracked:       it.TagPolicyTracked,
			ingestedTagHeaders:     it.TagHeaders,
			ingestedTagHeaderCount: it.TagHeaderCount,
		}
		c.indexKeyEntryLocked(st, e)
		st.retainedTags = append([]string(nil), it.Tags...)
		st.ingestedTagHeaderCount = it.TagHeaderCount
		st.ingestedTagHeaders = it.TagHeaders
		st.bodyBytes = it.BodyLen
	}
	for k, st := range c.keyGen {
		if _, ok := diskSeen[k]; !ok {
			st.inDisk = false
			st.diskBytes = 0
			c.maybeReclaimLocked(st)
		}
	}
	for _, rk := range retireKeys {
		if st := c.keyGen[rk]; st != nil {
			for _, vk := range st.variants {
				if _, ok := seenRetire[vk]; !ok {
					seenRetire[vk] = struct{}{}
					retireKeys = append(retireKeys, vk)
				}
			}
		}
	}
	if len(c.routeEpoch) > 0 || c.routeMatchers != nil {
		snap := &routeStateSnapshot{
			committed: false,
			epochs:    maps.Clone(c.routeEpoch),
			matchers:  maps.Clone(c.routeMatchers),
			backends:  maps.Clone(c.routeBackends),
		}
		c.routeStateSnap.Store(snap)
	}
	c.genMu.Unlock()
	for _, rk := range retireKeys {
		c.deleteAtMostGen(rk, math.MaxUint64)
	}
}

// expandResourceKeysToBaseKeys converts a slice of logical resource keys
// ("host\nreqURI") into the resident GET and HEAD base keys ("GET\nhost\nreqURI",
// "HEAD\nhost\nreqURI") present in c.byHost[host] or c.keyGen.
func (c *Cache) expandResourceKeysToBaseKeysLocked(resKeys []string) []string {
	if len(resKeys) == 0 {
		return nil
	}
	baseKeys := make([]string, 0, len(resKeys)*2)
	for _, rk := range resKeys {
		nl := strings.IndexByte(rk, '\n')
		if nl <= 0 {
			continue
		}
		host := rk[:nl]
		reqURI := rk[nl+1:]
		hm := c.byHost[host]
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			bk := keyFor(method, host, reqURI)
			if hm != nil {
				if _, ok := hm[bk]; ok {
					baseKeys = append(baseKeys, bk)
					continue
				}
			}
			if _, ok := c.keyGen[bk]; ok {
				baseKeys = append(baseKeys, bk)
			}
		}
	}
	sort.Strings(baseKeys)
	return baseKeys
}

// candidateBaseKeysLocked returns a bounded, deduplicated slice of base keys to
// evaluate for sel. When maxScan > 0, collection stops after scanning maxScan
// candidate entries so preview/execution never copies or sorts an unbounded
// host catalog under genMu (F13).
func (c *Cache) candidateBaseKeysLocked(sel NormalizedSelector, maxScan int) (baseKeys []string, scanned int, truncated bool, totalCandidates int) {
	return c.candidateBaseKeysSortedLocked(sel, maxScan, true)
}

func (c *Cache) candidateBaseKeysForExecLocked(sel NormalizedSelector, maxScan int) (baseKeys []string, scanned int, truncated bool, totalCandidates int) {
	return c.candidateBaseKeysSortedLocked(sel, maxScan, false)
}

func (c *Cache) candidateBaseKeysSortedLocked(sel NormalizedSelector, maxScan int, sortKeys bool) (baseKeys []string, scanned int, truncated bool, totalCandidates int) {
	switch sel.Kind {
	case SelectorKey:
		return []string{baseKeyOf(sel.Key)}, 1, false, 1
	case SelectorExactURL:
		return []string{
			keyFor(http.MethodGet, sel.Host, sel.URI),
			keyFor(http.MethodHead, sel.Host, sel.URI),
		}, 2, false, 2
	case SelectorTag:
		if c.tagIndex == nil {
			return nil, 0, false, 0
		}
		tm := c.tagIndex[sel.Host]
		if len(tm) == 0 || len(sel.Tags) == 0 {
			return nil, 0, false, 0
		}
		var resKeys []string
		if sel.TagMatch == TagMatchAll {
			firstMap := tm[sel.Tags[0]]
			if len(firstMap) == 0 {
				return nil, 0, false, 0
			}
			totalCandidates = len(firstMap)
			for rk := range firstMap {
				if maxScan > 0 && scanned >= maxScan {
					truncated = true
					break
				}
				scanned++
				allMatch := true
				for i := 1; i < len(sel.Tags); i++ {
					if _, ok := tm[sel.Tags[i]][rk]; !ok {
						allMatch = false
						break
					}
				}
				if allMatch {
					resKeys = append(resKeys, rk)
				}
			}
			baseKeys = c.expandResourceKeysToBaseKeysLocked(resKeys)
			return baseKeys, scanned, truncated, totalCandidates
		}
		seen := make(map[string]struct{})
		for _, t := range sel.Tags {
			totalCandidates += len(tm[t])
		}
		for _, t := range sel.Tags {
			for rk := range tm[t] {
				if _, ok := seen[rk]; ok {
					continue
				}
				if maxScan > 0 && scanned >= maxScan {
					truncated = true
					break
				}
				seen[rk] = struct{}{}
				scanned++
				resKeys = append(resKeys, rk)
			}
			if truncated {
				break
			}
		}
		baseKeys = c.expandResourceKeysToBaseKeysLocked(resKeys)
		return baseKeys, scanned, truncated, totalCandidates
	case SelectorHost, SelectorPrefix, SelectorGlob:
		if c.byHost == nil {
			return nil, 0, false, 0
		}
		if sel.cutoverMatchAnyPort {
			var maps []map[string]struct{}
			for h, hm := range c.byHost {
				if len(hm) > 0 && sel.matchesHost(h) {
					maps = append(maps, hm)
				}
			}
			return collectBoundedHostKeysSorted(maps, maxScan, sortKeys)
		}
		if len(c.byHost[sel.Host]) == 0 {
			return nil, 0, false, 0
		}
		return collectBoundedHostKeysSorted([]map[string]struct{}{c.byHost[sel.Host]}, maxScan, sortKeys)
	case SelectorAll:
		var maps []map[string]struct{}
		for _, hm := range c.byHost {
			maps = append(maps, hm)
		}
		return collectBoundedHostKeysSorted(maps, maxScan, sortKeys)
	default:
		return nil, 0, false, 0
	}
}

func collectBoundedHostKeysSorted(maps []map[string]struct{}, maxScan int, sortKeys bool) (baseKeys []string, scanned int, truncated bool, totalCandidates int) {
	for _, hm := range maps {
		totalCandidates += len(hm)
	}
	if totalCandidates == 0 {
		return nil, 0, false, 0
	}
	capHint := totalCandidates
	if maxScan > 0 && capHint > maxScan {
		capHint = maxScan
	}
	baseKeys = make([]string, 0, capHint)
	for _, hm := range maps {
		for bk := range hm {
			if maxScan > 0 && scanned >= maxScan {
				truncated = true
				if sortKeys {
					sort.Strings(baseKeys)
				}
				return baseKeys, scanned, truncated, totalCandidates
			}
			scanned++
			baseKeys = append(baseKeys, bk)
		}
	}
	if sortKeys {
		sort.Strings(baseKeys)
	}
	return baseKeys, scanned, truncated, totalCandidates
}

func (c *Cache) collectOwnedKeysForBaseLocked(baseKey string, exactKeyFilter string) (memKeys, diskKeys []string, repCount int, bodyBytes, memBytes, diskBytes int64, stBase *keyGenState, live bool) {
	memKeys, diskKeys, _, repCount, bodyBytes, memBytes, diskBytes, stBase, live = c.collectOwnedKeysForBaseLockedWithFence(baseKey, exactKeyFilter, 0)
	return
}

func (c *Cache) countMatchingInflightLocked(sel NormalizedSelector) (matched int, tagUnknown bool) {
	return c.visitMatchingInflightLocked(sel, false)
}

func (c *Cache) detachMatchingInflightLocked(sel NormalizedSelector) int {
	n, _ := c.visitMatchingInflightLocked(sel, true)
	return n
}

func (c *Cache) visitMatchingInflightLocked(sel NormalizedSelector, detach bool) (matched int, tagUnknown bool) {
	c.fillMu.Lock()
	for fk := range c.fills {
		if len(sel.cutoverRouteScopes) > 0 {
			if rScope := routeScopeFromVariantKey(fk); rScope != "" && !sel.matchesCutoverRoute(rScope) {
				continue
			}
		}
		base := baseKeyOf(fk)
		_, host, reqURI, _, ok := parseBaseKeyParts(base)
		if !ok {
			continue
		}
		hit := false
		switch sel.Kind {
		case SelectorKey:
			hit = fk == sel.Key || base == baseKeyOf(sel.Key)
		case SelectorTag:
			if host == sel.Host {
				tagUnknown = true
				hit = detach
			}
		default:
			hit = sel.MatchesResource(host, reqURI, nil)
		}
		if hit {
			if detach {
				delete(c.fills, fk)
			}
			matched++
		}
	}
	c.fillMu.Unlock()

	c.reMu.Lock()
	for rk := range c.calls {
		if len(sel.cutoverRouteScopes) > 0 && rk.route != "" && !sel.matchesCutoverRoute(rk.route) {
			continue
		}
		base := baseKeyOf(rk.key)
		_, host, reqURI, _, ok := parseBaseKeyParts(base)
		if !ok {
			continue
		}
		resKey := resourceKeyOf(host, reqURI)
		var tags []string
		if c.baseIndexedTags != nil {
			tags = c.baseIndexedTags[resKey]
		}
		hit := false
		switch sel.Kind {
		case SelectorKey:
			hit = rk.key == sel.Key || base == baseKeyOf(rk.key)
		case SelectorTag:
			if sel.MatchesResource(host, reqURI, tags) {
				hit = true
			} else if host == sel.Host {
				tagUnknown = true
				hit = detach && len(tags) == 0
			}
		default:
			hit = sel.MatchesResource(host, reqURI, tags)
		}
		if hit {
			if detach {
				delete(c.calls, rk)
			}
			matched++
		}
	}
	c.reMu.Unlock()
	return matched, tagUnknown
}

// PreviewScope performs a bounded, non-destructive dry-run evaluation of sel
// against the live cache catalog (ADR 0027 §5.2).
func (c *Cache) PreviewScope(sel NormalizedSelector, opts PreviewOptions) PurgePreview {
	out, _ := c.previewScope(context.Background(), sel, opts, 50, false)
	return out
}

func (c *Cache) previewScope(ctx context.Context, sel NormalizedSelector, opts PreviewOptions, maxSamples int, details bool) (PurgePreview, error) {
	now := time.Now().UTC()
	if c != nil {
		now = c.clock().UTC()
	}
	scanLimit := opts.MaxScanEntries
	if scanLimit <= 0 || scanLimit > MaxPreviewScanEntries {
		scanLimit = MaxPreviewScanEntries
	}
	sampleLimit := opts.MaxSampleItems
	if sampleLimit < 0 {
		sampleLimit = 0
	}
	if sampleLimit > maxSamples {
		sampleLimit = maxSamples
	}
	if c == nil {
		return PurgePreview{
			Selector:      sel,
			CountAccuracy: "exact",
			ScanLimit:     scanLimit,
			AsOf:          now,
			BroadScope:    sel.BroadScope,
		}, nil
	}

	pol := c.Policy()
	c.genMu.Lock()
	defer c.genMu.Unlock()

	out := PurgePreview{
		Selector:            sel,
		CountAccuracy:       "exact",
		ScanLimit:           scanLimit,
		CacheSequence:       c.genSeq.Load(),
		AsOf:                now,
		BroadScope:          sel.BroadScope,
		TagIngestionEnabled: pol.TagHeaderCount > 0,
	}

	candidates, _, candTrunc, _ := c.candidateBaseKeysLocked(sel, scanLimit)
	if candTrunc {
		out.Truncated = true
		out.CountAccuracy = "lower_bound_truncated"
	}
	type resourceAgg struct {
		host       string
		uri        string
		methods    []string
		reps       int
		inMem      bool
		inDisk     bool
		bodyBytes  int64
		varyFields []string
		tags       []string
		createdAt  time.Time
		expiresAt  time.Time
		staleUntil time.Time
		bases      []string
	}
	resources := make(map[string]*resourceAgg)
	var resourceOrder []string

	exactFilter := ""
	if sel.Kind == SelectorKey {
		exactFilter = sel.Key
	}

	for _, bk := range candidates {
		if err := ctx.Err(); err != nil {
			return PurgePreview{}, err
		}
		if out.ScannedEntries >= scanLimit {
			out.Truncated = true
			out.CountAccuracy = "lower_bound_truncated"
			break
		}
		out.ScannedEntries++
		method, host, reqURI, _, ok := parseBaseKeyParts(bk)
		if !ok {
			continue
		}
		resKey := resourceKeyOf(host, reqURI)
		var tags []string
		if c.baseIndexedTags != nil {
			tags = c.baseIndexedTags[resKey]
		}
		if (details || sel.Kind != SelectorKey) && !sel.MatchesResource(host, reqURI, tags) {
			continue
		}
		if sel.Kind == SelectorKey && bk != baseKeyOf(sel.Key) {
			continue
		}
		memKeys, diskKeys, reps, bodyB, memB, diskB, stBase, live := c.collectOwnedKeysForBaseLocked(bk, exactFilter)
		if !live {
			continue
		}
		if details {
			if sel.inspectionRouteScope != "" {
				filterRouteKeys := func(in []string) []string {
					var kept []string
					for _, k := range in {
						st := c.keyGen[k]
						if st == nil || st.isVaryStub {
							continue
						}
						if st.routeScope != "" && st.routeScope != sel.inspectionRouteScope {
							continue
						}
						kept = append(kept, k)
					}
					return kept
				}
				memKeys = filterRouteKeys(memKeys)
				diskKeys = filterRouteKeys(diskKeys)
				if len(memKeys) == 0 && len(diskKeys) == 0 {
					continue
				}
			}
			seen := map[string]bool{}
			var repBytes int64
			for _, ks := range [][]string{memKeys, diskKeys} {
				for _, k := range ks {
					if st := c.keyGen[k]; st != nil && !st.isVaryStub {
						if !seen[k] {
							seen[k] = true
							repBytes += st.bodyBytes
						}
					}
				}
			}
			reps = len(seen)
			if sel.inspectionRouteScope != "" {
				bodyB = repBytes
			}
		}
		out.MatchedRepresentations += reps
		out.MemoryEntries += len(memKeys)
		out.DiskEntries += len(diskKeys)
		out.MatchedBytes += bodyB
		out.MemoryBytes += memB
		out.DiskBytes += diskB

		agg, exists := resources[resKey]
		if !exists {
			agg = &resourceAgg{
				host: host,
				uri:  reqURI,
				tags: tags,
			}
			if stBase != nil {
				agg.varyFields = stBase.varyFields
				agg.createdAt = stBase.createdAt
				agg.expiresAt = stBase.expiresAt
				agg.staleUntil = stBase.staleUntil
			}
			resources[resKey] = agg
			resourceOrder = append(resourceOrder, resKey)
		}
		agg.bases = append(agg.bases, bk)
		agg.methods = append(agg.methods, strings.Clone(method))
		agg.reps += reps
		agg.bodyBytes += bodyB
		if len(memKeys) > 0 {
			agg.inMem = true
		}
		if len(diskKeys) > 0 {
			agg.inDisk = true
		}
	}

	out.MatchedResources = len(resources)
	if out.MatchedResources >= BroadScopeResourceThr || out.Truncated {
		out.BroadScope = true
	}
	if !details {
		out.InflightFillsMatched, out.InflightTagUnknown = c.countMatchingInflightLocked(sel)
	}

	if sampleLimit > 0 && len(resourceOrder) > 0 {
		if len(resourceOrder) > sampleLimit {
			resourceOrder = resourceOrder[:sampleLimit]
		}
		out.Samples = make([]SafeResourceSummary, 0, len(resourceOrder))
		for _, rk := range resourceOrder {
			agg := resources[rk]
			sort.Strings(agg.methods)
			var tiers []string
			if agg.inMem {
				tiers = append(tiers, "memory")
			}
			if agg.inDisk {
				tiers = append(tiers, "disk")
			}
			freshState := "fresh"
			if !agg.expiresAt.IsZero() && !now.Before(agg.expiresAt) {
				if now.Before(agg.staleUntil) {
					freshState = "stale"
				} else {
					freshState = "expired"
				}
			}
			sm := SafeResourceSummary{
				Host:            strings.Clone(agg.host),
				Path:            strings.Clone(stripQueryFromURI(agg.uri)),
				HasQuery:        strings.IndexByte(agg.uri, '?') >= 0,
				Methods:         agg.methods,
				Representations: agg.reps,
				Tiers:           tiers,
				BodyBytes:       agg.bodyBytes,
				VaryFields:      agg.varyFields,
				Tags:            agg.tags,
				FreshnessState:  freshState,
				CreatedAt:       agg.createdAt,
				ExpiresAt:       agg.expiresAt,
				StaleUntil:      agg.staleUntil,
			}
			if details {
				c.inspectDetailsLocked(&sm, agg.bases, sel.inspectionRouteScope, now)
			}
			out.Samples = append(out.Samples, sm)
		}
	}
	return out, ctx.Err()
}

func (c *Cache) snapshotMatchedResourcesLocked(sel NormalizedSelector) map[string]struct{} {
	if sel.Kind == SelectorAll || sel.Kind == SelectorKey {
		return nil
	}
	candidates, _, _, _ := c.candidateBaseKeysLocked(sel, MaxPurgeScanKeys)
	if len(candidates) == 0 {
		return nil
	}
	var matched map[string]struct{}
	for _, bk := range candidates {
		_, host, reqURI, _, ok := parseBaseKeyParts(bk)
		if !ok {
			continue
		}
		resKey := resourceKeyOf(host, reqURI)
		var tags []string
		if c.baseIndexedTags != nil {
			tags = c.baseIndexedTags[resKey]
		}
		if sel.MatchesResource(host, reqURI, tags) {
			if matched == nil {
				matched = make(map[string]struct{})
			}
			matched[resKey] = struct{}{}
		}
	}
	return matched
}

// BeginSelectorFence installs an active selector fence at a new monotonic
// generation and returns the fence sequence and a completion function
// (reusable contract for #624, ADR 0027 §6.2).
func (c *Cache) BeginSelectorFence(sel NormalizedSelector) (fenceGen uint64, complete func()) {
	fenceGen, complete, _ = c.BeginSelectorFenceWithError(sel)
	return fenceGen, complete
}

// ExecuteRouteCutover derives and executes scoped invalidations for route or
// backend changes between before and after configurations that opt into
// cutover invalidation (#624 / CACHE-OPS-04, ADR 0030 §4).
func (c *Cache) ExecuteRouteCutover(ctx context.Context, before, after *config.Config, overrideMode string) ([]InvalidateResult, error) {
	if c == nil || before == nil || after == nil {
		return nil, nil
	}
	sels := DeriveRouteCutoverSelectorsForPolicy(before, after, overrideMode)
	if len(sels) == 0 {
		return nil, nil
	}
	results := make([]InvalidateResult, 0, len(sels))
	var firstErr error
	for _, sel := range sels {
		res, err := c.InvalidateScope(ctx, sel, InvalidateOptions{})
		results = append(results, res)
		if err != nil && firstErr == nil {
			firstErr = err
		} else if res.InvalidationEffective != "effective" && firstErr == nil {
			firstErr = fmt.Errorf("cache cutover invalidation %s (%s): %s", sel.Kind, sel.Host, res.InvalidationEffective)
		}
	}
	return results, firstErr
}

// BeginSelectorFenceWithError installs an active selector fence at a new
// monotonic generation, persists the generation high-water mark when a disk
// tier is configured, and returns any durable marker persistence error alongside
// the fence sequence and completion callback (ADR 0027 §6.2).
func (c *Cache) BeginSelectorFenceWithError(sel NormalizedSelector) (fenceGen uint64, complete func(), err error) {
	if c == nil {
		return 0, func() {}, nil
	}
	c.genMu.Lock()
	fenceGen = c.genSeq.Add(1)
	matchedRes := c.snapshotMatchedResourcesLocked(sel)
	c.fences = append(c.fences, selectorFence{
		gen:              fenceGen,
		sel:              sel,
		active:           true,
		matchedResources: matchedRes,
	})
	c.enforceFenceCapLocked()
	c.detachMatchingInflightLocked(sel)
	c.genMu.Unlock()
	if c.disk != nil {
		if hwErr := c.disk.recordHighWaterGen(fenceGen); hwErr != nil {
			c.disk.unrecoverableInvalidation.Store(true)
			err = hwErr
		}
	}

	var done bool
	return fenceGen, func() {
		c.genMu.Lock()
		defer c.genMu.Unlock()
		if done {
			return
		}
		done = true
		for i := range c.fences {
			if c.fences[i].gen == fenceGen {
				c.fences[i].active = false
				break
			}
		}
		c.clearEvictedIncompleteFenceLocked(sel.Host, fenceGen)
		c.reclaimFencesLocked()
	}, err
}

// InvalidateScope executes a scoped cache invalidation with linearization
// fencing, conditional generation deletion across memory and disk tiers, and
// truthful partial/failed/canceled outcome reporting (ADR 0027 §3–§5).
func (c *Cache) InvalidateScope(ctx context.Context, sel NormalizedSelector, opts InvalidateOptions) (InvalidateResult, error) {
	started := time.Now().UTC()
	if c != nil {
		started = c.clock().UTC()
	}
	if c == nil {
		return InvalidateResult{
			Selector:              sel,
			State:                 "succeeded",
			Outcome:               "cache_disabled",
			InvalidationEffective: "effective",
			StartedAt:             started,
			CompletedAt:           started,
		}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	canceledBeforeExecution := func(err error) (InvalidateResult, error) {
		return InvalidateResult{
			Selector:              sel,
			State:                 "canceled",
			Outcome:               "canceled",
			InvalidationEffective: "incomplete",
			FenceSequence:         opts.ReuseFenceGen,
			StartedAt:             started,
			CompletedAt:           c.clock().UTC(),
			RecoveryGuidance:      "The invalidation request was canceled before execution started. Retry the operation.",
		}, err
	}
	if err := ctx.Err(); err != nil {
		return canceledBeforeExecution(err)
	}

	maxScan := opts.MaxScanKeys
	if maxScan <= 0 || maxScan > MaxPurgeScanKeys {
		maxScan = MaxPurgeScanKeys
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 512
	}

	c.genMu.Lock()
	if err := ctx.Err(); err != nil {
		c.genMu.Unlock()
		return canceledBeforeExecution(err)
	}
	if opts.RequireBroadConfirm && sel.BroadScope {
		c.genMu.Unlock()
		return InvalidateResult{}, ErrBroadConfirmRequired
	}
	seqBefore := c.genSeq.Load()
	changedSincePreview := opts.PreviewSequence > 0 && seqBefore != opts.PreviewSequence

	// Enforce scan bound for narrow selectors (prefix, glob, tag, or route-scoped host);
	// whole-hostname (kind = 'host' with no RouteID) and kind = 'all' are the
	// documented escape paths and are never rejected by maxScan (F13).
	isUnboundedHost := sel.Kind == SelectorHost && sel.RouteID == ""
	isRetryFence := opts.ReuseFenceGen > 0
	isFastAll := sel.Kind == SelectorAll && !isRetryFence && len(sel.cutoverRouteScopes) == 0
	isTagRetry := isRetryFence && sel.Kind == SelectorTag
	resourceIDs := opts.ReuseResourceIDs
	if isRetryFence {
		if opts.CrossBootRetry && opts.ReuseFenceGen > c.startupHighWaterGen.Load() {
			c.genMu.Unlock()
			return InvalidateResult{}, fmt.Errorf("cannot safely continue interrupted or uncertain purge fence %d across restart because durable high-water mark (%d) is lower; submit a fresh purge request", opts.ReuseFenceGen, c.startupHighWaterGen.Load())
		}
		for _, f := range c.fences {
			if f.gen != opts.ReuseFenceGen {
				continue
			}
			if f.sel.Digest() != sel.Digest() {
				c.genMu.Unlock()
				return InvalidateResult{}, fmt.Errorf("reuse_fence_gen %d selector digest does not match requested selector", opts.ReuseFenceGen)
			}
			if isTagRetry && resourceIDs == nil && f.matchedResources != nil {
				resourceIDs = resourceIDsOf(f.matchedResources)
			}
			break
		}
		if isTagRetry && resourceIDs == nil {
			c.genMu.Unlock()
			return InvalidateResult{}, ErrRetrySnapshotUnavailable
		}
	}
	var candidates []string
	if !isFastAll {
		scanCap := 0
		if !isUnboundedHost && sel.Kind != SelectorAll {
			scanCap = maxScan
		}
		var candTrunc bool
		var totalCand int
		if isTagRetry {
			candidates, candTrunc, totalCand = c.resourceBaseKeysLocked(resourceIDs, scanCap)
		} else {
			candidates, _, candTrunc, totalCand = c.candidateBaseKeysForExecLocked(sel, scanCap)
		}
		if !isUnboundedHost && sel.Kind != SelectorAll && (candTrunc || len(candidates) > maxScan) {
			c.genMu.Unlock()
			return InvalidateResult{}, fmt.Errorf("cache selector scope (%d candidate resources) exceeds maximum scan bound of %d; use kind = 'host' or 'all'", totalCand, maxScan)
		}
	}

	type matchedGroup struct {
		resKey       string
		keyStart     int
		keyEnd       int
		reps         int
		isPrimaryRes bool
	}
	var groups []matchedGroup
	var flatKeys []string
	var matchedResSet map[string]struct{}
	matchedResCount := 0
	hasDiskTargets := false
	skipped := 0
	exactFilter := ""
	if sel.Kind == SelectorKey {
		exactFilter = sel.Key
	}

	evalFenceGen := uint64(math.MaxUint64)
	if opts.ReuseFenceGen > 0 {
		evalFenceGen = opts.ReuseFenceGen
	}

	if !isFastAll {
		useResMap := sel.Kind == SelectorTag || len(candidates) <= 2048
		var countedResSet map[string]struct{}
		if useResMap && len(candidates) > 0 {
			matchedResSet = make(map[string]struct{}, min(len(candidates), 2048))
			countedResSet = make(map[string]struct{}, min(len(candidates), 2048))
		}
		if len(candidates) > 0 {
			groups = make([]matchedGroup, 0, len(candidates))
			flatKeys = make([]string, 0, len(candidates))
		}
		for _, bk := range candidates {
			method, host, reqURI, _, ok := parseBaseKeyParts(bk)
			if !ok {
				skipped++
				continue
			}
			nl := strings.IndexByte(bk, '\n')
			resKey := bk[nl+1:]
			var tags []string
			if c.baseIndexedTags != nil {
				tags = c.baseIndexedTags[resKey]
			}
			if isTagRetry && !sel.matchesHost(host) {
				c.genMu.Unlock()
				return InvalidateResult{}, fmt.Errorf("original tag purge resource snapshot does not match the selector host")
			}
			if !isTagRetry && sel.Kind != SelectorKey && !sel.MatchesResource(host, reqURI, tags) {
				skipped++
				continue
			}
			if sel.Kind == SelectorKey && bk != baseKeyOf(sel.Key) {
				skipped++
				continue
			}
			keyStart := len(flatKeys)
			reps, hasDisk, liveOrInflight := c.collectOwnedKeysIntoForCutoverLocked(bk, exactFilter, sel.cutoverRouteScopes, evalFenceGen, &flatKeys)
			if !liveOrInflight {
				skipped++
				continue
			}
			if hasDisk {
				hasDiskTargets = true
			}
			if useResMap {
				matchedResSet[resKey] = struct{}{}
			}
			isPrimary := false
			if reps > 0 {
				isPrimary = true
				if useResMap {
					if _, seen := countedResSet[resKey]; seen {
						isPrimary = false
					} else {
						countedResSet[resKey] = struct{}{}
						matchedResCount++
					}
				} else {
					if method == http.MethodHead {
						getBK := "GET\n" + resKey
						if hm := c.byHost[host]; hm != nil {
							if _, hasGet := hm[getBK]; hasGet && c.hasResidentBaseLocked(getBK, exactFilter, evalFenceGen) {
								isPrimary = false
							}
						}
					}
					if isPrimary {
						matchedResCount++
					}
				}
			}
			groups = append(groups, matchedGroup{
				resKey:       resKey,
				keyStart:     keyStart,
				keyEnd:       len(flatKeys),
				reps:         reps,
				isPrimaryRes: isPrimary,
			})
		}
		if opts.RequireBroadConfirm && max(len(matchedResSet), matchedResCount) >= BroadScopeResourceThr {
			c.genMu.Unlock()
			return InvalidateResult{}, ErrBroadConfirmRequired
		}
	}

	if sel.Kind == SelectorTag && !isRetryFence {
		resourceIDs = resourceIDsOf(matchedResSet)
	}
	if err := ctx.Err(); err != nil {
		c.genMu.Unlock()
		return canceledBeforeExecution(err)
	}
	var fenceGen uint64
	if opts.ReuseFenceGen > 0 {
		fenceGen = opts.ReuseFenceGen
		if fenceGen > c.genSeq.Load() {
			c.genSeq.Store(fenceGen)
		}
	} else {
		fenceGen = c.genSeq.Add(1)
	}
	if opts.CommitFence != nil {
		if err := opts.CommitFence(fenceGen, resourceIDs); err != nil {
			foundFence := false
			for _, f := range c.fences {
				if f.gen == fenceGen {
					foundFence = true
					break
				}
			}
			if !foundFence {
				c.fences = append(c.fences, selectorFence{
					gen: fenceGen, sel: sel, matchedResources: matchedResSet,
				})
			}
			c.reclaimFencesLocked()
			c.genMu.Unlock()
			return InvalidateResult{}, fmt.Errorf("%w: %w", ErrFenceCommit, err)
		}
	}
	if opts.ReuseFenceGen > 0 {
		if sel.Kind == SelectorAll && len(sel.cutoverRouteScopes) == 0 {
			if fenceGen > c.purgeGen {
				c.purgeGen = fenceGen
			}
			if fenceGen > c.fenceFloorGen {
				c.fenceFloorGen = fenceGen
			}
		}
		foundFence := false
		for i := range c.fences {
			if c.fences[i].gen == fenceGen {
				c.fences[i].active = true
				if len(matchedResSet) > 0 {
					if c.fences[i].matchedResources == nil {
						c.fences[i].matchedResources = make(map[string]struct{}, len(matchedResSet))
					}
					for rk := range matchedResSet {
						c.fences[i].matchedResources[rk] = struct{}{}
					}
				}
				foundFence = true
				break
			}
		}
		if !foundFence {
			c.fences = append(c.fences, selectorFence{
				gen:              fenceGen,
				sel:              sel,
				active:           true,
				matchedResources: matchedResSet,
			})
		}
	} else {
		c.fences = append(c.fences, selectorFence{
			gen:              fenceGen,
			sel:              sel,
			active:           true,
			matchedResources: matchedResSet,
		})
	}
	c.enforceFenceCapLocked()
	fencedOps := c.detachMatchingInflightLocked(sel)

	if isFastAll {
		c.purgeGen = fenceGen
		c.fenceFloorGen = fenceGen
		c.hostFenceFloorGen = nil
		c.incompleteFloorGen = 0
		c.incompleteHostFloorGen = nil
		c.evictedIncompleteHostGens = nil
		c.fences = nil
		c.latestFenceGen.Store(fenceGen)
		c.incompleteFenceGen.Store(0)
		c.incompleteFenceHost.Store(nil)

		resourceCount := 0
		for _, hm := range c.byHost {
			for bk := range hm {
				if strings.HasPrefix(bk, "GET\n") {
					resourceCount++
				} else if strings.HasPrefix(bk, "HEAD\n") {
					if _, hasGet := hm["GET\n"+bk[5:]]; !hasGet {
						resourceCount++
					}
				}
			}
		}
		memCount := 0
		var diskKeys []string
		if c.disk != nil {
			diskKeys = make([]string, 0, min(len(c.keyGen), 4096))
		}
		repCount := 0
		for k, st := range c.keyGen {
			if st.gen > fenceGen {
				continue
			}
			if c.disk != nil && (st.inDisk || st.inflight > 0) {
				diskKeys = append(diskKeys, k)
			}
			if !st.inMem && !st.inDisk {
				continue
			}
			if st.inMem {
				memCount++
			}
			if !st.isVaryStub {
				repCount++
			}
		}
		c.byHost = nil
		c.byResourceID = nil
		c.baseVariants = nil
		c.tagIndex = nil
		c.baseIndexedTags = nil
		c.totalTagLinks = 0
		c.keyGen = make(map[string]*keyGenState)
		c.mem.purge()
		c.genMu.Unlock()

		var diskRemoved, retiredReaders, diskFailed int
		var uncertain bool
		if c.disk != nil {
			c.disk.mu.Lock()
			for h, el := range c.disk.items {
				it := el.Value.(*diskItem)
				if it.gen <= fenceGen {
					if it.key != "" {
						diskKeys = append(diskKeys, it.key)
					} else if hk := c.disk.hashKeys[h]; hk != "" {
						diskKeys = append(diskKeys, hk)
					}
				}
			}
			c.disk.mu.Unlock()

			intentCommitted := false
			if len(diskKeys) > 0 {
				if err := c.disk.recordPurgeIntent(diskPurgeIntent{FenceGen: fenceGen, Kind: SelectorAll}); err != nil {
					uncertain = true
				} else {
					intentCommitted = true
				}
			}
			hwErr := c.disk.recordHighWaterGen(fenceGen)
			if c.onAfterFenceInstall != nil {
				c.onAfterFenceInstall(sel, fenceGen)
			}
			var batchUncertain bool
			diskRemoved, retiredReaders, diskFailed, batchUncertain = c.disk.deleteBatchAtMostGen(diskKeys, fenceGen)
			uncertain = uncertain || batchUncertain
			pending, repairUncertain := c.disk.reconcileInvalidation()
			diskFailed = max(diskFailed, pending)
			uncertain = uncertain || repairUncertain
			if intentCommitted && diskFailed == 0 && !uncertain {
				if err := c.disk.completePurgeIntent(fenceGen, true); err != nil {
					uncertain = true
				}
			}
			if hwErr != nil {
				uncertain = true
				if diskFailed > 0 || repairUncertain || c.disk.hasSurvivingCacheState() {
					c.disk.unrecoverableInvalidation.Store(true)
				}
			}
		} else if c.onAfterFenceInstall != nil {
			c.onAfterFenceInstall(sel, fenceGen)
		}
		res := InvalidateResult{
			Selector:                   sel,
			FenceSequence:              fenceGen,
			CacheSequenceBefore:        seqBefore,
			StateChangedSincePreview:   changedSincePreview,
			InvalidatedResources:       resourceCount,
			InvalidatedRepresentations: repCount,
			MemoryEntriesRemoved:       memCount,
			DiskEntriesRemoved:         diskRemoved,
			ActiveReadersRetired:       retiredReaders,
			FencedInflightOps:          fencedOps,
			FailedEntries:              diskFailed,
			StartedAt:                  started,
			CompletedAt:                c.clock().UTC(),
		}
		finalizeInvalidateOutcome(&res, uncertain, false)
		if c.onInvalidate != nil {
			c.onInvalidate(string(sel.Kind), res.Outcome, res.MemoryEntriesRemoved, res.DiskEntriesRemoved)
		}
		return res, nil
	}
	c.genMu.Unlock()

	invalidatedResCount := 0
	invalidatedReps := 0
	memRemoved := 0
	diskRemoved := 0
	retiredReaders := 0
	failedEntries := 0
	remainingGroups := 0
	uncertain := false
	canceledMidway := false

	intentCommitted := false
	if c.disk != nil {
		if hasDiskTargets || len(flatKeys) > 0 {
			intent := c.buildDiskPurgeIntent(fenceGen, sel, resourceIDs, flatKeys)
			if err := c.disk.recordPurgeIntent(intent); err != nil {
				uncertain = true
			} else {
				intentCommitted = true
			}
		}
		if err := c.disk.recordHighWaterGen(fenceGen); err != nil {
			uncertain = true
			if hasDiskTargets {
				c.disk.unrecoverableInvalidation.Store(true)
			}
		}
	}

	if c.onAfterFenceInstall != nil {
		c.onAfterFenceInstall(sel, fenceGen)
	}

	var completedEndIdx int
	for startIdx := 0; startIdx < len(groups); startIdx += batchSize {
		if err := ctx.Err(); err != nil {
			canceledMidway = true
			for i := startIdx; i < len(groups); i++ {
				remainingGroups += groups[i].keyEnd - groups[i].keyStart
			}
			break
		}
		endIdx := startIdx + batchSize
		if endIdx > len(groups) {
			endIdx = len(groups)
		}
		batch := groups[startIdx:endIdx]
		batchKeyStart := batch[0].keyStart
		batchKeyEnd := batch[len(batch)-1].keyEnd
		batchKeys := flatKeys[batchKeyStart:batchKeyEnd]

		c.genMu.Lock()
		for _, g := range batch {
			if g.reps > 0 {
				if g.isPrimaryRes {
					invalidatedResCount++
				}
				invalidatedReps += g.reps
			}
			for _, k := range flatKeys[g.keyStart:g.keyEnd] {
				st := c.keyGen[k]
				if st != nil && st.gen <= fenceGen {
					nextG := c.genSeq.Add(1)
					if nextG <= fenceGen {
						nextG = fenceGen + 1
					}
					st.gen = nextG
				}
				if c.mem.delIfGenAtMost(k, fenceGen) {
					memRemoved++
				}
				if st != nil {
					st.inMem = c.mem.hasKey(k)
					if !st.inMem {
						st.memBytes = 0
					}
					c.maybeReclaimLocked(st)
				}
			}
		}
		c.genMu.Unlock()
		completedEndIdx = endIdx

		if c.disk != nil && len(batchKeys) > 0 {
			dRem, dRet, dFail, dUnc := c.disk.deleteBatchAtMostGen(batchKeys, fenceGen)
			diskRemoved += dRem
			retiredReaders += dRet
			failedEntries += dFail
			if dUnc {
				uncertain = true
			}
		}
	}
	if canceledMidway && completedEndIdx > 0 {
		seenCanceledRes := make(map[string]struct{}, completedEndIdx)
		for i := 0; i < completedEndIdx; i++ {
			if groups[i].reps > 0 {
				seenCanceledRes[groups[i].resKey] = struct{}{}
			}
		}
		invalidatedResCount = len(seenCanceledRes)
	}

	if c.disk != nil {
		pending, repairUncertain := c.disk.reconcileInvalidation()
		failedEntries = max(failedEntries, pending)
		uncertain = uncertain || repairUncertain
		if intentCommitted && remainingGroups == 0 && failedEntries == 0 && !uncertain && !canceledMidway {
			if err := c.disk.completePurgeIntent(fenceGen, sel.Kind == SelectorAll && len(sel.cutoverRouteScopes) == 0); err != nil {
				uncertain = true
			}
		}
	}
	c.genMu.Lock()
	for i := range c.fences {
		if c.fences[i].gen == fenceGen {
			c.fences[i].active = false
			c.fences[i].remaining = remainingGroups + failedEntries
			break
		}
	}
	if remainingGroups+failedEntries == 0 {
		c.clearEvictedIncompleteFenceLocked(sel.Host, fenceGen)
	}
	c.reclaimFencesLocked()
	c.genMu.Unlock()

	res := InvalidateResult{
		Selector:                   sel,
		FenceSequence:              fenceGen,
		CacheSequenceBefore:        seqBefore,
		StateChangedSincePreview:   changedSincePreview,
		InvalidatedResources:       invalidatedResCount,
		InvalidatedRepresentations: invalidatedReps,
		MemoryEntriesRemoved:       memRemoved,
		DiskEntriesRemoved:         diskRemoved,
		ActiveReadersRetired:       retiredReaders,
		FencedInflightOps:          fencedOps,
		SkippedEntries:             skipped,
		FailedEntries:              failedEntries,
		RemainingEntries:           remainingGroups,
		StartedAt:                  started,
		CompletedAt:                c.clock().UTC(),
	}
	finalizeInvalidateOutcome(&res, uncertain, canceledMidway)
	if c.onInvalidate != nil {
		c.onInvalidate(string(sel.Kind), res.Outcome, res.MemoryEntriesRemoved, res.DiskEntriesRemoved)
	}
	if canceledMidway {
		return res, ctx.Err()
	}
	return res, nil
}

func (c *Cache) hasResidentBaseLocked(baseKey, _ string, ownFenceGen uint64) bool {
	if st := c.keyGen[baseKey]; st != nil && (ownFenceGen == 0 || st.gen <= ownFenceGen) && (st.inMem || st.inDisk) {
		return true
	}
	return c.baseVariants != nil && len(c.baseVariants[baseKey]) > 0
}

func (c *Cache) collectOwnedKeysIntoForCutoverLocked(baseKey, exactKeyFilter string, cutoverScopes []string, ownFenceGen uint64, dst *[]string) (repCount int, hasDisk, liveOrInflight bool) {
	matchesScope := func(routeScope string) bool {
		if len(cutoverScopes) == 0 || routeScope == "" {
			return true
		}
		for _, rs := range cutoverScopes {
			if rs == routeScope {
				return true
			}
		}
		return false
	}
	st := c.keyGen[baseKey]
	var vm map[string]struct{}
	if c.baseVariants != nil {
		vm = c.baseVariants[baseKey]
	}
	hasVariants := (st != nil && len(st.variants) > 0) || len(vm) > 0
	if !hasVariants {
		if st == nil || (exactKeyFilter != "" && baseKey != exactKeyFilter) {
			return 0, false, false
		}
		if !matchesScope(st.routeScope) {
			return 0, false, false
		}
		if (ownFenceGen > 0 && st.gen > ownFenceGen) || (ownFenceGen == 0 && c.isResidentEntryFencedLocked(st.gen, baseKey, st.tags)) {
			return 0, false, false
		}
		if !st.inMem && !st.inDisk {
			if ownFenceGen > 0 && st.inflight > 0 {
				*dst = append(*dst, baseKey)
				return 0, true, true
			}
			return 0, false, false
		}
		*dst = append(*dst, baseKey)
		return 1, st.inDisk || st.inflight > 0, true
	}

	startLen := len(*dst)
	live := false
	keptOtherRouteVariant := false
	addVariantKey := func(k string) {
		if exactKeyFilter != "" && k != exactKeyFilter && baseKeyOf(k) != exactKeyFilter {
			return
		}
		for i := startLen; i < len(*dst); i++ {
			if (*dst)[i] == k {
				return
			}
		}
		if len(cutoverScopes) > 0 {
			vScope := c.keyRouteScopeLocked(k)
			if !matchesScope(vScope) {
				if kst := c.keyGen[k]; kst != nil && (kst.inMem || kst.inDisk || kst.inflight > 0) {
					keptOtherRouteVariant = true
				}
				return
			}
		}
		kst := c.keyGen[k]
		if kst == nil {
			return
		}
		if ownFenceGen > 0 {
			if kst.gen > ownFenceGen {
				return
			}
		} else if c.isResidentEntryFencedLocked(kst.gen, k, kst.tags) {
			return
		}
		if !kst.inMem && !kst.inDisk {
			if ownFenceGen > 0 && kst.inflight > 0 {
				*dst = append(*dst, k)
				hasDisk = true
				liveOrInflight = true
			}
			return
		}
		live = true
		liveOrInflight = true
		if kst.inDisk || kst.inflight > 0 {
			hasDisk = true
		}
		*dst = append(*dst, k)
		repCount++
	}

	if st != nil {
		for _, vk := range st.variants {
			addVariantKey(vk)
		}
	}
	for vk := range vm {
		addVariantKey(vk)
	}
	// Only delete the base Vary stub when no other route owns live variants under baseKey
	// and baseKey itself matches the cutover route scope.
	if !keptOtherRouteVariant && (st == nil || matchesScope(st.routeScope)) {
		if st != nil && (exactKeyFilter == "" || baseKey == exactKeyFilter) {
			if (ownFenceGen == 0 || st.gen <= ownFenceGen) && (ownFenceGen > 0 || !c.isResidentEntryFencedLocked(st.gen, baseKey, st.tags)) {
				if st.inMem || st.inDisk || (ownFenceGen > 0 && st.inflight > 0) {
					liveOrInflight = true
					if st.inDisk || st.inflight > 0 {
						hasDisk = true
					}
					if st.inMem || st.inDisk {
						live = true
					}
					*dst = append(*dst, baseKey)
				}
			}
		}
		if live && exactKeyFilter == "" {
			foundBase := false
			for i := startLen; i < len(*dst); i++ {
				if (*dst)[i] == baseKey {
					foundBase = true
					break
				}
			}
			if !foundBase {
				*dst = append(*dst, baseKey)
			}
		}
	}
	if repCount == 0 && live {
		repCount = 1
	}
	return repCount, hasDisk, liveOrInflight
}

func (c *Cache) buildDiskPurgeIntent(fenceGen uint64, sel NormalizedSelector, resourceIDs []string, flatKeys []string) diskPurgeIntent {
	intent := diskPurgeIntent{
		FenceGen:            fenceGen,
		Kind:                sel.Kind,
		Host:                sel.Host,
		ExplicitDefaultPort: sel.ExplicitDefaultPort,
		CutoverMatchAnyPort: sel.cutoverMatchAnyPort,
		Prefix:              sel.Prefix,
		CutoverRawPrefix:    sel.cutoverRawPrefix,
		Pattern:             sel.Pattern,
		CutoverExactPath:    sel.cutoverExactPath,
		Tags:                append([]string(nil), sel.Tags...),
		TagMatch:            sel.TagMatch,
		RoutePath:           sel.inspectionRoutePrefix,
		RouteType:           sel.inspectionRouteExact,
		CutoverRouteScopes:  append([]string(nil), sel.cutoverRouteScopes...),
	}
	if sel.Kind == SelectorTag {
		if len(resourceIDs) > 4096 {
			// When a tag purge spans >4,096 resources, coalesce the on-disk crash
			// recovery intent to the tag's host scope (or all if host is empty)
			// so the serialized intent stays well within the 1 MiB manifest bound (#666).
			if sel.Host != "" {
				intent.Kind = SelectorHost
			} else {
				intent.Kind = SelectorAll
			}
			intent.Tags = nil
			intent.TagMatch = ""
		} else {
			intent.MatchedResourceIDs = append([]string(nil), resourceIDs...)
		}
	}
	if sel.Kind == SelectorExactURL || sel.Kind == SelectorKey {
		vctx := c.versionContext()
		if sel.Host != "" && sel.URI != "" {
			intent.TargetResourceMAC = vctx.Digest("cache-purge-target-v1", []byte(resourceKeyOf(sel.Host, sel.URI)))
		}
		if sel.Kind == SelectorKey && sel.Key != "" {
			intent.TargetKeyMAC = vctx.Digest("cache-purge-target-v1", []byte(sel.Key))
		}
		hashes := make([]string, 0, len(flatKeys)+3)
		seenH := make(map[string]struct{}, len(flatKeys)+3)
		addHash := func(k string) {
			if k == "" {
				return
			}
			h := hashKey(k)
			if _, ok := seenH[h]; !ok {
				seenH[h] = struct{}{}
				hashes = append(hashes, h)
			}
		}
		for _, k := range flatKeys {
			addHash(k)
		}
		switch sel.Kind {
		case SelectorExactURL:
			addHash(keyFor(http.MethodGet, sel.Host, sel.URI))
			addHash(keyFor(http.MethodHead, sel.Host, sel.URI))
		case SelectorKey:
			addHash(sel.Key)
			addHash(baseKeyOf(sel.Key))
		}
		sort.Strings(hashes)
		intent.TargetHashes = hashes
	}
	return intent
}

func (c *Cache) reconcileRecoveredPurgeIntents(intents []diskPurgeIntent) {
	if c == nil || c.disk == nil || len(intents) == 0 {
		return
	}
	vctx := c.versionContext()
	var remaining []diskPurgeIntent

	for _, intent := range intents {
		if intent.FenceGen == 0 {
			continue
		}
		sel := NormalizedSelector{
			Kind:                  intent.Kind,
			Host:                  intent.Host,
			Prefix:                intent.Prefix,
			Pattern:               intent.Pattern,
			Tags:                  append([]string(nil), intent.Tags...),
			TagMatch:              intent.TagMatch,
			ExplicitDefaultPort:   intent.ExplicitDefaultPort,
			cutoverMatchAnyPort:   intent.CutoverMatchAnyPort,
			cutoverRawPrefix:      intent.CutoverRawPrefix,
			cutoverExactPath:      intent.CutoverExactPath,
			cutoverRouteScopes:    append([]string(nil), intent.CutoverRouteScopes...),
			inspectionRoutePrefix: intent.RoutePath,
			inspectionRouteExact:  intent.RouteType,
			BroadScope:            intent.Kind == SelectorAll || intent.Kind == SelectorHost,
		}
		if sel.Kind == SelectorGlob && sel.Pattern != "" && sel.cutoverExactPath == "" {
			if _, segs, err := validateSelectorGlob(sel.Pattern); err == nil {
				sel.globSegments = segs
			}
		}
		sel.digest = computeSelectorDigest(sel)

		targetHashSet := make(map[string]struct{}, len(intent.TargetHashes))
		for _, h := range intent.TargetHashes {
			targetHashSet[h] = struct{}{}
		}
		resIDSet := make(map[string]struct{}, len(intent.MatchedResourceIDs))
		for _, id := range intent.MatchedResourceIDs {
			resIDSet[id] = struct{}{}
		}

		var targetKeys []string
		seenKey := make(map[string]struct{})
		matchedResSet := make(map[string]struct{})

		c.genMu.Lock()
		addGroup := func(bk string) {
			_, _, _ = c.collectOwnedKeysIntoForCutoverLocked(bk, "", sel.cutoverRouteScopes, intent.FenceGen, &targetKeys)
			if nl := strings.IndexByte(bk, '\n'); nl > 0 {
				matchedResSet[bk[nl+1:]] = struct{}{}
			}
		}
		for k, st := range c.keyGen {
			if st == nil || st.gen > intent.FenceGen {
				continue
			}
			_, host, reqURI, _, ok := parseBaseKeyParts(k)
			if !ok {
				continue
			}
			resKey := resourceKeyOf(host, reqURI)
			switch intent.Kind {
			case SelectorAll:
				if len(sel.cutoverRouteScopes) > 0 {
					addGroup(baseKeyOf(k))
				} else {
					if _, seen := seenKey[k]; !seen {
						seenKey[k] = struct{}{}
						targetKeys = append(targetKeys, k)
					}
					matchedResSet[resKey] = struct{}{}
				}
			case SelectorTag:
				var tags []string
				if c.baseIndexedTags != nil {
					tags = c.baseIndexedTags[resKey]
				}
				if _, inSnap := resIDSet[hashKey(resKey)]; (inSnap && sel.matchesHost(host)) || sel.MatchesResource(host, reqURI, tags) {
					addGroup(baseKeyOf(k))
				}
			case SelectorExactURL, SelectorKey:
				kh := hashKey(k)
				bkh := hashKey(baseKeyOf(k))
				_, hMatch := targetHashSet[kh]
				_, bMatch := targetHashSet[bkh]
				_, rMatch := resIDSet[hashKey(resKey)]
				macMatch := (intent.TargetResourceMAC != "" && vctx.Digest("cache-purge-target-v1", []byte(resKey)) == intent.TargetResourceMAC) ||
					(intent.TargetKeyMAC != "" && (vctx.Digest("cache-purge-target-v1", []byte(k)) == intent.TargetKeyMAC || vctx.Digest("cache-purge-target-v1", []byte(baseKeyOf(k))) == intent.TargetKeyMAC))
				if hMatch || bMatch || rMatch || macMatch {
					addGroup(baseKeyOf(k))
				}
			default:
				if sel.MatchesResource(host, reqURI, nil) {
					addGroup(baseKeyOf(k))
				}
			}
		}
		for _, k := range targetKeys {
			st := c.keyGen[k]
			if st != nil && st.gen <= intent.FenceGen {
				nextG := c.genSeq.Add(1)
				if nextG <= intent.FenceGen {
					nextG = intent.FenceGen + 1
				}
				st.gen = nextG
			}
			c.mem.delIfGenAtMost(k, intent.FenceGen)
			if st != nil {
				st.inMem = c.mem.hasKey(k)
				if !st.inMem {
					st.memBytes = 0
				}
				c.maybeReclaimLocked(st)
			}
		}
		c.genMu.Unlock()

		_, _, dFail, dUnc := c.disk.deleteBatchAtMostGen(targetKeys, intent.FenceGen)
		pending, repairUncertain := c.disk.reconcileInvalidation()
		failed := max(dFail, pending)
		uncertain := dUnc || repairUncertain

		if failed == 0 && !uncertain {
			c.genMu.Lock()
			if c.reconciledFenceGens == nil {
				c.reconciledFenceGens = make(map[uint64]bool)
			}
			c.reconciledFenceGens[intent.FenceGen] = true
			c.genMu.Unlock()
		} else {
			remaining = append(remaining, intent)
			c.genMu.Lock()
			c.fences = append(c.fences, selectorFence{
				gen:              intent.FenceGen,
				sel:              sel,
				remaining:        max(failed, 1),
				matchedResources: matchedResSet,
			})
			c.enforceFenceCapLocked()
			c.genMu.Unlock()
		}
	}

	if err := c.disk.replacePurgeIntents(remaining); err != nil {
		c.disk.unrecoverableInvalidation.Store(true)
	}
}

func (c *Cache) collectOwnedKeysForBaseLockedWithFence(baseKey, exactKeyFilter string, ownFenceGen uint64) (memKeys, diskKeys, inflightKeys []string, repCount int, bodyBytes, memBytes, diskBytes int64, stBase *keyGenState, live bool) {
	st := c.keyGen[baseKey]
	var varKeys []string
	seenVar := make(map[string]struct{})
	if st != nil {
		for _, vk := range st.variants {
			if _, ok := seenVar[vk]; !ok {
				seenVar[vk] = struct{}{}
				varKeys = append(varKeys, vk)
			}
		}
	}
	if c.baseVariants != nil {
		for vk := range c.baseVariants[baseKey] {
			if _, ok := seenVar[vk]; !ok {
				seenVar[vk] = struct{}{}
				varKeys = append(varKeys, vk)
			}
		}
	}
	sort.Strings(varKeys)

	addKey := func(k string, isStub bool) {
		if exactKeyFilter != "" && k != exactKeyFilter && baseKeyOf(k) != exactKeyFilter {
			return
		}
		kst := c.keyGen[k]
		if kst == nil {
			return
		}
		if ownFenceGen > 0 {
			if kst.gen > ownFenceGen {
				return
			}
		} else if c.isResidentEntryFencedLocked(kst.gen, k, kst.tags) {
			return
		}
		if !kst.inMem && !kst.inDisk {
			if ownFenceGen > 0 && kst.inflight > 0 {
				inflightKeys = append(inflightKeys, k)
			}
			return
		}
		live = true
		if kst.inMem {
			memKeys = append(memKeys, k)
			memBytes += kst.memBytes
		}
		if kst.inDisk {
			diskKeys = append(diskKeys, k)
			diskBytes += kst.diskBytes
		}
		if !isStub {
			repCount++
			bodyBytes += kst.bodyBytes
		}
	}

	if st != nil {
		stBase = st
		addKey(baseKey, st.isVaryStub || len(varKeys) > 0)
	}
	for _, vk := range varKeys {
		addKey(vk, false)
	}
	if repCount == 0 && live {
		repCount = 1
	}
	return memKeys, diskKeys, inflightKeys, repCount, bodyBytes, memBytes, diskBytes, stBase, live
}

func finalizeInvalidateOutcome(res *InvalidateResult, uncertain, canceled bool) {
	if canceled {
		res.State = "canceled"
		res.Outcome = "canceled"
		if uncertain {
			res.InvalidationEffective = "uncertain"
		} else {
			res.InvalidationEffective = "incomplete"
		}
		res.RecoveryGuidance = "The invalidation was interrupted before all matched disk/memory entries were physically removed. All matching pre-invalidation entries remain fenced in memory; retry the same selector or operation to complete disk cleanup."
		return
	}
	if res.FailedEntries > 0 || uncertain {
		if res.InvalidatedRepresentations > 0 && (res.MemoryEntriesRemoved > 0 || res.DiskEntriesRemoved > 0) {
			res.State = "partial"
			res.Outcome = "partial_failure"
		} else {
			res.State = "failed"
			res.Outcome = "failed"
		}
		if uncertain {
			res.InvalidationEffective = "uncertain"
			res.RecoveryGuidance = "One or more disk cache removals and tombstone writes failed; in-process disk hits are disabled fail-closed. Check [cache].disk_path permissions/free space and retry the purge before restarting."
		} else {
			res.InvalidationEffective = "incomplete"
			res.RecoveryGuidance = "One or more disk file removals failed and were recorded in .jul-cache-tombstones. Retry the purge or inspect [cache].disk_path."
		}
		return
	}
	res.State = "succeeded"
	res.InvalidationEffective = "effective"
	if res.InvalidatedRepresentations == 0 && res.MemoryEntriesRemoved == 0 && res.DiskEntriesRemoved == 0 {
		res.Outcome = "succeeded_no_matches"
	} else {
		res.Outcome = "succeeded"
	}
}
