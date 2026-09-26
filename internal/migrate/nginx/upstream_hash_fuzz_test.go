// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// FuzzParseNginxHashKey checks that any NGINX hash parameter list either maps
// onto exactly one closed key source that config validation accepts, or is
// refused with a reason — never a half-translated key.
func FuzzParseNginxHashKey(f *testing.F) {
	for _, s := range []string{"$remote_addr", "$http_x_tenant consistent", "$cookie_sid", "$request_uri", "$http_", "a b c", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		k := parseNginxHashKey(strings.Fields(raw))
		if k.reason != "" {
			if k.key != "" && k.key != config.HashKeyClientIP && k.key != config.HashKeyHeader && k.key != config.HashKeyCookie {
				t.Fatalf("refused key %q still carries kind %q", raw, k.key)
			}
			return
		}
		up := config.UpstreamConfig{
			Name: "u", Strategy: "consistent_hash", Hash: &config.HashConfig{Key: k.key, Name: k.name},
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		}
		cfg := &config.Config{
			Servers:   []config.ServerConfig{{Listen: "127.0.0.1:8080", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://u"}}}},
			Upstreams: []config.UpstreamConfig{up},
		}
		if err := config.Validate(cfg); err != nil {
			t.Fatalf("accepted NGINX key %q produced an invalid Jul hash block %+v: %v", raw, *up.Hash, err)
		}
	})
}
