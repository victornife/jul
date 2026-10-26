// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"errors"
	"fmt"
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
type SafeResourceSummary struct {
	Host            string    `json:"host"`
	Path            string    `json:"path"`
	HasQuery        bool      `json:"has_query,omitempty"`
	Methods         []string  `json:"methods"`
	Representations int       `json:"representations"`
	Tiers           []string  `json:"tiers"`
	BodyBytes       int64     `json:"body_bytes"`
	VaryFields      []string  `json:"vary_fields,omitempty"`
	Tags            []string  `json:"tags,omitempty"`
	FreshnessState  string    `json:"freshness_state"`
	CreatedAt       time.Time `json:"created_at,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	StaleUntil      time.Time `json:"stale_until,omitempty"`
}

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
	MaxScanKeys         int
	PreviewSequence     uint64
	ReuseFenceGen       uint64
	BatchSize           int
	RequireBroadConfirm bool
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
	c.latestFenceGen.Store(maxGen)
	c.incompleteFenceGen.Store(incompleteMax)
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
		c.latestFenceGen.Store(maxGen)
		c.incompleteFenceGen.Store(incompleteMax)
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

func (c *Cache) matchesAnyFenceLocked(entryGen uint64, key, host, reqURI string, ok bool, tags, extraTags []string, onlyIncomplete bool) bool {
	if len(c.fences) == 0 {
		return false
	}
	baseKey := baseKeyOf(key)
	resKey := ""
	if ok {
		resKey = resourceKeyOf(host, reqURI)
	}
	for i := range c.fences {
		f := &c.fences[i]
		if entryGen > f.gen || (onlyIncomplete && !f.active && f.remaining == 0) {
			continue
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
			st.ingestedTagHeaderCount = pol.TagHeaderCount
			st.ingestedTagHeaders = pol.TagHeaders
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
	st.expiresAt = e.ExpiresAt
	st.staleUntil = e.StaleUntil
	st.varyFields = append([]string(nil), e.Vary...)
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
	c.refreshBaseTagsLocked(host, st.baseKey)
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
	}
	c.refreshBaseTagsLocked(host, baseKey)

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
		}
	}
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

func (c *Cache) commitPolicyAndSyncTags(newPol CachePolicy) {
	if c == nil {
		return
	}
	var retireBases []string
	c.genMu.Lock()
	prevPol := c.Policy()
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
	c.genMu.Lock()
	defer c.genMu.Unlock()
	if c.keyGen == nil {
		c.keyGen = make(map[string]*keyGenState)
	}
	pol := c.Policy()
	diskSeen := make(map[string]struct{}, len(items))
	for _, it := range items {
		if it.Key == "" {
			continue
		}
		diskSeen[it.Key] = struct{}{}
		st, ok := c.keyGen[it.Key]
		if !ok {
			st = &keyGenState{key: it.Key, gen: it.Gen}
			c.keyGen[it.Key] = st
		} else if it.Gen > st.gen {
			st.gen = it.Gen
		}
		if it.Gen > c.genSeq.Load() {
			c.genSeq.Store(it.Gen)
		}
		st.inDisk = true
		st.diskBytes = it.FileSize
		e := &Entry{
			IsVaryStub: it.IsVaryStub,
			Variants:   it.Variants,
			Tags:       it.Tags,
			CreatedAt:  it.CreatedAt,
			ExpiresAt:  it.ExpiresAt,
			StaleUntil: it.StaleUntil,
			Vary:       it.VaryFields,
		}
		c.indexKeyEntryLocked(st, e)
		st.retainedTags = append([]string(nil), it.Tags...)
		if pol.TagHeaderCount > 0 {
			st.ingestedTagHeaderCount = pol.TagHeaderCount
			st.ingestedTagHeaders = pol.TagHeaders
		}
		st.bodyBytes = it.BodyLen
	}
	for k, st := range c.keyGen {
		if _, ok := diskSeen[k]; !ok {
			st.inDisk = false
			st.diskBytes = 0
			c.maybeReclaimLocked(st)
		}
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
		if c.byHost == nil || len(c.byHost[sel.Host]) == 0 {
			return nil, 0, false, 0
		}
		return collectBoundedHostKeys([]map[string]struct{}{c.byHost[sel.Host]}, maxScan)
	case SelectorAll:
		var maps []map[string]struct{}
		for _, hm := range c.byHost {
			maps = append(maps, hm)
		}
		return collectBoundedHostKeys(maps, maxScan)
	default:
		return nil, 0, false, 0
	}
}

func collectBoundedHostKeys(maps []map[string]struct{}, maxScan int) (baseKeys []string, scanned int, truncated bool, totalCandidates int) {
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
				sort.Strings(baseKeys)
				return baseKeys, scanned, truncated, totalCandidates
			}
			scanned++
			baseKeys = append(baseKeys, bk)
		}
	}
	sort.Strings(baseKeys)
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
	if sampleLimit > 50 {
		sampleLimit = 50
	}
	if c == nil {
		return PurgePreview{
			Selector:      sel,
			CountAccuracy: "exact",
			ScanLimit:     scanLimit,
			AsOf:          now,
			BroadScope:    sel.BroadScope,
		}
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
	}
	resources := make(map[string]*resourceAgg)
	var resourceOrder []string

	exactFilter := ""
	if sel.Kind == SelectorKey {
		exactFilter = sel.Key
	}

	for _, bk := range candidates {
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
		if sel.Kind != SelectorKey && !sel.MatchesResource(host, reqURI, tags) {
			continue
		}
		if sel.Kind == SelectorKey && bk != baseKeyOf(sel.Key) {
			continue
		}
		memKeys, diskKeys, reps, bodyB, memB, diskB, stBase, live := c.collectOwnedKeysForBaseLocked(bk, exactFilter)
		if !live {
			continue
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
		agg.methods = append(agg.methods, method)
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
	out.InflightFillsMatched, out.InflightTagUnknown = c.countMatchingInflightLocked(sel)

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
			out.Samples = append(out.Samples, SafeResourceSummary{
				Host:            agg.host,
				Path:            stripQueryFromURI(agg.uri),
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
			})
		}
	}
	return out
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
	if c == nil {
		return 0, func() {}
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
		if err := c.disk.recordHighWaterGen(fenceGen); err != nil {
			c.disk.unrecoverableInvalidation.Store(true)
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
	}
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
	if err := ctx.Err(); err != nil {
		return InvalidateResult{
			Selector:              sel,
			State:                 "canceled",
			Outcome:               "canceled",
			InvalidationEffective: "incomplete",
			StartedAt:             started,
			CompletedAt:           c.clock().UTC(),
			RecoveryGuidance:      "The invalidation request was canceled before execution started. Retry the operation.",
		}, err
	}

	maxScan := opts.MaxScanKeys
	if maxScan <= 0 || maxScan > MaxPurgeScanKeys {
		maxScan = MaxPurgeScanKeys
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 64
	}

	c.genMu.Lock()
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
	var candidates []string
	if sel.Kind != SelectorAll {
		scanCap := 0
		if !isUnboundedHost {
			scanCap = maxScan
		}
		var candTrunc bool
		var totalCand int
		candidates, _, candTrunc, totalCand = c.candidateBaseKeysLocked(sel, scanCap)
		if !isUnboundedHost && (candTrunc || len(candidates) > maxScan) {
			c.genMu.Unlock()
			return InvalidateResult{}, fmt.Errorf("cache selector scope (%d candidate resources) exceeds maximum scan bound of %d; use kind = 'host' or 'all'", totalCand, maxScan)
		}
	}

	if opts.ReuseFenceGen > 0 {
		for i := range c.fences {
			if c.fences[i].gen == opts.ReuseFenceGen {
				if c.fences[i].sel.Digest() != sel.Digest() {
					c.genMu.Unlock()
					return InvalidateResult{}, fmt.Errorf("reuse_fence_gen %d selector digest does not match requested selector", opts.ReuseFenceGen)
				}
				break
			}
		}
	}

	type matchedGroup struct {
		resKey   string
		baseKey  string
		allKeys  []string
		diskKeys []string
		reps     int
	}
	var groups []matchedGroup
	matchedResSet := make(map[string]struct{})
	skipped := 0
	exactFilter := ""
	if sel.Kind == SelectorKey {
		exactFilter = sel.Key
	}

	evalFenceGen := opts.ReuseFenceGen
	if evalFenceGen == 0 {
		evalFenceGen = math.MaxUint64
	}

	if sel.Kind != SelectorAll {
		for _, bk := range candidates {
			_, host, reqURI, _, ok := parseBaseKeyParts(bk)
			if !ok {
				skipped++
				continue
			}
			resKey := resourceKeyOf(host, reqURI)
			var tags []string
			if c.baseIndexedTags != nil {
				tags = c.baseIndexedTags[resKey]
			}
			if sel.Kind != SelectorKey && !sel.MatchesResource(host, reqURI, tags) {
				skipped++
				continue
			}
			if sel.Kind == SelectorKey && bk != baseKeyOf(sel.Key) {
				skipped++
				continue
			}
			matchedResSet[resKey] = struct{}{}
			memKeys, diskKeys, inflightKeys, reps, _, _, _, _, live := c.collectOwnedKeysForBaseLockedWithFence(bk, exactFilter, evalFenceGen)
			if !live && len(inflightKeys) == 0 {
				skipped++
				continue
			}
			keySet := make(map[string]struct{}, len(memKeys)+len(diskKeys)+len(inflightKeys)+1)
			var allKeys []string
			for _, slice := range [][]string{memKeys, diskKeys, inflightKeys} {
				for _, k := range slice {
					if _, exists := keySet[k]; !exists {
						keySet[k] = struct{}{}
						allKeys = append(allKeys, k)
					}
				}
			}
			if _, exists := keySet[bk]; !exists && exactFilter == "" {
				allKeys = append(allKeys, bk)
			}
			groups = append(groups, matchedGroup{
				resKey:   resKey,
				baseKey:  bk,
				allKeys:  allKeys,
				diskKeys: diskKeys,
				reps:     reps,
			})
		}
		if opts.RequireBroadConfirm && len(matchedResSet) >= BroadScopeResourceThr {
			c.genMu.Unlock()
			return InvalidateResult{}, ErrBroadConfirmRequired
		}
	}

	var fenceGen uint64
	if opts.ReuseFenceGen > 0 {
		fenceGen = opts.ReuseFenceGen
		if fenceGen > c.genSeq.Load() {
			c.genSeq.Store(fenceGen)
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
		fenceGen = c.genSeq.Add(1)
		c.fences = append(c.fences, selectorFence{
			gen:              fenceGen,
			sel:              sel,
			active:           true,
			matchedResources: matchedResSet,
		})
	}
	c.enforceFenceCapLocked()
	fencedOps := c.detachMatchingInflightLocked(sel)

	if sel.Kind == SelectorAll {
		c.purgeGen = fenceGen
		c.fenceFloorGen = fenceGen
		c.hostFenceFloorGen = nil
		c.incompleteFloorGen = 0
		c.incompleteHostFloorGen = nil
		c.evictedIncompleteHostGens = nil
		c.fences = nil
		c.latestFenceGen.Store(fenceGen)
		c.incompleteFenceGen.Store(0)

		resources := make(map[string]struct{})
		memCount := 0
		diskKeys := make([]string, 0, len(c.keyGen))
		repCount := 0
		for k, st := range c.keyGen {
			if !st.inMem && !st.inDisk {
				continue
			}
			if _, host, reqURI, _, ok := parseBaseKeyParts(k); ok {
				resources[host+"\n"+reqURI] = struct{}{}
			}
			if st.inMem {
				memCount++
			}
			if st.inDisk {
				diskKeys = append(diskKeys, k)
			}
			if !st.isVaryStub {
				repCount++
			}
		}
		c.byHost = nil
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
			hwErr := c.disk.recordHighWaterGen(fenceGen)
			diskRemoved, retiredReaders, diskFailed, uncertain = c.disk.deleteBatchAtMostGen(diskKeys, fenceGen)
			if len(diskKeys) == 0 {
				c.disk.purge()
			}
			if hwErr != nil {
				c.disk.unrecoverableInvalidation.Store(true)
				uncertain = true
			}
		}
		res := InvalidateResult{
			Selector:                   sel,
			FenceSequence:              fenceGen,
			CacheSequenceBefore:        seqBefore,
			StateChangedSincePreview:   changedSincePreview,
			InvalidatedResources:       len(resources),
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

	invalidatedRes := make(map[string]struct{}, len(groups))
	invalidatedReps := 0
	memRemoved := 0
	diskRemoved := 0
	retiredReaders := 0
	failedEntries := 0
	remainingGroups := 0
	uncertain := false
	canceledMidway := false

	if c.disk != nil {
		if err := c.disk.recordHighWaterGen(fenceGen); err != nil {
			c.disk.unrecoverableInvalidation.Store(true)
			uncertain = true
		}
	}

	if c.onAfterFenceInstall != nil {
		c.onAfterFenceInstall(sel, fenceGen)
	}

	for startIdx := 0; startIdx < len(groups); startIdx += batchSize {
		if err := ctx.Err(); err != nil {
			canceledMidway = true
			for i := startIdx; i < len(groups); i++ {
				if groups[i].reps > 0 || len(groups[i].diskKeys) > 0 {
					remainingGroups += len(groups[i].allKeys)
				}
			}
			break
		}
		endIdx := startIdx + batchSize
		if endIdx > len(groups) {
			endIdx = len(groups)
		}
		batch := groups[startIdx:endIdx]

		var batchDiskKeys []string
		c.genMu.Lock()
		for _, g := range batch {
			if g.reps > 0 {
				invalidatedRes[g.resKey] = struct{}{}
				invalidatedReps += g.reps
			}
			for _, k := range g.allKeys {
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
					st.memBytes = 0
					c.maybeReclaimLocked(st)
				}
			}
			batchDiskKeys = append(batchDiskKeys, g.diskKeys...)
		}
		c.genMu.Unlock()

		if c.disk != nil && len(batchDiskKeys) > 0 {
			dRem, dRet, dFail, dUnc := c.disk.deleteBatchAtMostGen(batchDiskKeys, fenceGen)
			diskRemoved += dRem
			retiredReaders += dRet
			failedEntries += dFail
			if dUnc {
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
		InvalidatedResources:       len(invalidatedRes),
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
