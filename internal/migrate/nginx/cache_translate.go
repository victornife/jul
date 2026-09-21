// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"

	"jul/internal/config"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

// cacheZoneDef is one declared `proxy_cache_path` zone (http-level).
type cacheZoneDef struct {
	path       string
	maxSize    config.Size
	hasMaxSize bool
}

// cacheUse is one `proxy_cache <name>;` reference found in a location.
type cacheUse struct {
	zone string
	line int
}

// cacheTTLCandidate is one recognized `proxy_cache_valid` occurrence, at any
// level (http, server, or location).
type cacheTTLCandidate struct {
	ttl  config.Duration
	line int
}

// cacheCollector accumulates every cache-related directive found anywhere in
// the http block during the pre-pass, before any per-location decision is
// made.
type cacheCollector struct {
	zones map[string]cacheZoneDef
	uses  []cacheUse
	ttls  []cacheTTLCandidate
}

// httpCacheResolution is the single globally-resolved proxy_cache zone and
// default TTL for the whole configuration.
//
// Jul's response cache is one process-wide store ([cache], a single
// CacheConfig on the root Config), unlike nginx's proxy_cache model, which
// supports any number of independently sized, independently named cache
// zones (proxy_cache_path ... keys_zone=name:size) selected per location
// (proxy_cache name;). Only the bounded case where exactly one zone is
// declared and every proxy_cache reference in the whole file names that same
// zone can be translated onto Jul's single cache without silently discarding
// a zone boundary the operator relied on. Anything wider than that (no
// matching declaration, or more than one distinct zone actually in use) is
// left untranslated and reported, never guessed.
type httpCacheResolution struct {
	zoneName string
	def      cacheZoneDef
	hasZone  bool
	ttl      config.Duration
	hasTTL   bool
}

// resolveHTTPCache walks the http block's full server/location subtree once,
// before any location is translated, exactly mirroring why the realip
// pre-pass in translateHTTP runs first: a proxy_cache reference inside a
// location can only be judged against zones declared anywhere in the same
// http block, including ones that appear later in the source.
func (t *translator) resolveHTTPCache(kids []ngx.IDirective) httpCacheResolution {
	col := &cacheCollector{zones: map[string]cacheZoneDef{}}
	for _, c := range kids {
		switch c.GetName() {
		case "proxy_cache_path":
			t.collectCacheZone(c, col)
		case "proxy_cache_valid":
			collectCacheValid(c, col)
		case "server":
			for _, sc := range children(c) {
				switch sc.GetName() {
				case "proxy_cache_valid":
					collectCacheValid(sc, col)
				case "location":
					for _, lc := range children(sc) {
						switch lc.GetName() {
						case "proxy_cache":
							collectCacheUse(lc, col)
						case "proxy_cache_valid":
							collectCacheValid(lc, col)
						}
					}
				}
			}
		}
	}
	return t.resolveCacheCollector(col)
}

func (t *translator) collectCacheZone(c ngx.IDirective, col *cacheCollector) {
	def, name, ok := parseCacheZonePath(paramValues(c))
	if !ok {
		t.report.skip(c, "proxy_cache_path is missing a path or a valid keys_zone=name:size")
		return
	}
	if _, exists := col.zones[name]; exists {
		t.report.skip(c, "duplicate proxy_cache_path keys_zone name \""+name+"\"; only the first declaration is honored")
		return
	}
	col.zones[name] = def
}

// parseCacheZonePath parses `proxy_cache_path path ... keys_zone=name:size
// ...;`. Disk-cache-manager tuning parameters that have no Jul equivalent
// (levels=, use_temp_path=, inactive=, min_free=, manager_*, loader_*,
// purger*) are accepted without effect: they govern nginx's own on-disk
// cache-manager process and do not change whether the zone itself is
// representable.
func parseCacheZonePath(params []string) (cacheZoneDef, string, bool) {
	if len(params) == 0 {
		return cacheZoneDef{}, "", false
	}
	path := params[0]
	if path == "" || strings.HasPrefix(path, "$") {
		return cacheZoneDef{}, "", false
	}
	var name string
	var def cacheZoneDef
	for _, p := range params[1:] {
		switch {
		case strings.HasPrefix(p, "keys_zone="):
			kv := strings.SplitN(strings.TrimPrefix(p, "keys_zone="), ":", 2)
			if len(kv) != 2 || kv[0] == "" {
				return cacheZoneDef{}, "", false
			}
			name = kv[0]
			// kv[1] bounds nginx's in-memory key/metadata index, not cached
			// response bodies; Jul's memory_max_size is the actual
			// response-data cap, a different quantity, so it is
			// deliberately not derived from this value.
		case strings.HasPrefix(p, "max_size="):
			var sz config.Size
			if err := sz.UnmarshalText([]byte(strings.TrimPrefix(p, "max_size="))); err != nil {
				return cacheZoneDef{}, "", false
			}
			def.maxSize, def.hasMaxSize = sz, true
		}
	}
	if name == "" {
		return cacheZoneDef{}, "", false
	}
	def.path = path
	return def, name, true
}

