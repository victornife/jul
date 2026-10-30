// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"jul/internal/cachemeta"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/http/httpguts"
)

// Inspection bounds apply independently of the normal cache capacity.
const (
	MaxInspectionResources = 256
	MaxInspectionDetails   = 32
	DecisionCapacity       = 256
	DecisionTTL            = time.Minute
)

// EntryEvidence contains no response/request header values. Its zero value
// denotes older entries whose publication provenance was not recorded.
type EntryEvidence = cachemeta.EntryEvidence

// publicationEvidence is one immutable 72-byte provenance allocation shared by
// Entry, the catalog and disk residency; it never retains headers or bodies.
// Exported fields permit additive gob persistence; zero means unknown.
type publicationEvidence struct {
	InsertedAt      int64
	ValidatedAt     int64
	PolicyID        [32]byte
	SWR             time.Duration
	SIE             time.Duration
	FreshnessSource uint8
	SWRSource       uint8
	SIESource       uint8
	Flags           uint8
}

// The provenance flag follows the same immutable clone and additive gob
// ownership as the deadline it describes. A successful 304/replacement creates
// new provenance and clears it. Legacy entries without provenance stay unknown.
const evidenceStaleIfError uint8 = 8
const evidenceStaleBasisKnown uint8 = 16
const evidenceNoRequestCookies uint8 = 32

func markStaleIfError(e *Entry) {
	if e.Evidence != nil {
		e.Evidence.Flags |= evidenceStaleIfError | evidenceStaleBasisKnown
	}
}

func (c *Cache) recordReuseDecision(r *http.Request, e *Entry, state string) {
	switch state {
	case stateHit:
		c.recordDecision(r, "fresh_hit")
	case stateRevalidated:
		c.recordDecision(r, "validated_reuse")
	case stateStale:
		basis := "stale_basis_unavailable"
		if e.Evidence != nil && e.Evidence.Flags&evidenceStaleBasisKnown != 0 {
			basis = "stale_while_revalidate"
			if e.Evidence.Flags&evidenceStaleIfError != 0 {
				basis = "stale_if_error_reuse"
			}
		}
		c.recordDecision(r, basis)
		c.recordDecision(r, "stale_reuse")
	}
}

// Bind every recorder's capture outcomes to the originating resource, without
// replacing the existing low-cardinality capture metrics/log owner.
func (c *Cache) observeRecorder(rec *recorder, r *http.Request) {
	rec.onDecision = func(reason string) { c.recordDecision(r, reason) }
	rec.acct.onFailure = func() { c.recordDecision(r, "disk_capture_failed") }
	rec.acct.onSkip = func(reason string) {
		c.captureSkipped(reason)
		c.recordDecision(r, "capture_"+reason)
	}
}

// Preserve disk metrics while attributing write-through outcomes to this resource.
func (c *Cache) writeThroughObserved(key string, e *Entry, gen, startGen uint64, hasStartGen bool, extraTags []string) bool {
	return c.disk.setWithGenObserved(key, e, gen, startGen, hasStartGen, extraTags,
		func(reason string) {
			if c.disk.onSkip != nil {
				c.disk.onSkip(reason)
			}
			c.recordKeyDecision(key, "capture_"+reason)
		}, func() { c.recordKeyDecision(key, "disk_capture_failed") })
}

// Observe the existing Vary matcher at recorder publication boundaries.
func (c *Cache) captureMatchesRequest(e *Entry, r *http.Request) bool {
	if e == nil {
		return false
	}
	if !e.matchesVary(r) {
		c.recordDecision(r, "vary_request_mismatch")
		return false
	}
	return true
}

var freshnessSources = [...]string{
	"",
	"gateway_fallback",
	"origin_s_maxage",
	"origin_max_age",
	"origin_expires",
	"gateway_fallback_no_cache",
	"targeted_gateway_fallback",
	"targeted_origin_s_maxage",
	"targeted_origin_max_age",
	"targeted_gateway_fallback_no_cache",
	"route_default_ttl",
	"route_default_ttl_no_cache",
	"targeted_route_default_ttl",
	"targeted_route_default_ttl_no_cache",
	"route_max_ttl_cap",
	"targeted_route_max_ttl_cap",
}
var windowSources = [...]string{"", "gateway_fallback", "origin", "mandatory_validation", "route_policy"}

