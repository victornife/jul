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
func diffLocations(server string, before, after []config.LocationConfig, beforeGlobWAF, afterGlobWAF config.WAFConfig, d *ConfigDiff) {
	bs, as := locationIndex(before), locationIndex(after)
	for _, key := range sortedKeys(as) {
		a := as[key]
		b, ok := bs[key]
		name := server + " " + key
		if !ok {
			d.add(DiffEntry{Kind: "location", Name: name, After: locationAction(a) + " → " + orNone(locationTarget(a)), Detail: "Add route " + key + " on " + server}, "route "+name)
			continue
		}
		diffLocationFields(server, key, b, a, beforeGlobWAF, afterGlobWAF, d)
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

func diffLocationFields(server, key string, b, a *config.LocationConfig, beforeGlobWAF, afterGlobWAF config.WAFConfig, d *ConfigDiff) {
	name := server + " " + key

	if locationAction(b) != locationAction(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: locationAction(b), After: locationAction(a), Detail: "Change action of route " + key}, "route "+name+" action")
	}
	if locationTarget(b) != locationTarget(a) {
		d.mod(DiffEntry{Kind: "location", Name: name, Before: orNone(locationTarget(b)), After: orNone(locationTarget(a)), Detail: "Change target of route " + key}, "route "+name+" target")
		d.warn("Changing the target of route %s on %s redirects matching traffic to a different backend or destination.", key, server)
	}

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
	} else if bHC && aHC {
		diffHealthCheckFields(name, b.HealthCheck, a.HealthCheck, d)
	}

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
	if b.StaleIfError != a.StaleIfError {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: durStr(b.StaleIfError), After: durStr(a.StaleIfError), Detail: "Change cache stale-if-error window"}, "cache sif")
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

// diffGlobalWAF compares the global [waf] block.
func diffGlobalWAF(before, after *config.Config, d *ConfigDiff) {
	b, a := before.WAF, after.WAF
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: action + " global WAF"}, "waf global")
		if a.Enabled {
			d.warn("Enabling the WAF inspects requests against the configured rules; it only enforces in binaries built with the waf tag, and the apply preflight rejects an enabled WAF otherwise.")
		} else {
			d.warn("Disabling the global WAF removes rule inspection from routes that do not have a per-location override.")
		}
		return
	}
	if !a.Enabled {
		return
	}
	if b.Mode != a.Mode {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: b.Mode, After: a.Mode, Detail: "Change global WAF mode"}, "waf global mode")
		if b.Mode == "block" && a.Mode == "detect" {
			d.warn("Switching global WAF to detect mode stops blocking threats.")
		}
	}
	if b.BlockStatus != a.BlockStatus {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: fmt.Sprintf("%d", b.BlockStatus), After: fmt.Sprintf("%d", a.BlockStatus), Detail: "Change global WAF block status"}, "waf global block_status")
	}
	if b.Paranoia != a.Paranoia {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: fmt.Sprintf("%d", b.Paranoia), After: fmt.Sprintf("%d", a.Paranoia), Detail: "Change global WAF paranoia level"}, "waf global paranoia")
		if a.Paranoia < b.Paranoia {
			d.warn("Lowering global WAF paranoia reduces rule coverage.")
		}
	}
	if b.CRSEnabled != a.CRSEnabled {
		action := "Enable"
		if !a.CRSEnabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: action + " global CRS"}, "waf global crs")
		if !a.CRSEnabled {
			d.warn("Disabling the global CRS removes the core rule set.")
		}
	}
	if b.RequestBodyLimit != a.RequestBodyLimit {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: sizeStr(b.RequestBodyLimit), After: sizeStr(a.RequestBodyLimit), Detail: "Change global WAF request body limit on global"}, "waf global body_limit")
		if a.RequestBodyLimit.Bytes() == 0 && b.RequestBodyLimit.Bytes() != 0 {
			d.warn("Removing the global WAF request body limit allows arbitrarily large uploads to be inspected.")
		}
	}
}

func diffSecretRefs(before, after *config.Config, d *ConfigDiff) {
	bN := config.CountSecretRefs(before)
	aN := config.CountSecretRefs(after)
	if bN != aN {
		d.mod(DiffEntry{Kind: "secrets", Name: "global", Before: fmt.Sprintf("%d", bN), After: fmt.Sprintf("%d", aN), Detail: "Change secret reference count"}, "secret refs")
	}
}

