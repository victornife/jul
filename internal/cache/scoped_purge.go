// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
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

type selectorFence struct {
	gen       uint64
	sel       NormalizedSelector
	active    bool
	remaining int
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
	MaxScanKeys     int
	PreviewSequence uint64
	ReuseFenceGen   uint64
	BatchSize       int
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
	for len(c.fences) > MaxActiveFences {
		evicted := c.fences[0]
		c.fences = c.fences[1:]
		if evicted.gen > c.fenceFloorGen {
			c.fenceFloorGen = evicted.gen
		}
	}
	maxGen := c.fenceFloorGen
	for _, f := range c.fences {
		if f.gen > maxGen {
			maxGen = f.gen
		}
	}
	c.latestFenceGen.Store(maxGen)
}

func (c *Cache) reclaimFencesLocked() {
	if len(c.fences) == 0 {
		c.latestFenceGen.Store(c.fenceFloorGen)
		return
	}
	minInflight := c.minInflightStartGenLocked()
	kept := c.fences[:0]
	for _, f := range c.fences {
		if f.active || f.remaining > 0 || minInflight <= f.gen {
			kept = append(kept, f)
		}
	}
	c.fences = kept
	c.enforceFenceCapLocked()
}

func (c *Cache) isEntryFencedLocked(entryGen uint64, key string, tags []string) bool {
	if entryGen < c.purgeGen {
		return true
	}
	if c.fenceFloorGen > 0 && entryGen <= c.fenceFloorGen {
		return true
	}
	if len(c.fences) == 0 {
		return false
	}
	_, host, reqURI, _, ok := parseBaseKeyParts(key)
	baseKey := baseKeyOf(key)
	for i := range c.fences {
		f := &c.fences[i]
		if entryGen > f.gen {
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
		effTags := tags
		if len(effTags) == 0 && c.baseIndexedTags != nil {
			effTags = c.baseIndexedTags[baseKey]
		}
		if f.sel.MatchesResource(host, reqURI, effTags) {
			return true
		}
	}
	return false
}

func (c *Cache) isEntryFenced(entryGen uint64, key string, e *Entry) bool {
	if c == nil {
		return false
	}
	var tags []string
	if e != nil {
		tags = e.Tags
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return c.isEntryFencedLocked(entryGen, key, tags)
}

func (c *Cache) canReserveTagLinksLocked(host, baseKey string, candidateTags []string) bool {
	if len(candidateTags) == 0 {
		return true
	}
	existing := c.baseIndexedTags[baseKey]
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
	st.method = method
	st.host = host
	st.requestURI = reqURI
	st.isVariant = isVar
	st.baseKey = baseKeyOf(st.key)
	st.isVaryStub = e.IsVaryStub
	st.variants = append([]string(nil), e.Variants...)
	if c.Policy().TagHeaderCount > 0 && !e.IsVaryStub {
		st.tags = append([]string(nil), e.Tags...)
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
	var sets [][]string
	if bst, ok := c.keyGen[baseKey]; ok && bst.indexed && !bst.isVaryStub && (bst.inMem || bst.inDisk || bst.inflight > 0) && len(bst.tags) > 0 {
		sets = append(sets, bst.tags)
	}
	if c.baseVariants != nil {
		for vk := range c.baseVariants[baseKey] {
			if vst, ok := c.keyGen[vk]; ok && vst.indexed && (vst.inMem || vst.inDisk || vst.inflight > 0) && len(vst.tags) > 0 {
				sets = append(sets, vst.tags)
			}
		}
	}
	desired := mergeBaseTagSets(sets...)
	old := c.baseIndexedTags[baseKey]

	// Remove tags no longer present on baseKey.
	for _, t := range old {
		if !containsSortedOrLinear(desired, t) {
			if c.tagIndex != nil {
				if tm := c.tagIndex[host]; tm != nil {
					if bm := tm[t]; bm != nil {
						if _, had := bm[baseKey]; had {
							delete(bm, baseKey)
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
			delete(c.baseIndexedTags, baseKey)
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
		if _, exists := bm[baseKey]; !exists {
			if c.totalTagLinks >= MaxTotalTagLinks {
				continue
			}
			bm[baseKey] = struct{}{}
			c.totalTagLinks++
		}
		indexed = append(indexed, t)
	}
	if len(indexed) == 0 {
		delete(c.baseIndexedTags, baseKey)
		if len(tm) == 0 {
			delete(c.tagIndex, host)
		}
	} else {
		c.baseIndexedTags[baseKey] = indexed
	}
}

func (c *Cache) clearTagIndex() {
	if c == nil {
		return
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	c.tagIndex = nil
	c.baseIndexedTags = nil
	c.totalTagLinks = 0
	for _, st := range c.keyGen {
		st.tags = nil
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
		var rehydratedTags []string
		if c.Policy().TagHeaderCount > 0 {
			rehydratedTags = it.Tags
		}
		e := &Entry{
			IsVaryStub: it.IsVaryStub,
			Variants:   it.Variants,
			Tags:       rehydratedTags,
			CreatedAt:  it.CreatedAt,
			ExpiresAt:  it.ExpiresAt,
			StaleUntil: it.StaleUntil,
			Vary:       it.VaryFields,
		}
		c.indexKeyEntryLocked(st, e)
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

// candidateBaseKeysLocked returns the deduplicated slice of base keys to
// evaluate for sel.
func (c *Cache) candidateBaseKeysLocked(sel NormalizedSelector) (baseKeys []string, directLookup bool) {
	switch sel.Kind {
	case SelectorKey:
		return []string{baseKeyOf(sel.Key)}, true
	case SelectorExactURL:
		return []string{
			keyFor(http.MethodGet, sel.Host, sel.URI),
			keyFor(http.MethodHead, sel.Host, sel.URI),
		}, true
	case SelectorTag:
		if c.tagIndex == nil {
			return nil, true
		}
		tm := c.tagIndex[sel.Host]
		if len(tm) == 0 || len(sel.Tags) == 0 {
			return nil, true
		}
		if sel.TagMatch == TagMatchAll {
			firstMap := tm[sel.Tags[0]]
			if len(firstMap) == 0 {
				return nil, true
			}
			for bk := range firstMap {
				allMatch := true
				for i := 1; i < len(sel.Tags); i++ {
					if _, ok := tm[sel.Tags[i]][bk]; !ok {
						allMatch = false
						break
					}
				}
				if allMatch {
					baseKeys = append(baseKeys, bk)
				}
			}
			sort.Strings(baseKeys)
			return baseKeys, true
		}
		seen := make(map[string]struct{})
		for _, t := range sel.Tags {
			for bk := range tm[t] {
				if _, ok := seen[bk]; !ok {
					seen[bk] = struct{}{}
					baseKeys = append(baseKeys, bk)
				}
			}
		}
		sort.Strings(baseKeys)
		return baseKeys, true
	case SelectorHost, SelectorPrefix, SelectorGlob:
		if c.byHost == nil {
			return nil, false
		}
		hm := c.byHost[sel.Host]
		if len(hm) == 0 {
			return nil, false
		}
		baseKeys = make([]string, 0, len(hm))
		for bk := range hm {
			baseKeys = append(baseKeys, bk)
		}
		sort.Strings(baseKeys)
		return baseKeys, false
	case SelectorAll:
		for _, hm := range c.byHost {
			for bk := range hm {
				baseKeys = append(baseKeys, bk)
			}
		}
		sort.Strings(baseKeys)
		return baseKeys, false
	default:
		return nil, false
	}
}

func (c *Cache) collectOwnedKeysForBaseLocked(baseKey string, exactKeyFilter string) (memKeys, diskKeys []string, repCount int, bodyBytes, memBytes, diskBytes int64, stBase *keyGenState, live bool) {
	return c.collectOwnedKeysForBaseLockedWithFence(baseKey, exactKeyFilter, 0)
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
		var tags []string
		if c.baseIndexedTags != nil {
			tags = c.baseIndexedTags[base]
		}
		hit := false
		switch sel.Kind {
		case SelectorKey:
			hit = rk.key == sel.Key || base == baseKeyOf(sel.Key)
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

	candidates, _ := c.candidateBaseKeysLocked(sel)
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
		var tags []string
		if c.baseIndexedTags != nil {
			tags = c.baseIndexedTags[bk]
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

		resKey := host + "\n" + reqURI
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

// BeginSelectorFence installs an active selector fence at a new monotonic
// generation and returns the fence sequence and a completion function
// (reusable contract for #624, ADR 0027 §6.2).
func (c *Cache) BeginSelectorFence(sel NormalizedSelector) (fenceGen uint64, complete func()) {
	if c == nil {
		return 0, func() {}
	}
	c.genMu.Lock()
	fenceGen = c.genSeq.Add(1)
	c.fences = append(c.fences, selectorFence{
		gen:    fenceGen,
		sel:    sel,
		active: true,
	})
	c.enforceFenceCapLocked()
	c.detachMatchingInflightLocked(sel)
	c.genMu.Unlock()

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
	seqBefore := c.genSeq.Load()
	changedSincePreview := opts.PreviewSequence > 0 && seqBefore != opts.PreviewSequence

	if sel.Kind != SelectorAll {
		candidates, _ := c.candidateBaseKeysLocked(sel)
		if len(candidates) > maxScan {
			c.genMu.Unlock()
			return InvalidateResult{}, fmt.Errorf("cache selector scope (%d candidate resources) exceeds maximum scan bound of %d; use kind = 'host' or 'all'", len(candidates), maxScan)
		}
	}

	var fenceGen uint64
	if opts.ReuseFenceGen > 0 {
		fenceGen = opts.ReuseFenceGen
		foundFence := false
		for i := range c.fences {
			if c.fences[i].gen == fenceGen {
				if c.fences[i].sel.Digest() != sel.Digest() {
					c.genMu.Unlock()
					return InvalidateResult{}, fmt.Errorf("reuse_fence_gen %d selector digest does not match requested selector", fenceGen)
				}
				c.fences[i].active = true
				foundFence = true
				break
			}
		}
		if !foundFence {
			c.fences = append(c.fences, selectorFence{
				gen:    fenceGen,
				sel:    sel,
				active: true,
			})
		}
	} else {
		fenceGen = c.genSeq.Add(1)
		c.fences = append(c.fences, selectorFence{
			gen:    fenceGen,
			sel:    sel,
			active: true,
		})
	}
	c.enforceFenceCapLocked()
	fencedOps := c.detachMatchingInflightLocked(sel)

	if sel.Kind == SelectorAll {
		c.purgeGen = fenceGen
		c.fenceFloorGen = fenceGen
		c.fences = nil
		c.latestFenceGen.Store(fenceGen)

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
			diskRemoved, retiredReaders, diskFailed, uncertain = c.disk.deleteBatchAtMostGen(diskKeys, fenceGen)
			if len(diskKeys) == 0 {
				c.disk.purge()
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

	// Collect matched target keys under genMu at the fence linearization point.
	candidates, _ := c.candidateBaseKeysLocked(sel)
	type matchedGroup struct {
		resKey   string
		baseKey  string
		allKeys  []string
		diskKeys []string
		reps     int
	}
	var groups []matchedGroup
	skipped := 0
	exactFilter := ""
	if sel.Kind == SelectorKey {
		exactFilter = sel.Key
	}

	for _, bk := range candidates {
		_, host, reqURI, _, ok := parseBaseKeyParts(bk)
		if !ok {
			skipped++
			continue
		}
		var tags []string
		if c.baseIndexedTags != nil {
			tags = c.baseIndexedTags[bk]
		}
		if sel.Kind != SelectorKey && !sel.MatchesResource(host, reqURI, tags) {
			skipped++
			continue
		}
		if sel.Kind == SelectorKey && bk != baseKeyOf(sel.Key) {
			skipped++
			continue
		}
		memKeys, diskKeys, reps, _, _, _, _, live := c.collectOwnedKeysForBaseLockedWithFence(bk, exactFilter, fenceGen)
		if !live {
			skipped++
			continue
		}
		keySet := make(map[string]struct{}, len(memKeys)+len(diskKeys)+1)
		var allKeys []string
		for _, k := range memKeys {
			keySet[k] = struct{}{}
			allKeys = append(allKeys, k)
		}
		for _, k := range diskKeys {
			if _, exists := keySet[k]; !exists {
				keySet[k] = struct{}{}
				allKeys = append(allKeys, k)
			}
		}
		if _, exists := keySet[bk]; !exists && exactFilter == "" {
			allKeys = append(allKeys, bk)
		}
		groups = append(groups, matchedGroup{
			resKey:   host + "\n" + reqURI,
			baseKey:  bk,
			allKeys:  allKeys,
			diskKeys: diskKeys,
			reps:     reps,
		})
	}
	c.genMu.Unlock()

	if c.onAfterFenceInstall != nil {
		c.onAfterFenceInstall(sel, fenceGen)
	}

	invalidatedRes := make(map[string]struct{}, len(groups))
	invalidatedReps := 0
	memRemoved := 0
	diskRemoved := 0
	retiredReaders := 0
	failedEntries := 0
	remainingGroups := 0
	uncertain := false
	canceledMidway := false

	for startIdx := 0; startIdx < len(groups); startIdx += batchSize {
		if err := ctx.Err(); err != nil {
			canceledMidway = true
			for i := startIdx; i < len(groups); i++ {
				remainingGroups += len(groups[i].allKeys)
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
			invalidatedRes[g.resKey] = struct{}{}
			invalidatedReps += g.reps
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

func (c *Cache) collectOwnedKeysForBaseLockedWithFence(baseKey, exactKeyFilter string, ownFenceGen uint64) (memKeys, diskKeys []string, repCount int, bodyBytes, memBytes, diskBytes int64, stBase *keyGenState, live bool) {
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
		if kst == nil || (!kst.inMem && !kst.inDisk) {
			return
		}
		if ownFenceGen > 0 {
			if kst.gen > ownFenceGen {
				return
			}
		} else if c.isEntryFencedLocked(kst.gen, k, kst.tags) {
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
	return memKeys, diskKeys, repCount, bodyBytes, memBytes, diskBytes, stBase, live
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
