// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"strconv"
	"strings"

	"jul/internal/config"
	"jul/internal/resilience"
)

// This file holds the pure builder/formatter helpers used by applyPatch: they
// translate the wire DTOs (patch_types.go) into validated config structs and
// render the human-readable audit summaries. They own no state and never touch
// the running Server, which keeps them trivially unit-testable in isolation and
// keeps patch.go focused on the operation dispatch itself.

func buildLocationAuth(a locationAuth) (*config.AuthConfig, string, error) {
	switch strings.TrimSpace(a.Method) {
	case "cidr":
		allow := normalizeStringSlice(a.Allow)
		deny := normalizeStringSlice(a.Deny)
		if len(allow) == 0 && len(deny) == 0 {
			return nil, "", fmt.Errorf("location_set_auth: the cidr method needs at least one allow or deny entry")
		}
		return &config.AuthConfig{Allow: allow, Deny: deny}, "IP allow/deny", nil
	case "basic":
		if strings.TrimSpace(a.BasicFile) == "" {
			return nil, "", fmt.Errorf("location_set_auth: the basic method needs an htpasswd file")
		}
		return &config.AuthConfig{Basic: &config.BasicAuthConfig{
			File:  strings.TrimSpace(a.BasicFile),
			Realm: strings.TrimSpace(a.BasicRealm),
		}}, "HTTP Basic", nil
	case "jwt":
		if strings.TrimSpace(a.JWTJWKSURL) == "" {
			return nil, "", fmt.Errorf("location_set_auth: the jwt method needs a jwks_url")
		}
		return &config.AuthConfig{JWT: &config.JWTAuthConfig{
			JWKSURL:  strings.TrimSpace(a.JWTJWKSURL),
			Issuer:   strings.TrimSpace(a.JWTIssuer),
			Audience: strings.TrimSpace(a.JWTAudience),
		}}, "JWT", nil
	case "forward":
		if strings.TrimSpace(a.ForwardURL) == "" {
			return nil, "", fmt.Errorf("location_set_auth: the forward method needs a url")
		}
		return &config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{
			URL: strings.TrimSpace(a.ForwardURL),
		}}, "forward-auth", nil
	default:
		return nil, "", fmt.Errorf("location_set_auth: unknown method %q (want cidr, basic, jwt, or forward)", a.Method)
	}
}

func onOff(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// applyLocationPredicates mutates loc's match predicate facets in place from
// the wire payload, replacing each facet the payload names and leaving the
// others untouched (nil means "leave this facet as configured"). Op-level
// checks mirror config.HeaderMatch/QueryMatch's own grammar (value required
// for exact/regex, forbidden for present) so a malformed predicate is rejected
// with an operation-indexed message before the diff is generated; the
// validated re-parse still enforces the rest (e.g. header name grammar).
func applyLocationPredicates(loc *config.LocationConfig, p locationPredicates) (string, error) {
	var applied []string
	if p.Methods != nil {
		loc.Match.Methods = normalizeStringSlice(*p.Methods)
		applied = append(applied, fmt.Sprintf("methods=%d", len(loc.Match.Methods)))
	}
	if p.Headers != nil {
		hs := make([]config.HeaderMatch, 0, len(*p.Headers))
		for i, h := range *p.Headers {
			name := strings.TrimSpace(h.Name)
			op := strings.ToLower(strings.TrimSpace(h.Op))
			if name == "" {
				return "", fmt.Errorf("location_set_predicates: headers[%d]: name is required", i)
			}
			switch op {
			case "present":
				if h.Value != nil {
					return "", fmt.Errorf("location_set_predicates: headers[%d]: value is forbidden for op %q", i, op)
				}
			case "exact", "regex":
				if h.Value == nil {
					return "", fmt.Errorf("location_set_predicates: headers[%d]: value is required for op %q", i, op)
				}
			default:
				return "", fmt.Errorf("location_set_predicates: headers[%d]: op must be %q, %q, or %q", i, "present", "exact", "regex")
			}
			hs = append(hs, config.HeaderMatch{Name: name, Op: op, Value: h.Value})
		}
		loc.Match.Headers = hs
		applied = append(applied, fmt.Sprintf("headers=%d", len(hs)))
	}
	if p.Query != nil {
		qs := make([]config.QueryMatch, 0, len(*p.Query))
		for i, q := range *p.Query {
			name := strings.TrimSpace(q.Name)
			op := strings.ToLower(strings.TrimSpace(q.Op))
			if name == "" {
				return "", fmt.Errorf("location_set_predicates: query[%d]: name is required", i)
			}
			switch op {
			case "present":
				if q.Value != nil {
					return "", fmt.Errorf("location_set_predicates: query[%d]: value is forbidden for op %q", i, op)
				}
			case "exact":
				if q.Value == nil {
					return "", fmt.Errorf("location_set_predicates: query[%d]: value is required for op %q", i, op)
				}
			default:
				return "", fmt.Errorf("location_set_predicates: query[%d]: op must be %q or %q", i, "present", "exact")
			}
			qs = append(qs, config.QueryMatch{Name: name, Op: op, Value: q.Value})
		}
		loc.Match.Query = qs
		applied = append(applied, fmt.Sprintf("query=%d", len(qs)))
	}
	if len(applied) == 0 {
		return "", fmt.Errorf("location_set_predicates: at least one of methods, headers, or query is required")
	}
	return strings.Join(applied, ", "), nil
}

// buildResponseHeaderOps translates the wire payload into the location's new
// ordered []config.ResponseHeaderOp wholesale. Op-level checks mirror
// config.ResponseHeaderOp's own grammar; bounds (max count, byte limits) are
// left to the validated re-parse, which reports them per ADR 0018 §8a/§8b.
func buildResponseHeaderOps(in []responseHeaderOpPatch) ([]config.ResponseHeaderOp, error) {
	ops := make([]config.ResponseHeaderOp, 0, len(in))
	for i, p := range in {
		op := strings.ToLower(strings.TrimSpace(p.Op))
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return nil, fmt.Errorf("location_response_headers_set: ops[%d]: name is required", i)
		}
		switch op {
		case "add", "set":
			if p.Value == nil {
				return nil, fmt.Errorf("location_response_headers_set: ops[%d]: value is required for op %q", i, op)
			}
		case "remove":
			if p.Value != nil {
				return nil, fmt.Errorf("location_response_headers_set: ops[%d]: value is forbidden for op %q", i, op)
			}
		default:
			return nil, fmt.Errorf("location_response_headers_set: ops[%d]: op must be %q, %q, or %q", i, "add", "set", "remove")
		}
		ops = append(ops, config.ResponseHeaderOp{Op: op, Name: name, Value: p.Value})
	}
	return ops, nil
}