func sourceCode(s string, values []string) uint8 {
	for i, v := range values {
		if s == v {
			return uint8(i)
		}
	}
	return 0
}
func evidenceTime(n int64) time.Time {
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n).UTC()
}

type policyIdentity struct {
	policy       CachePolicy
	digest       [32]byte
	bypassDigest [16]byte
}

// One immutable memo per handler bounds retention independently of reloads and
// request count. Compare the entire policy value so no field/epoch can be
// omitted from refresh, including fields added in the future. Concurrent old
// and new requests may replace the memo; each receives its own correct digest.
type policyIdentityMemo struct {
	latest atomic.Pointer[policyIdentity]
}

func (m *policyIdentityMemo) snapshot(p CachePolicy) *policyIdentity {
	if prior := m.latest.Load(); prior != nil && prior.policy == p {
		return prior
	}
	identity := newPolicyIdentity(p)
	m.latest.Store(&identity)
	return &identity
}

func newPolicyIdentity(p CachePolicy) policyIdentity {
	return policyIdentity{policy: p, digest: policyID(p), bypassDigest: bypassPolicyDigest(p)}
}

func policyID(p CachePolicy) [32]byte {
	// A stable, length-prefixed encoding avoids formatting the large fixed
	// cookie-name array on every hit. Collection, overlap bookkeeping and Vary
	// guards are excluded: they do not change freshness evidence. New policy
	// fields must be registered here (the completeness test enforces that).
	var storage [512]byte
	buf := append(storage[:0], "jul-cache-policy-v2\x00"...)
	for _, n := range []uint64{
		uint64(p.DefaultTTL), uint64(p.StaleWhileRevalidate), uint64(p.StaleIfError),
		uint64(p.MemoryMaxBytes), uint64(p.MaxEntryBytes), uint64(p.LockTimeout),
		uint64(p.MaxRanges), uint64(p.TagHeaderCount), p.RouteEpoch,
		uint64(p.MaxTTL), uint64(p.BypassCookieCount),
	} {
		buf = binary.AppendUvarint(buf, n)
	}
	for _, flag := range []bool{p.Lock, p.RangeEnabled, p.StripTagHeaders, p.HasRouteDefaultTTL,
		p.HasMaxTTL, p.HasRouteSWR, p.HasRouteSIE, p.BypassAnyCookie} {
		if flag {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
	}
	appendString := func(value string) {
		buf = binary.AppendUvarint(buf, uint64(len(value)))
		buf = append(buf, value...)
	}
	appendString(p.RouteScope)
	for _, value := range p.TagHeaders {
		appendString(value)
	}
	for _, value := range p.BypassCookies {
		appendString(value)
	}
	return sha256.Sum256(buf)
}

func (c *Cache) entryEvidence(status int, h http.Header, p responsePolicy, pol CachePolicy, now time.Time, ttl, swr time.Duration) *publicationEvidence {
	return c.entryEvidenceForRequest(nil, status, h, p, pol, now, ttl, swr)
}

func (c *Cache) entryEvidenceForRequest(r *http.Request, status int, h http.Header, p responsePolicy, pol CachePolicy, now time.Time, ttl, swr time.Duration) *publicationEvidence {
	_, _, source, _ := c.freshnessDecision(status, h, p, now, pol)
	swrSource, sieSource := "gateway_fallback", "gateway_fallback"
	if pol.HasRouteSWR {
		swrSource = "route_policy"
	}
	if pol.HasRouteSIE {
		sieSource = "route_policy"
	}
	if p.HasSWR {
		swrSource = "origin"
	}
	sie := pol.StaleIfError
	if p.HasSIE {
		sie, sieSource = p.SIE, "origin"
	}
	if p.revalidationRequired() {
		sie, sieSource = 0, "mandatory_validation"
	}
	if p.NoCache && !p.HasSIE {
		sie, sieSource = 0, "mandatory_validation"
	}
	if p.revalidationRequired() || p.NoCache {
		swrSource = "mandatory_validation"
	}
	flags := evidenceStaleBasisKnown
	if p.NoCache {
		flags |= 1
	}
	if p.revalidationRequired() {
		flags |= 2
	}
	if p.sharedAuthReuse() {
		flags |= 4
	}
	if r != nil && len(r.Header.Values("Cookie")) == 0 {
		flags |= evidenceNoRequestCookies
	}
	return &publicationEvidence{InsertedAt: now.UnixNano(), PolicyID: policyID(pol), SWR: swr, SIE: sie, FreshnessSource: sourceCode(source, freshnessSources[:]), SWRSource: sourceCode(swrSource, windowSources[:]), SIESource: sourceCode(sieSource, windowSources[:]), Flags: flags}
}

// RepresentationMetadata is a detached value projection; it owns no cache
// reader, response body, raw key, Vary value or response header map.
type RepresentationMetadata = cachemeta.RepresentationMetadata

// Inspection is bounded metadata observed at CapturedAt, not a prediction of
// future lookup results or a global snapshot across tiers and configuration.
type Inspection = cachemeta.Inspection

func (c *Cache) initInspection() error { _, err := rand.Read(c.inspectKey[:]); return err }

func (c *Cache) inspectionID(key string) string {
	h := hmac.New(sha256.New, c.inspectKey[:])
	_, _ = h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Cache) inspectDetailsLocked(sm *SafeResourceSummary, bases []string, routeScopeFilter string, now time.Time) {
	sm.Details = []RepresentationMetadata{}
	if len(bases) == 0 {
		return
	}
	_, host, uri, _, _ := parseBaseKeyParts(bases[0])
	sm.ResourceID = c.inspectionID(resourceKeyOf(host, uri))
	var truncated bool
	sm.VaryFields, truncated = boundedInspectionVary(sm.VaryFields)
	sm.DetailsTruncated = truncated
	sm.Tags, truncated = boundedInspectionTags(sm.Tags)
	sm.DetailsTruncated = sm.DetailsTruncated || truncated
	if len(sm.Path) > 2048 {
		sm.Path = "[path omitted: length limit]"
		sm.DetailsTruncated = true
	}
	globalPol := c.Policy()
	globalPolicyID := policyID(globalPol)
	for _, base := range bases {
		mem, disk, _, _, _, _, _, _ := c.collectOwnedKeysForBaseLocked(base, "")
		keys := make(map[string]bool, len(mem)+len(disk))
		for _, k := range mem {
			keys[k] = true
		}
		for _, k := range disk {
			keys[k] = true
		}
		ordered := make([]string, 0, len(keys))
		for k := range keys {
			ordered = append(ordered, k)
		}
		sort.Strings(ordered)
		for _, k := range ordered {
			st := c.keyGen[k]
			if st == nil || st.isVaryStub {
				continue
			}
			if routeScopeFilter != "" && st.routeScope != "" && st.routeScope != routeScopeFilter {
				continue
			}
			if len(sm.Details) >= MaxInspectionDetails {
				sm.DetailsTruncated = true
				continue
			}
			tiers := []string{}
			if st.inMem {
				tiers = append(tiers, "memory")
			}
			if st.inDisk {
				tiers = append(tiers, "disk")
			}
			freshness := "fresh"
			if !now.Before(st.expiresAt) {
				freshness = "expired"
				if now.Before(st.staleUntil) {
					freshness = "stale"
				}
			}
			evidence := "recorded"
			if st.evidence == nil || st.evidence.FreshnessSource == 0 || int(st.evidence.FreshnessSource) >= len(freshnessSources) {
				evidence = "legacy_unavailable"
			}
			fields, limited := boundedInspectionVary(st.varyFields)
			sm.DetailsTruncated = sm.DetailsTruncated || limited
			activePol := globalPol
			activePolicyID := globalPolicyID
			if st.routeScope != "" && c.routeConfigs != nil {
				if rc, ok := c.routeConfigs[st.routeScope]; ok {
					activePol = c.effectivePolicy(&rc)
					activePolicyID = policyID(activePol)
				}
			}
			var effectiveSIE *float64
			if evidence == "recorded" {
				v := c.staleOnErrorWindowWithPolicy(&Entry{RequiresValidation: st.evidence.Flags&1 != 0, MustRevalidate: st.evidence.Flags&2 != 0, HasStaleIfError: st.evidence.SIESource == 2, StaleIfError: st.evidence.SIE}, activePol).Seconds()
				effectiveSIE = &v
			}
			sm.Details = append(sm.Details, RepresentationMetadata{EffectiveSIESeconds: effectiveSIE, ID: c.inspectionID(k), Method: strings.Clone(st.method), Tiers: tiers, BodyBytes: st.bodyBytes,
				OriginAgeSeconds: max(0, now.Sub(st.createdAt).Seconds()), FreshnessState: freshness, VaryFields: fields,
				EvidenceState: evidence, Evidence: evidenceValue(st.evidence, st), RetainedPolicy: evidence == "recorded" && st.evidence.PolicyID != activePolicyID})
		}
	}
	if len(sm.Details) > 0 {
		sm.FreshnessState = sm.Details[0].FreshnessState
		for _, d := range sm.Details[1:] {
			if d.FreshnessState != sm.FreshnessState {
				sm.FreshnessState = "mixed"
				break
			}
		}
	}
	if len(sm.Details) == 0 {
		sm.FreshnessState = "metadata_only"
	}
	if sm.DetailsTruncated {
		sm.FreshnessState = "partial"
	}

}

// InspectScope reuses the purge preview's bounded selector/catalog traversal.
// It never calls a handler, a tier get, a file opener or a cache mutation.
func (c *Cache) InspectScope(ctx context.Context, sel NormalizedSelector) (Inspection, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, err
	}
	p, err := c.previewScope(ctx, sel, PreviewOptions{MaxScanEntries: MaxPreviewScanEntries, MaxSampleItems: MaxInspectionResources}, MaxInspectionResources, true)
	if err != nil {
		return Inspection{}, err
	}
	out := Inspection{State: "available", CapturedAt: p.AsOf, CacheSequence: p.CacheSequence, CountAccuracy: p.CountAccuracy,
		Truncated: p.Truncated || p.MatchedResources > len(p.Samples), MatchedResources: p.MatchedResources, MatchedRepresentations: p.MatchedRepresentations,
		MemoryEntries: p.MemoryEntries, DiskEntries: p.DiskEntries, MatchedBodyBytes: p.MatchedBytes, Resources: append([]SafeResourceSummary{}, p.Samples...),
		Decisions: []CacheDecision{}, EvidenceState: "disabled", Warnings: []string{"metadata_only_no_request_performed", "tier_entry_counts_include_vary_stubs", "not_a_global_snapshot", "absence_does_not_explain_history"}}
	if c == nil {
		out.State = "disabled"
		return out, nil
	}
	pol := c.Policy()
	out.Policy = cachemeta.EffectivePolicy{DefaultTTLSeconds: pol.DefaultTTL.Seconds(), SWRSeconds: pol.StaleWhileRevalidate.Seconds(), SIESeconds: pol.StaleIfError.Seconds(), MaxEntryBytes: pol.MaxEntryBytes, RangeEnabled: pol.RangeEnabled, FillLockEnabled: pol.Lock, TagIngestionEnabled: pol.TagHeaderCount > 0, RouteOverridesSupported: true, GatewayFreshnessCapSupported: true}
	// Stats() intentionally sweeps pending disk tombstones. Inspection must
	// perform no filesystem cleanup or cache mutation, even on a failed tier.
	bytes, capBytes, entries, evictions := c.mem.stats()
	out.MemoryOccupancy = TierStats{Bytes: bytes, MaxBytes: capBytes, Entries: entries, Evictions: evictions}
	if c.disk != nil {
		c.disk.mu.Lock()
		out.DiskOccupancy = &TierStats{Bytes: c.disk.physBytes + c.disk.retiredBytes + c.disk.tombstoneBytes, MaxBytes: c.disk.maxBytes, Entries: c.disk.ll.Len(), Evictions: c.disk.evictions}
		c.disk.mu.Unlock()
	}
	if p.MatchedResources == 0 {
		out.State = "empty"
	}
	if out.Truncated {
		out.State = "truncated"
	}
	if c.disk != nil && (c.disk.unrecoverableInvalidation.Load() || c.disk.routeEpochFailed.Load()) {
		out.State = "partial"
		out.Warnings = append(out.Warnings, "disk_unavailable")
	}
	if b := c.samples.Load(); b != nil {
		out.EvidenceState = "no_record"
		selectedIDs := map[string]bool{}
		if sel.Kind == SelectorTag {
			out.EvidenceState = "unavailable"
			out.Warnings = append(out.Warnings, "tag_evidence_uses_current_membership_not_historical_tags")
			for _, r := range out.Resources {
				selectedIDs[r.ResourceID] = true
			}
		}
		b.mu.Lock()
		b.expireLocked(c.clock())
		for _, d := range b.records {
			if !sel.matchesInspectionRoutePath(d.Path) {
				continue
			}
			if sel.Kind == SelectorExactURL || sel.Kind == SelectorKey {
				host, uri := sel.Host, sel.URI
				if sel.Kind == SelectorKey {
					_, host, uri, _, _ = parseBaseKeyParts(sel.Key)
				}
				if d.ResourceID != c.inspectionID(resourceKeyOf(host, uri)) {
					continue
				}
			} else if sel.Kind == SelectorTag {
				if !selectedIDs[d.ResourceID] {
					continue
				}
			} else if !sel.MatchesResource(d.Host, d.Path, nil) {
				continue
			}
			out.Decisions = append(out.Decisions, d)
		}
		b.mu.Unlock()
		if len(out.Decisions) > 0 {
			out.EvidenceState = "observed"
			if sel.Kind == SelectorTag {
				out.EvidenceState = "observed_current_membership"
			}
		}
	}
	return out, ctx.Err()
}

