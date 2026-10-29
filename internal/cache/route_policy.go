// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"net/http"
	"net/textproto"
	"sort"
	"strconv"
	"strings"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

const routeEpochFloorKey = "\x00floor"

// RouteScope builds restart-stable ownership without predicate values or their
// digests. Explicit route IDs survive reordering. Anonymous predicates use an
// ordinal among anonymous predicates with the same path/type; unrelated paths
// do not shift that identity. Matcher compatibility is proved separately.
// anonymousSlot, when supplied by RouteScopes, avoids scanning the server.
// Backend targets remain outside ownership and compatibility (#624).
func RouteScope(srv config.ServerConfig, loc config.LocationConfig, anonymousSlot ...int) string {
	names := make([]string, len(srv.ServerNames))
	for i, n := range srv.ServerNames {
		names[i] = config.NormalizeHostPattern(n)
	}
	sort.Strings(names)
	matchType := loc.Match.Type
	if matchType == "" {
		matchType = "prefix"
	}
	h := sha256.New()
	write := func(parts ...string) {
		for _, p := range parts {
			fmt.Fprintf(h, "%d:%s\n", len(p), p)
		}
	}
	write("route-scope-v3", config.CanonicalListenAddr(srv.Listen), strconv.Itoa(len(names)))
	write(names...)
	if loc.RouteID != nil && *loc.RouteID != "" {
		write("route_id", *loc.RouteID)
	} else {
		write("anonymous", matchType, loc.Match.Path)
		if loc.Match.HasPredicates() {
			slot := 0
			if len(anonymousSlot) > 0 {
				slot = anonymousSlot[0]
			} else {
				predicates := loc.Match.CanonicalPredicates()
				for _, candidate := range srv.Locations {
					if !anonymousPredicateCoordinate(candidate, matchType, loc.Match.Path) {
						continue
					}
					if candidate.Match.CanonicalPredicates() == predicates {
						break
					}
					slot++
				}
			}
			write("predicate-slot", strconv.Itoa(slot))
		}
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func anonymousPredicateCoordinate(loc config.LocationConfig, matchType, path string) bool {
	typ := loc.Match.Type
	if typ == "" {
		typ = "prefix"
	}
	return (loc.RouteID == nil || *loc.RouteID == "") && loc.Match.HasPredicates() && typ == matchType && loc.Match.Path == path
}

// RouteScopes compiles all location identities in linear time. Scope derivation
// and inspection share RouteScope; no persistent route-identity registry exists.
func RouteScopes(srv config.ServerConfig) []string {
	type coordinate struct{ typ, path string }
	counts := make(map[coordinate]int)
	out := make([]string, len(srv.Locations))
	for i, loc := range srv.Locations {
		typ := loc.Match.Type
		if typ == "" {
			typ = "prefix"
		}
		coord := coordinate{typ, loc.Match.Path}
		out[i] = RouteScope(srv, loc, counts[coord])
		if anonymousPredicateCoordinate(loc, typ, loc.Match.Path) {
			counts[coord]++
		}
	}
	return out
}

const (
	maxCookieHeaderLines = 16
	maxCookieHeaderBytes = 8192
	maxCookiePairs       = 128
)

// RouteCacheConfig is the immutable, compiled route-scoped cache policy and
// isolation configuration for one location (#623 / #672).
type RouteCacheConfig struct {
	Scope                string
	Epoch                uint64
	MultiRouteOverlap    bool
	HasDefaultTTL        bool
	DefaultTTL           time.Duration
	HasMaxTTL            bool
	MaxTTL               time.Duration
	HasSWR               bool
	StaleWhileRevalidate time.Duration
	HasSIE               bool
	StaleIfError         time.Duration
	BypassAnyCookie      bool
	BypassCookieCount    int
	BypassCookies        [config.MaxBypassCookies]string
	varyGuard            *varyTransformPolicy
	// matcherProof belongs only in the compatibility manifest, never keys or entries.
	matcherProof string
}

// WithMatcher binds the resolved matcher to compatibility using the existing
// confidential-version context. Nil uses the embedding process context: reload
// reuse works, while restart safely invalidates that non-durable context.
func (rc RouteCacheConfig) WithMatcher(loc config.LocationConfig, versions *versionid.Context) RouteCacheConfig {
	typ := loc.Match.Type
	if typ == "" {
		typ = "prefix"
	}
	var data strings.Builder
	for _, field := range []string{rc.Scope, typ, loc.Match.Path, loc.Match.CanonicalPredicates(), strconv.FormatBool(config.LocationPreflightWidening(loc))} {
		fmt.Fprintf(&data, "%d:%s\n", len(field), field)
	}
	rc.matcherProof = versions.Digest("cache-route-matcher-v1", []byte(data.String()))
	return rc
}

// CompileRouteCacheConfig compiles a LocationCachePolicy and route scope into
// an immutable RouteCacheConfig.
func CompileRouteCacheConfig(scope string, multiRouteOverlap bool, locPol *config.LocationCachePolicy) RouteCacheConfig {
	out := RouteCacheConfig{
		Scope:             scope,
		MultiRouteOverlap: multiRouteOverlap,
	}
	if locPol == nil {
		return out
	}
	if locPol.DefaultTTL != nil {
		out.HasDefaultTTL = true
		out.DefaultTTL = locPol.DefaultTTL.Std()
	}
	if locPol.MaxTTL != nil {
		out.HasMaxTTL = true
		out.MaxTTL = locPol.MaxTTL.Std()
	}
	if locPol.StaleWhileRevalidate != nil {
		out.HasSWR = true
		out.StaleWhileRevalidate = locPol.StaleWhileRevalidate.Std()
	}
	if locPol.StaleIfError != nil {
		out.HasSIE = true
		out.StaleIfError = locPol.StaleIfError.Std()
	}
	out.BypassAnyCookie = locPol.BypassAnyCookie
	if len(locPol.BypassCookies) > 0 && !locPol.BypassAnyCookie {
		norm := make([]string, 0, len(locPol.BypassCookies))
		seen := make(map[string]struct{}, len(locPol.BypassCookies))
		for _, raw := range locPol.BypassCookies {
			trimmed := strings.ToLower(strings.TrimSpace(raw))
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			norm = append(norm, trimmed)
		}
		sort.Strings(norm)
		if len(norm) > len(out.BypassCookies) {
			norm = norm[:len(out.BypassCookies)]
		}
		out.BypassCookieCount = len(norm)
		copy(out.BypassCookies[:], norm)
	}
	return out
}

// WithProxyVaryGuard attaches the HTTP reverse-proxy transformed-header Vary
// guard to rc (#672).
func (rc RouteCacheConfig) WithProxyVaryGuard(names []string, complete bool) RouteCacheConfig {
	rc.varyGuard = newProxyVaryTransformPolicy(names, complete)
	return rc
}

// WithCGIVaryGuard attaches the FastCGI/uWSGI transformed-header Vary guard
// to rc (#672).
func (rc RouteCacheConfig) WithCGIVaryGuard(names, prefixes []string, complete bool) RouteCacheConfig {
	rc.varyGuard = newCGIVaryTransformPolicy(names, prefixes, complete)
	return rc
}

// effectivePolicy merges the live global scalar CachePolicy with an optional
// route-scoped override in O(1) stack operations.
func (c *Cache) effectivePolicy(route *RouteCacheConfig) CachePolicy {
	pol := c.Policy()
	if route == nil {
		return pol
	}
	pol.RouteScope = route.Scope
	pol.RouteEpoch = route.Epoch
	if route.Scope != "" {
		if snap := c.routeEpochsSnap.Load(); snap != nil {
			if ep := max((*snap)[route.Scope], (*snap)[routeEpochFloorKey]); ep > pol.RouteEpoch {
				pol.RouteEpoch = ep
			}
		}
	}
	pol.MultiRouteOverlap = route.MultiRouteOverlap
	pol.varyGuard = route.varyGuard
	if route.HasDefaultTTL {
		pol.HasRouteDefaultTTL = true
		pol.DefaultTTL = route.DefaultTTL
	}
	if route.HasMaxTTL {
		pol.HasMaxTTL = true
		pol.MaxTTL = route.MaxTTL
	}
	if route.HasSWR {
		pol.HasRouteSWR = true
		pol.StaleWhileRevalidate = route.StaleWhileRevalidate
	}
	if route.HasSIE {
		pol.HasRouteSIE = true
		pol.StaleIfError = route.StaleIfError
	}
	pol.BypassAnyCookie = route.BypassAnyCookie
	pol.BypassCookieCount = route.BypassCookieCount
	pol.BypassCookies = route.BypassCookies
	return pol
}

func bypassPolicyDigest(pol CachePolicy) [16]byte {
	if !pol.BypassAnyCookie && pol.BypassCookieCount <= 0 {
		return [16]byte{}
	}
	h := sha256.New()
	if pol.BypassAnyCookie {
		_, _ = h.Write([]byte("any\n"))
	}
	for i := 0; i < pol.BypassCookieCount && i < len(pol.BypassCookies); i++ {
		_, _ = h.Write([]byte(pol.BypassCookies[i]))
		_, _ = h.Write([]byte{'\n'})
	}
	sum := h.Sum(nil)
	var out [16]byte
	copy(out[:], sum[:16])
	return out
}

func routeConfigDigest(pol CachePolicy) [16]byte {
	if pol.RouteScope == "" && !pol.HasRouteDefaultTTL && !pol.HasMaxTTL && !pol.HasRouteSWR && !pol.HasRouteSIE && !pol.BypassAnyCookie && pol.BypassCookieCount == 0 {
		return [16]byte{}
	}
	p := CachePolicy{
		RouteScope:         pol.RouteScope,
		RouteEpoch:         pol.RouteEpoch,
		HasRouteDefaultTTL: pol.HasRouteDefaultTTL,
		HasMaxTTL:          pol.HasMaxTTL,
		MaxTTL:             pol.MaxTTL,
		HasRouteSWR:        pol.HasRouteSWR,
		HasRouteSIE:        pol.HasRouteSIE,
		BypassAnyCookie:    pol.BypassAnyCookie,
		BypassCookieCount:  pol.BypassCookieCount,
		BypassCookies:      pol.BypassCookies,
	}
	if pol.HasRouteDefaultTTL {
		p.DefaultTTL = pol.DefaultTTL
	}
	if pol.HasRouteSWR {
		p.StaleWhileRevalidate = pol.StaleWhileRevalidate
	}
	if pol.HasRouteSIE {
		p.StaleIfError = pol.StaleIfError
	}
	sum := policyID(p)
	var out [16]byte
	copy(out[:], sum[:16])
	return out
}

func hasInvalidCookieValueByte(val string) bool {
	for i := 0; i < len(val); i++ {
		b := val[i]
		if b < 0x20 || b == 0x7f || b == ';' {
			return true
		}
	}
	return false
}

// evaluateCookieBypass evaluates the request's Cookie headers against pol's
// configured cookie-bypass rules (#623). It never inspects, logs, or retains
// cookie values beyond checking for forbidden control bytes. When a configured
// bypass cannot be evaluated safely due to malformed or oversized Cookie
// headers, it fails closed and returns "cookie_bypass_malformed".
func evaluateCookieBypass(r *http.Request, pol CachePolicy) string {
	if !pol.BypassAnyCookie && pol.BypassCookieCount <= 0 {
		return ""
	}
	lines := r.Header.Values("Cookie")
	if len(lines) == 0 {
		return ""
	}
	if pol.BypassAnyCookie {
		return "cookie_bypass"
	}
	if len(lines) > maxCookieHeaderLines {
		return "cookie_bypass_malformed"
	}
	totalBytes := 0
	pairs := 0
	matched := false
	for _, line := range lines {
		totalBytes += len(line)
		if totalBytes > maxCookieHeaderBytes {
			return "cookie_bypass_malformed"
		}
		trimmedLine := textproto.TrimString(line)
		if trimmedLine == "" {
			return "cookie_bypass_malformed"
		}
		rest := line
		for {
			var seg string
			hasMore := false
			if idx := strings.IndexByte(rest, ';'); idx >= 0 {
				seg = rest[:idx]
				rest = rest[idx+1:]
				hasMore = true
			} else {
				seg = rest
				rest = ""
			}
			pairs++
			if pairs > maxCookiePairs {
				return "cookie_bypass_malformed"
			}
			part := textproto.TrimString(seg)
			if part == "" {
				return "cookie_bypass_malformed"
			}
			eq := strings.IndexByte(part, '=')
			if eq <= 0 {
				return "cookie_bypass_malformed"
			}
			name := part[:eq]
			val := textproto.TrimString(part[eq+1:])
			if len(name) > config.MaxBypassCookieNameBytes || !isHTTPToken(name) || hasInvalidCookieValueByte(val) {
				return "cookie_bypass_malformed"
			}
			for i := 0; i < pol.BypassCookieCount && i < len(pol.BypassCookies); i++ {
				if strings.EqualFold(name, pol.BypassCookies[i]) {
					matched = true
					break
				}
			}
			if !hasMore {
				break
			}
		}
	}
	if matched {
		return "cookie_bypass"
	}
	return ""
}

// CurrentRouteEpoch returns the current ownership epoch for scope. An epoch
// starts at 0 and advances when matching compatibility changes, a route is
// removed/disabled, or bounded history compacts. A retired or changed route
// cannot inherit entries from its earlier ownership epoch (#623).
func (c *Cache) CurrentRouteEpoch(scope string) uint64 {
	if c == nil || scope == "" {
		return 0
	}
	c.genMu.Lock()
	defer c.genMu.Unlock()
	return max(c.routeEpoch[scope], c.routeEpoch[routeEpochFloorKey])
}

// CommitRoutePolicies records the active route policy digests and advances the
// per-route publication floor whenever a route's cache policy changes, so an
// in-flight origin fill or revalidation started under an older policy cannot
// publish into the cache after the new policy commits (#623).
func (c *Cache) CommitRoutePolicies(routes []RouteCacheConfig) {
	if c == nil {
		return
	}
	// Serialize durable commits without holding genMu during disk I/O or
	// recovery callbacks (disk -> genMu is the established lock order).
	c.routeCommitMu.Lock()
	defer c.routeCommitMu.Unlock()
	c.genMu.Lock()
	if c.routePolicyDigest == nil {
		c.routePolicyDigest = make(map[string][16]byte, len(routes))
	}
	if c.routePolicyFloor == nil {
		c.routePolicyFloor = make(map[string]uint64, len(routes))
	}
	if c.routeEpoch == nil {
		c.routeEpoch = make(map[string]uint64, len(routes))
	}
	epochSnap := make(map[string]uint64, len(c.routeEpoch))
	for k, v := range c.routeEpoch {
		epochSnap[k] = v
	}
	active := make(map[string][16]byte, len(routes))
	configs := make(map[string]RouteCacheConfig, len(routes))
	matchers := make(map[string]string, len(routes))
	for _, rc := range routes {
		if rc.Scope != "" {
			configs[rc.Scope] = rc
			if rc.matcherProof != "" {
				matchers[rc.Scope] = rc.matcherProof
			}
		}
	}
	// Retire before computing the active digests, so every digest includes the
	// post-compaction epoch. The floor is compatibility evidence for all scopes
	// forgotten by compaction, including disk-only entries and in-flight fills.
	previous := make(map[string]bool, len(c.routePolicyDigest)+len(c.routeMatchers))
	for scope := range c.routePolicyDigest {
		previous[scope] = true
	}
	for scope := range c.routeMatchers {
		previous[scope] = true
	}
	for scope := range previous {
		_, stillActive := configs[scope]
		if !stillActive || c.routeMatchers[scope] != matchers[scope] {
			epochSnap[scope] = max(epochSnap[scope], epochSnap[routeEpochFloorKey]) + 1
		}
	}
	if len(epochSnap) > 1024+len(configs) {
		floor := epochSnap[routeEpochFloorKey]
		for _, ep := range epochSnap {
			floor = max(floor, ep)
		}
		epochSnap = map[string]uint64{routeEpochFloorKey: floor + 1}
	}
	for i := range routes {
		rc := routes[i]
		if rc.Scope == "" {
			continue
		}
		rc.Epoch = max(epochSnap[rc.Scope], epochSnap[routeEpochFloorKey])
		eff := c.effectivePolicy(&rc)
		digest := routeConfigDigest(eff)
		active[rc.Scope] = digest
		configs[rc.Scope] = rc
	}
	// An unchanged durable proof needs no rewrite/fsync. Besides avoiding I/O
	// on no-op reloads, this keeps healthy warm entries usable when writes are
	// temporarily unavailable. Nil distinguishes missing/legacy durable proof.
	persist := c.routeMatchers == nil || !maps.Equal(epochSnap, c.routeEpoch) || !maps.Equal(matchers, c.routeMatchers)
	if c.disk != nil && c.disk.routeEpochFailed.Load() {
		persist = true
	}
	c.genMu.Unlock()
	// Durability precedes visibility. A crash after this write may make the
	// cache colder, but cannot restore pre-retirement compatibility evidence.
	if c.disk != nil && persist {
		c.disk.persistRouteEpochs(epochSnap, matchers)
	}
	c.genMu.Lock()
	for scope, prev := range c.routePolicyDigest {
		if next, ok := active[scope]; !ok || next != prev {
			c.routePolicyFloor[scope] = c.genSeq.Add(1)
		}
	}
	c.routeEpoch = maps.Clone(epochSnap)
	c.routeEpochsSnap.Store(&epochSnap)
	c.routePolicyDigest = active
	c.routeConfigs = configs
	c.routeMatchers = matchers
	if len(c.routePolicyFloor) > 1024 && c.minInflightStartGenLocked() == ^uint64(0) {
		c.routePolicyFloor = make(map[string]uint64)
	}
	c.genMu.Unlock()
}
