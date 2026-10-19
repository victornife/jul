// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"maps"
	"net"
	"net/netip"
	"reflect"
	"slices"
	"sort"
	"strings"

	"jul/internal/config"
)

// Audience-delta classes (ADR 0026 Decision 2, spec §9.5).
const (
	AudienceDeltaUnchanged    = "unchanged"
	AudienceDeltaMetadataOnly = "metadata_only"
	AudienceDeltaNarrowed     = "narrowed"
	AudienceDeltaWidened      = "widened"
	AudienceDeltaUncertain    = "uncertain"
)

const (
	fileBackedInputsPlanNotice = "Publishing reads file-backed inputs again; entries whose loaded file contents changed at publish will have their open exchanges ended."
	providerChecklistPlanNote  = "Review external provider policy, DNS, and connectors before applying: Jul cannot verify external audience changes."
	maxFileBackedEntriesInPlan = 50
)

// SharingEntryPosition is a 0-based revision-bound location coordinate
// ({revision, block_index, location_index}, spec §4.3, §9.4).
type SharingEntryPosition struct {
	Revision      string `json:"revision,omitempty"`
	BlockIndex    int    `json:"block_index"`
	LocationIndex int    `json:"location_index"`
}

// SharingEntryRef identifies a route entry in a plan or postcondition by its
// literal route_id when present, or by its revision-bound position otherwise
// (spec §9.4, RR06).
type SharingEntryRef struct {
	Kind     string                `json:"kind"`
	RouteID  string                `json:"route_id,omitempty"`
	Position *SharingEntryPosition `json:"position,omitempty"`
}

// String returns a human-readable identifier for the entry reference.
func (r SharingEntryRef) String() string {
	if r.Kind == "route_id" && r.RouteID != "" {
		return r.RouteID
	}
	if r.Position != nil {
		if r.Position.Revision != "" {
			return fmt.Sprintf("%s:servers[%d].locations[%d]", r.Position.Revision, r.Position.BlockIndex, r.Position.LocationIndex)
		}
		return fmt.Sprintf("servers[%d].locations[%d]", r.Position.BlockIndex, r.Position.LocationIndex)
	}
	return "unknown"
}

// SharingPlanFinding is one intent or cache-sharing finding on a planned entry.
type SharingPlanFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	Hint     string `json:"hint,omitempty"`
	Created  bool   `json:"created"`
}

// SharingEntryDelta is the per-entry audience-delta classification and intent
// assessment in a plan response (spec §4.5, §8.4, §9.5, ADR 0026 #642 amendment).
type SharingEntryDelta struct {
	Ref                       SharingEntryRef      `json:"ref"`
	DisplayLabel              string               `json:"display_label"`
	Expect                    string               `json:"expect"`
	Class                     string               `json:"class"`
	DeclaredModeBefore        string               `json:"declared_mode_before"`
	DeclaredModeAfter         string               `json:"declared_mode_after"`
	DeclaredModeWidened       bool                 `json:"declared_mode_widened"`
	RequiresAcknowledgement   bool                 `json:"requires_acknowledgement"`
	TriggersRetirement        bool                 `json:"triggers_retirement"`
	KeepOpenEligible          bool                 `json:"keep_open_eligible"`
	OpenExchangesSnapshot     int                  `json:"open_exchanges_snapshot"`
	RetirementBound           string               `json:"retirement_bound,omitempty"`
	Reasons                   []string             `json:"reasons,omitempty"`
	IntentFindings            []SharingPlanFinding `json:"intent_findings,omitempty"`
	ProviderChecklistRequired bool                 `json:"provider_checklist_required"`
}

// SharingFileBackedEntry names an entry that depends on file-backed access
// inputs (spec §9.5, RR12).
type SharingFileBackedEntry struct {
	Ref          SharingEntryRef `json:"ref"`
	DisplayLabel string          `json:"display_label"`
	InputKinds   []string        `json:"input_kinds"`
}

// SharingPlanReport is the sharing assessment attached to every plan/preview
// response (raw candidate and typed patch batch).
type SharingPlanReport struct {
	Entries                    []SharingEntryDelta       `json:"entries"`
	RequiresAcknowledgement    bool                      `json:"requires_acknowledgement"`
	AcknowledgementEntries     []string                  `json:"acknowledgement_entries,omitempty"`
	IntentFindings             []SharingPlanFinding      `json:"intent_findings,omitempty"`
	FileBackedInputsNotice     string                    `json:"file_backed_inputs_notice,omitempty"`
	FileBackedEntries          []SharingFileBackedEntry  `json:"file_backed_entries,omitempty"`
	FileBackedEntryCount       int                       `json:"file_backed_entry_count"`
	FileBackedEntriesTruncated bool                      `json:"file_backed_entries_truncated"`
	ProviderChecklistNotice    string                    `json:"provider_checklist_notice,omitempty"`
	Postcondition              *SharingPlanPostcondition `json:"postcondition,omitempty"`
}

type indexedLocation struct {
	srvIdx int
	locIdx int
	srv    config.ServerConfig
	loc    config.LocationConfig
}

type correlatedEntryPair struct {
	before *indexedLocation
	after  *indexedLocation
}

// BuildSharingPlanReport computes the §9.5 audience-delta classes, §4.5 intent
// findings, file-backed input notice, and §9.4 postcondition for a plan.
func (s *Server) BuildSharingPlanReport(
	beforeRaw *config.Config,
	beforeEff *config.Config,
	afterRaw *config.Config,
	afterEff *config.Config,
	isRawPlan bool,
	ops []patchRequest,
	callerHasRaw bool,
) SharingPlanReport {
	report := SharingPlanReport{
		Entries: []SharingEntryDelta{},
	}
	if beforeRaw == nil || afterRaw == nil {
		return report
	}
	if beforeEff == nil {
		beforeEff = beforeRaw
	}
	if afterEff == nil {
		afterEff = afterRaw
	}

	beforeFindingsByLoc := collectSharingFindingsByLoc(beforeRaw, beforeEff)
	afterFindingsByLoc := collectSharingFindingsByLoc(afterRaw, afterEff)

	pairs := correlatePlanEntries(beforeRaw, afterRaw)
	hostDispatchAffectedBefore, hostDispatchAffectedAfter := computeHostDispatchAffectedBlocks(beforeRaw, afterRaw)
	blockStructureChangedAfter := computeBlockStructureChanged(beforeRaw, afterRaw)
	hiddenPluginsChanged, hiddenUpstreamsChanged := hiddenOpsTouched(ops, callerHasRaw)

	var openByCoord map[[2]int]int
	var openByRouteID map[string]int
	if s != nil && s.deps.LiveSnapshot != nil {
		if ex := s.deps.LiveSnapshot().Exchanges; ex != nil {
			openByCoord, openByRouteID = ex.SnapshotCounts()
		}
	}

	var allFindings []SharingPlanFinding
	var ackEntries []string
	anyProviderChecklist := false

	for _, pair := range pairs {
		delta := classifyEntryPair(
			beforeRaw, beforeEff,
			afterRaw, afterEff,
			pair,
			hostDispatchAffectedBefore,
			hostDispatchAffectedAfter,
			blockStructureChangedAfter,
			hiddenPluginsChanged,
			hiddenUpstreamsChanged,
			beforeFindingsByLoc,
			afterFindingsByLoc,
		)
		if pair.before != nil {
			openCount := 0
			if rid := routeID(&pair.before.loc); rid != "" && openByRouteID != nil {
				openCount = openByRouteID[rid]
			}
			if openCount == 0 && openByCoord != nil {
				openCount = openByCoord[[2]int{pair.before.srvIdx, pair.before.locIdx}]
			}
			delta.OpenExchangesSnapshot = openCount
		}
		// Include all entries that either changed, have intent findings, or were
		// expanded by §9.5.
		if delta.Class != AudienceDeltaUnchanged || len(delta.IntentFindings) > 0 || delta.DeclaredModeBefore != delta.DeclaredModeAfter {
			report.Entries = append(report.Entries, delta)
		}
		if delta.RequiresAcknowledgement {
			report.RequiresAcknowledgement = true
			ackEntries = append(ackEntries, delta.DisplayLabel)
		}
		if delta.ProviderChecklistRequired && delta.Class != AudienceDeltaUnchanged {
			anyProviderChecklist = true
		}
		allFindings = append(allFindings, delta.IntentFindings...)
	}

	report.AcknowledgementEntries = ackEntries
	report.IntentFindings = allFindings
	if anyProviderChecklist {
		report.ProviderChecklistNotice = providerChecklistPlanNote
	}

	fileEntries := collectFileBackedEntries(afterRaw)
	report.FileBackedEntryCount = len(fileEntries)
	if len(fileEntries) > 0 {
		report.FileBackedInputsNotice = fileBackedInputsPlanNotice
		if len(fileEntries) > maxFileBackedEntriesInPlan {
			report.FileBackedEntries = fileEntries[:maxFileBackedEntriesInPlan]
			report.FileBackedEntriesTruncated = true
		} else {
			report.FileBackedEntries = fileEntries
		}
	}

	post := s.producePlanPostcondition(beforeRaw, afterRaw, afterEff, pairs, report.Entries, isRawPlan, ops, callerHasRaw)
	report.Postcondition = &post
	return report
}

