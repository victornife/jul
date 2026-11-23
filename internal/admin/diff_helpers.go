// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"jul/internal/config"
)

type serverWrapper struct {
	*config.ServerConfig
}

func addRateLimitExplanations(d *ConfigDiff, scope, key string) {
	d.explain(candidateExplanation(
		"rate.request_not_bandwidth",
		"information",
		scope,
		"Rate and burst limit request counts, not bytes transferred; they do not cap bandwidth.",
		"Use a bandwidth control separately if byte throughput is the constraint.",
		"docs/ratelimit.md",
	))
	switch {
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "header:"):
		d.explain(candidateExplanation(
			"rate.header_key_untrusted",
			"warning",
			scope,
			"A header-based key is client-controlled unless a trusted proxy overwrites it; a missing header falls back to the client IP.",
			"Use only behind a verified proxy, or prefer a validated identity key.",
			"docs/ratelimit.md#key-strategies",
		))
	case strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "jwt:"):
		d.explain(candidateExplanation(
			"rate.jwt_key_requires_auth",
			"tradeoff",
			scope,
			"A JWT-claim key depends on authentication middleware running first; without a usable claim it falls back to the client IP.",
			"Keep authentication ahead of rate limiting and account for shared client IPs.",
			"docs/ratelimit.md#key-strategies",
		))
	default:
		d.explain(candidateExplanation(
			"rate.shared_client_ip",
			"tradeoff",
			scope,
			"IP-keyed clients behind one proxy or NAT can share a bucket; forwarded identity is trusted only according to the listener's trusted-proxy policy.",
			"Review trusted_proxies and expected client grouping for the listener.",
			"docs/ratelimit.md#key-strategies",
		))
	}
}

func addBodyInspectionExplanation(d *ConfigDiff, scope string) {
	d.explain(candidateExplanation(
		"inspection.body_resource_cost",
		"tradeoff",
		scope,
		"Body inspection can require buffering and delay streaming. Governed inspection budgets and logical guest reservations are distinct from physical memory and aggregate spool limits.",
		"Review the inspection limits and the deployment's memory/spool capacity for the affected routes.",
		"docs/admin-runtime-hot-reload.md",
	))
}

func addWAFOutcomeLimitation(d *ConfigDiff, scope string) {
	d.explain(ConfigExplanation{
		Code:           "waf.request_outcome_unobserved",
		Kind:           "information",
		Scope:          scope,
		EvidenceSource: "request_event_producer",
		EvidenceState:  "not_observed",
		Message:        "This preview contains candidate policy only, not request-level evidence. A logged match does not prove interruption, and auth or origin denials are not WAF decisions.",
		NextAction:     "Use an event/result source that identifies the actual producer before attributing a request outcome.",
		Documentation:  "docs/security-posture.md",
	})
}

func relativeOrderChanged(before, after []string) bool {
	beforeCounts, afterCounts := make(map[string]int), make(map[string]int)
	for _, key := range before {
		beforeCounts[key]++
	}
	for _, key := range after {
		afterCounts[key]++
	}
	common := make(map[string]int, len(beforeCounts))
	for key, count := range beforeCounts {
		common[key] = min(count, afterCounts[key])
	}
	filter := func(keys []string) []string {
		remaining := maps.Clone(common)
		var result []string
		for _, key := range keys {
			if remaining[key] > 0 {
				result = append(result, key)
				remaining[key]--
			}
		}
		return result
	}
	return !slices.Equal(filter(before), filter(after))
}

func serverOrderIdentity(server config.ServerConfig) string {
	names := slices.Clone(server.ServerNames)
	slices.Sort(names)
	return fmt.Sprintf("%q:%q", server.Listen, names)
}

func locationOrder(locations []config.LocationConfig) []string {
	keys := make([]string, len(locations))
	for i := range locations {
		if id := routeID(&locations[i]); id != "" {
			keys[i] = "id:" + id
		} else {
			keys[i] = "match:" + locationKey(&locations[i])
		}
	}
	return keys
}

func serverPreviewKey(server config.ServerConfig) string {
	if len(server.ServerNames) > 0 {
		return server.ServerNames[0] + " " + server.Listen
	}
	return server.Listen
}

func upstreamIndex(upstreams []config.UpstreamConfig) map[string]*config.UpstreamConfig {
	m := make(map[string]*config.UpstreamConfig, len(upstreams))
	for i := range upstreams {
		up := &upstreams[i]
		m[up.Name] = up
	}
	return m
}

// sortedKeys returns the keys of a string-keyed map in lexical order so diff
// output is deterministic regardless of map iteration order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stringSlicesEqual reports whether two string slices contain the same elements
// in the same order.
func stringSlicesEqual(x, y []string) bool {
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// sortedStringSlice returns a sorted copy of a string slice; used for
// order-independent comparisons such as CIDR lists or JWT algorithms.
func sortedStringSlice(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	cp := make([]string, len(s))
	copy(cp, s)
	sort.Strings(cp)
	return cp
}

func durStr(d config.Duration) string {
	if time.Duration(d) == 0 {
		return "(none)"
	}
	return time.Duration(d).String()
}

func sizeStr(s config.Size) string {
	if s.Bytes() == 0 {
		return "(none)"
	}
	b, _ := s.MarshalText()
	return string(b)
}

func inspectionSize(value *config.Size) string {
	if value == nil {
		return sizeStr(config.Size(256 << 20))
	}
	return sizeStr(*value)
}

func inspectionResponseHeaderSize(value *config.Size) string {
	if value == nil {
		return sizeStr(config.Size(config.InspectionDefaultMaxResponseHeaderBytes))
	}
	return sizeStr(*value)
}

func inspectionDuration(value *config.Duration, fallback time.Duration) string {
	if value == nil {
		return fallback.String()
	}
	if *value == 0 {
		return "0s (disabled)"
	}
	return time.Duration(*value).String()
}

func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(none)"
	}
	return s
}

// locationKey correlates a location across two revisions. Match type and path
// stopped being unique when ADR 0018 §14 gave a location predicates, and a
// colliding key silently drops one of two same-path routes from the preview —
// an operator approving a change they were never shown. It therefore keys on the
// same normalized predicate set the policy-scope fingerprint uses, not on an
// ordinal: an ordinal would re-key every route below an inserted one, rendering
// an insertion as a mutation of all of them.
func locationKey(l *config.LocationConfig) string {
	return locationCoordinates(l) + "\x00" + l.Match.CanonicalPredicates()
}

// locationCoordinates is the match type and path alone.
func locationCoordinates(l *config.LocationConfig) string {
	t := l.Match.Type
	if t == "" {
		t = "prefix"
	}
	return t + " " + l.Match.Path
}

// locationLabel is the operator-facing name of a route in the diff. It carries a
// predicate summary so two routes sharing a path are distinguishable in a
// preview rather than appearing as the same line twice.
func locationLabel(l *config.LocationConfig) string {
	if summary := locationPredicateSummary(l); summary != "" {
		return locationCoordinates(l) + " " + summary
	}
	return locationCoordinates(l)
}