// buildCORS translates the wire payload into a *config.CORSConfig wholesale,
// the same "replace in full" convention as buildLocationAuth. Bounds (origin
// count/length, no wildcard alongside credentials, etc.) are left to the
// validated re-parse (ADR 0018 §9), which reports them with the exact field.
func buildCORS(p corsPatch) (*config.CORSConfig, error) {
	cors := &config.CORSConfig{
		Enabled:          p.Enabled,
		AllowedOrigins:   normalizeStringSlice(p.AllowedOrigins),
		AllowedMethods:   normalizeStringSlice(p.AllowedMethods),
		AllowedHeaders:   normalizeStringSlice(p.AllowedHeaders),
		ExposedHeaders:   normalizeStringSlice(p.ExposedHeaders),
		AllowCredentials: p.AllowCredentials,
	}
	if p.MaxAge != nil {
		if trimmed := strings.TrimSpace(*p.MaxAge); trimmed != "" {
			var d config.Duration
			if err := d.UnmarshalText([]byte(trimmed)); err != nil {
				return nil, fmt.Errorf("location_cors_set: max_age: %w", err)
			}
			cors.MaxAge = &d
		}
	}
	return cors, nil
}

// orDefault returns s, or def when s is empty — used to echo the effective value
// (after re-parse defaulting) in an audit summary.
func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// buildHealthCheck turns the editor payload into a *config.HealthCheckConfig.
// A disabled payload returns nil so the serialized pool drops the [health_check]
// block entirely (passive health only). Durations are parsed here; everything
// else (defaulting, timeout < interval, http-needs-path) is enforced by the
// validated SaveConfig re-parse, so the structured edit never bypasses it.
func buildHealthCheck(in upstreamHealthCheck) (*config.HealthCheckConfig, string, error) {
	if !in.Enabled {
		return nil, "disabled", nil
	}
	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		typ = "http"
	}
	if typ != "http" && typ != "tcp" {
		return nil, "", fmt.Errorf("upstream_set_health_check: type must be %q or %q", "http", "tcp")
	}
	hc := &config.HealthCheckConfig{
		Enabled:            true,
		Type:               typ,
		Path:               strings.TrimSpace(in.Path),
		HealthyThreshold:   in.HealthyThreshold,
		UnhealthyThreshold: in.UnhealthyThreshold,
		ExpectBody:         strings.TrimSpace(in.ExpectBody),
	}
	if typ == "http" && hc.Path == "" {
		return nil, "", fmt.Errorf("upstream_set_health_check: path is required for http probes")
	}
	if err := parseDurInto(in.Interval, &hc.Interval, "interval"); err != nil {
		return nil, "", fmt.Errorf("upstream_set_health_check: %w", err)
	}
	if err := parseDurInto(in.Timeout, &hc.Timeout, "timeout"); err != nil {
		return nil, "", fmt.Errorf("upstream_set_health_check: %w", err)
	}
	if len(in.ExpectStatus) > 0 {
		hc.ExpectStatus = append([]int(nil), in.ExpectStatus...)
	}
	note := typ
	if typ == "http" && hc.Path != "" {
		note = typ + " " + hc.Path
	}
	return hc, "enabled (" + note + ")", nil
}

