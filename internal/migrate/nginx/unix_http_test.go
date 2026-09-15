// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"

	"jul/internal/config"
)

func TestTranslateNamedUnixHTTPUpstream(t *testing.T) {
	cfg, rep := translate(t, `
http {
  upstream app {
    server unix:/tmp/jul-import-app.sock;
  }
  server {
    listen 8080;
    location / {
      proxy_pass http://app;
    }
  }
}`)
	if len(rep.Skipped) != 0 {
		t.Fatalf("unexpected skipped directives: %+v", rep.Skipped)
	}
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 1 {
		t.Fatalf("upstreams = %+v", cfg.Upstreams)
	}
	if got := cfg.Upstreams[0].Servers[0].Address; got != "unix:/tmp/jul-import-app.sock" {
		t.Fatalf("unix address = %q", got)
	}
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 || cfg.Servers[0].Locations[0].ProxyPass != "http://app" {
		t.Fatalf("server/location = %+v", cfg.Servers)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("imported named Unix config should validate: %v", err)
	}
}

func TestTranslateDirectUnixProxyPassIsActionableFinding(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 8080;
    location / {
      proxy_pass http://unix:/tmp/jul-direct.sock;
    }
  }
}`)
	if len(rep.Skipped) == 0 {
		t.Fatal("direct Unix proxy_pass should be reported for manual mapping")
	}
	found := false
	for _, f := range rep.Skipped {
		if f.Name == "proxy_pass" && strings.Contains(f.Reason, "named [[upstreams]]") {
			found = true
		}
	}
	if !found {
		t.Fatalf("findings = %+v", rep.Skipped)
	}
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 0 {
		t.Fatalf("direct Unix location must not be emitted as malformed Jul config: %+v", cfg.Servers)
	}
}
