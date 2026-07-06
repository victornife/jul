// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// translate parses nginx text and translates it, failing the test on a parse
// error. It returns the translated config and report for assertions.
func translate(t *testing.T, src string) (*config.Config, *Report) {
	t.Helper()
	parsed, err := parseString(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	cfg, rep := Translate(parsed, "test.conf")
	return cfg, rep
}

// onlyServer returns the single server from a translated config, failing if
// there is not exactly one.
func onlyServer(t *testing.T, cfg *config.Config) config.ServerConfig {
	t.Helper()
	if len(cfg.Servers) != 1 {
		t.Fatalf("want 1 server, got %d", len(cfg.Servers))
	}
	return cfg.Servers[0]
}

func TestTranslateListenAndServerName(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 8080;
    server_name example.com www.example.com _;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.Listen != ":8080" {
		t.Errorf("listen: got %q want :8080", s.Listen)
	}
	// The "_" catch-all is dropped; the two real names remain.
	if got := strings.Join(s.ServerNames, ","); got != "example.com,www.example.com" {
		t.Errorf("server_names: got %q", got)
	}
}

func TestTranslateListenSSLImpliesTLS(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 443 ssl;
    server_name secure.example.com;
    ssl_certificate /etc/ssl/cert.pem;
    ssl_certificate_key /etc/ssl/key.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.Listen != ":443" {
		t.Errorf("listen: got %q want :443", s.Listen)
	}
	if s.TLS == nil {
		t.Fatal("expected TLS to be set")
	}
	if !s.TLS.Enabled {
		t.Error("expected TLS.Enabled")
	}
	if s.TLS.Cert != "/etc/ssl/cert.pem" || s.TLS.Key != "/etc/ssl/key.pem" {
		t.Errorf("cert/key: got %q / %q", s.TLS.Cert, s.TLS.Key)
	}
	if s.TLS.MinVersion != "1.2" {
		t.Errorf("min_version: got %q want 1.2", s.TLS.MinVersion)
	}
}

func TestTranslateLocationModifiers(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location = /exact { return 204; }
    location ^~ /prefix { return 204; }
    location ~ \.php$ { return 204; }
    location ~* \.JPG$ { return 204; }
    location /plain { return 204; }
  }
}`)
	s := onlyServer(t, cfg)
	want := map[string]string{
		"/exact":  "exact",
		"/prefix": "prefix",
		`\.php$`:  "regex",
		`\.JPG$`:  "regex",
		"/plain":  "prefix",
	}
	if len(s.Locations) != len(want) {
		t.Fatalf("want %d locations, got %d", len(want), len(s.Locations))
	}
	for _, l := range s.Locations {
		wt, ok := want[l.Match.Path]
		if !ok {
			t.Errorf("unexpected location path %q", l.Match.Path)
			continue
		}
		if l.Match.Type != wt {
			t.Errorf("location %q: got type %q want %q", l.Match.Path, l.Match.Type, wt)
		}
	}
}

func TestTranslateNamedLocationSkipped(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / { return 200; }
    location @fallback { return 404; }
  }
}`)
	s := onlyServer(t, cfg)
	for _, l := range s.Locations {
		if strings.HasPrefix(l.Match.Path, "@") {
			t.Errorf("named location should not be translated: %q", l.Match.Path)
		}
	}
	if !hasSkip(rep, "named location") {
		t.Errorf("expected a skip note for the named location, got %+v", rep.Skipped)
	}
}