// buildDiscovery turns the editor payload into a *config.DiscoveryConfig. A
// static/empty type returns nil so the pool falls back to its static Servers
// list. Secret tokens are never carried on the wire: when the provider type is
// unchanged, the existing Consul/Kubernetes token is preserved from prev rather
// than wiped. Per-provider required fields and refresh range are enforced by the
// validated SaveConfig re-parse.
func buildDiscovery(in upstreamDiscovery, prev *config.DiscoveryConfig) (*config.DiscoveryConfig, string, error) {
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	switch typ {
	case "", "static":
		return nil, "disabled (static backends)", nil
	case "dns", "dns_srv", "consul", "kubernetes":
	default:
		return nil, "", fmt.Errorf("upstream_set_discovery: invalid type %q (want static|dns|dns_srv|consul|kubernetes)", in.Type)
	}
	d := &config.DiscoveryConfig{Type: typ, Target: strings.TrimSpace(in.Target)}
	if err := parseDurInto(in.Refresh, &d.Refresh, "refresh"); err != nil {
		return nil, "", fmt.Errorf("upstream_set_discovery: %w", err)
	}
	sameType := prev != nil && strings.EqualFold(strings.TrimSpace(prev.Type), typ)
	if typ == "consul" {
		cd := &config.ConsulDiscovery{}
		if in.Consul != nil {
			cd.Address = strings.TrimSpace(in.Consul.Address)
			cd.Service = strings.TrimSpace(in.Consul.Service)
			cd.Tag = strings.TrimSpace(in.Consul.Tag)
			cd.Datacenter = strings.TrimSpace(in.Consul.Datacenter)
			cd.PassingOnly = in.Consul.PassingOnly
		}
		if sameType && prev.Consul != nil {
			cd.Token = prev.Consul.Token // preserve the secret ACL token
		}
		d.Consul = cd
	}
	if typ == "kubernetes" {
		kd := &config.KubernetesDiscovery{}
		if in.Kubernetes != nil {
			kd.Namespace = strings.TrimSpace(in.Kubernetes.Namespace)
			kd.Service = strings.TrimSpace(in.Kubernetes.Service)
			kd.Port = strings.TrimSpace(in.Kubernetes.Port)
			kd.APIServer = strings.TrimSpace(in.Kubernetes.APIServer)
			kd.CAFile = strings.TrimSpace(in.Kubernetes.CAFile)
			kd.InsecureSkipTLSVerify = in.Kubernetes.InsecureSkipTLSVerify
		}
		if sameType && prev.Kubernetes != nil {
			kd.Token = prev.Kubernetes.Token // preserve the secret bearer token
		}
		d.Kubernetes = kd
	}
	return d, "set to " + typ, nil
}

// parseDurInto parses an optional duration string (e.g. "5s") into dst. An empty
// string leaves dst at its zero value so the re-parse defaulting applies.
func parseDurInto(val string, dst *config.Duration, name string) error {
	if strings.TrimSpace(val) == "" {
		return nil
	}
	var d config.Duration
	if err := d.UnmarshalText([]byte(val)); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	*dst = d
	return nil
}

// wafModeNote renders the mode/CRS suffix for a location_waf_set audit summary,
// e.g. " — block, CRS". It is empty when the override is disabled, since mode
// and CRS do not apply to a switched-off firewall.
func wafModeNote(enabled bool, mode string, crs bool) string {
	if !enabled {
		return ""
	}
	if crs {
		return fmt.Sprintf(" — %s, CRS", mode)
	}
	return fmt.Sprintf(" — %s", mode)
}