// locationPredicateSummary renders a route's predicates compactly. Values are
// deliberately omitted: a diff line is not the place to print a header value an
// operator may consider sensitive, and the name and operation are enough to tell
// two routes apart.
func locationPredicateSummary(l *config.LocationConfig) string {
	m := l.Match
	if !m.HasPredicates() {
		return ""
	}
	var parts []string
	if m.Methods != nil {
		parts = append(parts, strings.Join(m.Methods, "|"))
	}
	for _, h := range m.Headers {
		parts = append(parts, h.Name+" "+h.Op)
	}
	for _, q := range m.Query {
		parts = append(parts, "?"+q.Name+" "+q.Op)
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// locationAction summarizes the action a location performs for diff display.
func locationAction(l *config.LocationConfig) string {
	switch {
	case l.GRPCTranscode != nil:
		return "grpc-transcode"
	case l.GRPC:
		return "grpc"
	case l.ProxyPass != "":
		return "proxy"
	case l.FastCGIPass != "":
		return "fastcgi"
	case l.UWSGIPass != "":
		return "uwsgi"
	case l.Redirect != "":
		return "redirect"
	case l.Deny:
		return "deny"
	case l.Return != 0:
		return "return"
	case l.Plugin != "":
		return "plugin"
	case l.Root != "":
		return "static"
	default:
		return "none"
	}
}

// locationTarget reports the dispatch target (upstream/path/url) for an action.
func locationTarget(l *config.LocationConfig) string {
	switch {
	case l.GRPCTranscode != nil:
		return l.GRPCTranscode.Target
	case l.ProxyPass != "":
		return l.ProxyPass
	case l.FastCGIPass != "":
		return l.FastCGIPass
	case l.UWSGIPass != "":
		return l.UWSGIPass
	case l.Redirect != "":
		return l.Redirect
	case l.Root != "":
		return l.Root
	case l.Plugin != "":
		return l.Plugin
	default:
		return ""
	}
}

// coverLocationAction marks the lifecycle registry path that corresponds to
// the location's action as covered, so the registry completeness pass does
// not double-report it.
func coverLocationAction(b, a *config.LocationConfig, d *ConfigDiff) {
	switch {
	case b.ProxyPass != "" || a.ProxyPass != "":
		d.cover("servers.*.locations.*.proxy_pass")
	case b.Root != "" || a.Root != "":
		d.cover("servers.*.locations.*.root")
	case b.Plugin != "" || a.Plugin != "":
		d.cover("servers.*.locations.*.plugins")
	}
}

// coverLocationTarget marks the lifecycle registry path that corresponds to
// the location's target as covered.
func coverLocationTarget(b, a *config.LocationConfig, d *ConfigDiff) {
	coverLocationAction(b, a, d)
}

// routeID returns a location's durable identity, or "" when it has none.
func routeID(l *config.LocationConfig) string {
	if l.RouteID == nil {
		return ""
	}
	return *l.RouteID
}

// correlateLocations pairs before/after locations across a revision boundary
// per ADR 0019 §7. A durable route_id present on both sides is authoritative
// and wins over the fingerprint: two locations sharing a route_id are the
// same resource even if every predicate changed, and two locations that
// happen to share a fingerprint are NOT the same resource if only one side
// carries a route_id (that is an identity change, not a coincidence — it
// renders as remove+add). Only when NEITHER side has a route_id does the
// existing fingerprint-based locationKey decide correlation, exactly as
// before route_id existed.
type locationPair struct {
	key     string // stable sort/display key: route_id when present, else fingerprint
	before  *config.LocationConfig
	after   *config.LocationConfig
	routeID string // non-empty only when this pair was correlated by route_id
	byID    bool
}

func correlateLocations(before, after []config.LocationConfig) (afterPairs, removedPairs []locationPair) {
	byIDBefore := make(map[string]*config.LocationConfig)
	byFPBefore := make(map[string][]*config.LocationConfig)
	for i := range before {
		b := &before[i]
		if id := routeID(b); id != "" {
			byIDBefore[id] = b
		} else {
			key := locationKey(b)
			byFPBefore[key] = append(byFPBefore[key], b)
		}
	}
	usedBefore := make(map[*config.LocationConfig]bool, len(before))

	for i := range after {
		a := &after[i]
		if id := routeID(a); id != "" {
			if b, ok := byIDBefore[id]; ok {
				usedBefore[b] = true
				afterPairs = append(afterPairs, locationPair{key: id, before: b, after: a, routeID: id, byID: true})
				continue
			}
			// The before side either has no route_id or a different one:
			// per ADR 0019 §7 that is not the same resource, regardless of
			// any fingerprint coincidence.
			afterPairs = append(afterPairs, locationPair{key: id, after: a})
			continue
		}
		key := locationKey(a)
		if matches := byFPBefore[key]; len(matches) > 0 {
			b := matches[0]
			byFPBefore[key] = matches[1:]
			usedBefore[b] = true
			afterPairs = append(afterPairs, locationPair{key: key, before: b, after: a})
			continue
		}
		afterPairs = append(afterPairs, locationPair{key: key, after: a})
	}
	for i := range before {
		b := &before[i]
		if usedBefore[b] {
			continue
		}
		key := routeID(b)
		if key == "" {
			key = locationKey(b)
		}
		removedPairs = append(removedPairs, locationPair{key: key, before: b})
	}

	sort.SliceStable(afterPairs, func(i, j int) bool { return afterPairs[i].key < afterPairs[j].key })
	sort.SliceStable(removedPairs, func(i, j int) bool { return removedPairs[i].key < removedPairs[j].key })
	return afterPairs, removedPairs
}

// diffLocations compares the locations (routes) within a single server block,
// reporting additions, removals, and per-field modifications with operational
// consequences (action/target, auth, cache, compression, rate limit, body
// size, proxy timeouts).
func diffLocations(server string, before, after []config.LocationConfig, beforeGlobWAF, afterGlobWAF config.WAFConfig, beforeConfig, afterConfig *config.Config, d *ConfigDiff) {
	oldOrder, newOrder := locationOrder(before), locationOrder(after)
	if len(oldOrder) == len(newOrder) {
		counts := make(map[string]int, len(oldOrder))
		for _, key := range oldOrder {
			counts[key]++
		}
		for i, key := range oldOrder {
			if counts[key] > 1 && key == newOrder[i] && !reflect.DeepEqual(before[i], after[i]) {
				d.mod(DiffEntry{Kind: "location", Name: server, Detail: "Change duplicate route declaration on " + server}, "server "+server+" duplicate route")
				break
			}
		}
	}
	// route_id is fully accounted for by correlateLocations: it decides
	// resource identity (matched/added/removed) rather than being reported as
	// a plain field change, so the registry completeness pass must not
	// separately flag it.
	d.cover("servers.*.locations.*.route_id")
	afterPairs, removedPairs := correlateLocations(before, after)

	// Collect the ID-less unmatched adds/removes up front so each side can
	// check the other for a plausible same-coordinates counterpart when
	// deciding whether to admit the relationship is unproven (ADR 0019 §7:
	// "uncorrelated (no route_id)").
	var addedNoID, removedNoID []*config.LocationConfig
	for _, p := range afterPairs {
		if p.before == nil && routeID(p.after) == "" {
			addedNoID = append(addedNoID, p.after)
		}
	}
	for _, p := range removedPairs {
		if routeID(p.before) == "" {
			removedNoID = append(removedNoID, p.before)
		}
	}

	for _, p := range afterPairs {
		a := p.after
		label := locationLabel(a)
		name := server + " " + label
		if p.before == nil {
			detail := "Add route " + label + " on " + server
			if note := addedRouteIdentityNote(a, before, removedNoID); note != "" {
				detail += " (" + note + ")"
			}
			d.add(DiffEntry{Kind: "location", Name: name, After: locationAction(a) + " → " + orNone(locationTarget(a)), Detail: detail}, "route "+name)
			continue
		}
		diffLocationFields(server, label, p.before, a, beforeGlobWAF, afterGlobWAF, beforeConfig, afterConfig, d)
	}
	for _, p := range removedPairs {
		b := p.before
		label := locationLabel(b)
		name := server + " " + label
		detail := "Remove route " + label + " on " + server
		if note := removedRouteIdentityNote(b, after, addedNoID); note != "" {
			detail += " (" + note + ")"
		}
		d.del(DiffEntry{Kind: "location", Name: name, Before: locationAction(b) + " → " + orNone(locationTarget(b)), Detail: detail}, "route "+name)
		// Editing a predicate re-keys the route, so it renders as a removal
		// plus an addition. Warning that traffic will stop being handled would
		// be false whenever another route still covers the same coordinates.
		if !coordinatesStillPresent(b, after) {
			d.warn("Removing route %s on %s will stop matching requests from being handled by it.", label, server)
		}
	}
}

// fingerprintPeer returns the location in candidates sharing target's exact
// match-type+path+predicate fingerprint, or nil. It is used only to explain
// an otherwise-uncorrelated add/remove pair — it never itself decides
// correlation, which is correlateLocations' job alone.
func fingerprintPeer(target *config.LocationConfig, candidates []config.LocationConfig) *config.LocationConfig {
	key := locationKey(target)
	for i := range candidates {
		if locationKey(&candidates[i]) == key {
			return &candidates[i]
		}
	}
	return nil
}

// addedRouteIdentityNote explains, when it can, why an added route was not
// correlated with anything in the previous revision (ADR 0019 §7). It never
// asserts a relationship route_id itself does not establish — an add with no
// plausible counterpart gets no note at all.
func addedRouteIdentityNote(a *config.LocationConfig, before []config.LocationConfig, removedNoID []*config.LocationConfig) string {
	if id := routeID(a); id != "" {
		peer := fingerprintPeer(a, before)
		if peer == nil {
			return ""
		}
		if routeID(peer) == "" {
			return "route_id introduced on an existing route"
		}
		return "route_id changed"
	}
	// No route_id on the added side: only admit the ambiguity when a
	// same-coordinates removed route exists to be confused with — a
	// same-fingerprint match would already have been correlated by
	// correlateLocations' fallback, so reaching here with a coordinates match
	// means the predicates changed.
	want := locationCoordinates(a)
	for _, r := range removedNoID {
		if locationCoordinates(r) == want {
			return "uncorrelated (no route_id)"
		}
	}
	return ""
}

// removedRouteIdentityNote is addedRouteIdentityNote's mirror for a removed
// route.
func removedRouteIdentityNote(b *config.LocationConfig, after []config.LocationConfig, addedNoID []*config.LocationConfig) string {
	if id := routeID(b); id != "" {
		peer := fingerprintPeer(b, after)
		if peer == nil {
			return ""
		}
		if routeID(peer) == "" {
			return "route_id removed"
		}
		return "route_id changed"
	}
	want := locationCoordinates(b)
	for _, a := range addedNoID {
		if locationCoordinates(a) == want {
			return "uncorrelated (no route_id)"
		}
	}
	return ""
}

// coordinatesStillPresent reports whether any location in the new revision keeps
// the same match type and path.
func coordinatesStillPresent(l *config.LocationConfig, after []config.LocationConfig) bool {
	want := locationCoordinates(l)
	for i := range after {
		if locationCoordinates(&after[i]) == want {
			return true
		}
	}
	return false
}

func diffLocationFields(server, key string, b, a *config.LocationConfig, beforeGlobWAF, afterGlobWAF config.WAFConfig, beforeConfig, afterConfig *config.Config, d *ConfigDiff) {
	name := server + " " + key

	explainLocationBackendTLS(name, beforeConfig, afterConfig, b, a, d)
	if !reflect.DeepEqual(b.Resilience, a.Resilience) {
		d.explain(candidateExplanation(
			"resilience.location_override",
			"tradeoff",
			name,
			"Route-level resilience settings override selected pool defaults; the exact effective timeout and admission behavior also depends on the upstream and protocol.",
			"Review the route override together with its upstream pool policy.",
			"docs/upstreams.md",
		))
	}

	if locationAction(b) != locationAction(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: locationAction(b), After: locationAction(a), Detail: "Change action of route " + key}, "route "+name+" action")
	}
	coverLocationAction(b, a, d)
	if locationTarget(b) != locationTarget(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: orNone(locationTarget(b)), After: orNone(locationTarget(a)), Detail: "Change target of route " + key}, "route "+name+" target")
		d.warn("Changing the target of route %s on %s redirects matching traffic to a different backend or destination.", key, server)
		d.explain(candidateExplanation(
			"routing.target_retargeted",
			"warning",
			name,
			"Requests matching this route will be sent to a different backend or destination; unrelated routes are not implied to change. The preview does not establish external reachability.",
			"Verify the target service and its expected TLS/authentication requirements.",
			"docs/upstreams.md",
		))
	}
	coverLocationTarget(b, a, d)

	// Effective WAF diff (inherits global policy when loc.WAF is nil).
	bEffWAF := locationEffectiveWAF(b, beforeGlobWAF)
	aEffWAF := locationEffectiveWAF(a, afterGlobWAF)
	bWAFOn := bEffWAF != nil
	aWAFOn := aEffWAF != nil
	if bWAFOn != aWAFOn {
		action := "Enable"
		if !aWAFOn {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "waf", Name: name, Detail: fmt.Sprintf("%s WAF on route %s", action, key)}, "route "+name+" waf")
		if action == "Enable" {
			d.warn("Enabling WAF on route %s on %s may reject legitimate requests while rules are tuned.", key, server)
			d.explain(candidateExplanation(
				"waf.effective_policy",
				"warning",
				name,
				"WAF is enabled for this route after applying its inherited policy and local override. The preview does not prove request-level enforcement.",
				"Check the effective mode, build validation, and intended public/private audience.",
				"docs/security-posture.md",
			))
			addWAFOutcomeLimitation(d, name)
		} else {
			d.warn("Disabling WAF on route %s on %s removes rule inspection for that route.", key, server)
			d.explain(candidateExplanation(
				"waf.effective_policy",
				"warning",
				name,
				"Disabling the effective WAF policy removes rule inspection for this route, including an inherited global policy when no local override remains.",
				"Confirm another intended control protects the route, or that public access is deliberate.",
				"docs/security-posture.md",
			))
			addWAFOutcomeLimitation(d, name)
		}
	} else if aWAFOn && bWAFOn {
		// Both enabled — compare effective policy fields
		if bEffWAF.Mode != aEffWAF.Mode {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: bEffWAF.Mode, After: aEffWAF.Mode, Detail: "Change WAF mode on route " + key}, "route "+name+" waf mode")
			if bEffWAF.Mode == "block" && aEffWAF.Mode == "detect" {
				d.warn("Switching WAF to detect mode on route %s on %s stops blocking threats.", key, server)
			}
			switch aEffWAF.Mode {
			case "detect":
				d.explain(candidateExplanation(
					"waf.detect_not_block",
					"warning",
					name,
					"The effective route mode is detect: matches are recorded but not blocked. This candidate preview does not establish whether a specific request was interrupted.",
					"Review the inherited mode and use block only after tuning false positives.",
					"docs/security-posture.md",
				))
			case "block":
				d.explain(candidateExplanation(
					"waf.block_policy",
					"warning",
					name,
					"Block mode can interrupt requests that match this route's effective policy; this candidate preview does not attribute or prove any individual request outcome.",
					"Review inherited rules and false-positive impact before applying.",
					"docs/security-posture.md",
				))
			}
			addWAFOutcomeLimitation(d, name)
		}
		if bEffWAF.BlockStatus != aEffWAF.BlockStatus {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: fmt.Sprintf("%d", bEffWAF.BlockStatus), After: fmt.Sprintf("%d", aEffWAF.BlockStatus), Detail: "Change WAF block status on route " + key}, "route "+name+" waf block_status")
		}
		if bEffWAF.Paranoia != aEffWAF.Paranoia {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: fmt.Sprintf("%d", bEffWAF.Paranoia), After: fmt.Sprintf("%d", aEffWAF.Paranoia), Detail: "Change WAF paranoia level on route " + key}, "route "+name+" waf paranoia")
			if aEffWAF.Paranoia < bEffWAF.Paranoia {
				d.warn("Lowering WAF paranoia on route %s on %s reduces rule coverage.", key, server)
			}
		}
		if bEffWAF.BlockingParanoia != aEffWAF.BlockingParanoia {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: fmt.Sprintf("%d", bEffWAF.BlockingParanoia), After: fmt.Sprintf("%d", aEffWAF.BlockingParanoia), Detail: "Change WAF blocking paranoia level on route " + key}, "route "+name+" waf blocking_paranoia")
			if aEffWAF.BlockingParanoia < bEffWAF.BlockingParanoia {
				d.warn("Lowering WAF blocking paranoia on route %s on %s reduces blocking rule coverage.", key, server)
			}
		}
		if bEffWAF.DetectionParanoia != aEffWAF.DetectionParanoia {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: fmt.Sprintf("%d", bEffWAF.DetectionParanoia), After: fmt.Sprintf("%d", aEffWAF.DetectionParanoia), Detail: "Change WAF detection paranoia level on route " + key}, "route "+name+" waf detection_paranoia")
			d.explain(candidateExplanation(
				"waf.detection_vs_blocking",
				"tradeoff",
				name,
				"Detection and blocking paranoia levels are separate; raising detection does not by itself raise the blocking level.",
				"Review both levels and the effective mode when changing rule coverage.",
				"docs/security-posture.md",
			))
		}
		if formatWAFExceptionsDiff(bEffWAF.Exceptions) != formatWAFExceptionsDiff(aEffWAF.Exceptions) {
			d.mod(DiffEntry{
				Kind:   "waf",
				Name:   name,
				Before: formatWAFExceptionsDiff(bEffWAF.Exceptions),
				After:  formatWAFExceptionsDiff(aEffWAF.Exceptions),
				Detail: fmt.Sprintf("Change WAF exceptions on route %s (%d -> %d)", key, len(bEffWAF.Exceptions), len(aEffWAF.Exceptions)),
			}, "route "+name+" waf exceptions")
			for _, exc := range aEffWAF.Exceptions {
				if config.CanonicalizeWAFExceptionScope(exc) == "rule_remove" {
					d.warn("WAF exception on route %s on %s removes entire rule %d (scope=rule_remove); prefer narrow_target when possible.", key, server, exc.RuleID)
				}
			}
			d.explain(candidateExplanation(
				"waf.exception_scope",
				"warning",
				name,
				"These exclusions apply to this route's effective policy; a global exclusion also affects every route inheriting the global policy. review_at is a review reminder, not an automatic enforcement expiry.",
				"Prefer a route-scoped narrow_target exclusion when only this route needs an exception.",
				"docs/security-posture.md",
			))
		}
		if bEffWAF.CRSEnabled != aEffWAF.CRSEnabled {
			action := "Enable"
			if !aEffWAF.CRSEnabled {
				action = "Disable"
			}
			d.mod(DiffEntry{Kind: "waf", Name: name, Detail: fmt.Sprintf("%s CRS on route %s", action, key)}, "route "+name+" waf crs")
			if !aEffWAF.CRSEnabled {
				d.warn("Disabling CRS on route %s on %s removes the core rule set.", key, server)
			}
		}
		if bEffWAF.RequestBodyLimit != aEffWAF.RequestBodyLimit {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: sizeStr(bEffWAF.RequestBodyLimit), After: sizeStr(aEffWAF.RequestBodyLimit), Detail: "Change WAF request body limit on route " + key}, "route "+name+" waf body_limit")
			if aEffWAF.RequestBodyLimit.Bytes() == 0 && bEffWAF.RequestBodyLimit.Bytes() != 0 {
				d.warn("Removing the WAF request body limit on route %s on %s allows arbitrarily large uploads to be inspected.", key, server)
			}
			addBodyInspectionExplanation(d, name)
		}
		bf, af := strings.Join(bEffWAF.DirectivesFiles, ","), strings.Join(aEffWAF.DirectivesFiles, ",")
		if bf != af {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: orNone(bf), After: orNone(af), Detail: "Change WAF directive files on route " + key}, "route "+name+" waf directives_files")
		}
		if strings.TrimSpace(bEffWAF.InlineRules) != strings.TrimSpace(aEffWAF.InlineRules) {
			d.mod(DiffEntry{Kind: "waf", Name: name, Detail: "Change WAF inline rules on route " + key}, "route "+name+" waf inline_rules")
		}
		if bEffWAF.ResponseBodyCheck != aEffWAF.ResponseBodyCheck {
			action := "Enable"
			if !aEffWAF.ResponseBodyCheck {
				action = "Disable"
			}
			d.mod(DiffEntry{Kind: "waf", Name: name, Detail: fmt.Sprintf("%s WAF response-body inspection on route %s", action, key)}, "route "+name+" waf response_body_check")
			addBodyInspectionExplanation(d, name)
		}
		if bEffWAF.Mode != aEffWAF.Mode ||
			bEffWAF.Paranoia != aEffWAF.Paranoia ||
			bEffWAF.BlockingParanoia != aEffWAF.BlockingParanoia ||
			bEffWAF.DetectionParanoia != aEffWAF.DetectionParanoia ||
			formatWAFExceptionsDiff(bEffWAF.Exceptions) != formatWAFExceptionsDiff(aEffWAF.Exceptions) {
			addWAFOutcomeLimitation(d, name)
		}
	}
	d.cover("servers.*.locations.*.waf")

	// Auth changes (CIDR lists, Basic, JWT, forward-auth).
	diffAuth(server, key, b.Auth, a.Auth, d)

	// Cache toggle and route-scoped cache policy (#623).
	if b.Cache != a.Cache {
		action := "Enable"
		if !a.Cache {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "cache", Name: name, Detail: fmt.Sprintf("%s response cache on route %s", action, key)}, "route "+name+" cache")
		if a.Cache && a.Auth != nil {
			d.warn("Enabling cache on authenticated route %s on %s risks serving private responses to other clients.", key, server)
		}
		if a.Cache {
			d.explain(candidateExplanation(
				"cache.shared_copy",
				"warning",
				name,
				"This route uses a shared response cache. Authentication and cookie-session separation must be enforced by the cache policy and origin response rules.",
				"Review authentication, cookie bypass, and origin Cache-Control behavior for this route.",
				"docs/cache.md",
			))
		}
	}
	d.cover("servers.*.locations.*.cache")
	diffLocationCachePolicy(server, key, b, a, d)

	// Per-route rate-limit override.
	diffRateLimit(server, key, b.RateLimit, a.RateLimit, d)

	// mTLS require-client-cert toggle.
	if b.RequireClientCert != a.RequireClientCert {
		action := "Require"
		if !a.RequireClientCert {
			action = "Stop requiring"
		}
		d.mod(DiffEntry{Kind: "mtls", Name: name, Detail: fmt.Sprintf("%s client certificate on route %s", action, key)}, "route "+name+" client cert")
		d.explain(candidateExplanation(
			"tls.mtls_route_prerequisite",
			"warning",
			name,
			"Changing the route's client-certificate requirement changes which handshakes or requests can proceed; this candidate does not prove that intended clients possess acceptable certificates.",
			"Verify the listener's certificate policy and the route's intended client population.",
			"docs/mtls.md",
		))
	}

	// Per-route body-size override.
	if b.ClientMaxBodySize != a.ClientMaxBodySize {
		d.mod(DiffEntry{Kind: "timeouts", Name: name, Before: sizeStr(b.ClientMaxBodySize), After: sizeStr(a.ClientMaxBodySize), Detail: "Change body size limit on route " + key}, "route "+name+" body limit")
	}

	// Per-route plugin middleware chain (loc.Plugins) — attach/detach.
	if attached, detached := stringSetDiff(b.Plugins, a.Plugins); len(attached) > 0 || len(detached) > 0 {
		for _, p := range attached {
			d.mod(DiffEntry{Kind: "plugin", Name: name, After: p, Detail: fmt.Sprintf("Attach plugin %s to route %s", p, key)}, "route "+name+" plugin "+p)
			d.warn("Attaching plugin %s to route %s on %s runs guest WASM in the request path; it only loads in binaries built with the wasmplugins tag.", p, key, server)
		}
		for _, p := range detached {
			d.mod(DiffEntry{Kind: "plugin", Name: name, Before: p, Detail: fmt.Sprintf("Detach plugin %s from route %s", p, key)}, "route "+name+" plugin "+p)
		}
	}
	if !slices.Equal(b.Plugins, a.Plugins) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: "Change plugin order or membership on route " + key}, "route "+name+" plugin order")
	}
	d.cover("servers.*.locations.*.plugins")

	// Proxy timeouts.
	diffProxyTimeouts(server, key, b, a, d)

	// Response-header operations (ADR 0018 §8).
	diffResponseHeaders(server, key, b.ResponseHeaders, a.ResponseHeaders, d)

	// CORS policy (ADR 0018 §9).
	diffCORS(server, key, b.CORS, a.CORS, d)

	// Declared sharing intent (ADR 0026 Decision 2, spec §4.5).
	diffSharing(server, key, b.Sharing, a.Sharing, d)

	// Outermost withdrawal gate (ADR 0026 #642 amendment, spec §9.3).
	if b.Withdrawn != a.Withdrawn {
		if a.Withdrawn {
			d.mod(DiffEntry{Kind: "location", Name: name, Before: "active", After: "withdrawn", Detail: "Withdraw route " + key + " (deny new requests at stage 0)"}, "route "+name+" withdrawn")
		} else {
			d.mod(DiffEntry{Kind: "location", Name: name, Before: "withdrawn", After: "active", Detail: "Restore withdrawn route " + key}, "route "+name+" withdrawn")
			d.warn("Clearing withdrawn = true on route %s on %s restores external access to that route.", key, server)
		}
	}
	d.cover("servers.*.locations.*.withdrawn")
}

