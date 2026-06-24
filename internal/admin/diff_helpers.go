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
			key = srv.ServerNames[0] + ":" + srv.Listen
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

// locationKey identifies a location within a server block by match type+path,
// which is how routes are addressed operationally.
func locationKey(l *config.LocationConfig) string {
	t := l.Match.Type
	if t == "" {
		t = "prefix"
	}
	return t + " " + l.Match.Path
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

func locationIndex(locs []config.LocationConfig) map[string]*config.LocationConfig {
	m := make(map[string]*config.LocationConfig, len(locs))
	for i := range locs {
		l := &locs[i]
		m[locationKey(l)] = l
	}
	return m
}

// diffLocations compares the locations (routes) within a single server block,
// reporting additions, removals, and per-field modifications with operational
// consequences (action/target, auth, cache, compression, rate limit, body
// size, proxy timeouts).
func diffLocations(server string, before, after []config.LocationConfig, d *ConfigDiff) {
	bs, as := locationIndex(before), locationIndex(after)
	for _, key := range sortedKeys(as) {
		a := as[key]
		b, ok := bs[key]
		name := server + " " + key
		if !ok {
			d.add(DiffEntry{Kind: "location", Name: name, After: locationAction(a) + " → " + orNone(locationTarget(a)), Detail: "Add route " + key + " on " + server}, "route "+name)
			continue
		}
		diffLocationFields(server, key, b, a, d)
	}
	for _, key := range sortedKeys(bs) {
		if _, ok := as[key]; !ok {
			b := bs[key]
			name := server + " " + key
			d.del(DiffEntry{Kind: "location", Name: name, Before: locationAction(b) + " → " + orNone(locationTarget(b)), Detail: "Remove route " + key + " on " + server}, "route "+name)
			d.warn("Removing route %s on %s will stop matching requests from being handled by it.", key, server)
		}
	}
}

func diffLocationFields(server, key string, b, a *config.LocationConfig, d *ConfigDiff) {
	name := server + " " + key

	if locationAction(b) != locationAction(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: locationAction(b), After: locationAction(a), Detail: "Change action of route " + key}, "route "+name+" action")
	}
	if locationTarget(b) != locationTarget(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: orNone(locationTarget(b)), After: orNone(locationTarget(a)), Detail: "Change target of route " + key}, "route "+name+" target")
		d.warn("Changing the target of route %s on %s redirects matching traffic to a different backend or destination.", key, server)
	}

	// Auth toggle.
	if (b.Auth != nil) != (a.Auth != nil) {
		action := "Enable"
		if a.Auth == nil {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "auth", Name: name, Detail: fmt.Sprintf("%s access control on route %s", action, key)}, "route "+name+" auth")
		if action == "Disable" {
			d.warn("Disabling access control on route %s on %s exposes it without authentication.", key, server)
		}
	}

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

	// Per-route rate-limit override.
	if (b.RateLimit != nil) != (a.RateLimit != nil) {
		action := "Add"
		if a.RateLimit == nil {
			action = "Remove"
		}
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Detail: fmt.Sprintf("%s rate-limit override on route %s", action, key)}, "route "+name+" rate limit")
	} else if a.RateLimit != nil && b.RateLimit != nil && (a.RateLimit.Rate != b.RateLimit.Rate || a.RateLimit.Burst != b.RateLimit.Burst || a.RateLimit.Key != b.RateLimit.Key) {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: rlStr(b.RateLimit), After: rlStr(a.RateLimit), Detail: "Change rate-limit policy on route " + key}, "route "+name+" rate limit")
	}

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

	// Proxy timeouts.
	diffProxyTimeouts(server, key, b, a, d)
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
	if !strings.EqualFold(b.Strategy, a.Strategy) {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(b.Strategy), After: orNone(a.Strategy), Detail: "Change load-balancing strategy of " + name}, "upstream "+name+" strategy")
	}

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

	// Retries / passive health (max_fails, fail_timeout).
	if b.MaxFails != a.MaxFails {
		d.mod(DiffEntry{Kind: "retries", Name: name, Before: fmt.Sprintf("%d", b.MaxFails), After: fmt.Sprintf("%d", a.MaxFails), Detail: "Change max_fails (passive health/retry threshold) of " + name}, "upstream "+name+" max_fails")
		if a.MaxFails == 0 && b.MaxFails != 0 {
			d.warn("Setting max_fails to 0 on %s disables passive health checking; failed backends stay in rotation.", name)
		}
	}
	if b.FailTimeout != a.FailTimeout {
		d.mod(DiffEntry{Kind: "retries", Name: name, Before: durStr(b.FailTimeout), After: durStr(a.FailTimeout), Detail: "Change fail_timeout of " + name}, "upstream "+name+" fail_timeout")
	}

	// Active health checks.
	bHC := b.HealthCheck != nil && b.HealthCheck.Enabled
	aHC := a.HealthCheck != nil && a.HealthCheck.Enabled
	if bHC != aHC {
		action := "Enable"
		if !aHC {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "upstream", Name: name, Detail: fmt.Sprintf("%s active health checks on %s", action, name)}, "upstream "+name+" health check")
	}

	// Service discovery.
	bDisc := discoveryType(b.Discovery)
	aDisc := discoveryType(a.Discovery)
	if bDisc != aDisc {
		d.mod(DiffEntry{Kind: "upstream", Name: name, Before: orNone(bDisc), After: orNone(aDisc), Detail: "Change service discovery of " + name}, "upstream "+name+" discovery")
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

// diffGlobalCache compares the [cache] block.
func diffGlobalCache(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Cache, after.Cache
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "cache", Name: "global", Detail: action + " the response cache"}, "cache")
		return
	}
	if !a.Enabled {
		return
	}
	if b.MemoryMaxSize != a.MemoryMaxSize {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: sizeStr(b.MemoryMaxSize), After: sizeStr(a.MemoryMaxSize), Detail: "Change cache memory size"}, "cache memory")
	}
	if b.DiskPath != a.DiskPath {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: orNone(b.DiskPath), After: orNone(a.DiskPath), Detail: "Change cache disk path"}, "cache disk")
	}
	if b.DefaultTTL != a.DefaultTTL {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: durStr(b.DefaultTTL), After: durStr(a.DefaultTTL), Detail: "Change cache default TTL"}, "cache ttl")
	}
	if b.StaleWhileRevalidate != a.StaleWhileRevalidate {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: durStr(b.StaleWhileRevalidate), After: durStr(a.StaleWhileRevalidate), Detail: "Change cache stale-while-revalidate window"}, "cache swr")
	}
}