// buildResilience turns an upstream_set_resilience payload into a config block
// and a summary of what changed. It replaces the whole block rather than
// merging, matching upstream_set_health_check: a partial merge would make the
// result depend on state the caller cannot see in the request they are sending.
func buildResilience(in upstreamResilience) (*config.ResilienceConfig, error) {
	out := &config.ResilienceConfig{
		MaxFails:                 in.MaxFails,
		MaxActiveRequests:        in.MaxActiveRequests,
		MaxActivePerBackend:      in.MaxActivePerBackend,
		MaxPendingRequests:       in.MaxPendingRequests,
		MaxConnectionsPerBackend: in.MaxConnectionsPerBackend,
		RetryAttempts:            in.RetryAttempts,
		RetryBudgetPercent:       in.RetryBudgetPercent,
		CircuitHalfOpenProbes:    in.CircuitHalfOpenProbes,
	}
	durations := []struct {
		val  string
		dst  *config.Duration
		name string
	}{
		{in.FailTimeout, &out.FailTimeout, "fail_timeout"},
		{in.PendingTimeout, &out.PendingTimeout, "pending_timeout"},
		{in.RetryDeadline, &out.RetryDeadline, "retry_deadline"},
		{in.RetryBackoffInitial, &out.RetryBackoffInitial, "retry_backoff_initial"},
		{in.RetryBackoffMax, &out.RetryBackoffMax, "retry_backoff_max"},
	}
	for _, d := range durations {
		if err := parseDurInto(d.val, d.dst, "upstream_set_resilience: "+d.name); err != nil {
			return nil, err
		}
	}
	// The bounds are the runtime's, not this package's, so they are checked by
	// the same resolver the proxy uses rather than restated here where the two
	// could drift.
	if _, err := resilience.Resolve(resilience.Options{
		MaxActiveRequests:        out.MaxActiveRequests,
		MaxActivePerBackend:      out.MaxActivePerBackend,
		MaxPendingRequests:       out.MaxPendingRequests,
		PendingTimeout:           out.PendingTimeout.Std(),
		MaxConnectionsPerBackend: out.MaxConnectionsPerBackend,
		RetryAttempts:            out.RetryAttempts,
		RetryDeadline:            out.RetryDeadline.Std(),
		RetryBackoffInitial:      out.RetryBackoffInitial.Std(),
		RetryBackoffMax:          out.RetryBackoffMax.Std(),
		RetryBudgetPercent:       out.RetryBudgetPercent,
	}); err != nil {
		return nil, fmt.Errorf("upstream_set_resilience: %w", err)
	}
	if out.MaxFails < 0 {
		return nil, fmt.Errorf("upstream_set_resilience: max_fails must not be negative")
	}
	if out.CircuitHalfOpenProbes != nil && *out.CircuitHalfOpenProbes < 0 {
		return nil, fmt.Errorf("upstream_set_resilience: circuit_half_open_probes must not be negative")
	}
	return out, nil
}

// resilienceSummary describes the applied block for the audit log, naming only
// the limits actually set so the line stays readable.
func resilienceSummary(r *config.ResilienceConfig) string {
	if r == nil {
		return "cleared"
	}
	parts := make([]string, 0, 8)
	add := func(name string, set bool, val string) {
		if set {
			parts = append(parts, name+"="+val)
		}
	}
	add("max_fails", r.MaxFails > 0, strconv.Itoa(r.MaxFails))
	add("fail_timeout", r.FailTimeout > 0, r.FailTimeout.Std().String())
	add("max_active_requests", r.MaxActiveRequests > 0, strconv.Itoa(r.MaxActiveRequests))
	add("max_active_per_backend", r.MaxActivePerBackend > 0, strconv.Itoa(r.MaxActivePerBackend))
	add("max_pending_requests", r.MaxPendingRequests > 0, strconv.Itoa(r.MaxPendingRequests))
	add("pending_timeout", r.PendingTimeout > 0, r.PendingTimeout.Std().String())
	add("max_connections_per_backend", r.MaxConnectionsPerBackend > 0, strconv.Itoa(r.MaxConnectionsPerBackend))
	add("retry_attempts", r.RetryAttempts > 0, strconv.Itoa(r.RetryAttempts))
	add("retry_deadline", r.RetryDeadline > 0, r.RetryDeadline.Std().String())
	add("retry_backoff_initial", r.RetryBackoffInitial > 0, r.RetryBackoffInitial.Std().String())
	add("retry_backoff_max", r.RetryBackoffMax > 0, r.RetryBackoffMax.Std().String())
	add("retry_budget_percent", r.RetryBudgetPercent > 0, strconv.Itoa(r.RetryBudgetPercent))
	if r.CircuitHalfOpenProbes != nil {
		parts = append(parts, "circuit_half_open_probes="+strconv.Itoa(*r.CircuitHalfOpenProbes))
	}
	if len(parts) == 0 {
		return "set to defaults"
	}
	return "set to " + strings.Join(parts, ", ")
}