// diffGlobalTracing compares the [observability.tracing] block. Tracing changes
// govern what telemetry leaves the process and where it is sent, so each is
// surfaced explicitly; enabling exports spans and is only active in binaries
// built with the otel tag.
func diffGlobalTracing(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Observability.Tracing, after.Observability.Tracing
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Detail: action + " distributed tracing"}, "tracing")
		if a.Enabled {
			d.warn("Enabling tracing exports spans to the collector; it is only active in binaries built with the otel tag.")
		}
		return
	}
	if !a.Enabled {
		return
	}
	if b.Exporter != a.Exporter {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: orNone(b.Exporter), After: orNone(a.Exporter), Detail: "Change tracing exporter"}, "tracing exporter")
	}
	if b.Endpoint != a.Endpoint {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: orNone(b.Endpoint), After: orNone(a.Endpoint), Detail: "Change tracing collector endpoint"}, "tracing endpoint")
	}
	if b.SampleRatio != a.SampleRatio {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: fmt.Sprintf("%g", b.SampleRatio), After: fmt.Sprintf("%g", a.SampleRatio), Detail: "Change tracing sample ratio"}, "tracing sample_ratio")
	}
	if b.ServiceName != a.ServiceName {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: orNone(b.ServiceName), After: orNone(a.ServiceName), Detail: "Change tracing service name"}, "tracing service_name")
	}
	if b.Insecure != a.Insecure {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Detail: "Change tracing transport security"}, "tracing insecure")
		if a.Insecure {
			d.warn("Tracing now sends spans over plaintext (insecure); only use this for a local collector on a trusted network.")
		}
	}
}

// diffGlobalPlugins compares the declared WASM plugin set ([plugins.NAME]),
// reporting added/removed declarations and per-plugin changes to the module
// source, type, granted host capabilities, and limits. Attachment (which routes
// run a plugin) is diffed per-location in diffLocationFields.
func diffGlobalPlugins(before, after *config.Config, d *ConfigDiff) {
	for _, name := range sortedKeys(after.Plugins) {
		a := after.Plugins[name]
		b, ok := before.Plugins[name]
		if !ok {
			d.add(DiffEntry{Kind: "plugin", Name: name, After: pluginSummary(a), Detail: "Add plugin " + name}, "plugin "+name)
			d.warn("Plugin %s runs guest WASM; it only loads in binaries built with the wasmplugins tag, and the apply preflight rejects it otherwise.", name)
			continue
		}
		diffPluginFields(name, b, a, d)
	}
	for _, name := range sortedKeys(before.Plugins) {
		if _, ok := after.Plugins[name]; !ok {
			b := before.Plugins[name]
			d.del(DiffEntry{Kind: "plugin", Name: name, Before: pluginSummary(b), Detail: "Remove plugin " + name}, "plugin "+name)
		}
	}
}

// diffPluginFields reports per-plugin declaration changes between matched
// [plugins.NAME] blocks, warning when a host capability (kv/fetch) is newly
// granted.
func diffPluginFields(name string, b, a config.PluginConfig, d *ConfigDiff) {
	if pluginSource(b) != pluginSource(a) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: pluginSource(b), After: pluginSource(a), Detail: "Change plugin module source for " + name}, "plugin "+name+" source")
	}
	if pluginTypeOrDefault(b) != pluginTypeOrDefault(a) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: pluginTypeOrDefault(b), After: pluginTypeOrDefault(a), Detail: "Change plugin type for " + name}, "plugin "+name+" type")
	}
	if b.KV != a.KV {
		action := "Grant"
		if !a.KV {
			action = "Revoke"
		}
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: fmt.Sprintf("%s KV store access for plugin %s", action, name)}, "plugin "+name+" kv")
		if a.KV {
			d.warn("Plugin %s now has KV store access; it can read and write shared key-value state.", name)
		}
	}
	if b.Fetch != a.Fetch {
		action := "Grant"
		if !a.Fetch {
			action = "Revoke"
		}
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: fmt.Sprintf("%s outbound fetch for plugin %s", action, name)}, "plugin "+name+" fetch")
		if a.Fetch {
			d.warn("Plugin %s can now make outbound HTTP requests; it is restricted to the allowed_hosts allowlist.", name)
		}
	}
	if bf, af := strings.Join(b.AllowedHosts, ","), strings.Join(a.AllowedHosts, ","); bf != af {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: orNone(bf), After: orNone(af), Detail: "Change plugin fetch allowlist for " + name}, "plugin "+name+" allowed_hosts")
	}
	if !stringMapEqual(b.Config, a.Config) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: "Change plugin config for " + name}, "plugin "+name+" config")
	}
	if b.MemoryLimit != a.MemoryLimit {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: sizeStr(b.MemoryLimit), After: sizeStr(a.MemoryLimit), Detail: "Change plugin memory limit for " + name}, "plugin "+name+" memory_limit")
	}
	if b.Timeout != a.Timeout {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: durStr(b.Timeout), After: durStr(a.Timeout), Detail: "Change plugin timeout for " + name}, "plugin "+name+" timeout")
	}
}

// pluginSource renders a plugin's module source for a diff: "inline" for an
// embedded module, or "path <file>" for a file-backed one.
func pluginSource(p config.PluginConfig) string {
	if strings.TrimSpace(p.Inline) != "" {
		return "inline"
	}
	return "path " + p.Path
}

