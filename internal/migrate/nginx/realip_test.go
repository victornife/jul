// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// hasFinding reports whether any skipped directive mentions both name and a
// fragment of the reason.
func hasFinding(rep *Report, name, fragment string) bool {
	for _, f := range rep.Skipped {
		if f.Name == name && strings.Contains(f.Reason, fragment) {
			return true
		}
	}
	return false
}

func TestTranslateRealIPToClientAddress(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    server_name app.example.com;
    set_real_ip_from 10.0.0.0/8;
    set_real_ip_from 2001:db8:100::/48;
    real_ip_header X-Forwarded-For;
    real_ip_recursive on;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`)
	srv := onlyServer(t, cfg)
	if srv.ClientAddress == nil {
		t.Fatal("no client_address policy was emitted")
	}
	if got := srv.ClientAddress.TrustedProxies; len(got) != 2 || got[0] != "10.0.0.0/8" || got[1] != "2001:db8:100::/48" {
		t.Errorf("trusted_proxies = %v", got)
	}
	if got := srv.ClientAddress.ForwardedHeaders; len(got) != 1 || got[0] != "x-forwarded-for" {
		t.Errorf("forwarded_headers = %v, want [x-forwarded-for]", got)
	}
	if !hasNote(rep, "right to left") {
		t.Errorf("real_ip_recursive on should be noted as already the default: %v", rep.Notes)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("translated config does not validate: %v", err)
	}
}

func TestTranslateRealIPInheritsFromHTTPBlock(t *testing.T) {
	cfg, _ := translate(t, `
http {
  set_real_ip_from 10.0.0.0/8;
  real_ip_header X-Forwarded-For;
  server {
    listen 80;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`)
	srv := onlyServer(t, cfg)
	if srv.ClientAddress == nil || len(srv.ClientAddress.TrustedProxies) != 1 {
		t.Fatalf("http-level realip was not inherited: %+v", srv.ClientAddress)
	}
}

func TestTranslateRealIPUnsupportedForms(t *testing.T) {
	tests := []struct {
		name      string
		directive string
		findName  string
		fragment  string
	}{
		{
			name:      "proxy protocol source",
			directive: "real_ip_header proxy_protocol;",
			findName:  "real_ip_header",
			fragment:  "PROXY-protocol",
		},
		{
			name:      "x-real-ip",
			directive: "real_ip_header X-Real-IP;",
			findName:  "real_ip_header",
			fragment:  "not supported",
		},
		{
			name:      "non-recursive evaluation",
			directive: "real_ip_recursive off;",
			findName:  "real_ip_recursive",
			fragment:  "right to left",
		},
		{
			name:      "unix socket peer",
			directive: "set_real_ip_from unix:;",
			findName:  "set_real_ip_from",
			fragment:  "unix-socket",
		},
		{
			name:      "hostname",
			directive: "set_real_ip_from proxy.example.com;",
			findName:  "set_real_ip_from",
			fragment:  "not an address",
		},
		{
			name:      "prefix with host bits set",
			directive: "set_real_ip_from 10.1.2.3/8;",
			findName:  "set_real_ip_from",
			fragment:  "canonical CIDR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, rep := translate(t, `
http {
  server {
    listen 80;
    set_real_ip_from 10.0.0.0/8;
    real_ip_header X-Forwarded-For;
    `+tt.directive+`
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`)
			if !hasFinding(rep, tt.findName, tt.fragment) {
				t.Fatalf("no finding for %q; skipped = %+v", tt.directive, rep.Skipped)
			}
			// An unsupported form must not degrade into a different policy:
			// nothing is emitted at all.
			if srv := onlyServer(t, cfg); srv.ClientAddress != nil {
				t.Fatalf("a blocked realip scope still emitted %+v", srv.ClientAddress)
			}
		})
	}
}

func TestTranslateRealIPWithoutHeaderIsReported(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    set_real_ip_from 10.0.0.0/8;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`)
	if srv := onlyServer(t, cfg); srv.ClientAddress != nil {
		t.Fatalf("nginx defaults real_ip_header to the unsupported X-Real-IP, so nothing should be emitted; got %+v", srv.ClientAddress)
	}
	if !hasFinding(rep, "real_ip_header", "X-Real-IP") {
		t.Fatalf("the defaulted header was not reported: %+v", rep.Skipped)
	}
}

func TestTranslateRealIPHoistsAcrossServerBlocks(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 443 ssl;
    server_name public.example.com;
    ssl_certificate /etc/ssl/pub.pem;
    ssl_certificate_key /etc/ssl/pub.key;
    set_real_ip_from 10.0.0.0/8;
    real_ip_header X-Forwarded-For;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
  server {
    listen 443 ssl;
    server_name internal.example.com;
    ssl_certificate /etc/ssl/int.pem;
    ssl_certificate_key /etc/ssl/int.key;
    location / { proxy_pass http://127.0.0.1:3001; }
  }
}`)
	if len(cfg.Servers) != 2 {
		t.Fatalf("want 2 servers, got %d", len(cfg.Servers))
	}
	for i, srv := range cfg.Servers {
		if srv.ClientAddress == nil || len(srv.ClientAddress.TrustedProxies) != 1 {
			t.Fatalf("server %d did not receive the hoisted policy: %+v", i, srv.ClientAddress)
		}
	}
	if !hasNote(rep, "applies to every block on that address") {
		t.Errorf("hoisting was not reported: %v", rep.Notes)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("hoisted config does not validate: %v", err)
	}
}

func TestTranslateRealIPConflictEmitsNoPolicy(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    server_name a.example.com;
    set_real_ip_from 10.0.0.0/8;
    real_ip_header X-Forwarded-For;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
  server {
    listen 80;
    server_name b.example.com;
    set_real_ip_from 192.168.0.0/16;
    real_ip_header X-Forwarded-For;
    location / { proxy_pass http://127.0.0.1:3001; }
  }
}`)
	for i, srv := range cfg.Servers {
		if srv.ClientAddress != nil {
			t.Fatalf("server %d kept a policy despite a conflict: %+v", i, srv.ClientAddress)
		}
	}
	if !hasFinding(rep, "set_real_ip_from", "different realip policies") {
		t.Fatalf("the conflict was not reported: %+v", rep.Skipped)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("config with a reported conflict does not validate: %v", err)
	}
}

func TestTranslateWithoutRealIPEmitsNoPolicy(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`)
	if srv := onlyServer(t, cfg); srv.ClientAddress != nil {
		t.Fatalf("a source without realip must never gain trust: %+v", srv.ClientAddress)
	}
}
