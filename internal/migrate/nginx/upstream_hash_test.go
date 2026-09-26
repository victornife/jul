// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"

	"jul/internal/config"
)

func TestParseNginxHashKey(t *testing.T) {
	cases := []struct {
		params     []string
		key, name  string
		consistent bool
		blocked    string
	}{
		{params: []string{"$remote_addr"}, key: "client_ip"},
		{params: []string{"$binary_remote_addr", "consistent"}, key: "client_ip", consistent: true},
		{params: []string{"$http_x_tenant", "consistent"}, key: "header", name: "x-tenant", consistent: true},
		{params: []string{"$cookie_session_id"}, key: "cookie", name: "session_id"},
		{params: nil, blocked: "requires a key"},
		{params: []string{"$remote_addr", "ketama"}, blocked: "unsupported hash parameters"},
		{params: []string{"$remote_addr", "consistent", "x"}, blocked: "unsupported hash parameters"},
		{params: []string{"$request_uri"}, blocked: "not a single"},
		{params: []string{"$host$uri"}, blocked: "not a single"},
		{params: []string{"$http_"}, blocked: "not a single"},
		{params: []string{"$http_x${uri}"}, blocked: "not a single"},
		{params: []string{"$cookie_"}, blocked: "not a single"},
		{params: []string{"static-key"}, blocked: "not a single"},
		{params: []string{"$arg_session"}, blocked: "not a single"},
	}
	for _, c := range cases {
		got := parseNginxHashKey(c.params)
		if c.blocked != "" {
			if !strings.Contains(got.reason, c.blocked) {
				t.Errorf("%v: reason %q, want %q", c.params, got.reason, c.blocked)
			}
			continue
		}
		if got.reason != "" || got.key != c.key || got.name != c.name || got.consistent != c.consistent {
			t.Errorf("%v = %+v", c.params, got)
		}
	}
}

func TestClassifyUpstreamHash(t *testing.T) {
	ok := classifyUpstreamHash([]string{"$cookie_sid", "consistent"})
	if ok.code != "NGX_UPSTREAM_HASH" || ok.class != AssessmentApproximated || !strings.Contains(ok.message, "cookie sid") || !strings.Contains(ok.message, "ketama") {
		t.Fatalf("cookie hash = %+v", ok)
	}
	modular := classifyUpstreamHash([]string{"$remote_addr"})
	if !strings.Contains(modular.message, "modular hashing") {
		t.Fatalf("non-consistent hash message = %q", modular.message)
	}
	bad := classifyUpstreamHash([]string{"$request_uri", "consistent"})
	if bad.code != "NGX_UPSTREAM_HASH_KEY" || bad.class != AssessmentBlocking || len(bad.targetPaths) != 2 {
		t.Fatalf("uri hash = %+v", bad)
	}
}

func TestTranslateUpstreamHashAndIPHash(t *testing.T) {
	cfg, report := translate(t, `
http {
  upstream by_ip {
    ip_hash;
    server 10.0.0.1:80 weight=2;
    server 10.0.0.2:80;
    server 10.0.0.3:80 down;
  }
  upstream by_tenant {
    hash $http_x_tenant consistent;
    server 10.0.1.1:80;
    server 10.0.1.2:80;
  }
  upstream by_cookie {
    hash $cookie_sid;
    server 10.0.2.1:80;
  }
  upstream by_uri {
    hash $request_uri consistent;
    server 10.0.3.1:80;
  }
  upstream last_wins {
    ip_hash;
    least_conn;
    server 10.0.4.1:80;
  }
  server {
    listen 8080;
    location /a { proxy_pass http://by_ip; }
    location /b { proxy_pass http://by_tenant; }
    location /c { proxy_pass http://by_cookie; }
    location /d { proxy_pass http://by_uri; }
    location /e { proxy_pass http://last_wins; }
  }
}`)
	ups := map[string]config.UpstreamConfig{}
	for _, u := range cfg.Upstreams {
		ups[u.Name] = u
	}
	check := func(name, strategy string, hash *config.HashConfig) {
		t.Helper()
		u := ups[name]
		if u.Strategy != strategy {
			t.Errorf("%s strategy = %q, want %q", name, u.Strategy, strategy)
		}
		switch {
		case hash == nil && u.Hash != nil:
			t.Errorf("%s has hash %+v, want none", name, u.Hash)
		case hash != nil && (u.Hash == nil || *u.Hash != *hash):
			t.Errorf("%s hash = %+v, want %+v", name, u.Hash, hash)
		}
	}
	check("by_ip", "consistent_hash", &config.HashConfig{Key: "client_ip"})
	check("by_tenant", "consistent_hash", &config.HashConfig{Key: "header", Name: "x-tenant"})
	check("by_cookie", "consistent_hash", &config.HashConfig{Key: "cookie", Name: "sid"})
	check("by_uri", "round_robin", nil)
	check("last_wins", "least_conn", nil)
	if len(ups["by_ip"].Servers) != 2 || ups["by_ip"].Servers[0].Weight != 2 {
		t.Errorf("ip_hash weights/down not preserved: %+v", ups["by_ip"].Servers)
	}
	notes := strings.Join(report.Notes, "\n")
	for _, want := range []string{"ip_hash translated to consistent_hash on client_ip", "hash translated to consistent_hash on header", "is not a single client-address"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	// The generated candidate for the representable upstreams validates.
	cfg.Upstreams = []config.UpstreamConfig{ups["by_ip"], ups["by_tenant"], ups["by_cookie"]}
	cfg.Servers[0].Locations = cfg.Servers[0].Locations[:3]
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("candidate invalid: %v", err)
	}
}
