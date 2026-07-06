// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"

	"jul/internal/config"
)

// dnsDiscoverer resolves a host's A/AAAA records and attaches a fixed port to
// each address (DNS address records carry no port). It uses the system resolver.
type dnsDiscoverer struct {
	host     string
	port     string
	resolver *net.Resolver
}

func newDNSDiscoverer(cfg config.DiscoveryConfig) (Discoverer, error) {
	host, port, err := net.SplitHostPort(cfg.Target)
	if err != nil {
		return nil, fmt.Errorf("dns discovery target %q must be host:port: %w", cfg.Target, err)
	}
	if strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return nil, fmt.Errorf("dns discovery target %q must be host:port", cfg.Target)
	}
	return &dnsDiscoverer{host: host, port: port, resolver: net.DefaultResolver}, nil
}

func (d *dnsDiscoverer) Resolve(ctx context.Context) ([]Target, error) {
	addrs, err := d.resolver.LookupHost(ctx, d.host)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, Target{Address: net.JoinHostPort(a, d.port)})
	}
	return out, nil
}

func (d *dnsDiscoverer) Describe() string { return "dns:" + net.JoinHostPort(d.host, d.port) }

// dnsSRVDiscoverer resolves SRV records, which carry the target host, port, and
// weight directly. The configured target is the full SRV name (for example
// "_grpc._tcp.svc.example.com").
type dnsSRVDiscoverer struct {
	name     string
	resolver *net.Resolver
}

func newDNSSRVDiscoverer(cfg config.DiscoveryConfig) (Discoverer, error) {
	name := strings.TrimSpace(cfg.Target)
	if name == "" {
		return nil, fmt.Errorf("dns_srv discovery requires a target SRV name")
	}
	return &dnsSRVDiscoverer{name: name, resolver: net.DefaultResolver}, nil
}

func (d *dnsSRVDiscoverer) Resolve(ctx context.Context) ([]Target, error) {
	// Empty service and proto make LookupSRV resolve the name directly (the RFC
	// 2782 exception), so callers pass the complete "_svc._proto.host" SRV name.
	_, srvs, err := d.resolver.LookupSRV(ctx, "", "", d.name)
	if err != nil {
		return nil, err
	}
	out := make([]Target, 0, len(srvs))
	for _, s := range srvs {
		host := strings.TrimSuffix(s.Target, ".")
		if strings.TrimSpace(host) == "" {
			continue
		}
		out = append(out, Target{
			Address: net.JoinHostPort(host, strconv.Itoa(int(s.Port))),
			Weight:  int(s.Weight),
		})
	}
	return out, nil
}

func (d *dnsSRVDiscoverer) Describe() string { return "dns_srv:" + d.name }
