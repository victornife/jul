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
	d.cover("servers.*.locations.*.auth")

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
	if (b.RateLimit != nil) != (a.RateLimit != nil) {
		action := "Add"
		if a.RateLimit == nil {
			action = "Remove"
		}
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Detail: fmt.Sprintf("%s rate-limit override on route %s", action, key)}, "route "+name+" rate limit")
	} else if a.RateLimit != nil && b.RateLimit != nil && (a.RateLimit.Rate != b.RateLimit.Rate || a.RateLimit.Burst != b.RateLimit.Burst || a.RateLimit.Key != b.RateLimit.Key) {
		d.mod(DiffEntry{Kind: "rate_limit", Name: name, Before: rlStr(b.RateLimit), After: rlStr(a.RateLimit), Detail: "Change rate-limit policy on route " + key}, "route "+name+" rate limit")
	}
	d.cover("servers.*.locations.*.rate_limit")

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