func entryRefForLocation(loc config.LocationConfig, revision string, srvIdx, locIdx int) SharingEntryRef {
	if loc.RouteID != nil {
		rid := strings.TrimSpace(*loc.RouteID)
		if rid != "" && !config.ContainsSecretRef(rid) {
			return SharingEntryRef{
				Kind:    "route_id",
				RouteID: rid,
			}
		}
	}
	return SharingEntryRef{
		Kind: "position",
		Position: &SharingEntryPosition{
			Revision:      revision,
			BlockIndex:    srvIdx,
			LocationIndex: locIdx,
		},
	}
}

func entryDisplayLabel(srv config.ServerConfig, loc config.LocationConfig) string {
	hostLabel := srv.Listen
	if len(srv.ServerNames) > 0 && strings.TrimSpace(srv.ServerNames[0]) != "" {
		hostLabel = strings.TrimSpace(srv.ServerNames[0])
		if len(srv.ServerNames) > 1 {
			hostLabel = fmt.Sprintf("%s (+%d)", hostLabel, len(srv.ServerNames)-1)
		}
	} else if strings.TrimSpace(srv.Name) != "" {
		hostLabel = strings.TrimSpace(srv.Name)
	}
	path := loc.Match.Path
	if path == "" {
		path = "/"
	}
	if loc.RouteID != nil && strings.TrimSpace(*loc.RouteID) != "" && !config.ContainsSecretRef(*loc.RouteID) {
		return fmt.Sprintf("%s %s (%s)", hostLabel, path, strings.TrimSpace(*loc.RouteID))
	}
	return fmt.Sprintf("%s %s", hostLabel, path)
}

func declaredSharingMode(loc config.LocationConfig) string {
	if loc.Sharing == nil || strings.TrimSpace(loc.Sharing.Mode) == "" {
		return config.SharingModeNotDeclared
	}
	if config.ContainsSecretRef(loc.Sharing.Mode) {
		return config.SharingModeUnknown
	}
	return strings.TrimSpace(loc.Sharing.Mode)
}

func collectSharingFindingsByLoc(rawCfg, effCfg *config.Config) map[[2]int][]config.Diagnostic {
	out := make(map[[2]int][]config.Diagnostic)
	if rawCfg == nil && effCfg == nil {
		return out
	}
	for _, d := range config.LintSharingFindingsWithRaw(rawCfg, effCfg, true) {
		var srvIdx, locIdx int
		if _, err := fmt.Sscanf(d.Field, "servers[%d].locations[%d]", &srvIdx, &locIdx); err == nil {
			key := [2]int{srvIdx, locIdx}
			out[key] = append(out[key], d)
		}
	}
	return out
}

func correlatePlanEntries(before, after *config.Config) []correlatedEntryPair {
	beforeUsed := make(map[[2]int]bool)
	afterUsed := make(map[[2]int]bool)

	beforeByRouteID := make(map[string]indexedLocation)
	for i, srv := range before.Servers {
		for j, loc := range srv.Locations {
			if id := routeID(&loc); id != "" && !config.ContainsSecretRef(id) {
				beforeByRouteID[id] = indexedLocation{srvIdx: i, locIdx: j, srv: srv, loc: loc}
			}
		}
	}

	var pairs []correlatedEntryPair

	// 1. Correlate by literal route_id first (global across all server blocks).
	afterPairByCoord := make(map[[2]int]correlatedEntryPair)
	for i, srv := range after.Servers {
		for j, loc := range srv.Locations {
			if id := routeID(&loc); id != "" && !config.ContainsSecretRef(id) {
				if b, ok := beforeByRouteID[id]; ok {
					bCopy := b
					aCopy := indexedLocation{srvIdx: i, locIdx: j, srv: srv, loc: loc}
					beforeUsed[[2]int{b.srvIdx, b.locIdx}] = true
					afterUsed[[2]int{i, j}] = true
					afterPairByCoord[[2]int{i, j}] = correlatedEntryPair{before: &bCopy, after: &aCopy}
				}
			}
		}
	}

	// 2. Correlate remaining ID-less entries within matched server blocks.
	srvPairs := correlateServerBlocks(before.Servers, after.Servers)
	for bSrvIdx, aSrvIdx := range srvPairs {
		bSrv := before.Servers[bSrvIdx]
		aSrv := after.Servers[aSrvIdx]
		// Match by exact locationKey (match type + path + predicates) when unique among unused.
		for jAfter, aLoc := range aSrv.Locations {
			if afterUsed[[2]int{aSrvIdx, jAfter}] {
				continue
			}
			aKey := locationKey(&aLoc)
			matchIdx := -1
			ambiguous := false
			for jBefore, bLoc := range bSrv.Locations {
				if beforeUsed[[2]int{bSrvIdx, jBefore}] {
					continue
				}
				if locationKey(&bLoc) == aKey {
					if matchIdx >= 0 {
						ambiguous = true
						break
					}
					matchIdx = jBefore
				}
			}
			if matchIdx >= 0 && !ambiguous {
				bCopy := indexedLocation{srvIdx: bSrvIdx, locIdx: matchIdx, srv: bSrv, loc: bSrv.Locations[matchIdx]}
				aCopy := indexedLocation{srvIdx: aSrvIdx, locIdx: jAfter, srv: aSrv, loc: aLoc}
				beforeUsed[[2]int{bSrvIdx, matchIdx}] = true
				afterUsed[[2]int{aSrvIdx, jAfter}] = true
				afterPairByCoord[[2]int{aSrvIdx, jAfter}] = correlatedEntryPair{before: &bCopy, after: &aCopy}
			}
		}
		// Match by unique (matchType, path) coordinates among remaining unused in the block.
		for jAfter, aLoc := range aSrv.Locations {
			if afterUsed[[2]int{aSrvIdx, jAfter}] {
				continue
			}
			aCoord := locationCoordinates(&aLoc)
			matchIdx := -1
			ambiguous := false
			for jBefore, bLoc := range bSrv.Locations {
				if beforeUsed[[2]int{bSrvIdx, jBefore}] {
					continue
				}
				if locationCoordinates(&bLoc) == aCoord {
					if matchIdx >= 0 {
						ambiguous = true
						break
					}
					matchIdx = jBefore
				}
			}
			if matchIdx >= 0 && !ambiguous {
				bCopy := indexedLocation{srvIdx: bSrvIdx, locIdx: matchIdx, srv: bSrv, loc: bSrv.Locations[matchIdx]}
				aCopy := indexedLocation{srvIdx: aSrvIdx, locIdx: jAfter, srv: aSrv, loc: aLoc}
				beforeUsed[[2]int{bSrvIdx, matchIdx}] = true
				afterUsed[[2]int{aSrvIdx, jAfter}] = true
				afterPairByCoord[[2]int{aSrvIdx, jAfter}] = correlatedEntryPair{before: &bCopy, after: &aCopy}
			}
		}
	}

	// Emit present entries in candidate declaration order.
	for i, srv := range after.Servers {
		for j, loc := range srv.Locations {
			if p, ok := afterPairByCoord[[2]int{i, j}]; ok {
				pairs = append(pairs, p)
				continue
			}
			aCopy := indexedLocation{srvIdx: i, locIdx: j, srv: srv, loc: loc}
			pairs = append(pairs, correlatedEntryPair{after: &aCopy})
		}
	}

	// Emit removed entries in baseline declaration order.
	for i, srv := range before.Servers {
		for j, loc := range srv.Locations {
			if beforeUsed[[2]int{i, j}] {
				continue
			}
			bCopy := indexedLocation{srvIdx: i, locIdx: j, srv: srv, loc: loc}
			pairs = append(pairs, correlatedEntryPair{before: &bCopy})
		}
	}

	return pairs
}