func diffSharing(server, key string, b, a *config.SharingConfig, d *ConfigDiff) {
	name := server + " " + key
	switch {
	case b == nil && a == nil:
	case b == nil && a != nil:
		d.mod(DiffEntry{
			Kind:   "sharing",
			Name:   name,
			Before: config.SharingModeNotDeclared,
			After:  a.Mode,
			Detail: fmt.Sprintf("Declare sharing intent %q on route %s", a.Mode, key),
		}, "route "+name+" sharing")
		if config.IsSharingModeWider(config.SharingModeNotDeclared, a.Mode) {
			d.warn("Declaring sharing.mode = %q on route %s on %s widens the declared audience intent.", a.Mode, key, server)
		}
	case b != nil && a == nil:
		d.mod(DiffEntry{
			Kind:   "sharing",
			Name:   name,
			Before: b.Mode,
			After:  config.SharingModeNotDeclared,
			Detail: fmt.Sprintf("Clear declared sharing intent from route %s", key),
		}, "route "+name+" sharing")
	default:
		if b.Mode != a.Mode {
			d.mod(DiffEntry{
				Kind:   "sharing",
				Name:   name,
				Before: b.Mode,
				After:  a.Mode,
				Detail: fmt.Sprintf("Change declared sharing mode on route %s (%s -> %s)", key, b.Mode, a.Mode),
			}, "route "+name+" sharing mode")
			if config.IsSharingModeWider(b.Mode, a.Mode) {
				d.warn("Changing sharing.mode from %q to %q on route %s on %s widens the declared audience intent.", b.Mode, a.Mode, key, server)
			}
		}
		if !stringPtrsEqual(b.AudienceLabel, a.AudienceLabel) {
			d.mod(DiffEntry{
				Kind:   "sharing",
				Name:   name,
				Detail: fmt.Sprintf("Change sharing audience_label on route %s", key),
			}, "route "+name+" sharing audience_label")
		}
		if !stringPtrsEqual(b.ExternalEnforcer, a.ExternalEnforcer) {
			d.mod(DiffEntry{
				Kind:   "sharing",
				Name:   name,
				Detail: fmt.Sprintf("Change sharing external_enforcer on route %s", key),
			}, "route "+name+" sharing external_enforcer")
		}
	}
	d.cover("servers.*.locations.*.sharing")
}