// CacheDecision reports one actual decision stage, not an inferred URL history.
type CacheDecision = cachemeta.CacheDecision

type decisionBuffer struct {
	mu      sync.Mutex
	records []CacheDecision
	timer   *time.Timer
	closed  bool
	next    int
}

func (b *decisionBuffer) expireLocked(now time.Time) {
	before := len(b.records)
	kept := b.records[:0]
	for _, d := range b.records {
		if now.Before(d.ExpiresAt) {
			kept = append(kept, d)
		}
	}
	clear(b.records[len(kept):])
	b.records = kept
	if len(kept) != before {
		b.next = 0
	}
}

func (c *Cache) setDecisionSampling(enabled bool) {
	if enabled {
		c.samples.CompareAndSwap(nil, &decisionBuffer{})
		return
	}
	if b := c.samples.Swap(nil); b != nil {
		b.mu.Lock()
		b.closed = true
		if b.timer != nil {
			b.timer.Stop()
		}
		clear(b.records)
		b.records = nil
		b.mu.Unlock()
	}
}

func (c *Cache) recordDecision(r *http.Request, reason string) {
	if c.samples.Load() == nil {
		return
	}
	c.recordKeyDecision(key(r), reason)
}

func (c *Cache) recordKeyDecision(k, reason string) {
	b := c.samples.Load()
	if b == nil {
		return
	}
	desc, known := decisionCatalog[reason]
	if !known {
		return
	}
	_, host, uri, _, ok := parseBaseKeyParts(baseKeyOf(k))
	if !ok {
		return
	}
	path := stripQueryFromURI(uri)
	if len(host)+len(path) > 512 {
		return
	}
	now := c.clock()
	d := CacheDecision{Source: "observed", Stage: desc.stage, Summary: desc.summary, NextStep: desc.next, ResourceID: c.inspectionID(resourceKeyOf(host, uri)), Host: strings.Clone(host), Path: strings.Clone(path), HasQuery: strings.Contains(uri, "?"), Reason: reason, ObservedAt: now, ExpiresAt: now.Add(DecisionTTL)}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || c.samples.Load() != b {
		return
	}
	if len(b.records) == DecisionCapacity {
		b.records[b.next] = d
		b.next = (b.next + 1) % DecisionCapacity
	} else {
		b.records = append(b.records, d)
	}
	if b.timer == nil {
		// One cleanup timer per buffer, independent of future requests. The timer
		// clears all samples after a fixed window; losing recent evidence is safe.
		b.timer = time.AfterFunc(DecisionTTL, func() {
			b.mu.Lock()
			clear(b.records)
			b.records = nil
			b.next = 0
			b.timer = nil
			b.mu.Unlock()
		})
	}
}