// correlateServerBlocks maps before server index -> after server index.
func correlateServerBlocks(before, after []config.ServerConfig) map[int]int {
	out := make(map[int]int)
	usedAfter := make(map[int]bool)

	// 1. Exact listen + server_names multiset when unique.
	for bi, bs := range before {
		match := -1
		for ai, as := range after {
			if usedAfter[ai] {
				continue
			}
			if bs.Listen == as.Listen && stringSetsEqual(bs.ServerNames, as.ServerNames) {
				if match >= 0 {
					match = -2
					break
				}
				match = ai
			}
		}
		if match >= 0 {
			out[bi] = match
			usedAfter[match] = true
		}
	}

	// 2. Non-empty ServerConfig.Name when unique.
	for bi, bs := range before {
		if _, ok := out[bi]; ok || strings.TrimSpace(bs.Name) == "" {
			continue
		}
		match := -1
		for ai, as := range after {
			if usedAfter[ai] {
				continue
			}
			if bs.Name == as.Name {
				if match >= 0 {
					match = -2
					break
				}
				match = ai
			}
		}
		if match >= 0 {
			out[bi] = match
			usedAfter[match] = true
		}
	}

	// 3. Shared literal route_id between a before block and an after block.
	for bi, bs := range before {
		if _, ok := out[bi]; ok {
			continue
		}
		match := -1
		for _, bloc := range bs.Locations {
			id := routeID(&bloc)
			if id == "" || config.ContainsSecretRef(id) {
				continue
			}
			for ai, as := range after {
				if usedAfter[ai] {
					continue
				}
				for _, aloc := range as.Locations {
					if routeID(&aloc) == id {
						match = ai
						break
					}
				}
				if match >= 0 {
					break
				}
			}
			if match >= 0 {
				break
			}
		}
		if match >= 0 {
			out[bi] = match
			usedAfter[match] = true
		}
	}

	// 4. Single block on the same listener.
	for bi, bs := range before {
		if _, ok := out[bi]; ok {
			continue
		}
		var bCount, aCount, aIdx int
		for _, other := range before {
			if other.Listen == bs.Listen {
				bCount++
			}
		}
		for ai, as := range after {
			if as.Listen == bs.Listen {
				aCount++
				aIdx = ai
			}
		}
		if bCount == 1 && aCount == 1 && !usedAfter[aIdx] {
			out[bi] = aIdx
			usedAfter[aIdx] = true
		}
	}

	return out
}

// computeHostDispatchAffectedBlocks implements the normative §9.5 host-dispatch
// expansion rule using config.NormalizeHostPattern and config.HostPatternsOverlap.
func computeHostDispatchAffectedBlocks(before, after *config.Config) (map[int]bool, map[int]bool) {
	affectedBefore := make(map[int]bool)
	affectedAfter := make(map[int]bool)

	bToA := correlateServerBlocks(before.Servers, after.Servers)
	aToB := make(map[int]int, len(bToA))
	for b, a := range bToA {
		aToB[a] = b
	}

	listeners := make(map[string]bool)
	for _, s := range before.Servers {
		listeners[s.Listen] = true
	}
	for _, s := range after.Servers {
		listeners[s.Listen] = true
	}

	countOnListen := func(srvs []config.ServerConfig, l string) int {
		n := 0
		for _, s := range srvs {
			if s.Listen == l {
				n++
			}
		}
		return n
	}

	for listen := range listeners {
		var changedPatterns []string
		listenerBlocksMutated := false
		multiBlockListener := countOnListen(before.Servers, listen) > 1 || countOnListen(after.Servers, listen) > 1

		addPatterns := func(names []string) {
			if config.IsCatchAllServerNames(names) {
				changedPatterns = append(changedPatterns, "*")
				return
			}
			for _, n := range names {
				if norm := config.NormalizeHostPattern(n); norm != "" {
					changedPatterns = append(changedPatterns, norm)
				}
			}
		}

		var survivingBeforeOrder []int
		for bi, bs := range before.Servers {
			if bs.Listen != listen {
				continue
			}
			ai, matched := bToA[bi]
			if !matched {
				// Block removed from this listener.
				listenerBlocksMutated = true
				addPatterns(bs.ServerNames)
				affectedBefore[bi] = true
				continue
			}
			if after.Servers[ai].Listen != listen {
				// Block relocated from this listener: affects host dispatch only when
				// another block shares either the old or the new listener.
				if multiBlockListener || countOnListen(before.Servers, after.Servers[ai].Listen) > 0 || countOnListen(after.Servers, after.Servers[ai].Listen) > 1 {
					listenerBlocksMutated = true
					addPatterns(bs.ServerNames)
					affectedBefore[bi] = true
				}
				continue
			}
			survivingBeforeOrder = append(survivingBeforeOrder, bi)
			as := after.Servers[ai]
			diff := symmetricNormalizedHostDiff(bs.ServerNames, as.ServerNames)
			if len(diff) > 0 {
				changedPatterns = append(changedPatterns, diff...)
				affectedBefore[bi] = true
				affectedAfter[ai] = true
			}
		}

		var survivingAfterMapped []int
		for ai, as := range after.Servers {
			if as.Listen != listen {
				continue
			}
			bi, matched := aToB[ai]
			if !matched {
				// Block added to this listener.
				listenerBlocksMutated = true
				addPatterns(as.ServerNames)
				affectedAfter[ai] = true
				continue
			}
			if before.Servers[bi].Listen != listen {
				if multiBlockListener || countOnListen(after.Servers, before.Servers[bi].Listen) > 0 || countOnListen(before.Servers, before.Servers[bi].Listen) > 1 {
					listenerBlocksMutated = true
					addPatterns(as.ServerNames)
					affectedAfter[ai] = true
				}
				continue
			}
			survivingAfterMapped = append(survivingAfterMapped, bi)
		}

		// Check if relative declaration order of surviving blocks on this listener changed.
		if len(survivingBeforeOrder) == len(survivingAfterMapped) {
			for k := range survivingBeforeOrder {
				if survivingBeforeOrder[k] != survivingAfterMapped[k] {
					listenerBlocksMutated = true
					for _, bi := range survivingBeforeOrder {
						addPatterns(before.Servers[bi].ServerNames)
						affectedBefore[bi] = true
						if ai, ok := bToA[bi]; ok {
							addPatterns(after.Servers[ai].ServerNames)
							affectedAfter[ai] = true
						}
					}
					break
				}
			}
		}

		if len(changedPatterns) == 0 && !listenerBlocksMutated {
			continue
		}

		// Mark the listener's first-declared block in either revision, and any block
		// whose pattern in either revision overlaps any pattern in changedPatterns.
		firstBefore := -1
		for bi, bs := range before.Servers {
			if bs.Listen == listen {
				if firstBefore < 0 {
					firstBefore = bi
				}
				if blockOverlapsPatterns(bs.ServerNames, changedPatterns) {
					affectedBefore[bi] = true
					if ai, ok := bToA[bi]; ok {
						affectedAfter[ai] = true
					}
				}
			}
		}
		if firstBefore >= 0 {
			affectedBefore[firstBefore] = true
			if ai, ok := bToA[firstBefore]; ok {
				affectedAfter[ai] = true
			}
		}

		firstAfter := -1
		for ai, as := range after.Servers {
			if as.Listen == listen {
				if firstAfter < 0 {
					firstAfter = ai
				}
				if blockOverlapsPatterns(as.ServerNames, changedPatterns) {
					affectedAfter[ai] = true
					if bi, ok := aToB[ai]; ok {
						affectedBefore[bi] = true
					}
				}
			}
		}
		if firstAfter >= 0 {
			affectedAfter[firstAfter] = true
			if bi, ok := aToB[firstAfter]; ok {
				affectedBefore[bi] = true
			}
		}
	}

	return affectedBefore, affectedAfter
}

func symmetricNormalizedHostDiff(beforeNames, afterNames []string) []string {
	bCatch := config.IsCatchAllServerNames(beforeNames)
	aCatch := config.IsCatchAllServerNames(afterNames)
	if bCatch != aCatch {
		var out []string
		out = append(out, "*")
		for _, n := range beforeNames {
			if norm := config.NormalizeHostPattern(n); norm != "" {
				out = append(out, norm)
			}
		}
		for _, n := range afterNames {
			if norm := config.NormalizeHostPattern(n); norm != "" {
				out = append(out, norm)
			}
		}
		return out
	}
	bSet := make(map[string]bool, len(beforeNames))
	for _, n := range beforeNames {
		if norm := config.NormalizeHostPattern(n); norm != "" {
			bSet[norm] = true
		}
	}
	aSet := make(map[string]bool, len(afterNames))
	for _, n := range afterNames {
		if norm := config.NormalizeHostPattern(n); norm != "" {
			aSet[norm] = true
		}
	}
	var diff []string
	for k := range bSet {
		if !aSet[k] {
			diff = append(diff, k)
		}
	}
	for k := range aSet {
		if !bSet[k] {
			diff = append(diff, k)
		}
	}
	sort.Strings(diff)
	return diff
}

func blockOverlapsPatterns(blockNames, changed []string) bool {
	if len(changed) == 0 {
		return false
	}
	if config.IsCatchAllServerNames(blockNames) {
		return true
	}
	for _, bn := range blockNames {
		for _, ch := range changed {
			if config.HostPatternsOverlap(bn, ch) {
				return true
			}
		}
	}
	return false
}

// computeBlockStructureChanged reports which after server blocks had a location
// added, removed, reordered, or its Match/predicates edited.
func computeBlockStructureChanged(before, after *config.Config) map[int]bool {
	changed := make(map[int]bool)
	bToA := correlateServerBlocks(before.Servers, after.Servers)
	for bi, ai := range bToA {
		bs := before.Servers[bi]
		as := after.Servers[ai]
		if len(bs.Locations) != len(as.Locations) {
			changed[ai] = true
			continue
		}
		for j := range bs.Locations {
			bLoc := bs.Locations[j]
			aLoc := as.Locations[j]
			if routeID(&bLoc) != routeID(&aLoc) || locationKey(&bLoc) != locationKey(&aLoc) {
				changed[ai] = true
				break
			}
		}
	}
	return changed
}

