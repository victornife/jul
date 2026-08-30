// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"jul/internal/config"
)

type serverWrapper struct {
	Name string
	*config.ServerConfig
}

func serverIndex(servers []config.ServerConfig) map[string]serverWrapper {
	m := make(map[string]serverWrapper, len(servers))
	for i := range servers {
		srv := &servers[i]
		key := srv.Listen
		if len(srv.ServerNames) > 0 {
			// Separated by a space rather than a colon: an IPv6 listen address
			// already contains colons, so "host:[::1]:8080" reads as garbage.
			key = srv.ServerNames[0] + " " + srv.Listen
		}
		m[key] = serverWrapper{Name: key, ServerConfig: srv}
	}
	return m
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
	byFPBefore := make(map[string]*config.LocationConfig)
	for i := range before {
		b := &before[i]
		if id := routeID(b); id != "" {
			byIDBefore[id] = b
		} else {
			byFPBefore[locationKey(b)] = b
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
		if b, ok := byFPBefore[locationKey(a)]; ok {
			usedBefore[b] = true
			afterPairs = append(afterPairs, locationPair{key: locationKey(a), before: b, after: a})
			continue
		}
		afterPairs = append(afterPairs, locationPair{key: locationKey(a), after: a})
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
func diffLocations(server string, before, after []config.LocationConfig, beforeGlobWAF, afterGlobWAF config.WAFConfig, d *ConfigDiff) {
	// route_id is fully accounted for by correlateLocations: it decides
	// resource identity (matched/added/removed) rather than being reported as
	// a plain field change, so the registry completeness pass must not
	// separately flag it.
	d.cover("servers.*.locations.*.route_id")
	afterPairs, removedPairs := correlateLocations(before, after)
	for _, p := range afterPairs {
		a := p.after
		label := locationLabel(a)
		name := server + " " + label
		if p.before == nil {
			d.add(DiffEntry{Kind: "location", Name: name, After: locationAction(a) + " → " + orNone(locationTarget(a)), Detail: "Add route " + label + " on " + server}, "route "+name)
			continue
		}
		diffLocationFields(server, label, p.before, a, beforeGlobWAF, afterGlobWAF, d)
	}
	for _, p := range removedPairs {
		b := p.before
		label := locationLabel(b)
		name := server + " " + label
		d.del(DiffEntry{Kind: "location", Name: name, Before: locationAction(b) + " → " + orNone(locationTarget(b)), Detail: "Remove route " + label + " on " + server}, "route "+name)
		// Editing a predicate re-keys the route, so it renders as a removal
		// plus an addition. Warning that traffic will stop being handled would
		// be false whenever another route still covers the same coordinates.
		if !coordinatesStillPresent(b, after) {
			d.warn("Removing route %s on %s will stop matching requests from being handled by it.", label, server)
		}
	}
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

func diffLocationFields(server, key string, b, a *config.LocationConfig, beforeGlobWAF, afterGlobWAF config.WAFConfig, d *ConfigDiff) {
	name := server + " " + key

	if locationAction(b) != locationAction(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: locationAction(b), After: locationAction(a), Detail: "Change action of route " + key}, "route "+name+" action")
	}
	coverLocationAction(b, a, d)
	if locationTarget(b) != locationTarget(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: orNone(locationTarget(b)), After: orNone(locationTarget(a)), Detail: "Change target of route " + key}, "route "+name+" target")
		d.warn("Changing the target of route %s on %s redirects matching traffic to a different backend or destination.", key, server)
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
		} else {
			d.warn("Disabling WAF on route %s on %s removes rule inspection for that route.", key, server)
		}
	} else if aWAFOn && bWAFOn {
		// Both enabled — compare effective policy fields
		if bEffWAF.Mode != aEffWAF.Mode {
			d.mod(DiffEntry{Kind: "waf", Name: name, Before: bEffWAF.Mode, After: aEffWAF.Mode, Detail: "Change WAF mode on route " + key}, "route "+name+" waf mode")
			if bEffWAF.Mode == "block" && aEffWAF.Mode == "detect" {
				d.warn("Switching WAF to detect mode on route %s on %s stops blocking threats.", key, server)
			}
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
		}
	}
	d.cover("servers.*.locations.*.waf")

	// Auth changes (CIDR lists, Basic, JWT, forward-auth).
	diffAuth(server, key, b.Auth, a.Auth, d)

	// Cache toggle.
	if b.Cache != a.Cache {
		action := "Enable"
		if !a.Cache {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "cache", Name: name, Detail: fmt.Sprintf("%s response cache on route %s", action, key)}, "route "+name+" cache")
		if a.Cache && a.Auth != nil {
			d.warn("Enabling cache on authenticated route %s on %s risks serving private responses to other clients.", key, server)
		}
	}
	d.cover("servers.*.locations.*.cache")

	// Per-route rate-limit override.
	diffRateLimit(server, key, b.RateLimit, a.RateLimit, d)

	// mTLS require-client-cert toggle.
	if b.RequireClientCert != a.RequireClientCert {
		action := "Require"
		if !a.RequireClientCert {
			action = "Stop requiring"
		}
		d.mod(DiffEntry{Kind: "mtls", Name: name, Detail: fmt.Sprintf("%s client certificate on route %s", action, key)}, "route "+name+" client cert")
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
	d.cover("servers.*.locations.*.plugins")

	// Proxy timeouts.
	diffProxyTimeouts(server, key, b, a, d)

	// Response-header operations (ADR 0018 §8).
	diffResponseHeaders(server, key, b.ResponseHeaders, a.ResponseHeaders, d)

	// CORS policy (ADR 0018 §9).
	diffCORS(server, key, b.CORS, a.CORS, d)
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
		}
		d.cover("servers.*.locations.*.auth")
		return
	}
	if a == nil {
		return
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
	}
	if b.Key != a.Key {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: orNone(b.Key), After: orNone(a.Key), Detail: "Change rate-limit key on route " + key}, "route "+name+" rate limit key")
	}
	if b.Rate != a.Rate {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: fmt.Sprintf("%d/s", b.Rate), After: fmt.Sprintf("%d/s", a.Rate), Detail: "Change rate-limit rate on route " + key}, "route "+name+" rate limit rate")
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
	d.cover("upstreams.*.name")
	if !strings.EqualFold(b.Strategy, a.Strategy) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Strategy), After: orNone(a.Strategy), Detail: "Change load-balancing strategy of " + name}, "upstream "+name+" strategy")
	}
	d.cover("upstreams.*.strategy")

	// Backend set (targets).
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
	}
	d.cover("upstreams.*.max_fails")
	if b.FailTimeout != a.FailTimeout {
		d.mod(DiffEntry{Kind: "retries", Name: name, Before: durStr(b.FailTimeout), After: durStr(a.FailTimeout), Detail: "Change fail_timeout of " + name}, "upstream "+name+" fail_timeout")
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
// active provider's non-secret knobs). Token changes are not surfaced because
// tokens are preserved server-side and never diffed.
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
	for _, f := range ints {
		if f.before != f.after {
			d.mod(DiffEntry{Kind: "resilience", Name: name, Before: fmt.Sprintf("%d", f.before), After: fmt.Sprintf("%d", f.after), Detail: "Change " + f.key + " of " + name}, "upstream "+name+" "+f.key)
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
		}
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

func probeStr(p *int) string {
	if p == nil {
		return "(default)"
	}
	if *p == 0 {
		return "0 (unbounded)"
	}
	return fmt.Sprintf("%d", *p)
}
