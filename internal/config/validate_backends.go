// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"

	"jul/internal/backendtls"
	"jul/internal/clientaddr"
)

// This file holds the backend-facing validators — L4 stream listeners, active
// health checks, and dynamic service discovery — split out of validate.go to
// keep each validation file focused and under the size bar (Finding CQ-3).

// validateStreams checks the [[stream]] L4 proxy listeners. It validates
// structure only (addresses, protocol, target references, mode keywords);
// whether L4 proxying is compiled in is reported at startup by
// internal/stream.Check, which fails a lean build that declares any stream.
func validateStreams(streams []StreamServer, upstreamNames map[string]int) []error {
	var errs []error
	seen := map[string]int{}
	for i, st := range streams {
		where := fmt.Sprintf("stream[%d]", i)
		if strings.TrimSpace(st.Listen) == "" {
			errs = append(errs, fmt.Errorf("%s: 'listen' is required", where))
		}
		proto := strings.ToLower(strings.TrimSpace(st.Protocol))
		switch proto {
		case "", "tcp", "udp":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid protocol %q (want tcp or udp)", where, st.Protocol))
		}
		if proto == "" {
			proto = "tcp"
		}
		// One listener may not be claimed by two stream blocks of the same proto.
		if st.Listen != "" {
			key := proto + "/" + st.Listen
			seen[key]++
			if seen[key] == 2 {
				errs = append(errs, fmt.Errorf("%s: duplicate %s listener %q", where, proto, st.Listen))
			}
		}
		// A stream must have at least one target: a default proxy_pass and/or
		// SNI routes.
		if strings.TrimSpace(st.ProxyPass) == "" && len(st.SNIRoutes) == 0 {
			errs = append(errs, fmt.Errorf("%s: 'proxy_pass' or 'sni_routes' is required", where))
		}
		if strings.TrimSpace(st.ProxyPass) != "" {
			errs = append(errs, validateStreamTarget(st.ProxyPass, upstreamNames, where+".proxy_pass")...)
		}
		for host, target := range st.SNIRoutes {
			if strings.TrimSpace(host) == "" {
				errs = append(errs, fmt.Errorf("%s.sni_routes: empty SNI host key", where))
			}
			errs = append(errs, validateStreamTarget(target, upstreamNames, fmt.Sprintf("%s.sni_routes[%q]", where, host))...)
		}
		// SNI routing, TLS passthrough, and the PROXY protocol are TCP-only in v1.
		if proto == "udp" {
			if len(st.SNIRoutes) > 0 {
				errs = append(errs, fmt.Errorf("%s: 'sni_routes' is only supported for tcp streams", where))
			}
			if st.TLSPassthrough {
				errs = append(errs, fmt.Errorf("%s: 'tls_passthrough' is only supported for tcp streams", where))
			}
			if strings.TrimSpace(st.ProxyProtocol) != "" {
				errs = append(errs, fmt.Errorf("%s: 'proxy_protocol' is only supported for tcp streams", where))
			}
		}
		switch strings.ToLower(strings.TrimSpace(st.ProxyProtocol)) {
		case "", "in", "out", "both":
		default:
			errs = append(errs, fmt.Errorf("%s: invalid proxy_protocol %q (want in, out, or both)", where, st.ProxyProtocol))
		}
		errs = append(errs, validateStreamTrustedProxies(st, where)...)
		if st.ConnectTimeout < 0 {
			errs = append(errs, fmt.Errorf("%s: 'connect_timeout' must not be negative", where))
		}
		if st.IdleTimeout < 0 {
			errs = append(errs, fmt.Errorf("%s: 'idle_timeout' must not be negative", where))
		}
		if st.MaxUDPSessions < 0 {
			errs = append(errs, fmt.Errorf("%s: 'max_udp_sessions' must not be negative", where))
		}
	}
	return errs
}