func hiddenOpsTouched(ops []patchRequest, callerHasRaw bool) (map[string]bool, map[string]bool) {
	plugins := make(map[string]bool)
	upstreams := make(map[string]bool)
	if callerHasRaw {
		return plugins, upstreams
	}
	for _, op := range ops {
		switch op.Op {
		case "plugin_set":
			if name := strings.TrimSpace(op.PluginName); name != "" {
				plugins[name] = true
			}
		}
	}
	return plugins, upstreams
}

func classifyEntryPair(
	beforeRaw, beforeEff *config.Config,
	afterRaw, afterEff *config.Config,
	pair correlatedEntryPair,
	hostDispatchBefore map[int]bool,
	hostDispatchAfter map[int]bool,
	blockStructureChangedAfter map[int]bool,
	hiddenPluginsChanged map[string]bool,
	hiddenUpstreamsChanged map[string]bool,
	beforeFindingsByLoc map[[2]int][]config.Diagnostic,
	afterFindingsByLoc map[[2]int][]config.Diagnostic,
) SharingEntryDelta {
	if pair.before == nil && pair.after != nil {
		a := pair.after
		ref := entryRefForLocation(a.loc, "candidate", a.srvIdx, a.locIdx)
		afterMode := declaredSharingMode(a.loc)
		modeWidened := config.IsSharingModeWider(config.SharingModeNotDeclared, afterMode)
		findings := buildEntryPlanFindings(nil, afterFindingsByLoc[[2]int{a.srvIdx, a.locIdx}])
		return SharingEntryDelta{
			Ref:                       ref,
			DisplayLabel:              entryDisplayLabel(a.srv, a.loc),
			Expect:                    "present",
			Class:                     AudienceDeltaUncertain,
			DeclaredModeBefore:        config.SharingModeNotDeclared,
			DeclaredModeAfter:         afterMode,
			DeclaredModeWidened:       modeWidened,
			RequiresAcknowledgement:   true,
			TriggersRetirement:        false,
			Reasons:                   []string{"entry added"},
			IntentFindings:            findings,
			ProviderChecklistRequired: entryRequiresProviderChecklist(afterRaw, afterEff, a.srvIdx, a.locIdx),
		}
	}

	if pair.before != nil && pair.after == nil {
		b := pair.before
		ref := entryRefForLocation(b.loc, "base", b.srvIdx, b.locIdx)
		beforeMode := declaredSharingMode(b.loc)
		return SharingEntryDelta{
			Ref:                       ref,
			DisplayLabel:              entryDisplayLabel(b.srv, b.loc),
			Expect:                    "absent",
			Class:                     AudienceDeltaNarrowed,
			DeclaredModeBefore:        beforeMode,
			DeclaredModeAfter:         config.SharingModeNotDeclared,
			DeclaredModeWidened:       false,
			RequiresAcknowledgement:   false,
			TriggersRetirement:        true,
			KeepOpenEligible:          false,
			RetirementBound:           "2s",
			Reasons:                   []string{"entry removed"},
			ProviderChecklistRequired: entryRequiresProviderChecklist(beforeRaw, beforeEff, b.srvIdx, b.locIdx),
		}
	}

	b := pair.before
	a := pair.after
	ref := entryRefForLocation(a.loc, "candidate", a.srvIdx, a.locIdx)
	beforeMode := declaredSharingMode(b.loc)
	afterMode := declaredSharingMode(a.loc)
	modeWidened := config.IsSharingModeWider(beforeMode, afterMode)

	var classes []string
	var reasons []string
	targetChanged := false

	record := func(cls, reason string) {
		if cls == AudienceDeltaUnchanged || cls == "" {
			return
		}
		classes = append(classes, cls)
		if reason != "" && !slices.Contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}

	// 1. Disclosure precedence for hidden fields set by typed ops (RR11).
	for _, pName := range append(append([]string(nil), a.srv.Plugins...), a.loc.Plugins...) {
		if hiddenPluginsChanged[pName] {
			record(AudienceDeltaUncertain, fmt.Sprintf("plugin %q sets fields hidden without config:raw", pName))
		}
	}
	if a.loc.Plugin != "" && hiddenPluginsChanged[a.loc.Plugin] {
		record(AudienceDeltaUncertain, fmt.Sprintf("plugin %q sets fields hidden without config:raw", a.loc.Plugin))
	}
	if upName := locationUpstreamName(a.loc); upName != "" && hiddenUpstreamsChanged[upName] {
		record(AudienceDeltaUncertain, fmt.Sprintf("upstream %q discovery sets fields hidden without config:raw", upName))
	}

	// 2. Host-dispatch and block-structure expansion (§9.5).
	if hostDispatchBefore[b.srvIdx] || hostDispatchAfter[a.srvIdx] {
		record(AudienceDeltaUncertain, "listener host dispatch or server block order changed")
	}
	if blockStructureChangedAfter[a.srvIdx] {
		record(AudienceDeltaUncertain, "route match, predicates, or declaration order changed in server block")
	}

	// 3. Listener bind / port change.
	if b.srv.Listen != a.srv.Listen {
		cls, why := classifyBindChange(b.srv.Listen, a.srv.Listen)
		record(cls, why)
	}

	// 4. Listener TLS, client_auth, client_address, proxy_protocol, redirect_https.
	for _, item := range classifyListenerAndCertChanges(beforeRaw, afterRaw, b.srv, a.srv, b.loc, a.loc) {
		record(item.class, item.reason)
	}

	// 5. Location Match & Predicates on this entry itself.
	if locationKey(&b.loc) != locationKey(&a.loc) {
		record(AudienceDeltaUncertain, "entry match or predicates changed")
	}

	// 6. Location Rewrites, Plugins, and CORS.
	if !rewritesEqual(b.loc.Rewrites, a.loc.Rewrites) {
		record(AudienceDeltaUncertain, "location rewrites changed")
	}
	if !pluginsEqualForEntry(beforeRaw, afterRaw, b.srv, a.srv, b.loc, a.loc) {
		record(AudienceDeltaUncertain, "request plugin configuration changed")
	}
	if cls, why := classifyCORSChange(b.loc.CORS, a.loc.CORS); cls != AudienceDeltaUnchanged {
		record(cls, why)
	}

	// 7. Location Auth (CIDR allow/deny and credential checks).
	for _, item := range classifyAuthChanges(b.loc.Auth, a.loc.Auth) {
		record(item.class, item.reason)
	}

	// 8. WAF policy (SD-14, RR12).
	if cls, why := classifyWAFChange(beforeRaw, afterRaw, b.loc.WAF, a.loc.WAF); cls != AudienceDeltaUnchanged {
		record(cls, why)
	}

	// 9. Outermost withdrawal gate (withdrawn) and action / target / upstream changes.
	if b.loc.Withdrawn != a.loc.Withdrawn {
		if a.loc.Withdrawn {
			record(AudienceDeltaNarrowed, "location withdrawal gate enabled")
		} else {
			record(AudienceDeltaWidened, "location withdrawal gate cleared")
		}
	}
	if b.loc.Deny != a.loc.Deny {
		if a.loc.Deny {
			record(AudienceDeltaNarrowed, "location deny enabled")
		} else {
			record(AudienceDeltaWidened, "location deny cleared")
		}
	} else if !entryTargetAndUpstreamEqual(beforeRaw, afterRaw, b.loc, a.loc) {
		targetChanged = true
		record(AudienceDeltaUncertain, "entry target or upstream backend configuration changed")
	}

	// Combine effective input classes.
	finalClass := combineAudienceDeltaClasses(classes)
	sharingMetaChanged := !sharingConfigsEqual(b.loc.Sharing, a.loc.Sharing)
	if finalClass == AudienceDeltaUnchanged && sharingMetaChanged {
		finalClass = AudienceDeltaMetadataOnly
		reasons = append(reasons, "declared sharing intent changed")
	}

	findings := buildEntryPlanFindings(
		beforeFindingsByLoc[[2]int{b.srvIdx, b.locIdx}],
		afterFindingsByLoc[[2]int{a.srvIdx, a.locIdx}],
	)
	requiresAck := finalClass == AudienceDeltaWidened || finalClass == AudienceDeltaUncertain || modeWidened
	hasNarrowedConstituent := slices.Contains(classes, AudienceDeltaNarrowed) || a.loc.Withdrawn || a.loc.Deny
	triggersRetirement := finalClass == AudienceDeltaNarrowed || finalClass == AudienceDeltaUncertain || targetChanged || hasNarrowedConstituent
	// Point 1 (ADR 0026 #642 amendment): keep_open_eligible is defined from
	// constituent reasons, never from combined class alone. Any mandatory
	// tightening, withdrawal, or removal overrides keep_open_entries.
	keepOpenEligible := triggersRetirement && !hasNarrowedConstituent
	retirementBound := ""
	if triggersRetirement {
		retirementBound = "2s"
	}

	return SharingEntryDelta{
		Ref:                       ref,
		DisplayLabel:              entryDisplayLabel(a.srv, a.loc),
		Expect:                    "present",
		Class:                     finalClass,
		DeclaredModeBefore:        beforeMode,
		DeclaredModeAfter:         afterMode,
		DeclaredModeWidened:       modeWidened,
		RequiresAcknowledgement:   requiresAck,
		TriggersRetirement:        triggersRetirement,
		KeepOpenEligible:          keepOpenEligible,
		RetirementBound:           retirementBound,
		Reasons:                   reasons,
		IntentFindings:            findings,
		ProviderChecklistRequired: entryRequiresProviderChecklist(beforeRaw, beforeEff, b.srvIdx, b.locIdx) || entryRequiresProviderChecklist(afterRaw, afterEff, a.srvIdx, a.locIdx),
	}
}