// diffResponseHeaders compares a route's ordered response-header operations.
// Values are omitted from the diff (the same reasoning as
// locationPredicateSummary): the operation and header name are enough to tell
// an operator what changed without repeating a value they may consider
// sensitive, and the full list is already visible in the preview's generated
// config.
func diffResponseHeaders(server, key string, b, a []config.ResponseHeaderOp, d *ConfigDiff) {
	name := server + " " + key
	if responseHeaderOpsEqual(b, a) {
		d.cover("servers.*.locations.*.response_headers")
		return
	}
	d.mod(DiffEntry{Kind: "response_headers", Name: name, Before: responseHeaderOpsSummary(b), After: responseHeaderOpsSummary(a), Detail: "Change response-header operations on route " + key}, "route "+name+" response_headers")
	d.cover("servers.*.locations.*.response_headers")
}

func responseHeaderOpsEqual(b, a []config.ResponseHeaderOp) bool {
	if len(b) != len(a) {
		return false
	}
	for i := range b {
		if b[i].Op != a[i].Op || b[i].Name != a[i].Name || !stringPtrsEqual(b[i].Value, a[i].Value) {
			return false
		}
	}
	return true
}

func responseHeaderOpsSummary(ops []config.ResponseHeaderOp) string {
	if len(ops) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		parts = append(parts, op.Op+" "+op.Name)
	}
	return strings.Join(parts, ", ")
}

func stringPtrsEqual(x, y *string) bool {
	if x == nil || y == nil {
		return x == y
	}
	return *x == *y
}

// diffCORS compares a route's CORS policy, reporting enable/disable and which
// fields changed by name (ADR 0018 §9). Origin/method/header lists are not
// classified sensitive, so their values are surfaced elsewhere (the preview's
// generated config); this comparator names only which fields moved, matching
// the granularity #147 requires without duplicating the full policy here.
func diffCORS(server, key string, b, a *config.CORSConfig, d *ConfigDiff) {
	name := server + " " + key
	bOn, aOn := b != nil && b.Enabled, a != nil && a.Enabled
	switch {
	case a == nil && b != nil:
		d.mod(DiffEntry{Kind: "cors", Name: name, Detail: "Remove CORS policy from route " + key}, "route "+name+" cors")
	case b == nil && a != nil:
		d.mod(DiffEntry{Kind: "cors", Name: name, Detail: "Add CORS policy to route " + key}, "route "+name+" cors")
		if a.Enabled {
			d.warn("Adding an enabled CORS policy to route %s on %s allows cross-origin browser requests from the configured origins.", key, server)
		}
	case bOn != aOn:
		action := "Enable"
		if !aOn {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "cors", Name: name, Detail: fmt.Sprintf("%s CORS on route %s", action, key)}, "route "+name+" cors enabled")
	}
	if b != nil && a != nil {
		for _, field := range corsFieldsChanged(b, a) {
			d.mod(DiffEntry{Kind: "cors", Name: name, Detail: fmt.Sprintf("Change CORS %s on route %s", field, key)}, "route "+name+" cors "+field)
		}
		if !b.AllowCredentials && a.AllowCredentials {
			d.warn("Enabling allow_credentials in the CORS policy on route %s on %s lets approved browsers send cookies/credentials cross-origin.", key, server)
		}
	}
	d.cover("servers.*.locations.*.cors")
}