// validateStreamTrustedProxies checks the trusted-proxy set that governs an
// inbound PROXY header.
//
// A PROXY header is an assertion, not a kernel fact: whoever may send one
// chooses the address the listener reports and re-emits to the backend. Since
// L4 backends commonly authorise by source address, believing it from any peer
// would hand that decision to the client. The set is therefore required
// whenever a header is ingested, and rejected when none is, so the
// configuration cannot imply a boundary that is never enforced.
func validateStreamTrustedProxies(st StreamServer, where string) []error {
	var errs []error
	ingests := false
	switch strings.ToLower(strings.TrimSpace(st.ProxyProtocol)) {
	case "in", "both":
		ingests = true
	}
	for i, raw := range st.TrustedProxies {
		if _, err := clientaddr.ParsePrefix(raw); err != nil {
			errs = append(errs, fmt.Errorf("%s.trusted_proxies[%d]: %v", where, i, err))
		}
	}
	switch {
	case ingests && len(st.TrustedProxies) == 0:
		errs = append(errs, fmt.Errorf("%s: 'trusted_proxies' is required when proxy_protocol ingests a header (%q or %q); list the proxies permitted to assert a client address", where, "in", "both"))
	case !ingests && len(st.TrustedProxies) > 0:
		errs = append(errs, fmt.Errorf("%s: 'trusted_proxies' applies only when proxy_protocol is %q or %q; remove it or set proxy_protocol", where, "in", "both"))
	}
	return errs
}

// validateStreamTarget checks that a stream target is either a known upstream
// name or a literal host:port address.
func validateStreamTarget(target string, upstreamNames map[string]int, where string) []error {
	target = strings.TrimSpace(target)
	if target == "" {
		return []error{fmt.Errorf("%s: target is empty", where)}
	}
	if _, ok := upstreamNames[target]; ok {
		return nil
	}
	if _, _, err := net.SplitHostPort(target); err != nil {
		return []error{fmt.Errorf("%s: target %q is neither a known upstream nor a host:port address", where, target)}
	}
	return nil
}

// validateHealthCheck checks an upstream's active health-check block. It assumes
// defaults have been applied, so it validates the effective values: the probe
// type, that HTTP probes carry a path, positive interval/timeout with timeout
// strictly below the interval (so a probe finishes before the next round), and
// thresholds of at least one. It is a no-op when nil or disabled.
func validateHealthCheck(h *HealthCheckConfig, where string) []error {
	if h == nil || !h.Enabled {
		return nil
	}
	var errs []error
	switch h.Type {
	case "http":
		if strings.TrimSpace(h.Path) == "" {
			errs = append(errs, fmt.Errorf("%s: 'path' is required for http health checks", where))
		}
		for _, code := range h.ExpectStatus {
			if code < 100 || code > 599 {
				errs = append(errs, fmt.Errorf("%s: invalid expect_status %d (want 100-599)", where, code))
			}
		}
	case "tcp":
		// No path/status semantics for raw TCP connect probes.
	default:
		errs = append(errs, fmt.Errorf("%s: invalid type %q (want http or tcp)", where, h.Type))
	}
	if h.Interval <= 0 {
		errs = append(errs, fmt.Errorf("%s: interval must be greater than 0", where))
	}
	if h.Timeout <= 0 {
		errs = append(errs, fmt.Errorf("%s: timeout must be greater than 0", where))
	}
	if h.Interval > 0 && h.Timeout > 0 && h.Timeout >= h.Interval {
		errs = append(errs, fmt.Errorf("%s: timeout (%s) must be less than interval (%s)", where, h.Timeout.Std(), h.Interval.Std()))
	}
	if h.HealthyThreshold < 1 {
		errs = append(errs, fmt.Errorf("%s: healthy_threshold must be at least 1", where))
	}
	if h.UnhealthyThreshold < 1 {
		errs = append(errs, fmt.Errorf("%s: unhealthy_threshold must be at least 1", where))
	}
	return errs
}

// discoveryEnabled reports whether a discovery block selects a non-static
// provider (so the upstream's backend set is resolved dynamically rather than
// read from the static Servers list).
func discoveryEnabled(d *DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return false
	default:
		return true
	}
}