// diffGlobalCompression compares the [compression] block.
func diffGlobalCompression(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Compression, after.Compression
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "compression", Name: "global", Detail: action + " response compression"}, "compression")
		return
	}
	if !a.Enabled {
		return
	}
	if strings.Join(b.Encoders, ",") != strings.Join(a.Encoders, ",") {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Before: orNone(strings.Join(b.Encoders, ", ")), After: orNone(strings.Join(a.Encoders, ", ")), Detail: "Change compression encoders"}, "compression encoders")
	}
	if b.Level != a.Level {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Before: fmt.Sprintf("%d", b.Level), After: fmt.Sprintf("%d", a.Level), Detail: "Change compression level"}, "compression level")
	}
	if b.MinSize != a.MinSize {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Before: sizeStr(b.MinSize), After: sizeStr(a.MinSize), Detail: "Change compression minimum size"}, "compression min size")
	}
	if strings.Join(b.Types, ",") != strings.Join(a.Types, ",") {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Detail: "Change compression content types"}, "compression types")
	}
}

// diffGlobalRateLimit compares the global [rate_limit] block.
func diffGlobalRateLimit(before, after *config.Config, d *ConfigDiff) {
	b, a := before.RateLimit, after.RateLimit
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Detail: action + " global rate limiting"}, "rate limit")
		if !a.Enabled {
			d.warn("Disabling global rate limiting removes protection against request floods.")
		}
		return
	}
	if !a.Enabled {
		return
	}
	if b.Key != a.Key || b.Rate != a.Rate || b.Burst != a.Burst {
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Before: rlStr(&b), After: rlStr(&a), Detail: "Change global rate-limit policy"}, "rate limit policy")
	}
	if b.MaxConns != a.MaxConns {
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Before: fmt.Sprintf("%d", b.MaxConns), After: fmt.Sprintf("%d", a.MaxConns), Detail: "Change max concurrent connections"}, "rate limit max conns")
	}
}