func collectCacheUse(c ngx.IDirective, col *cacheCollector) {
	params := paramValues(c)
	if len(params) == 0 {
		return
	}
	name := strings.TrimSpace(params[0])
	if name == "" || name == "off" || strings.Contains(name, "$") {
		return
	}
	col.uses = append(col.uses, cacheUse{zone: name, line: c.GetLine()})
}

func collectCacheValid(c ngx.IDirective, col *cacheCollector) {
	ttl, ok := parseSimpleCacheValidTime(paramValues(c))
	if !ok {
		return
	}
	col.ttls = append(col.ttls, cacheTTLCandidate{ttl: ttl, line: c.GetLine()})
}

// parseSimpleCacheValidTime recognizes the bounded proxy_cache_valid forms
// Jul's single default_ttl can represent: a bare time, or a time preceded
// only by status codes drawn from nginx's own default cacheable set (200,
// 301, 302). Any other form (the "any" keyword, codes outside that set, or a
// missing time) is left unresolved here; the per-directive assessment
// classifier reports those separately.
func parseSimpleCacheValidTime(params []string) (config.Duration, bool) {
	if len(params) == 0 {
		return 0, false
	}
	last := params[len(params)-1]
	ttl, ok := parseNginxDuration(last)
	if !ok {
		return 0, false
	}
	for _, code := range params[:len(params)-1] {
		switch code {
		case "200", "301", "302":
		default:
			return 0, false
		}
	}
	return ttl, true
}

// resolveCacheCollector turns the raw collected directives into the single
// global decision: either exactly one zone/TTL pairing was in consistent use
// everywhere, or nothing is enabled and every offending use is reported so
// the assessment can attach a conflict finding to its own real source line.
func (t *translator) resolveCacheCollector(col *cacheCollector) httpCacheResolution {
	var res httpCacheResolution

	if len(col.uses) > 0 {
		distinct := map[string]bool{}
		for _, u := range col.uses {
			distinct[u.zone] = true
		}
		if len(distinct) == 1 {
			var only string
			for z := range distinct {
				only = z
			}
			if def, ok := col.zones[only]; ok {
				res.zoneName, res.def, res.hasZone = only, def, true
			}
		}
		if !res.hasZone {
			reason := "proxy_cache references a cache zone with no matching proxy_cache_path declaration"
			if len(distinct) > 1 {
				reason = "multiple distinct proxy_cache zone names are used across the configuration; Jul has one process-wide cache zone"
			}
			for _, u := range col.uses {
				t.report.skipNamed("proxy_cache", u.line, reason)
			}
		}
	}

	if len(col.ttls) > 0 {
		agree := true
		first := col.ttls[0].ttl
		for _, c := range col.ttls[1:] {
			if c.ttl != first {
				agree = false
				break
			}
		}
		if agree {
			res.ttl, res.hasTTL = first, true
		} else {
			t.report.note("multiple conflicting proxy_cache_valid values were found across the configuration; [cache].default_ttl was left unset")
		}
	}
	return res
}

// applyHTTPCacheConfig sets the global [cache] block once the whole http
// block has been walked and a single zone was resolved. MemoryMaxSize is
// deliberately left zero: Jul applies its own 64MiB default for an enabled
// cache during config.Parse, matching how every other importer-emitted
// config relies on parser defaults rather than hard-coding them.
func (t *translator) applyHTTPCacheConfig(out *config.Config) {
	if !t.httpCache.hasZone {
		return
	}
	out.Cache.Enabled = true
	out.Cache.DiskPath = t.httpCache.def.path
	if t.httpCache.def.hasMaxSize {
		out.Cache.DiskMaxSize = t.httpCache.def.maxSize
	}
	if t.httpCache.hasTTL {
		out.Cache.DefaultTTL = t.httpCache.ttl
	}
}

// applyProxyCache is called for a location's `proxy_cache <name>;`
// directive. It enables per-location caching only when name names the one
// globally resolved zone; any other legitimate (non-off, non-variable) name
// was already reported by resolveHTTPCache with a source-attributed
// conflict finding.
func (t *translator) applyProxyCache(loc *config.LocationConfig, name string) {
	name = strings.TrimSpace(name)
	if name == "" || name == "off" || strings.Contains(name, "$") {
		return
	}
	if t.httpCache.hasZone && name == t.httpCache.zoneName {
		loc.Cache = true
	}
}