func TestTranslateProxyPassWithUpstream(t *testing.T) {
	cfg, _ := translate(t, `
http {
  upstream backend {
    server 10.0.0.1:8080 weight=3;
    server 10.0.0.2:8080;
  }
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
    }
  }
}`)
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("want 1 upstream, got %d", len(cfg.Upstreams))
	}
	u := cfg.Upstreams[0]
	if u.Name != "backend" {
		t.Errorf("upstream name: got %q", u.Name)
	}
	// A weight > 1 should switch the default strategy to weighted_round_robin.
	if u.Strategy != "weighted_round_robin" {
		t.Errorf("strategy: got %q want weighted_round_robin", u.Strategy)
	}
	if len(u.Servers) != 2 {
		t.Fatalf("want 2 upstream servers, got %d", len(u.Servers))
	}
	if u.Servers[0].Address != "10.0.0.1:8080" || u.Servers[0].Weight != 3 {
		t.Errorf("server[0]: got %+v", u.Servers[0])
	}
	s := onlyServer(t, cfg)
	if got := s.Locations[0].ProxyPass; got != "http://backend" {
		t.Errorf("proxy_pass: got %q want http://backend", got)
	}
}

func TestTranslateProxyPassBareHostGetsScheme(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / { proxy_pass 127.0.0.1:9000; }
  }
}`)
	s := onlyServer(t, cfg)
	if got := s.Locations[0].ProxyPass; got != "http://127.0.0.1:9000" {
		t.Errorf("proxy_pass: got %q want http://127.0.0.1:9000", got)
	}
}

func TestTranslateLeastConn(t *testing.T) {
	cfg, _ := translate(t, `
http {
  upstream api {
    least_conn;
    server a:80;
    server b:80;
  }
}`)
	if len(cfg.Upstreams) != 1 {
		t.Fatalf("want 1 upstream, got %d", len(cfg.Upstreams))
	}
	if cfg.Upstreams[0].Strategy != "least_conn" {
		t.Errorf("strategy: got %q want least_conn", cfg.Upstreams[0].Strategy)
	}
}

func TestTranslateUpstreamDownServerOmitted(t *testing.T) {
	cfg, rep := translate(t, `
http {
  upstream pool {
    server a:80;
    server b:80 down;
  }
}`)
	u := cfg.Upstreams[0]
	if len(u.Servers) != 1 || u.Servers[0].Address != "a:80" {
		t.Errorf("expected only a:80, got %+v", u.Servers)
	}
	if len(rep.Notes) == 0 {
		t.Error("expected a note about the omitted down server")
	}
}

func TestTranslateReturnRedirect(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location /old {
      return 301 https://new.example.com/;
    }
    location /teapot {
      return 418;
    }
  }
}`)
	s := onlyServer(t, cfg)
	byPath := map[string]config.LocationConfig{}
	for _, l := range s.Locations {
		byPath[l.Match.Path] = l
	}
	if l := byPath["/old"]; l.Return != 301 || l.Redirect != "https://new.example.com/" {
		t.Errorf("/old: got return=%d redirect=%q", l.Return, l.Redirect)
	}
	if l := byPath["/teapot"]; l.Return != 418 {
		t.Errorf("/teapot: got return=%d want 418", l.Return)
	}
}

func TestTranslateRewrite(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / {
      rewrite ^/old/(.*)$ /new/$1 permanent;
      root /var/www;
    }
  }
}`)
	s := onlyServer(t, cfg)
	l := s.Locations[0]
	if len(l.Rewrites) != 1 {
		t.Fatalf("want 1 rewrite, got %d", len(l.Rewrites))
	}
	rw := l.Rewrites[0]
	if rw.Pattern != "^/old/(.*)$" || rw.Replacement != "/new/$1" || rw.Flag != "permanent" {
		t.Errorf("rewrite: got %+v", rw)
	}
}

func TestTranslateServerRootSynthesizesLocation(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    root /var/www/html;
    index index.html;
    location /api { proxy_pass http://127.0.0.1:9000; }
  }
}`)
	s := onlyServer(t, cfg)
	var hasRoot bool
	for _, l := range s.Locations {
		if l.Match.Path == "/" && l.Root == "/var/www/html" {
			hasRoot = true
			if len(l.Index) != 1 || l.Index[0] != "index.html" {
				t.Errorf("synthesized index: got %v", l.Index)
			}
		}
	}
	if !hasRoot {
		t.Errorf("expected a synthesized / location serving the server root, got %+v", s.Locations)
	}
}