type classReason struct {
	class  string
	reason string
}

func combineAudienceDeltaClasses(classes []string) string {
	hasNarrowed := false
	hasWidened := false
	for _, c := range classes {
		switch c {
		case AudienceDeltaUncertain:
			return AudienceDeltaUncertain
		case AudienceDeltaNarrowed:
			hasNarrowed = true
		case AudienceDeltaWidened:
			hasWidened = true
		}
	}
	switch {
	case hasNarrowed && hasWidened:
		return AudienceDeltaUncertain
	case hasNarrowed:
		return AudienceDeltaNarrowed
	case hasWidened:
		return AudienceDeltaWidened
	default:
		return AudienceDeltaUnchanged
	}
}

func classifyBindChange(beforeListen, afterListen string) (string, string) {
	if beforeListen == afterListen {
		return AudienceDeltaUnchanged, ""
	}
	if config.ContainsSecretRef(beforeListen) || config.ContainsSecretRef(afterListen) {
		return AudienceDeltaUncertain, "listener bind comparison involves a secret reference"
	}
	bHost, bPort, err1 := net.SplitHostPort(strings.TrimSpace(beforeListen))
	aHost, aPort, err2 := net.SplitHostPort(strings.TrimSpace(afterListen))
	if err1 != nil || err2 != nil || bPort != aPort || bHost == "" || aHost == "" {
		return AudienceDeltaUncertain, "listener bind or port changed"
	}
	bAddr, err1 := netip.ParseAddr(bHost)
	aAddr, err2 := netip.ParseAddr(aHost)
	if err1 != nil || err2 != nil {
		return AudienceDeltaUncertain, "listener bind changed"
	}
	bAddr = bAddr.Unmap()
	aAddr = aAddr.Unmap()
	if bAddr == aAddr {
		return AudienceDeltaUnchanged, ""
	}
	if bAddr.Is4() && aAddr.Is4() {
		if !bAddr.IsUnspecified() && aAddr.IsUnspecified() {
			return AudienceDeltaWidened, "listener bind widened from specific IPv4 address to 0.0.0.0"
		}
		if bAddr.IsUnspecified() && !aAddr.IsUnspecified() {
			return AudienceDeltaNarrowed, "listener bind narrowed from 0.0.0.0 to specific IPv4 address"
		}
	}
	if bAddr.Is6() && aAddr.Is6() {
		if !bAddr.IsUnspecified() && aAddr.IsUnspecified() {
			return AudienceDeltaWidened, "listener bind widened from specific IPv6 address to ::"
		}
		if bAddr.IsUnspecified() && !aAddr.IsUnspecified() {
			return AudienceDeltaNarrowed, "listener bind narrowed from :: to specific IPv6 address"
		}
	}
	return AudienceDeltaUncertain, "listener bind changed between specific addresses"
}

type listenerEffectiveInputs struct {
	TLSEnabled      bool   `json:"tls_enabled"`
	MinVersion      string `json:"min_version,omitempty"`
	TLSCertSig      string `json:"tls_cert_sig,omitempty"`
	HTTP3Sig        string `json:"http3_sig,omitempty"`
	H2C             bool   `json:"h2c,omitempty"`
	RedirectHTTPS   int    `json:"redirect_https,omitempty"`
	ProxyProtocol   string `json:"proxy_protocol,omitempty"`
	ClientAddrSig   string `json:"client_addr_sig,omitempty"`
	ClientAuthMode  string `json:"client_auth_mode,omitempty"`
	ClientAuthFiles string `json:"client_auth_files,omitempty"`
}

func summarizeListenerInputs(c *config.Config, listen string) (listenerEffectiveInputs, bool) {
	var out listenerEffectiveInputs
	var caFiles, crlFiles, sans, fwd, certs []string
	hasSecretRef := false
	for _, srv := range c.Servers {
		if srv.Listen != listen {
			continue
		}
		if srv.H2C {
			out.H2C = true
		}
		if srv.HTTP3 != nil && srv.HTTP3.Enabled && out.HTTP3Sig == "" {
			out.HTTP3Sig = fmt.Sprintf("enabled:%d", srv.HTTP3.AltSvcMaxAge)
		}
		if srv.RedirectHTTPS != 0 && out.RedirectHTTPS == 0 {
			out.RedirectHTTPS = srv.RedirectHTTPS
		}
		if srv.ProxyProtocol != "" && out.ProxyProtocol == "" {
			out.ProxyProtocol = srv.ProxyProtocol
		}
		if srv.ClientAddress != nil && out.ClientAddrSig == "" {
			for _, tp := range srv.ClientAddress.TrustedProxies {
				if config.ContainsSecretRef(tp) {
					hasSecretRef = true
				}
			}
			out.ClientAddrSig = fmt.Sprintf("%v|%v|%d", srv.ClientAddress.TrustedProxies, srv.ClientAddress.ForwardedHeaders, srv.ClientAddress.MaxHops)
		}
		if srv.TLS != nil && srv.TLS.Enabled {
			out.TLSEnabled = true
			if out.MinVersion == "" {
				out.MinVersion = srv.TLS.MinVersion
			}
			acmeSig := ""
			if srv.TLS.ACME != nil && srv.TLS.ACME.Enabled {
				acmeSig = fmt.Sprintf("acme:%s|%s|%s|%v", srv.TLS.ACME.Email, srv.TLS.ACME.CA, srv.TLS.ACME.Challenge, srv.TLS.ACME.Domains)
			}
			certs = append(certs, fmt.Sprintf("%s|%s|%s", strings.TrimSpace(srv.TLS.Cert), strings.TrimSpace(srv.TLS.Key), acmeSig))
			if srv.TLS.ClientAuth != nil && srv.TLS.ClientAuth.Active() {
				mode := strings.TrimSpace(srv.TLS.ClientAuth.Mode)
				if config.ContainsSecretRef(mode) || config.ContainsSecretRef(srv.TLS.ClientAuth.CAFile) || config.ContainsSecretRef(srv.TLS.ClientAuth.CRLFile) {
					hasSecretRef = true
				}
				if mode == "require" || out.ClientAuthMode == "" {
					out.ClientAuthMode = mode
				}
				caFiles = append(caFiles, strings.TrimSpace(srv.TLS.ClientAuth.CAFile))
				crlFiles = append(crlFiles, strings.TrimSpace(srv.TLS.ClientAuth.CRLFile))
				for _, san := range srv.TLS.ClientAuth.VerifySAN {
					if config.ContainsSecretRef(san) {
						hasSecretRef = true
					}
					sans = append(sans, strings.TrimSpace(san))
				}
				fwd = append(fwd, strings.TrimSpace(srv.TLS.ClientAuth.ForwardCertificate))
			}
		}
	}
	if out.ClientAuthMode == "" {
		out.ClientAuthMode = "none"
	}
	sort.Strings(caFiles)
	sort.Strings(crlFiles)
	sort.Strings(sans)
	sort.Strings(fwd)
	out.TLSCertSig = strings.Join(certs, ";")
	out.ClientAuthFiles = fmt.Sprintf("ca=%v|crl=%v|san=%v|fwd=%v", caFiles, crlFiles, sans, fwd)
	return out, hasSecretRef
}