// corsFieldsChanged returns the names of the CORSConfig fields that differ
// between b and a, in a fixed order.
func corsFieldsChanged(b, a *config.CORSConfig) []string {
	var changed []string
	if !stringSlicesEqual(sortedStringSlice(b.AllowedOrigins), sortedStringSlice(a.AllowedOrigins)) {
		changed = append(changed, "allowed_origins")
	}
	if !stringSlicesEqual(sortedStringSlice(b.AllowedMethods), sortedStringSlice(a.AllowedMethods)) {
		changed = append(changed, "allowed_methods")
	}
	if !stringSlicesEqual(sortedStringSlice(b.AllowedHeaders), sortedStringSlice(a.AllowedHeaders)) {
		changed = append(changed, "allowed_headers")
	}
	if !stringSlicesEqual(sortedStringSlice(b.ExposedHeaders), sortedStringSlice(a.ExposedHeaders)) {
		changed = append(changed, "exposed_headers")
	}
	if b.AllowCredentials != a.AllowCredentials {
		changed = append(changed, "allow_credentials")
	}
	if durPtrStr(b.MaxAge) != durPtrStr(a.MaxAge) {
		changed = append(changed, "max_age")
	}
	return changed
}

// durPtrStr renders an optional duration for comparison/display, treating a
// nil pointer as its own distinct state ("(unset)") so an omitted max_age never
// compares equal to an explicit "0s".
func durPtrStr(d *config.Duration) string {
	if d == nil {
		return "(unset)"
	}
	return durStr(*d)
}

func cacheDurPtrStr(d *config.Duration, unsetLabel string) string {
	if d == nil {
		return unsetLabel
	}
	return time.Duration(*d).String()
}

func normalizeCookieListForDiff(cookies []string) []string {
	if len(cookies) == 0 {
		return nil
	}
	out := make([]string, len(cookies))
	for i, c := range cookies {
		out[i] = strings.ToLower(strings.TrimSpace(c))
	}
	sort.Strings(out)
	return out
}

func diffLocationCachePolicy(server, key string, bLoc, aLoc *config.LocationConfig, d *ConfigDiff) {
	defer d.cover("servers.*.locations.*.cache_policy")
	name := server + " " + key
	b, a := bLoc.CachePolicy, aLoc.CachePolicy
	switch {
	case b == nil && a == nil:
		return
	case b == nil && a != nil:
		d.mod(DiffEntry{Kind: "cache", Name: name, Detail: "Add route cache_policy override on route " + key}, "route "+name+" cache_policy")
		b = &config.LocationCachePolicy{}
	case b != nil && a == nil:
		d.mod(DiffEntry{Kind: "cache", Name: name, Detail: "Remove route cache_policy override from route " + key + " (inherits global [cache])"}, "route "+name+" cache_policy")
		if aLoc.Cache && (b.BypassAnyCookie || len(b.BypassCookies) > 0) {
			d.warn("Removing cookie-bypass controls on cached route %s on %s allows requests carrying cookies to use the shared cache unless origin headers prohibit it.", key, server)
		}
		return
	}
	for _, field := range []struct {
		name       string
		unsetLabel string
		before     *config.Duration
		after      *config.Duration
	}{
		{"default_ttl", "(inherit)", b.DefaultTTL, a.DefaultTTL},
		{"max_ttl", "(none)", b.MaxTTL, a.MaxTTL},
		{"stale_while_revalidate", "(inherit)", b.StaleWhileRevalidate, a.StaleWhileRevalidate},
		{"stale_if_error", "(inherit)", b.StaleIfError, a.StaleIfError},
	} {
		bs := cacheDurPtrStr(field.before, field.unsetLabel)
		as := cacheDurPtrStr(field.after, field.unsetLabel)
		if bs != as {
			d.mod(DiffEntry{
				Kind:   "cache",
				Name:   name,
				Before: bs,
				After:  as,
				Detail: fmt.Sprintf("Change cache_policy.%s on route %s", field.name, key),
			}, "route "+name+" cache_policy "+field.name)
			switch field.name {
			case "default_ttl", "max_ttl":
				d.explain(candidateExplanation(
					"cache.origin_freshness",
					"information",
					name,
					"Route TTL settings are bounded policy inputs, not proof that the origin response is cacheable; origin freshness directives still matter.",
					"Review origin Cache-Control and Expires behavior for this route.",
					"docs/cache.md#freshness-and-stale-while-revalidate",
				))
			case "stale_while_revalidate", "stale_if_error":
				d.explain(candidateExplanation(
					"cache.stale_serving",
					"tradeoff",
					name,
					"Stale windows trade freshness for availability: cached content can be older while refresh runs or when the origin fails.",
					"Choose windows that match this route's acceptable staleness.",
					"docs/cache.md#freshness-and-stale-while-revalidate",
				))
			}
		}
	}
	if b.BypassAnyCookie != a.BypassAnyCookie {
		action := "Enable"
		if !a.BypassAnyCookie {
			action = "Disable"
		}
		d.mod(DiffEntry{
			Kind:   "cache",
			Name:   name,
			Before: fmt.Sprintf("%t", b.BypassAnyCookie),
			After:  fmt.Sprintf("%t", a.BypassAnyCookie),
			Detail: fmt.Sprintf("%s cache_policy.bypass_any_cookie on route %s", action, key),
		}, "route "+name+" cache_policy bypass_any_cookie")
		if b.BypassAnyCookie && !a.BypassAnyCookie && len(a.BypassCookies) == 0 && aLoc.Cache {
			d.warn("Disabling bypass_any_cookie on cached route %s on %s allows cookie-bearing requests to use the shared cache unless origin headers prohibit it.", key, server)
		}
		if aLoc.Cache {
			d.explain(candidateExplanation(
				"cache.cookie_session_boundary",
				"warning",
				name,
				"Cookie bypass determines whether cookie-bearing sessions can reach the shared cache; this setting does not override origin cache directives.",
				"Confirm that responses cannot cross user sessions and that origin headers agree with the intended policy.",
				"docs/cache.md",
			))
		}
	}
	bCookies := normalizeCookieListForDiff(b.BypassCookies)
	aCookies := normalizeCookieListForDiff(a.BypassCookies)
	if !stringSlicesEqual(bCookies, aCookies) {
		d.mod(DiffEntry{
			Kind:   "cache",
			Name:   name,
			Before: orNone(strings.Join(bCookies, ", ")),
			After:  orNone(strings.Join(aCookies, ", ")),
			Detail: "Change cache_policy.bypass_cookies on route " + key,
		}, "route "+name+" cache_policy bypass_cookies")
		if len(bCookies) > 0 && len(aCookies) == 0 && !a.BypassAnyCookie && aLoc.Cache {
			d.warn("Clearing bypass_cookies on cached route %s on %s allows cookie-bearing requests to use the shared cache unless origin headers prohibit it.", key, server)
		}
		if aLoc.Cache {
			d.explain(candidateExplanation(
				"cache.cookie_session_boundary",
				"warning",
				name,
				"Cookie bypass determines whether cookie-bearing sessions can reach the shared cache; this setting does not override origin cache directives.",
				"Confirm that responses cannot cross user sessions and that origin headers agree with the intended policy.",
				"docs/cache.md",
			))
		}
	}
	bReload := orDefault(strings.ToLower(strings.TrimSpace(b.ReloadPolicy)), "(inherit)")
	aReload := orDefault(strings.ToLower(strings.TrimSpace(a.ReloadPolicy)), "(inherit)")
	if bReload != aReload {
		d.mod(DiffEntry{
			Kind:   "cache",
			Name:   name,
			Before: bReload,
			After:  aReload,
			Detail: "Change cache_policy.reload_policy on route " + key,
		}, "route "+name+" cache_policy reload_policy")
	}
	if aLoc.CachePolicy != nil && !aLoc.Cache {
		d.warn("Route %s on %s configures cache_policy while cache = false; the policy remains dormant until cache is enabled.", key, server)
	}
}

func diffProxyTimeouts(server, key string, b, a *config.LocationConfig, d *ConfigDiff) {
	name := server + " " + key
	type pair struct {
		label string
		b, a  config.Duration
	}
	for _, p := range []pair{
		{"proxy connect timeout", b.ProxyConnectTimeout, a.ProxyConnectTimeout},
		{"proxy read timeout", b.ProxyReadTimeout, a.ProxyReadTimeout},
		{"proxy send timeout", b.ProxySendTimeout, a.ProxySendTimeout},
	} {
		if p.b != p.a {
			d.mod(DiffEntry{Kind: "timeouts", Name: name, Before: durStr(p.b), After: durStr(p.a), Detail: fmt.Sprintf("Change %s on route %s", p.label, key)}, fmt.Sprintf("route %s %s", name, p.label))
			message := "The proxy connect timeout bounds establishing the upstream connection; it is not an end-to-end request deadline."
			nextAction := "Use a separate overall deadline when the complete request and response need a total time bound."
			code := "resilience.connect_timeout_semantics"
			if p.label != "proxy connect timeout" {
				message = "Proxy read/send timeouts are progress (inactivity) bounds, not a single end-to-end deadline; a stream with no progress can be interrupted when its timeout expires."
				nextAction = "Check the route's streaming behavior and separate retry_deadline when setting an overall work bound."
				code = "resilience.inactivity_timeout_semantics"
			}
			d.explain(candidateExplanation(
				code,
				"tradeoff",
				name,
				message,
				nextAction,
				"docs/upstreams.md#retry",
			))
		}
	}
}