// stringSetDiff returns the elements added to and removed from a string slice
// (set semantics, ignoring order and duplicates), used to diff a location's
// plugin middleware chain.
func stringSetDiff(before, after []string) (added, removed []string) {
	bset := make(map[string]bool, len(before))
	for _, s := range before {
		bset[s] = true
	}
	aset := make(map[string]bool, len(after))
	for _, s := range after {
		aset[s] = true
	}
	for _, s := range after {
		if !bset[s] {
			added = append(added, s)
			bset[s] = true // dedupe
		}
	}
	for _, s := range before {
		if !aset[s] {
			removed = append(removed, s)
			aset[s] = true // dedupe
		}
	}
	return added, removed
}

// stringMapEqual reports whether two string maps have identical keys and values.
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// locationEffectiveWAF returns the WAF policy that applies to a location,
// taking inheritance from the global policy into account.  If the location
// does not define a WAF block at all, the global policy is returned (when
// enabled).  A nil result means no WAF protection for this location.
func locationEffectiveWAF(loc *config.LocationConfig, global config.WAFConfig) *config.WAFConfig {
	if loc.WAF != nil {
		if loc.WAF.Enabled {
			return loc.WAF
		}
		return nil
	}
	if global.Enabled {
		return &global
	}
	return nil
}

// diffStreams compares the declared [[stream]] L4 listeners, reporting added and
// removed listeners plus per-listener field changes. Streams are a slice keyed
// by their protocol + listen identity (the same key the validator dedups on), so
// a change to that identity surfaces as a remove + add rather than a modify.
func diffStreams(before, after *config.Config, d *ConfigDiff) {
	bs, as := streamIndex(before.Streams), streamIndex(after.Streams)
	for _, key := range sortedKeys(as) {
		a := as[key]
		b, ok := bs[key]
		if !ok {
			d.add(DiffEntry{Kind: "stream", Name: key, After: streamSummary(a), Detail: "Add L4 stream listener " + key}, "stream "+key)
			d.warn("Stream %s opens an L4 (TCP/UDP) listener; it only serves in binaries built with the stream tag, and a lean binary refuses to start with it.", key)
			continue
		}
		diffStreamFields(key, b, a, d)
	}
	for _, key := range sortedKeys(bs) {
		if _, ok := as[key]; !ok {
			b := bs[key]
			d.del(DiffEntry{Kind: "stream", Name: key, Before: streamSummary(b), Detail: "Remove L4 stream listener " + key}, "stream "+key)
			d.warn("Removing stream %s stops L4 proxying on that listener.", key)
		}
	}
}

// streamIndex keys a stream slice by its normalized "proto/listen" identity for
// diffing. A duplicate key (which the validated config rejects) keeps the last
// occurrence, mirroring how the runtime would treat the live set.
func streamIndex(streams []config.StreamServer) map[string]config.StreamServer {
	out := make(map[string]config.StreamServer, len(streams))
	for _, st := range streams {
		out[streamProtoOrDefault(st.Protocol)+"/"+strings.TrimSpace(st.Listen)] = st
	}
	return out
}

// diffStreamFields reports per-listener changes between two [[stream]] blocks
// with the same proto/listen identity: the default target, SNI routes, TLS
// passthrough, the PROXY protocol, and the connect/idle timeouts.
func diffStreamFields(key string, b, a config.StreamServer, d *ConfigDiff) {
	if strings.TrimSpace(b.ProxyPass) != strings.TrimSpace(a.ProxyPass) {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: orNone(b.ProxyPass), After: orNone(a.ProxyPass), Detail: "Change default backend for stream " + key}, "stream "+key+" proxy_pass")
	}
	if !stringMapEqual(trimSNIRoutes(b.SNIRoutes), trimSNIRoutes(a.SNIRoutes)) {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: fmt.Sprintf("%d route%s", len(b.SNIRoutes), plural(len(b.SNIRoutes))), After: fmt.Sprintf("%d route%s", len(a.SNIRoutes), plural(len(a.SNIRoutes))), Detail: "Change SNI routes for stream " + key}, "stream "+key+" sni_routes")
	}
	if b.TLSPassthrough != a.TLSPassthrough {
		d.mod(DiffEntry{Kind: "stream", Name: key, Detail: "Change TLS passthrough flag for stream " + key}, "stream "+key+" tls_passthrough")
	}
	if bp, ap := strings.ToLower(strings.TrimSpace(b.ProxyProtocol)), strings.ToLower(strings.TrimSpace(a.ProxyProtocol)); bp != ap {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: orNone(bp), After: orNone(ap), Detail: "Change PROXY protocol for stream " + key}, "stream "+key+" proxy_protocol")
	}
	if b.ConnectTimeout != a.ConnectTimeout {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: durStr(b.ConnectTimeout), After: durStr(a.ConnectTimeout), Detail: "Change connect timeout for stream " + key}, "stream "+key+" connect_timeout")
	}
	if b.IdleTimeout != a.IdleTimeout {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: durStr(b.IdleTimeout), After: durStr(a.IdleTimeout), Detail: "Change idle timeout for stream " + key}, "stream "+key+" idle_timeout")
	}
}
