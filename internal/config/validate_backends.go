// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
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