// diffAuth exhaustively compares per-location auth configuration. Add/remove
// of the whole auth block is reported as a high-level enable/disable; every
// sub-field change (CIDR lists, Basic/JWT/forward-auth settings) is reported
// individually. The lifecycle path is only covered once all possible sub-field
// changes have been inspected, so no auth change can be silently absorbed by
// a broad `cover` call (R8-09).
func diffAuth(server, key string, b, a *config.AuthConfig, d *ConfigDiff) {
	name := server + " " + key

	// Whole-block add/remove.
	if (b != nil) != (a != nil) {
		action := "Enable"
		if a == nil {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "auth", Name: name, Detail: fmt.Sprintf("%s access control on route %s", action, key)}, "route "+name+" auth")
		if action == "Disable" {
			d.warn("Disabling access control on route %s on %s exposes it without authentication.", key, server)
			d.explain(candidateExplanation(
				"access.route_auth_removed",
				"critical",
				name,
				"This removes the route's authentication check. Other controls may remain, but CORS and VPN reachability do not establish per-application authorization; intentional public access is valid.",
				"Confirm who owns application authorization and whether public access is intended.",
				"docs/auth.md",
			))
		}
		d.cover("servers.*.locations.*.auth")
		return
	}
	if a == nil {
		return
	}
	if _, reason := classifyCredentialChange(b, a); reason == "all credential checks removed" {
		message := "This removes the route's application credential checks."
		if len(a.Allow) > 0 || len(a.Deny) > 0 {
			message += " CIDR allow or deny rules remain as network gates, not per-application authorization."
		}
		d.explain(candidateExplanation(
			"access.route_auth_removed",
			"critical",
			name,
			message,
			"Confirm who owns application authorization and whether public access is intended.",
			"docs/auth.md",
		))
	}

	// CIDR allow/deny lists.
	if !stringSlicesEqual(sortedStringSlice(b.Allow), sortedStringSlice(a.Allow)) {
		d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(strings.Join(b.Allow, ",")), After: orNone(strings.Join(a.Allow, ",")), Detail: "Change auth allow CIDRs on route " + key}, "route "+name+" auth allow")
	}
	if !stringSlicesEqual(sortedStringSlice(b.Deny), sortedStringSlice(a.Deny)) {
		d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(strings.Join(b.Deny, ",")), After: orNone(strings.Join(a.Deny, ",")), Detail: "Change auth deny CIDRs on route " + key}, "route "+name+" auth deny")
	}

	// Basic auth.
	if (b.Basic != nil) != (a.Basic != nil) {
		action := "Enable"
		if a.Basic == nil {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "auth", Name: name, Detail: fmt.Sprintf("%s Basic auth on route %s", action, key)}, "route "+name+" auth basic")
	} else if a.Basic != nil {
		if b.Basic.File != a.Basic.File {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(b.Basic.File), After: orNone(a.Basic.File), Detail: "Change Basic auth htpasswd file on route " + key}, "route "+name+" auth basic file")
		}
		if b.Basic.Realm != a.Basic.Realm {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(b.Basic.Realm), After: orNone(a.Basic.Realm), Detail: "Change Basic auth realm on route " + key}, "route "+name+" auth basic realm")
		}
	}

	// JWT auth.
	if (b.JWT != nil) != (a.JWT != nil) {
		action := "Enable"
		if a.JWT == nil {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "auth", Name: name, Detail: fmt.Sprintf("%s JWT auth on route %s", action, key)}, "route "+name+" auth jwt")
	} else if a.JWT != nil {
		if b.JWT.JWKSURL != a.JWT.JWKSURL {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(b.JWT.JWKSURL), After: orNone(a.JWT.JWKSURL), Detail: "Change JWT JWKS URL on route " + key}, "route "+name+" auth jwt jwks_url")
		}
		if b.JWT.Issuer != a.JWT.Issuer {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(b.JWT.Issuer), After: orNone(a.JWT.Issuer), Detail: "Change JWT issuer on route " + key}, "route "+name+" auth jwt issuer")
		}
		if b.JWT.Audience != a.JWT.Audience {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(b.JWT.Audience), After: orNone(a.JWT.Audience), Detail: "Change JWT audience on route " + key}, "route "+name+" auth jwt audience")
		}
		if !stringSlicesEqual(sortedStringSlice(b.JWT.Algorithms), sortedStringSlice(a.JWT.Algorithms)) {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(strings.Join(b.JWT.Algorithms, ",")), After: orNone(strings.Join(a.JWT.Algorithms, ",")), Detail: "Change JWT allowed algorithms on route " + key}, "route "+name+" auth jwt algorithms")
		}
	}

	// Forward auth.
	if (b.ForwardAuth != nil) != (a.ForwardAuth != nil) {
		action := "Enable"
		if a.ForwardAuth == nil {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "auth", Name: name, Detail: fmt.Sprintf("%s forward-auth on route %s", action, key)}, "route "+name+" auth forward_auth")
	} else if a.ForwardAuth != nil {
		if b.ForwardAuth.URL != a.ForwardAuth.URL {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(b.ForwardAuth.URL), After: orNone(a.ForwardAuth.URL), Detail: "Change forward-auth URL on route " + key}, "route "+name+" auth forward_auth url")
		}
		if !stringSlicesEqual(sortedStringSlice(b.ForwardAuth.AuthResponseHeaders), sortedStringSlice(a.ForwardAuth.AuthResponseHeaders)) {
			d.mod(DiffEntry{Kind: "auth", Name: name, Before: orNone(strings.Join(b.ForwardAuth.AuthResponseHeaders, ",")), After: orNone(strings.Join(a.ForwardAuth.AuthResponseHeaders, ",")), Detail: "Change forward-auth response headers on route " + key}, "route "+name+" auth forward_auth auth_response_headers")
		}
	}

	d.cover("servers.*.locations.*.auth")
}

// diffRateLimit exhaustively compares per-location rate-limit overrides.
// Add/remove of the block is reported separately; every field change
// (enabled, key, rate, burst, max_conns) is reported. The lifecycle path is
// covered only after all fields have been inspected (R8-09).
func diffRateLimit(server, key string, b, a *config.RateLimitConfig, d *ConfigDiff) {
	name := server + " " + key

	if (b != nil) != (a != nil) {
		action := "Add"
		if a == nil {
			action = "Remove"
		}
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Detail: fmt.Sprintf("%s rate-limit override on route %s", action, key)}, "route "+name+" rate limit")
		if a != nil && a.Enabled {
			addRateLimitExplanations(d, name, a.Key)
		} else if a != nil {
			d.explain(candidateExplanation(
				"rate.location_override_disabled",
				"warning",
				name,
				"A disabled per-location override turns rate limiting off for this route even when the global policy is enabled.",
				"Confirm that this route is intentionally excluded from the global request limit.",
				"docs/ratelimit.md#policy-scope",
			))
		}
		d.cover("servers.*.locations.*.rate_limit")
		return
	}
	if a == nil {
		return
	}

	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Detail: fmt.Sprintf("%s rate-limit on route %s", action, key)}, "route "+name+" rate limit enabled")
		addRateLimitExplanations(d, name, a.Key)
	}
	if b.Key != a.Key {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: orNone(b.Key), After: orNone(a.Key), Detail: "Change rate-limit key on route " + key}, "route "+name+" rate limit key")
		addRateLimitExplanations(d, name, a.Key)
	}
	if b.Rate != a.Rate {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: fmt.Sprintf("%d/s", b.Rate), After: fmt.Sprintf("%d/s", a.Rate), Detail: "Change rate-limit rate on route " + key}, "route "+name+" rate limit rate")
		d.explain(candidateExplanation(
			"rate.request_not_bandwidth",
			"information",
			name,
			"Rate and burst limit request counts, not bytes transferred.",
			"Use a bandwidth control separately if byte throughput is the constraint.",
			"docs/ratelimit.md",
		))
	}
	if b.Burst != a.Burst {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: fmt.Sprintf("%d", b.Burst), After: fmt.Sprintf("%d", a.Burst), Detail: "Change rate-limit burst on route " + key}, "route "+name+" rate limit burst")
	}
	if b.MaxConns != a.MaxConns {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: fmt.Sprintf("%d", b.MaxConns), After: fmt.Sprintf("%d", a.MaxConns), Detail: "Change rate-limit max_conns on route " + key}, "route "+name+" rate limit max_conns")
	}

	d.cover("servers.*.locations.*.rate_limit")
}

func rlStr(r *config.RateLimitConfig) string {
	if r == nil {
		return "(none)"
	}
	key := r.Key
	if key == "" {
		key = "ip"
	}
	return fmt.Sprintf("key=%s, rate=%d/s, burst=%d", key, r.Rate, r.Burst)
}

// diffUpstreams compares upstream pools, reporting pool add/remove plus
// per-pool changes to strategy, backend set (targets), retries (max_fails),
// fail timeout, health checks, and discovery.
func diffUpstreams(before, after *config.Config, d *ConfigDiff) {
	bs, as := upstreamIndex(before.Upstreams), upstreamIndex(after.Upstreams)
	for _, name := range sortedKeys(as) {
		a := as[name]
		b, ok := bs[name]
		if !ok {
			d.add(DiffEntry{Kind: "upstream", Name: name, After: fmt.Sprintf("%d backends", len(a.Servers)), Detail: "Add upstream pool " + name}, "upstream "+name)
			continue
		}
		diffUpstreamFields(name, b, a, d)
	}
	for _, name := range sortedKeys(bs) {
		if _, ok := as[name]; !ok {
			b := bs[name]
			d.del(DiffEntry{Kind: "upstream", Name: name, Before: fmt.Sprintf("%d backends", len(b.Servers)), Detail: "Remove upstream pool " + name}, "upstream "+name)
			d.warn("Removing upstream %s may break routes that proxy to it.", name)
		}
	}
}