func TestTranslateGzipEnablesCompression(t *testing.T) {
	cfg, _ := translate(t, `
http {
  gzip on;
  server { listen 80; location / { return 200; } }
}`)
	if !cfg.Compression.Enabled {
		t.Error("expected compression to be enabled by `gzip on`")
	}
}

func TestTranslateStreamModuleSkipped(t *testing.T) {
	_, rep := translate(t, `
stream {
  server { listen 5353; }
}`)
	if !hasSkip(rep, "stream") {
		t.Errorf("expected a skip for the stream module, got %+v", rep.Skipped)
	}
}

func TestTranslateUnknownDirectiveReported(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      add_header X-Frame-Options DENY;
      return 200;
    }
  }
}`)
	if !hasSkip(rep, "add_header") {
		t.Errorf("expected add_header to be reported, got %+v", rep.Skipped)
	}
}

// Round-trip integration tests: a representative config must translate to a
// config that marshals and then parses+validates cleanly, exactly as the server
// would load it.

func TestTranslateRoundTripStaticSite(t *testing.T) {
	assertRoundTrips(t, `
http {
  server {
    listen 80;
    server_name static.example.com;
    root /var/www/site;
    index index.html index.htm;
    location / {
      try_files $uri $uri/ =404;
    }
    location ~* \.(jpg|png|css|js)$ {
      root /var/www/assets;
    }
  }
}`)
}

func TestTranslateRoundTripReverseProxy(t *testing.T) {
	assertRoundTrips(t, `
http {
  upstream app {
    server 10.0.0.1:8080 weight=2;
    server 10.0.0.2:8080;
    least_conn;
  }
  server {
    listen 443 ssl;
    server_name app.example.com;
    ssl_certificate /etc/ssl/app.pem;
    ssl_certificate_key /etc/ssl/app.key;
    ssl_protocols TLSv1.2 TLSv1.3;
    location / {
      proxy_pass http://app;
    }
    location /static {
      root /var/www;
    }
  }
}`)
}

// assertRoundTrips translates src, marshals it, then parses and validates the
// result, failing on any error.
func assertRoundTrips(t *testing.T, src string) {
	t.Helper()
	cfg, _ := translate(t, src)
	toml, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	loaded, err := config.Parse(toml)
	if err != nil {
		t.Fatalf("re-parse:\n%s\nerror: %v", toml, err)
	}
	if err := config.Validate(loaded); err != nil {
		t.Fatalf("validate:\n%s\nerror: %v", toml, err)
	}
}

func hasSkip(rep *Report, substr string) bool {
	for _, f := range rep.Skipped {
		if strings.Contains(f.Reason, substr) || strings.Contains(f.Name, substr) {
			return true
		}
	}
	return false
}

func hasNote(rep *Report, substr string) bool {
	for _, n := range rep.Notes {
		if strings.Contains(n, substr) {
			return true
		}
	}
	return false
}

func TestTranslateProxyPassTrailingSlashWarns(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / { proxy_pass http://backend/; }
  }
}`)
	s := onlyServer(t, cfg)
	if got := s.Locations[0].ProxyPass; got != "http://backend" {
		t.Errorf("proxy_pass: got %q want http://backend", got)
	}
	if !hasNote(rep, "trailing slash dropped") {
		t.Errorf("expected trailing-slash note, notes=%v", rep.Notes)
	}
}

func TestTranslateExtraListenDropped(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    listen 8080;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.Listen != ":80" {
		t.Errorf("listen: got %q want :80", s.Listen)
	}
	if !hasNote(rep, "extra listen") {
		t.Errorf("expected extra-listen note, notes=%v", rep.Notes)
	}
}

func TestTranslateServerReturnPrecedenceWarns(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    return 403;
    location /api { proxy_pass http://backend; }
  }
}`)
	if !hasNote(rep, "before locations") {
		t.Errorf("expected server-return precedence note, notes=%v", rep.Notes)
	}
}
