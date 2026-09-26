// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"os"
	"strings"
	"testing"
)

func hashConfig(strategy string, h *HashConfig, servers ...string) *Config {
	cfg := validKnownValueConfig()
	if len(servers) == 0 {
		servers = []string{"127.0.0.1:3000", "127.0.0.1:3001"}
	}
	srv := make([]UpstreamServer, len(servers))
	for i, s := range servers {
		srv[i] = UpstreamServer{Address: s, Weight: 1}
	}
	cfg.Upstreams = []UpstreamConfig{{Name: "api", Strategy: strategy, Hash: h, Servers: srv}}
	return cfg
}

func TestValidateAcceptsConsistentHash(t *testing.T) {
	for _, h := range []*HashConfig{
		{Key: "client_ip"},
		{Key: "client_ip", Fallback: "least_conn", Algorithm: "rendezvous_v1"},
		{Key: "header", Name: "X-Tenant"},
		{Key: "header", Name: "x-tenant-id", Fallback: "weighted_round_robin"},
		{Key: "cookie", Name: "session_id", Fallback: "round_robin"},
	} {
		if err := Validate(hashConfig("consistent_hash", h)); err != nil {
			t.Errorf("Validate(%+v): %v", *h, err)
		}
	}
	// Unix backends and weights are part of the supported surface.
	cfg := hashConfig("consistent_hash", &HashConfig{Key: "client_ip"}, "unix:/run/a.sock", "unix:/run/b.sock")
	cfg.Upstreams[0].Servers[1].Weight = 3
	if err := Validate(cfg); err != nil {
		t.Errorf("unix/weighted consistent_hash: %v", err)
	}
}

func TestValidateRejectsConsistentHash(t *testing.T) {
	cases := []struct {
		name     string
		strategy string
		hash     *HashConfig
		servers  []string
		want     string
	}{
		{"missing block", "consistent_hash", nil, nil, "requires an [upstreams.hash] block"},
		{"block without strategy", "round_robin", &HashConfig{Key: "client_ip"}, nil, "applies only to strategy"},
		{"block on default strategy", "", &HashConfig{Key: "client_ip"}, nil, `(strategy is "round_robin")`},
		{"no key", "consistent_hash", &HashConfig{}, nil, "hash.key: required"},
		{"unknown key", "consistent_hash", &HashConfig{Key: "uri"}, nil, `invalid key "uri"`},
		{"client_ip with name", "consistent_hash", &HashConfig{Key: "client_ip", Name: "X"}, nil, "must not be set"},
		{"header without name", "consistent_hash", &HashConfig{Key: "header"}, nil, "hash.name: required"},
		{"cookie without name", "consistent_hash", &HashConfig{Key: "cookie"}, nil, "hash.name: required"},
		{"header name not a token", "consistent_hash", &HashConfig{Key: "header", Name: "X Tenant"}, nil, "not a valid header name"},
		{"cookie name not a token", "consistent_hash", &HashConfig{Key: "cookie", Name: "a;b"}, nil, "not a valid cookie name"},
		{"name too long", "consistent_hash", &HashConfig{Key: "header", Name: strings.Repeat("x", hashNameMaxLen+1)}, nil, "longer than"},
		{"whole Cookie header", "consistent_hash", &HashConfig{Key: "header", Name: "cookie"}, nil, `use key = "cookie"`},
		{"bad fallback", "consistent_hash", &HashConfig{Key: "client_ip", Fallback: "consistent_hash"}, nil, "invalid fallback"},
		{"bad algorithm", "consistent_hash", &HashConfig{Key: "client_ip", Algorithm: "ketama"}, nil, "unsupported algorithm"},
		{"duplicate backend", "consistent_hash", &HashConfig{Key: "client_ip"}, []string{"10.0.0.1:80", "tcp://10.0.0.1:080"}, "are the same backend (10.0.0.1:80)"},
		{"duplicate ipv6 spelling", "consistent_hash", &HashConfig{Key: "client_ip"}, []string{"[2001:db8::1]:80", "[2001:DB8:0::1]:80"}, "are the same backend"},
		{"duplicate hostname case", "consistent_hash", &HashConfig{Key: "client_ip"}, []string{"App.local:80", "app.local.:80"}, "are the same backend"},
		{"duplicate unix", "consistent_hash", &HashConfig{Key: "client_ip"}, []string{"unix:/run/a.sock", "unix:/run/a.sock"}, "are the same backend"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := Validate(hashConfig(c.strategy, c.hash, c.servers...))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Validate = %v, want error containing %q", err, c.want)
			}
		})
	}
}

// Duplicate addresses stay legal for strategies that do not hash; the rule is
// about mapping determinism, not a new global constraint.
func TestDuplicateServersStillAllowedWithoutHash(t *testing.T) {
	if err := Validate(hashConfig("round_robin", nil, "10.0.0.1:80", "10.0.0.1:80")); err != nil {
		t.Fatalf("round_robin duplicates: %v", err)
	}
}

func TestValidateStreamHashKeyApplicability(t *testing.T) {
	for _, tc := range []struct {
		key, name string
		wantErr   bool
	}{
		{"client_ip", "", false},
		{"header", "X-Tenant", true},
		{"cookie", "sid", true},
	} {
		cfg := hashConfig("consistent_hash", &HashConfig{Key: tc.key, Name: tc.name})
		cfg.Streams = []StreamServer{{Listen: "127.0.0.1:5432", Protocol: "tcp", ProxyPass: "api"}}
		err := Validate(cfg)
		if tc.wantErr != (err != nil) {
			t.Fatalf("key %s on a stream route: err = %v, wantErr %v", tc.key, err, tc.wantErr)
		}
		if tc.wantErr && !strings.Contains(err.Error(), "which a stream route cannot read") {
			t.Fatalf("key %s: unexpected error %v", tc.key, err)
		}
	}
	// SNI routes are checked too.
	cfg := hashConfig("consistent_hash", &HashConfig{Key: "header", Name: "X-Tenant"})
	cfg.Streams = []StreamServer{{Listen: "127.0.0.1:8443", Protocol: "tcp", SNIRoutes: map[string]string{"a.example": "api"}}}
	if err := Validate(cfg); err == nil {
		t.Fatal("header-keyed upstream on an SNI route was accepted")
	}
}

func TestAffinityExampleIsValid(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/affinity.toml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	keys := map[string]bool{}
	for _, up := range cfg.Upstreams {
		if up.Strategy == "consistent_hash" && up.Hash != nil {
			keys[up.Hash.Key] = true
		}
	}
	if len(keys) != 3 {
		t.Fatalf("example covers key sources %v, want all three", keys)
	}
}

func TestConsistentHashRoundTripsThroughTOML(t *testing.T) {
	raw := `
[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://api"

[[upstreams]]
name = "api"
strategy = "consistent_hash"
servers = ["127.0.0.1:3000", "127.0.0.1:3001 weight=2"]
[upstreams.hash]
key = "cookie"
name = "sid"
fallback = "least_conn"
`
	cfg, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	clone, err := cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	h := clone.Upstreams[0].Hash
	if h == nil || h.Key != "cookie" || h.Name != "sid" || h.Fallback != "least_conn" {
		t.Fatalf("hash block did not round-trip: %+v", h)
	}
	if _, err := Parse([]byte(strings.Replace(raw, `fallback = "least_conn"`, `fallback = "least_conn"`+"\nexpression = \"$uri\"", 1))); err == nil {
		t.Fatal("unknown key in [upstreams.hash] was accepted by strict decoding")
	}
}