// validateDiscovery checks an upstream's dynamic discovery block. It validates
// the provider type and the per-provider required fields. It is a no-op when nil
// or static. The "consul"/"kubernetes" types are accepted here regardless of
// build tags: a build lacking the provider rejects the config at startup/reload
// (the discoverer factory errors), the same model as other gated features.
func validateDiscovery(d *DiscoveryConfig, where string) []error {
	if d == nil {
		return nil
	}
	var errs []error
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return nil
	case "dns":
		if strings.TrimSpace(d.Target) == "" {
			errs = append(errs, fmt.Errorf("%s: 'target' is required for dns discovery (want host:port)", where))
		} else if _, port, err := net.SplitHostPort(d.Target); err != nil || strings.TrimSpace(port) == "" {
			errs = append(errs, fmt.Errorf("%s: dns target %q must be host:port (A/AAAA records carry no port)", where, d.Target))
		}
	case "dns_srv":
		if strings.TrimSpace(d.Target) == "" {
			errs = append(errs, fmt.Errorf("%s: 'target' is required for dns_srv discovery (the SRV name)", where))
		}
	case "consul":
		if d.Consul == nil || strings.TrimSpace(d.Consul.Service) == "" {
			errs = append(errs, fmt.Errorf("%s: consul discovery requires [consul] with a 'service'", where))
		}
		if d.Consul != nil && strings.TrimSpace(d.Consul.Address) != "" {
			if u, err := url.Parse(d.Consul.Address); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: consul address %q must be an http(s) URL", where, d.Consul.Address))
			}
		}
		if d.Consul != nil && d.Consul.TLS != nil {
			for _, err := range backendtls.Validate(d.Consul.TLS.Options()) {
				errs = append(errs, fmt.Errorf("%s.consul.tls: %w", where, err))
			}
		}
	case "kubernetes":
		if d.Kubernetes == nil || strings.TrimSpace(d.Kubernetes.Service) == "" || strings.TrimSpace(d.Kubernetes.Namespace) == "" {
			errs = append(errs, fmt.Errorf("%s: kubernetes discovery requires [kubernetes] with 'namespace' and 'service'", where))
		}
		if d.Kubernetes != nil && strings.TrimSpace(d.Kubernetes.APIServer) != "" {
			if u, err := url.Parse(d.Kubernetes.APIServer); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				errs = append(errs, fmt.Errorf("%s: kubernetes api_server %q must be an http(s) URL", where, d.Kubernetes.APIServer))
			}
		}
	default:
		errs = append(errs, fmt.Errorf("%s: invalid type %q (want static|dns|dns_srv|consul|kubernetes)", where, d.Type))
	}
	if d.Refresh < 0 {
		errs = append(errs, fmt.Errorf("%s: refresh must not be negative", where))
	}
	return errs
}

// validateBackendTLS checks one backend_tls block. Structure and cross-field
// rules come from internal/backendtls, so configuration validation and the
// runtime resolver can never disagree about what is acceptable; this function
// adds the file-readability checks and the canonical path prefix.
//
// insecure_skip_verify is accepted here by design (ADR 0016 §8): a field whose
// only purpose is opting into an insecure mode cannot be a validation
// rejection, or it is unusable and the emergency path disappears. `jul lint`
// reports it as an error instead. Self-contradictory combinations are still
// hard errors, and those come from backendtls.Validate.
func validateBackendTLS(c *BackendTLSConfig, where string) []error {
	if c == nil {
		return nil
	}
	var errs []error
	for _, err := range backendtls.Validate(c.Options()) {
		errs = append(errs, fmt.Errorf("%s.%v", where, err))
	}
	for _, f := range []struct {
		field, path string
	}{
		{"ca_file", c.CAFile},
		{"client_cert", c.ClientCert},
		{"client_key", c.ClientKey},
	} {
		path := strings.TrimSpace(f.path)
		if path == "" || strings.HasPrefix(path, "-----BEGIN") {
			continue
		}
		if info, err := os.Stat(path); err != nil {
			errs = append(errs, fmt.Errorf("%s.%s: %q is not readable: %v", where, f.field, path, err))
		} else if info.IsDir() {
			errs = append(errs, fmt.Errorf("%s.%s: %q is a directory, want a PEM file", where, f.field, path))
		}
	}
	return errs
}

// locationUsesTLSBackend reports whether a location's backend is reached over
// TLS, so a backend_tls block that could never apply is rejected instead of
// silently doing nothing.
func locationUsesTLSBackend(loc LocationConfig) bool {
	if loc.GRPCTranscode != nil {
		return loc.GRPCTranscode.TLS
	}
	if loc.ProxyPass == "" {
		return false
	}
	u, err := url.Parse(loc.ProxyPass)
	if err != nil {
		return false
	}
	return u.Scheme == "https"
}

// validateUnixBackends rejects the combinations a unix-socket backend cannot
// satisfy.
//
// An HTTP probe needs a URL, and a unix socket has no host to put in one, so an
// http health check over a unix-socket backend could never run. Accepting it
// would leave the operator with a pool whose probes silently never succeed.
func validateUnixBackends(up UpstreamConfig, where string) []error {
	if up.HealthCheck == nil || !up.HealthCheck.Enabled || up.HealthCheck.Type != "http" {
		return nil
	}
	var errs []error
	for i, s := range up.Servers {
		if strings.HasPrefix(s.Address, "unix:") {
			errs = append(errs, fmt.Errorf("%s.servers[%d]: health_check.type = \"http\" cannot probe the unix socket %q; use type = \"tcp\"", where, i, s.Address))
		}
	}
	return errs
}