func diffUpstreamFields(name string, b, a *config.UpstreamConfig, d *ConfigDiff) {
	explainBackendTLS(name, b.BackendTLS, a.BackendTLS, d)
	d.cover("upstreams.*.name")
	if !strings.EqualFold(b.Strategy, a.Strategy) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Strategy), After: orNone(a.Strategy), Detail: "Change load-balancing strategy of " + name}, "upstream "+name+" strategy")
	}
	d.cover("upstreams.*.strategy")

	if bh, ah := hashSummary(b.Hash), hashSummary(a.Hash); bh != ah {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(bh), After: orNone(ah), Detail: "Change affinity key of " + name + "; keys are re-placed from the next reload"}, "upstream "+name+" hash")
	}
	d.cover("upstreams.*.hash")

	// Backend set (targets).
	if !slices.Equal(b.Servers, a.Servers) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Detail: "Change backend order or entries in " + name}, "upstream "+name+" backend order")
	}
	bb, ab := backendSet(b.Servers), backendSet(a.Servers)
	for _, addr := range sortedKeys(ab) {
		if _, ok := bb[addr]; !ok {
			d.add(DiffEntry{Kind: "upstream", Name: name, After: addr, Detail: "Add backend " + addr + " to " + name}, "upstream "+name+" backend "+addr)
		} else if bb[addr] != ab[addr] {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: fmt.Sprintf("%s weight=%d", addr, bb[addr]), After: fmt.Sprintf("%s weight=%d", addr, ab[addr]), Detail: "Change weight of backend " + addr + " in " + name}, "upstream "+name+" backend "+addr)
		}
	}
	for _, addr := range sortedKeys(bb) {
		if _, ok := ab[addr]; !ok {
			d.del(DiffEntry{Kind: "upstream", Name: name, Before: addr, Detail: "Remove backend " + addr + " from " + name}, "upstream "+name+" backend "+addr)
			d.warn("Removing backend %s from %s reduces its capacity and may overload remaining backends.", addr, name)
		}
	}
	d.cover("upstreams.*.servers")

	// Retries / passive health (max_fails, fail_timeout).
	if b.MaxFails != a.MaxFails {
		d.mod(DiffEntry{Kind: "retries", Name: name, Before: fmt.Sprintf("%d", b.MaxFails), After: fmt.Sprintf("%d", a.MaxFails), Detail: "Change max_fails (passive health/retry threshold) of " + name}, "upstream "+name+" max_fails")
		if a.MaxFails == 0 && b.MaxFails != 0 {
			d.warn("Setting max_fails to 0 on %s disables passive health checking; failed backends stay in rotation.", name)
		}
		d.explain(candidateExplanation(
			"resilience.passive_health",
			"tradeoff",
			name,
			"max_fails and fail_timeout govern passive failure handling; they do not prove a backend is healthy or reachable.",
			"Review the failure threshold with the separate active health-check and retry policies.",
			"docs/upstreams.md",
		))
	}
	d.cover("upstreams.*.max_fails")
	if b.FailTimeout != a.FailTimeout {
		d.mod(DiffEntry{Kind: "retries", Name: name, Before: durStr(b.FailTimeout), After: durStr(a.FailTimeout), Detail: "Change fail_timeout of " + name}, "upstream "+name+" fail_timeout")
		d.explain(candidateExplanation(
			"resilience.passive_health",
			"tradeoff",
			name,
			"max_fails and fail_timeout govern passive failure handling; they do not prove a backend is healthy or reachable.",
			"Review the failure threshold with the separate active health-check and retry policies.",
			"docs/upstreams.md",
		))
	}
	d.cover("upstreams.*.fail_timeout")

	// Active health checks.
	bHC := b.HealthCheck != nil && b.HealthCheck.Enabled
	aHC := a.HealthCheck != nil && a.HealthCheck.Enabled
	if bHC != aHC {
		action := "Enable"
		if !aHC {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "upstream", Name: name, Detail: fmt.Sprintf("%s active health checks on %s", action, name)}, "upstream "+name+" health check")
	} else if bHC && aHC {
		diffHealthCheckFields(name, b.HealthCheck, a.HealthCheck, d)
	}
	d.cover("upstreams.*.health_check")

	diffResilienceFields(name, b.Resilience, a.Resilience, d)
	d.cover("upstreams.*.resilience")

	// Service discovery.
	bDisc := discoveryType(b.Discovery)
	aDisc := discoveryType(a.Discovery)
	if bDisc != aDisc {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(bDisc), After: orNone(aDisc), Detail: "Change service discovery of " + name}, "upstream "+name+" discovery")
	} else if aDisc != "" {
		diffDiscoveryFields(name, b.Discovery, a.Discovery, d)
	}
}

// diffHealthCheckFields reports per-field changes to an upstream's active
// health check when it is enabled on both sides (probe type, path, timing,
// thresholds, expected status set, expected body).
func diffHealthCheckFields(name string, b, a *config.HealthCheckConfig, d *ConfigDiff) {
	if b.Host != a.Host {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Host), After: orNone(a.Host), Detail: "Change health-check Host/authority of " + name}, "upstream "+name+" health check host")
	}
	if !maps.Equal(b.Headers, a.Headers) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: "[redacted]", After: "[redacted]", Detail: "Change health-check headers of " + name}, "upstream "+name+" health check headers")
	}
	if !strings.EqualFold(b.Type, a.Type) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Type), After: orNone(a.Type), Detail: "Change health-check probe type of " + name}, "upstream "+name+" health check type")
	}
	if b.Path != a.Path {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Path), After: orNone(a.Path), Detail: "Change health-check path of " + name}, "upstream "+name+" health check path")
	}
	if b.Interval != a.Interval {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: durStr(b.Interval), After: durStr(a.Interval), Detail: "Change health-check interval of " + name}, "upstream "+name+" health check interval")
	}
	if b.Timeout != a.Timeout {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: durStr(b.Timeout), After: durStr(a.Timeout), Detail: "Change health-check timeout of " + name}, "upstream "+name+" health check timeout")
	}
	if b.HealthyThreshold != a.HealthyThreshold {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: fmt.Sprintf("%d", b.HealthyThreshold), After: fmt.Sprintf("%d", a.HealthyThreshold), Detail: "Change healthy_threshold of " + name}, "upstream "+name+" healthy_threshold")
	}
	if b.UnhealthyThreshold != a.UnhealthyThreshold {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: fmt.Sprintf("%d", b.UnhealthyThreshold), After: fmt.Sprintf("%d", a.UnhealthyThreshold), Detail: "Change unhealthy_threshold of " + name}, "upstream "+name+" unhealthy_threshold")
	}
	if !intsEqual(b.ExpectStatus, a.ExpectStatus) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(intsStr(b.ExpectStatus)), After: orNone(intsStr(a.ExpectStatus)), Detail: "Change expected status codes of " + name}, "upstream "+name+" expect_status")
	}
	if b.ExpectBody != a.ExpectBody {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.ExpectBody), After: orNone(a.ExpectBody), Detail: "Change expected body of " + name}, "upstream "+name+" expect_body")
	}
}

// diffDiscoveryFields reports per-field changes to an upstream's dynamic
// discovery when the provider type is unchanged (target, refresh, and the
// active provider's non-secret knobs). This helper omits tokens; the lifecycle
// completeness pass still reports credential changes without their values. Full
// comparison disclosure is therefore separately protected by config:raw (#645).
func diffDiscoveryFields(name string, b, a *config.DiscoveryConfig, d *ConfigDiff) {
	if b == nil || a == nil {
		return
	}
	if b.Target != a.Target {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Target), After: orNone(a.Target), Detail: "Change discovery target of " + name}, "upstream "+name+" discovery target")
	}
	if b.Refresh != a.Refresh {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: durStr(b.Refresh), After: durStr(a.Refresh), Detail: "Change discovery refresh interval of " + name}, "upstream "+name+" discovery refresh")
	}
	if b.Consul != nil && a.Consul != nil {
		if b.Consul.Service != a.Consul.Service {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Consul.Service), After: orNone(a.Consul.Service), Detail: "Change Consul service of " + name}, "upstream "+name+" consul service")
		}
		if b.Consul.Address != a.Consul.Address {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Consul.Address), After: orNone(a.Consul.Address), Detail: "Change Consul address of " + name}, "upstream "+name+" consul address")
		}
		if b.Consul.Tag != a.Consul.Tag {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Consul.Tag), After: orNone(a.Consul.Tag), Detail: "Change Consul tag of " + name}, "upstream "+name+" consul tag")
		}
		if b.Consul.Datacenter != a.Consul.Datacenter {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Consul.Datacenter), After: orNone(a.Consul.Datacenter), Detail: "Change Consul datacenter of " + name}, "upstream "+name+" consul datacenter")
		}
	}
	if b.Kubernetes != nil && a.Kubernetes != nil {
		if b.Kubernetes.Namespace != a.Kubernetes.Namespace {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Kubernetes.Namespace), After: orNone(a.Kubernetes.Namespace), Detail: "Change Kubernetes namespace of " + name}, "upstream "+name+" k8s namespace")
		}
		if b.Kubernetes.Service != a.Kubernetes.Service {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Kubernetes.Service), After: orNone(a.Kubernetes.Service), Detail: "Change Kubernetes service of " + name}, "upstream "+name+" k8s service")
		}
		if b.Kubernetes.Port != a.Kubernetes.Port {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Kubernetes.Port), After: orNone(a.Kubernetes.Port), Detail: "Change Kubernetes port of " + name}, "upstream "+name+" k8s port")
		}
		if b.Kubernetes.APIServer != a.Kubernetes.APIServer {
			d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Kubernetes.APIServer), After: orNone(a.Kubernetes.APIServer), Detail: "Change Kubernetes API server of " + name}, "upstream "+name+" k8s api_server")
		}
	}
}

