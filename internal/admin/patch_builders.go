// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"strings"

	"jul/internal/config"
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