func boundedInspectionVary(in []string) ([]string, bool) {
	out := []string{}
	limited := false
	n := 0
	for i, v := range in {
		if i >= 16 || n+len(v) > 2048 {
			return out, true
		}
		if !httpguts.ValidHeaderFieldName(v) || len(v) > 128 {
			limited = true
			continue
		}
		out = append(out, strings.Clone(v))
		n += len(v)
	}
	return out, limited
}
func boundedInspectionTags(in []string) ([]string, bool) {
	out := []string{}
	limited := false
	n := 0
	for i, v := range in {
		if i >= 32 || n+len(v) > 2048 {
			return out, true
		}
		if ValidateTag(v) != nil {
			limited = true
			continue
		}
		out = append(out, strings.Clone(v))
		n += len(v)
	}
	return out, limited
}

func evidenceValue(e *publicationEvidence, st *keyGenState) EntryEvidence {
	if e == nil || int(e.FreshnessSource) >= len(freshnessSources) || int(e.SWRSource) >= len(windowSources) || int(e.SIESource) >= len(windowSources) {
		return EntryEvidence{}
	}
	return EntryEvidence{InsertedAt: evidenceTime(e.InsertedAt), ValidatedAt: evidenceTime(e.ValidatedAt), FreshnessSource: freshnessSources[e.FreshnessSource], SWRSource: windowSources[e.SWRSource], SIESource: windowSources[e.SIESource], FreshnessSeconds: st.expiresAt.Sub(st.createdAt).Seconds(), SWRSeconds: e.SWR.Seconds(), SIESeconds: e.SIE.Seconds(), RequiresValidation: e.Flags&1 != 0, MustRevalidate: e.Flags&2 != 0, SharedAuthReuse: e.Flags&4 != 0}
}