func classifyListenerAndCertChanges(
	beforeRaw, afterRaw *config.Config,
	bSrv, aSrv config.ServerConfig,
	bLoc, aLoc config.LocationConfig,
) []classReason {
	var out []classReason
	bIn, bRef := summarizeListenerInputs(beforeRaw, bSrv.Listen)
	aIn, aRef := summarizeListenerInputs(afterRaw, aSrv.Listen)

	if bIn.TLSEnabled != aIn.TLSEnabled || bIn.MinVersion != aIn.MinVersion || bIn.TLSCertSig != aIn.TLSCertSig || bIn.HTTP3Sig != aIn.HTTP3Sig || bIn.H2C != aIn.H2C {
		out = append(out, classReason{AudienceDeltaUncertain, "listener TLS or protocol configuration changed"})
	}
	if bSrv.RedirectHTTPS != aSrv.RedirectHTTPS || bIn.RedirectHTTPS != aIn.RedirectHTTPS {
		out = append(out, classReason{AudienceDeltaUncertain, "server redirect_https changed"})
	}
	if bIn.ProxyProtocol != aIn.ProxyProtocol || bIn.ClientAddrSig != aIn.ClientAddrSig {
		out = append(out, classReason{AudienceDeltaUncertain, "listener client_address or proxy_protocol changed"})
	}

	if bIn.ClientAuthMode != aIn.ClientAuthMode || bIn.ClientAuthFiles != aIn.ClientAuthFiles || bLoc.RequireClientCert != aLoc.RequireClientCert {
		if bRef || aRef {
			out = append(out, classReason{AudienceDeltaUncertain, "client_auth comparison involves a secret reference"})
			return out
		}
		if bIn.ClientAuthMode != "none" && aIn.ClientAuthMode != "none" && bIn.ClientAuthFiles != aIn.ClientAuthFiles {
			out = append(out, classReason{AudienceDeltaUncertain, "client_auth CA bundle, CRL, SAN allow-list, or forwarded certificate setting changed"})
			return out
		}
		if bIn.ClientAuthMode != aIn.ClientAuthMode {
			switch {
			case aIn.ClientAuthMode == "require":
				out = append(out, classReason{AudienceDeltaNarrowed, "listener client_auth mode raised to require"})
			case bIn.ClientAuthMode == "require" && aIn.ClientAuthMode == "none":
				out = append(out, classReason{AudienceDeltaWidened, "listener client_auth mode lowered from require"})
			case bIn.ClientAuthMode == "require" && aIn.ClientAuthMode == "request":
				if aLoc.RequireClientCert {
					// Still requires client cert on this route.
				} else {
					out = append(out, classReason{AudienceDeltaWidened, "listener client_auth mode lowered from require to request"})
				}
			default:
				// none <-> request
				if aIn.ClientAuthMode == "request" && aLoc.RequireClientCert && bIn.ClientAuthMode == "none" {
					out = append(out, classReason{AudienceDeltaNarrowed, "client certificate requirement added"})
				} else if bIn.ClientAuthMode == "request" && bLoc.RequireClientCert && aIn.ClientAuthMode == "none" {
					out = append(out, classReason{AudienceDeltaWidened, "client certificate requirement removed"})
				} else {
					out = append(out, classReason{AudienceDeltaUncertain, "listener client_auth mode changed between none and request"})
				}
			}
			return out
		}
		if bLoc.RequireClientCert != aLoc.RequireClientCert && aIn.ClientAuthMode == "request" {
			if aLoc.RequireClientCert {
				out = append(out, classReason{AudienceDeltaNarrowed, "require_client_cert enabled"})
			} else {
				out = append(out, classReason{AudienceDeltaWidened, "require_client_cert cleared"})
			}
		}
	}
	return out
}

func classifyAuthChanges(b, a *config.AuthConfig) []classReason {
	var out []classReason
	var bAllow, bDeny, aAllow, aDeny []string
	if b != nil {
		bAllow, bDeny = b.Allow, b.Deny
	}
	if a != nil {
		aAllow, aDeny = a.Allow, a.Deny
	}
	if cls, why := classifyCIDRChange(bAllow, bDeny, aAllow, aDeny); cls != AudienceDeltaUnchanged {
		out = append(out, classReason{cls, why})
	}
	if cls, why := classifyCredentialChange(b, a); cls != AudienceDeltaUnchanged {
		out = append(out, classReason{cls, why})
	}
	return out
}

func classifyCredentialChange(b, a *config.AuthConfig) (string, string) {
	bHasBasic := b != nil && b.Basic != nil
	bHasJWT := b != nil && b.JWT != nil
	bHasFwd := b != nil && b.ForwardAuth != nil
	aHasBasic := a != nil && a.Basic != nil
	aHasJWT := a != nil && a.JWT != nil
	aHasFwd := a != nil && a.ForwardAuth != nil

	bAny := bHasBasic || bHasJWT || bHasFwd
	aAny := aHasBasic || aHasJWT || aHasFwd

	switch {
	case !bAny && !aAny:
		return AudienceDeltaUnchanged, ""
	case !bAny && aAny:
		return AudienceDeltaNarrowed, "credential check added"
	case bAny && !aAny:
		return AudienceDeltaWidened, "all credential checks removed"
	}

	if bHasBasic != aHasBasic || bHasJWT != aHasJWT || bHasFwd != aHasFwd {
		return AudienceDeltaUncertain, "credential check methods changed"
	}
	if bHasBasic && aHasBasic {
		if strings.TrimSpace(b.Basic.File) != strings.TrimSpace(a.Basic.File) {
			return AudienceDeltaUncertain, "HTTP Basic htpasswd file changed"
		}
	}
	if bHasJWT && aHasJWT {
		if strings.TrimSpace(b.JWT.JWKSURL) != strings.TrimSpace(a.JWT.JWKSURL) ||
			strings.TrimSpace(b.JWT.Issuer) != strings.TrimSpace(a.JWT.Issuer) ||
			strings.TrimSpace(b.JWT.Audience) != strings.TrimSpace(a.JWT.Audience) ||
			!stringSetsEqual(b.JWT.Algorithms, a.JWT.Algorithms) {
			return AudienceDeltaUncertain, "JWT authentication settings changed"
		}
	}
	if bHasFwd && aHasFwd {
		if strings.TrimSpace(b.ForwardAuth.URL) != strings.TrimSpace(a.ForwardAuth.URL) ||
			!stringSetsEqual(b.ForwardAuth.AuthResponseHeaders, a.ForwardAuth.AuthResponseHeaders) {
			return AudienceDeltaUncertain, "forward_auth settings changed"
		}
	}
	return AudienceDeltaUnchanged, ""
}

func classifyCIDRChange(bAllow, bDeny, aAllow, aDeny []string) (string, string) {
	return config.ClassifyCIDRChange(bAllow, bDeny, aAllow, aDeny, true)
}

func classifyCORSChange(b, a *config.CORSConfig) (string, string) {
	bOn := b != nil && b.Enabled
	aOn := a != nil && a.Enabled
	switch {
	case !bOn && !aOn:
		return AudienceDeltaUnchanged, ""
	case !bOn && aOn:
		return AudienceDeltaWidened, "CORS policy enabled"
	case bOn && !aOn:
		return AudienceDeltaNarrowed, "CORS policy disabled"
	}

	for _, s := range append(append([]string(nil), b.AllowedOrigins...), a.AllowedOrigins...) {
		if config.ContainsSecretRef(s) {
			return AudienceDeltaUncertain, "CORS allowed_origins comparison involves a secret reference"
		}
	}
	if !stringSetsEqual(b.AllowedMethods, a.AllowedMethods) || !stringSetsEqual(b.AllowedHeaders, a.AllowedHeaders) {
		return AudienceDeltaUncertain, "CORS allowed_methods or allowed_headers changed"
	}

	bOrigins := make(map[string]bool, len(b.AllowedOrigins))
	for _, o := range b.AllowedOrigins {
		bOrigins[strings.TrimSpace(o)] = true
	}
	aOrigins := make(map[string]bool, len(a.AllowedOrigins))
	for _, o := range a.AllowedOrigins {
		aOrigins[strings.TrimSpace(o)] = true
	}

	added := false
	removed := false
	for o := range aOrigins {
		if !bOrigins[o] {
			added = true
		}
	}
	for o := range bOrigins {
		if !aOrigins[o] {
			removed = true
		}
	}

	if !added && !removed && b.AllowCredentials == a.AllowCredentials {
		return AudienceDeltaUnchanged, ""
	}
	if added && removed {
		return AudienceDeltaUncertain, "CORS allowed_origins added and removed origins"
	}
	if aOrigins["*"] && !bOrigins["*"] && !removed && (!b.AllowCredentials || a.AllowCredentials) {
		return AudienceDeltaWidened, "CORS wildcard origin introduced"
	}
	if !b.AllowCredentials && a.AllowCredentials && !removed {
		return AudienceDeltaWidened, "CORS allow_credentials enabled"
	}
	if added && !removed && a.AllowCredentials && b.AllowCredentials {
		return AudienceDeltaWidened, "CORS origin added while credentials are allowed"
	}
	if added && !removed && !a.AllowCredentials && !b.AllowCredentials {
		return AudienceDeltaWidened, "CORS allowed_origins expanded"
	}
	if removed && !added && (b.AllowCredentials == a.AllowCredentials || (b.AllowCredentials && !a.AllowCredentials)) {
		return AudienceDeltaNarrowed, "CORS allowed_origins narrowed"
	}
	if !added && !removed && b.AllowCredentials && !a.AllowCredentials {
		return AudienceDeltaNarrowed, "CORS allow_credentials disabled"
	}
	return AudienceDeltaUncertain, "CORS policy changed"
}

