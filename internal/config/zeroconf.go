// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import "strings"

// DefaultZeroConfigListen is the listen address used by the zero-config
// synthesizers when the caller does not specify one. It is a high port so it
// works without elevated privileges during local development.
const DefaultZeroConfigListen = ":8080"

// ServeDir synthesizes an in-memory Config that serves the directory dir as
// static files on listen, with production-ready defaults (compression on,
// standard timeouts via applyDefaults). It is the backing for `jul run --serve`
// and the Console setup wizard, so no config file is required to get started.
func ServeDir(dir, listen string) *Config {
	if listen == "" {
		listen = DefaultZeroConfigListen
	}
	c := &Config{
		Servers: []ServerConfig{{
			Listen: listen,
			Locations: []LocationConfig{{
				Match: MatchConfig{Type: "prefix", Path: "/"},
				Root:  dir,
				Index: []string{"index.html"},
			}},
		}},
		Compression: CompressionConfig{Enabled: Bool(true)},
	}
	c.applyDefaults()
	return c
}

// ProxyTarget synthesizes an in-memory Config that reverse-proxies every request
// to target on listen. target may be a full URL ("http://host:port"), a
// host:port pair ("127.0.0.1:3000"), or a bare ":port" (proxied to loopback).
// It is the backing for `jul run --proxy`.
func ProxyTarget(target, listen string) *Config {
	if listen == "" {
		listen = DefaultZeroConfigListen
	}
	c := &Config{
		Servers: []ServerConfig{{
			Listen: listen,
			Locations: []LocationConfig{{
				Match:     MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: normalizeProxyTarget(target),
			}},
		}},
		Compression: CompressionConfig{Enabled: Bool(true)},
	}
	c.applyDefaults()
	return c
}

// normalizeProxyTarget turns a user-supplied proxy target into a proxy_pass URL
// that Validate accepts. A target that already carries a scheme is returned
// unchanged; a bare ":port" is bound to loopback; anything else gets an http://
// scheme prepended.
func normalizeProxyTarget(target string) string {
	t := strings.TrimSpace(target)
	switch {
	case t == "":
		return t
	case strings.Contains(t, "://"):
		return t
	case strings.HasPrefix(t, ":"):
		return "http://127.0.0.1" + t
	default:
		return "http://" + t
	}
}