// InspectionSequence exposes publication/purge sequence without a tier lookup.
// Unchanged sequence does not exclude independent capacity eviction.
func (c *Cache) InspectionSequence() uint64 {
	if c == nil {
		return 0
	}
	return c.genSeq.Load()
}

type decisionDescription struct{ stage, summary, next string }

var decisionCatalog = map[string]decisionDescription{
	"disk_capture_failed":          {"capture", "A disk operation prevented response capture or publication.", "Inspect disk availability, permissions and write-health counters; raw filesystem errors are not exposed."},
	"fresh_hit":                    {"reuse", "A fresh cached representation was reused.", "Inspect its age and validation restrictions; future reuse is not guaranteed."},
	"stale_while_revalidate":       {"reuse", "Stale-while-revalidate reuse was selected.", "Check the recorded stale window and whether background refresh can acquire a generation lease."},
	"stale_reuse":                  {"reuse", "A cached representation was served stale.", "Inspect the companion stale-while-revalidate, stale-if-error or unavailable-basis decision."},
	"stale_if_error":               {"validation", "Origin validation failed and stale-if-error eligibility was selected.", "Check origin health and the current effective stale-if-error window."},
	"stale_if_error_extended":      {"validation", "An origin failure selected an extended stale-if-error deadline.", "Check origin health; this deadline is not a guarantee of later residency or reuse."},
	"stale_if_error_reuse":         {"reuse", "A cached representation was reused under an origin-error-extended stale deadline.", "Check origin health and the recorded extension; background refresh may still run."},
	"stale_basis_unavailable":      {"reuse", "A cached representation was reused within its stale deadline, but the deadline's basis is unavailable.", "Legacy provenance cannot distinguish original stale-while-revalidate from an error extension; do not infer a basis."},
	"revalidation_required":        {"validation", "The cached representation requires validation before reuse.", "Inspect request/origin no-cache restrictions, age and validators."},
	"validated_reuse":              {"reuse", "A successfully validated representation was reused.", "Inspect the recorded validation time."},
	"response_no_store":            {"admission", "The origin prohibited storage with no-store.", "Change origin policy only if shared caching is appropriate."},
	"response_private":             {"admission", "The origin marked the response private.", "Keep personalized responses outside the shared cache."},
	"response_set_cookie":          {"admission", "A Set-Cookie response was excluded from shared storage.", "Review application session behavior; do not weaken cookie protections."},
	"auth_storage_restricted":      {"admission", "The authenticated response lacked explicit shared-storage permission.", "Review origin cache directives without exposing credentials."},
	"auth_reuse_restricted":        {"reuse", "An authenticated request could not reuse this shared representation.", "Review the origin's explicit shared-auth reuse permission."},
	"request_no_store":             {"bypass", "The request opted out of cache lookup and storage.", "Inspect the client's Cache-Control behavior."},
	"response_policy_rejected":     {"admission", "The response was not eligible under the existing storage policy.", "Review status, freshness, validators and origin directives."},
	"vary_request_mismatch":        {"admission", "The captured representation varies on a request header modified for cache validation or range filling.", "A fresh origin response for the original request is required."},
	"vary_star":                    {"admission", "Vary: * prevented reusable shared storage.", "Review the origin's Vary policy."},
	"vary_changed":                 {"validation", "Validation changed the representation's Vary identity.", "A fresh fill is required under the new identity."},
	"upgrade_bypass":               {"bypass", "A protocol upgrade bypassed response caching.", "Protocol-switch traffic is not a cacheable HTTP representation."},
	"range_bypass":                 {"bypass", "The range request was forwarded outside cache reuse.", "Inspect range support, syntax, method and complete-object availability."},
	"sse_bypass":                   {"bypass", "A live event stream was excluded from capture.", "Do not buffer an unbounded SSE response for shared caching."},
	"capture_oversize":             {"capture", "The response body or encoded disk metadata exceeded its capture size limit.", "Compare maximum entry size, tier support, object size and the fixed disk metadata limit."},
	"capture_budget":               {"capture", "The bounded concurrent capture budget was unavailable.", "Inspect cache pressure and concurrent fills."},
	"capture_not_stored":           {"capture", "This capture did not produce a stored response.", "Inspect companion admission/capture decisions and cancellation."},
	"response_stored":              {"publication", "The response was published to at least one cache tier.", "Inspect current residency; publication does not guarantee a future hit."},
	"publication_failed_or_fenced": {"publication", "Publication failed or was prevented by invalidation fencing.", "Inspect purge outcomes and disk health; no successful store is claimed."},
	"disk_read_failed":             {"reuse", "The disk response could not be opened for reuse.", "Inspect disk health and permissions; no body is exposed here."},
	"fill_lock_timeout":            {"fill_lock", "Waiting for another fill timed out.", "This request falls back without taking ownership of the original fill."},
	"fill_lock_uncacheable":        {"fill_lock", "The joined fill did not publish a reusable response.", "Inspect the fill's admission and capture decisions."},
	"fill_lock_not_reusable":       {"fill_lock", "The completed fill could not satisfy this request.", "Review Vary and authentication restrictions; values remain private."},
	"memory_eviction":              {"eviction", "A memory copy was evicted for capacity.", "A disk copy may remain; inspect tier residency and configured capacity."},
	"disk_eviction":                {"eviction", "A disk copy was evicted for capacity.", "A memory copy may remain; inspect tier residency and configured capacity."},
	"capture_incomplete":           {"admission", "Captured body length differs from the declared Content-Length.", "Check origin response framing; the incomplete representation was not stored."},
	"tag_capture_rejected":         {"admission", "Trusted origin tag ingestion was rejected.", "Review configured tag names and bounded tag syntax."},
	"cookie_bypass":                {"bypass", "The request matched the route's configured cookie bypass policy and skipped cache lookup and storage.", "Inspect the route's cache_policy.bypass_cookies or bypass_any_cookie settings; cookie values are never recorded."},
	"cookie_bypass_malformed":      {"bypass", "The request carried a malformed or oversized Cookie header while route cookie bypass was active, conservatively skipping lookup and storage.", "Inspect client Cookie header formatting and size bounds; cookie values are never recorded."},
	"vary_sensitive_rejected":      {"admission", "The representation varies on Cookie or Authorization and was rejected to prevent shared-cache credential retention or reuse.", "Use Cache-Control: private/no-store or route cookie bypass for personalized responses instead of Vary: Cookie or Authorization."},
	"vary_proxy_rewrite_rejected":  {"admission", "The representation varies on a request header rewritten, generated or stripped by the HTTP proxy and could not be matched safely.", "Avoid varying on proxy-rewritten identity or forwarding headers, or disable caching on this route."},
	"vary_cgi_rewrite_rejected":    {"admission", "The representation varies on a CGI-sanitized, overridden or underscore-aliased request header and could not be matched safely.", "Avoid varying on CGI-transformed or underscore-aliased headers, or disable caching on this route."},
	"route_ownership_mismatch":     {"reuse", "The stored representation belongs to a different route context and was not reused.", "Overlapping routes sharing a hostname and URL isolate their cached representations."},
	"route_policy_mismatch":        {"reuse", "The stored representation does not satisfy the active route cache policy and was not reused.", "Tightened route freshness, caps or cookie-bypass rules apply immediately to new requests."},
}