// wafProvablyWithoutEngineControl reports whether w is provably free of
// ctl:ruleEngine, directives_files, Include, @pmFromFile, and secret references
// from inputs the caller can read (spec §9.5, SD-14, RR12).
func wafProvablyWithoutEngineControl(w config.WAFConfig) bool {
	if !w.Enabled {
		return true
	}
	if len(w.DirectivesFiles) > 0 {
		return false
	}
	if config.ContainsSecretRef(w.InlineRules) || config.ContainsSecretRef(w.Mode) {
		return false
	}
	lower := strings.ToLower(w.InlineRules)
	if strings.Contains(lower, "ctl:ruleengine") ||
		strings.Contains(lower, "include ") ||
		strings.Contains(lower, "include\t") ||
		strings.Contains(lower, "@pmfromfile") {
		return false
	}
	return true
}

func effectiveRawWAFForLocation(c *config.Config, locWAF *config.WAFConfig) config.WAFConfig {
	w := c.WAF
	if locWAF != nil {
		w = *locWAF
	}
	if w.Enabled {
		if strings.TrimSpace(w.Mode) == "" {
			w.Mode = "block"
		}
		if w.BlockStatus == 0 {
			w.BlockStatus = 403
		}
		if w.RequestBodyLimit == 0 {
			w.RequestBodyLimit = config.Size(128 << 10)
		}
	}
	return w
}

func wafNonModeSettingsEqual(b, a config.WAFConfig) bool {
	return b.CRSEnabled == a.CRSEnabled &&
		b.Paranoia == a.Paranoia &&
		b.BlockStatus == a.BlockStatus &&
		b.RequestBodyLimit == a.RequestBodyLimit &&
		b.ResponseBodyCheck == a.ResponseBodyCheck &&
		stringSlicesEqual(b.DirectivesFiles, a.DirectivesFiles) &&
		strings.TrimSpace(b.InlineRules) == strings.TrimSpace(a.InlineRules)
}

func classifyWAFChange(beforeRaw, afterRaw *config.Config, bLocWAF, aLocWAF *config.WAFConfig) (string, string) {
	bw := effectiveRawWAFForLocation(beforeRaw, bLocWAF)
	aw := effectiveRawWAFForLocation(afterRaw, aLocWAF)

	if !bw.Enabled && !aw.Enabled {
		return AudienceDeltaUnchanged, ""
	}
	if config.ContainsSecretRef(bw.Mode) || config.ContainsSecretRef(aw.Mode) ||
		config.ContainsSecretRef(bw.InlineRules) || config.ContainsSecretRef(aw.InlineRules) {
		return AudienceDeltaUncertain, "WAF comparison involves a secret reference"
	}

	bMode := "disabled"
	if bw.Enabled {
		bMode = bw.Mode
	}
	aMode := "disabled"
	if aw.Enabled {
		aMode = aw.Mode
	}

	if bw.Enabled && aw.Enabled && !wafNonModeSettingsEqual(bw, aw) {
		return AudienceDeltaUncertain, "WAF rules or inspection settings changed"
	}
	if bMode == aMode {
		return AudienceDeltaUnchanged, ""
	}

	noCtl := wafProvablyWithoutEngineControl(bw) && wafProvablyWithoutEngineControl(aw)
	if noCtl {
		switch {
		case (bMode == "disabled" && aMode == "detect") || (bMode == "detect" && aMode == "disabled"):
			return AudienceDeltaUnchanged, ""
		case (bMode == "disabled" && aMode == "block") || (bMode == "detect" && aMode == "block"):
			return AudienceDeltaNarrowed, fmt.Sprintf("WAF mode changed from %s to block", bMode)
		case (bMode == "block" && aMode == "detect") || (bMode == "block" && aMode == "disabled"):
			return AudienceDeltaWidened, fmt.Sprintf("WAF mode changed from block to %s", aMode)
		}
	} else {
		switch {
		case bMode == "disabled" && aMode == "detect":
			return AudienceDeltaNarrowed, "WAF with rule files or ctl:ruleEngine enabled in detect mode"
		case bMode == "detect" && aMode == "disabled":
			return AudienceDeltaWidened, "WAF with rule files or ctl:ruleEngine disabled from detect mode"
		case bMode == "disabled" && aMode == "block":
			return AudienceDeltaNarrowed, "WAF enabled in block mode"
		case bMode == "block" && aMode == "disabled":
			return AudienceDeltaWidened, "WAF disabled from block mode"
		case (bMode == "detect" && aMode == "block") || (bMode == "block" && aMode == "detect"):
			return AudienceDeltaUncertain, "WAF with rule files or ctl:ruleEngine switched between detect and block"
		}
	}
	return AudienceDeltaUncertain, "WAF policy changed"
}

func rewritesEqual(b, a []config.RewriteConfig) bool {
	if len(b) != len(a) {
		return false
	}
	for i := range b {
		if b[i] != a[i] {
			return false
		}
	}
	return true
}

func pluginsEqualForEntry(before, after *config.Config, bSrv, aSrv config.ServerConfig, bLoc, aLoc config.LocationConfig) bool {
	if !stringSlicesEqual(bSrv.Plugins, aSrv.Plugins) ||
		!stringSlicesEqual(bLoc.Plugins, aLoc.Plugins) ||
		bLoc.Plugin != aLoc.Plugin {
		return false
	}
	names := make(map[string]bool)
	for _, n := range aSrv.Plugins {
		names[n] = true
	}
	for _, n := range aLoc.Plugins {
		names[n] = true
	}
	if aLoc.Plugin != "" {
		names[aLoc.Plugin] = true
	}
	for name := range names {
		bp, bok := before.Plugins[name]
		ap, aok := after.Plugins[name]
		if bok != aok || !pluginConfigsEqual(bp, ap) {
			return false
		}
	}
	return true
}

func pluginConfigsEqual(b, a config.PluginConfig) bool {
	if b.Path != a.Path ||
		b.Inline != a.Inline ||
		b.Type != a.Type ||
		b.ABI != a.ABI ||
		b.KV != a.KV ||
		b.Fetch != a.Fetch ||
		b.SHA256 != a.SHA256 ||
		b.MaxInstances != a.MaxInstances ||
		b.MaxInvocations != a.MaxInvocations ||
		b.MemoryLimit != a.MemoryLimit ||
		b.Timeout != a.Timeout ||
		b.MaxRequestBody != a.MaxRequestBody ||
		b.MaxResponseBody != a.MaxResponseBody ||
		b.FetchTimeout != a.FetchTimeout ||
		b.MaxFetchResponse != a.MaxFetchResponse ||
		b.KVMaxEntries != a.KVMaxEntries ||
		b.KVMaxBytes != a.KVMaxBytes ||
		!stringSlicesEqual(b.AllowedHosts, a.AllowedHosts) ||
		!maps.Equal(b.Config, a.Config) {
		return false
	}
	return true
}

func locationUpstreamName(loc config.LocationConfig) string {
	switch {
	case loc.GRPCTranscode != nil:
		return upstreamRef(loc.GRPCTranscode.Target, "proxy")
	case loc.GRPC:
		return upstreamRef(loc.ProxyPass, "grpc")
	case loc.ProxyPass != "":
		return upstreamRef(loc.ProxyPass, "proxy")
	default:
		return ""
	}
}

func entryTargetAndUpstreamEqual(before, after *config.Config, bLoc, aLoc config.LocationConfig) bool {
	if locationAction(&bLoc) != locationAction(&aLoc) ||
		locationTarget(&bLoc) != locationTarget(&aLoc) ||
		bLoc.GRPC != aLoc.GRPC ||
		bLoc.Return != aLoc.Return ||
		bLoc.DirectoryListing != aLoc.DirectoryListing ||
		bLoc.AllowHidden != aLoc.AllowHidden ||
		!stringSlicesEqual(bLoc.Index, aLoc.Index) ||
		!stringSlicesEqual(bLoc.TryFiles, aLoc.TryFiles) ||
		!maps.Equal(bLoc.FastCGIParams, aLoc.FastCGIParams) ||
		!reflect.DeepEqual(bLoc.GRPCTranscode, aLoc.GRPCTranscode) ||
		!backendTLSConfigsEqual(bLoc.BackendTLS, aLoc.BackendTLS) {
		return false
	}
	bUp := locationUpstreamName(bLoc)
	aUp := locationUpstreamName(aLoc)
	if bUp != aUp {
		return false
	}
	if aUp != "" {
		bu := findUpstreamByName(before, aUp)
		au := findUpstreamByName(after, aUp)
		if (bu == nil) != (au == nil) {
			return false
		}
		if bu != nil && au != nil && !upstreamsAccessEqual(*bu, *au) {
			return false
		}
	}
	return true
}

func findUpstreamByName(c *config.Config, name string) *config.UpstreamConfig {
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return &c.Upstreams[i]
		}
	}
	return nil
}