func backendSet(servers []config.UpstreamServer) map[string]int {
	m := make(map[string]int, len(servers))
	for _, s := range servers {
		w := s.Weight
		if w == 0 {
			w = 1
		}
		m[s.Address] = w
	}
	return m
}

func discoveryType(d *config.DiscoveryConfig) string {
	if d == nil {
		return ""
	}
	t := strings.ToLower(strings.TrimSpace(d.Type))
	if t == "static" {
		return ""
	}
	return t
}

// intsEqual reports whether two int slices have the same elements in order.
func intsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// intsStr renders an int slice as a comma-separated list (e.g. "200,204").
func intsStr(xs []int) string {
	if len(xs) == 0 {
		return ""
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%d", x)
	}
	return strings.Join(parts, ",")
}

// diffGlobalCache compares the [cache] block.

// diffResilienceFields reports per-field changes to an upstream's resilience
// block.
//
// Without it the registry completeness pass produces a row for every resilience
// path whether or not it changed, with empty before/after values and the schema
// path as the name — thirteen rows saying nothing for a four-field change. An
// operator previewing that cannot tell what they are about to apply.
func diffResilienceFields(name string, b, a *config.ResilienceConfig, d *ConfigDiff) {
	if b == nil {
		b = &config.ResilienceConfig{}
	}
	if a == nil {
		a = &config.ResilienceConfig{}
	}
	ints := []struct {
		key    string
		before int
		after  int
	}{
		{"max_fails", b.MaxFails, a.MaxFails},
		{"max_active_requests", b.MaxActiveRequests, a.MaxActiveRequests},
		{"max_active_per_backend", b.MaxActivePerBackend, a.MaxActivePerBackend},
		{"max_pending_requests", b.MaxPendingRequests, a.MaxPendingRequests},
		{"max_connections_per_backend", b.MaxConnectionsPerBackend, a.MaxConnectionsPerBackend},
		{"retry_attempts", b.RetryAttempts, a.RetryAttempts},
		{"retry_budget_percent", b.RetryBudgetPercent, a.RetryBudgetPercent},
	}
	retryChanged := false
	admissionChanged := false
	queueChanged := false
	for _, f := range ints {
		if f.before != f.after {
			d.mod(DiffEntry{Kind: "resilience", Name: name, Before: fmt.Sprintf("%d", f.before), After: fmt.Sprintf("%d", f.after), Detail: "Change " + f.key + " of " + name}, "upstream "+name+" "+f.key)
			switch f.key {
			case "retry_attempts", "retry_budget_percent":
				retryChanged = true
			case "max_pending_requests":
				queueChanged = true
			case "max_active_requests", "max_active_per_backend", "max_connections_per_backend":
				admissionChanged = true
			}
		}
	}
	durs := []struct {
		key    string
		before config.Duration
		after  config.Duration
	}{
		{"fail_timeout", b.FailTimeout, a.FailTimeout},
		{"pending_timeout", b.PendingTimeout, a.PendingTimeout},
		{"retry_deadline", b.RetryDeadline, a.RetryDeadline},
		{"retry_backoff_initial", b.RetryBackoffInitial, a.RetryBackoffInitial},
		{"retry_backoff_max", b.RetryBackoffMax, a.RetryBackoffMax},
	}
	for _, f := range durs {
		if f.before != f.after {
			d.mod(DiffEntry{Kind: "resilience", Name: name, Before: durStr(f.before), After: durStr(f.after), Detail: "Change " + f.key + " of " + name}, "upstream "+name+" "+f.key)
			switch f.key {
			case "retry_deadline", "retry_backoff_initial", "retry_backoff_max":
				retryChanged = true
			case "pending_timeout":
				queueChanged = true
			}
		}
	}
	if retryChanged {
		d.explain(candidateExplanation(
			"resilience.retry_repeats_work",
			"warning",
			name,
			"Retries run only for eligible upstream failures and can repeat work after the backend has already received a request. The retry deadline bounds the whole sequence, not each attempt.",
			"Keep retries bounded and avoid enabling them for non-idempotent operations unless duplicate work is acceptable.",
			"docs/upstreams.md#retry",
		))
	}
	if queueChanged {
		d.explain(candidateExplanation(
			"resilience.bounded_queue",
			"tradeoff",
			name,
			"Pending-request limits and their timeout bound waiting for upstream capacity; a full or expired queue rejects work instead of waiting without bound.",
			"Check the pool's admission caps and the application's tolerance for queued latency and rejection.",
			"docs/upstreams.md",
		))
	}
	if admissionChanged {
		d.explain(candidateExplanation(
			"resilience.admission_bounds",
			"tradeoff",
			name,
			"Active-request and connection limits bound upstream admission independently of request rate; requests beyond available capacity may wait only within the configured pending policy.",
			"Review active, pending, and per-backend limits together.",
			"docs/upstreams.md",
		))
	}
	// An explicit 0 asks for unbounded half-open probing, which is a different
	// request from omitting the key, so the two must not render the same.
	if bp, ap := probeStr(b.CircuitHalfOpenProbes), probeStr(a.CircuitHalfOpenProbes); bp != ap {
		d.mod(DiffEntry{Kind: "resilience", Name: name, Before: bp, After: ap, Detail: "Change circuit_half_open_probes of " + name}, "upstream "+name+" circuit_half_open_probes")
		if a.CircuitHalfOpenProbes != nil && *a.CircuitHalfOpenProbes == 0 {
			d.warn("Setting circuit_half_open_probes to 0 on %s makes half-open probing unbounded; a recovering backend takes the full waiting load the instant its cooldown ends.", name)
		}
	}
}

func effectiveLocationBackendTLS(c *config.Config, loc *config.LocationConfig) *config.BackendTLSConfig {
	if loc.BackendTLS != nil || c == nil {
		return loc.BackendTLS
	}
	name := config.ResolveLocationUpstreamName(c, *loc)
	for i := range c.Upstreams {
		if c.Upstreams[i].Name == name {
			return c.Upstreams[i].BackendTLS
		}
	}
	return nil
}

func explainLocationBackendTLS(scope string, beforeConfig, afterConfig *config.Config, before, after *config.LocationConfig, d *ConfigDiff) {
	beforeUsesTLS, _ := config.LocationProxyBackendTLS(*before)
	afterUsesTLS, afterHasBackend := config.LocationProxyBackendTLS(*after)
	if !afterUsesTLS {
		if beforeUsesTLS && afterHasBackend {
			d.explain(candidateExplanation(
				"tls.backend_transport_plaintext",
				"critical",
				scope,
				"The route now connects to its backend without TLS. Traffic is not encrypted in transit, and backend TLS certificate verification does not apply.",
				"Use TLS for the backend if confidentiality and peer authentication are required.",
				"docs/upstreams.md#backend_tls",
			))
		}
		return
	}

	afterPolicy := effectiveLocationBackendTLS(afterConfig, after)
	var beforePolicy *config.BackendTLSConfig
	if beforeUsesTLS {
		beforePolicy = effectiveLocationBackendTLS(beforeConfig, before)
	}
	if afterPolicy != nil && afterPolicy.InsecureSkipVerify &&
		(!beforeUsesTLS || beforePolicy == nil || !beforePolicy.InsecureSkipVerify ||
			locationTarget(before) != locationTarget(after) ||
			(before.BackendTLS != nil && after.BackendTLS == nil)) {
		message := "Backend TLS is encrypted, but insecure_skip_verify disables certificate-chain and server-identity verification; encryption alone does not authenticate the backend."
		if before.BackendTLS != nil && after.BackendTLS == nil {
			message = "The route now inherits a backend policy that still skips certificate-chain and server-identity verification."
		}
		d.explain(candidateExplanation(
			"tls.backend_identity_unverified",
			"critical",
			scope,
			message,
			"Restore verification or configure the intended trust roots, server_name, and peer identities.",
			"docs/upstreams.md#backend_tls",
		))
		return
	}
	if beforeUsesTLS {
		explainBackendTLS(scope, beforePolicy, afterPolicy, d)
	}
}

func explainBackendTLS(scope string, before, after *config.BackendTLSConfig, d *ConfigDiff) {
	if reflect.DeepEqual(before, after) {
		return
	}
	if after != nil && after.InsecureSkipVerify && (before == nil || !before.InsecureSkipVerify) {
		d.explain(candidateExplanation(
			"tls.backend_identity_unverified",
			"critical",
			scope,
			"For backend TLS connections using this policy, insecure_skip_verify disables certificate-chain and server-identity verification; encryption alone does not authenticate the backend.",
			"Restore verification or configure the intended trust roots, server_name, and peer identities.",
			"docs/upstreams.md#backend_tls",
		))
		return
	}
	if before != nil && before.InsecureSkipVerify && (after == nil || !after.InsecureSkipVerify) {
		d.explain(candidateExplanation(
			"tls.backend_verification_restored",
			"information",
			scope,
			"For backend TLS connections using this policy, certificate and server-identity verification is enabled again. This policy does not enable TLS on a plaintext connection.",
			"Confirm the configured trust roots and server name match the backend certificate.",
			"docs/upstreams.md#backend_tls",
		))
		return
	}
	d.explain(candidateExplanation(
		"tls.backend_trust_changed",
		"warning",
		scope,
		"Backend trust, verified name, or client identity settings changed. Candidate validation does not prove that the configured backend will complete a TLS handshake.",
		"Verify the trust roots, expected backend identity, and any client-certificate prerequisites against the intended backend.",
		"docs/upstreams.md#backend_tls",
	))
}

func probeStr(p *int) string {
	if p == nil {
		return "(default)"
	}
	if *p == 0 {
		return "0 (unbounded)"
	}
	return fmt.Sprintf("%d", *p)
}
