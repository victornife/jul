// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"net/url"
	"strings"
)

func validateWebSocketUpstreamProtocol(up UpstreamConfig, where string) []error {
	protocol := strings.TrimSpace(up.WebSocketUpstreamProtocol)
	switch protocol {
	case "", "http1":
		return nil
	case "http3":
		for _, server := range up.Servers {
			if strings.HasPrefix(server.Address, "unix:") {
				return []error{fmt.Errorf("%s.websocket_upstream_protocol: http3 requires UDP backends, not Unix sockets", where)}
			}
		}
		return nil
	default:
		return []error{fmt.Errorf("%s.websocket_upstream_protocol: unsupported value %q (want %q or %q)", where, protocol, "http1", "http3")}
	}
}

func validateWebSocketUpstreamConsumers(c *Config) []error {
	http3Pools := make(map[string]bool)
	for _, up := range c.Upstreams {
		if strings.TrimSpace(up.WebSocketUpstreamProtocol) == "http3" {
			http3Pools[up.Name] = true
		}
	}
	var errs []error
	for i, server := range c.Servers {
		for j, loc := range server.Locations {
			if loc.GRPC || loc.ProxyPass == "" {
				continue
			}
			target, err := url.Parse(loc.ProxyPass)
			if err == nil && http3Pools[target.Host] && target.Scheme != "https" {
				errs = append(errs, fmt.Errorf("servers[%d].locations[%d].proxy_pass: upstream %q selects http3 WebSockets and requires an https:// proxy_pass", i, j, target.Host))
			}
		}
	}
	return errs
}