func upstreamsAccessEqual(b, a config.UpstreamConfig) bool {
	if b.Strategy != a.Strategy ||
		!reflect.DeepEqual(b.Hash, a.Hash) ||
		!backendTLSConfigsEqual(b.BackendTLS, a.BackendTLS) {
		return false
	}
	if len(b.Servers) != len(a.Servers) {
		return false
	}
	for i := range b.Servers {
		if b.Servers[i] != a.Servers[i] {
			return false
		}
	}
	return reflect.DeepEqual(b.Discovery, a.Discovery)
}

func backendTLSConfigsEqual(b, a *config.BackendTLSConfig) bool {
	if b == nil || a == nil {
		return b == a
	}
	return b.CAFile == a.CAFile &&
		b.CAMode == a.CAMode &&
		b.ClientCert == a.ClientCert &&
		b.ClientKey == a.ClientKey &&
		b.ServerName == a.ServerName &&
		b.MinVersion == a.MinVersion &&
		b.InsecureSkipVerify == a.InsecureSkipVerify &&
		stringSlicesEqual(b.PeerIdentities, a.PeerIdentities)
}

func sharingConfigsEqual(b, a *config.SharingConfig) bool {
	if b == nil || a == nil {
		return b == a
	}
	return b.Mode == a.Mode &&
		stringPtrsEqual(b.AudienceLabel, a.AudienceLabel) &&
		stringPtrsEqual(b.ExternalEnforcer, a.ExternalEnforcer)
}

func buildEntryPlanFindings(beforeDiags, afterDiags []config.Diagnostic) []SharingPlanFinding {
	if len(afterDiags) == 0 {
		return nil
	}
	beforeCodes := make(map[string]bool, len(beforeDiags))
	for _, d := range beforeDiags {
		if d.Code != "" {
			beforeCodes[d.Code] = true
		}
	}
	out := make([]SharingPlanFinding, 0, len(afterDiags))
	for _, d := range afterDiags {
		out = append(out, SharingPlanFinding{
			Code:     d.Code,
			Severity: d.Severity.String(),
			Field:    d.Field,
			Message:  d.Message,
			Hint:     d.Hint,
			Created:  !beforeCodes[d.Code],
		})
	}
	return out
}

func entryRequiresProviderChecklist(rawCfg, effCfg *config.Config, srvIdx, locIdx int) bool {
	if rawCfg == nil {
		rawCfg = effCfg
	}
	if effCfg == nil {
		effCfg = rawCfg
	}
	if rawCfg == nil || srvIdx < 0 || srvIdx >= len(rawCfg.Servers) {
		return false
	}
	rawSrv := rawCfg.Servers[srvIdx]
	if locIdx < 0 || locIdx >= len(rawSrv.Locations) {
		return false
	}
	rawLoc := rawSrv.Locations[locIdx]
	if rawLoc.Sharing == nil {
		return false
	}
	effLoc := rawLoc
	if srvIdx < len(effCfg.Servers) && locIdx < len(effCfg.Servers[srvIdx].Locations) {
		effLoc = effCfg.Servers[srvIdx].Locations[locIdx]
	}
	if strings.TrimSpace(rawLoc.Sharing.ExternalEnforcerValue()) != "" {
		return true
	}
	rawMode := strings.TrimSpace(rawLoc.Sharing.Mode)
	if _, hasFile := config.SecretRefSchemes(rawMode); hasFile {
		return false
	}
	mode := rawMode
	if config.ContainsSecretRef(rawMode) && effLoc.Sharing != nil && !config.ContainsSecretRef(effLoc.Sharing.Mode) {
		mode = strings.TrimSpace(effLoc.Sharing.Mode)
	}
	switch mode {
	case config.SharingModePrivateNetwork:
		return true
	case config.SharingModeRestricted:
		hasNet := config.LocationHasNetworkCheck(effLoc)
		hasCred := config.LocationHasCredentialCheck(effLoc)
		hasCert, _ := config.LocationHasClientCertCheckForPlan(rawCfg, effCfg, srvIdx, effLoc)
		return !hasNet && !hasCred && !hasCert
	default:
		return false
	}
}

// collectFileBackedEntries returns every entry in rawCfg that depends on
// file-backed access inputs (${file:...} references, htpasswd, CA, CRL, WAF
// rule files/includes, or WASM plugin modules; spec §9.5, RR12).
func collectFileBackedEntries(rawCfg *config.Config) []SharingFileBackedEntry {
	if rawCfg == nil {
		return nil
	}
	var out []SharingFileBackedEntry
	for i, srv := range rawCfg.Servers {
		for j, loc := range srv.Locations {
			kinds := entryFileBackedInputKinds(rawCfg, srv, loc)
			if len(kinds) == 0 {
				continue
			}
			out = append(out, SharingFileBackedEntry{
				Ref:          entryRefForLocation(loc, "candidate", i, j),
				DisplayLabel: entryDisplayLabel(srv, loc),
				InputKinds:   kinds,
			})
		}
	}
	return out
}

func entryFileBackedInputKinds(rawCfg *config.Config, srv config.ServerConfig, loc config.LocationConfig) []string {
	var kinds []string
	addKind := func(k string) {
		if !slices.Contains(kinds, k) {
			kinds = append(kinds, k)
		}
	}

	if hasFileSecretRefInEntry(rawCfg, srv, loc) {
		addKind("file_reference")
	}
	if loc.Auth != nil && loc.Auth.Basic != nil && strings.TrimSpace(loc.Auth.Basic.File) != "" {
		addKind("htpasswd")
	}
	// Listener-scoped client_auth CA and CRL files apply across all blocks on the listener.
	for _, ls := range rawCfg.Servers {
		if ls.Listen != srv.Listen || ls.TLS == nil || !ls.TLS.Enabled || ls.TLS.ClientAuth == nil || !ls.TLS.ClientAuth.Active() {
			continue
		}
		if strings.TrimSpace(ls.TLS.ClientAuth.CAFile) != "" {
			addKind("client_ca_file")
		}
		if strings.TrimSpace(ls.TLS.ClientAuth.CRLFile) != "" {
			addKind("client_crl_file")
		}
	}
	w := effectiveRawWAFForLocation(rawCfg, loc.WAF)
	if w.Enabled {
		lower := strings.ToLower(w.InlineRules)
		if len(w.DirectivesFiles) > 0 || strings.Contains(lower, "include ") || strings.Contains(lower, "include\t") || strings.Contains(lower, "@pmfromfile") {
			addKind("waf_rules_file")
		}
	}
	if len(srv.Plugins) > 0 || len(loc.Plugins) > 0 || strings.TrimSpace(loc.Plugin) != "" {
		addKind("plugin_module")
	}
	return kinds
}

func hasFileSecretRefInEntry(rawCfg *config.Config, srv config.ServerConfig, loc config.LocationConfig) bool {
	checkStr := func(s string) bool {
		_, hasFile := config.SecretRefSchemes(s)
		return hasFile
	}
	if checkStr(srv.Listen) {
		return true
	}
	if rawCfg != nil {
		for _, ls := range rawCfg.Servers {
			if ls.Listen != srv.Listen {
				continue
			}
			if ls.TLS != nil && ls.TLS.ClientAuth != nil {
				if checkStr(ls.TLS.ClientAuth.Mode) || checkStr(ls.TLS.ClientAuth.CAFile) || checkStr(ls.TLS.ClientAuth.CRLFile) {
					return true
				}
				for _, san := range ls.TLS.ClientAuth.VerifySAN {
					if checkStr(san) {
						return true
					}
				}
			}
			if ls.ClientAddress != nil {
				for _, tp := range ls.ClientAddress.TrustedProxies {
					if checkStr(tp) {
						return true
					}
				}
			}
		}
	}
	for _, n := range srv.ServerNames {
		if checkStr(n) {
			return true
		}
	}
	if checkStr(loc.Match.Path) || checkStr(loc.ProxyPass) || checkStr(loc.Root) || checkStr(loc.Redirect) {
		return true
	}
	if loc.Sharing != nil {
		if checkStr(loc.Sharing.Mode) || checkStr(loc.Sharing.AudienceLabelValue()) || checkStr(loc.Sharing.ExternalEnforcerValue()) {
			return true
		}
	}
	if loc.Auth != nil {
		for _, cidr := range append(append([]string(nil), loc.Auth.Allow...), loc.Auth.Deny...) {
			if checkStr(cidr) {
				return true
			}
		}
		if loc.Auth.Basic != nil && checkStr(loc.Auth.Basic.File) {
			return true
		}
		if loc.Auth.JWT != nil && (checkStr(loc.Auth.JWT.JWKSURL) || checkStr(loc.Auth.JWT.Issuer) || checkStr(loc.Auth.JWT.Audience)) {
			return true
		}
		if loc.Auth.ForwardAuth != nil && checkStr(loc.Auth.ForwardAuth.URL) {
			return true
		}
	}
	w := effectiveRawWAFForLocation(rawCfg, loc.WAF)
	if w.Enabled && (checkStr(w.Mode) || checkStr(w.InlineRules)) {
		return true
	}
	for _, f := range w.DirectivesFiles {
		if checkStr(f) {
			return true
		}
	}
	return false
}
