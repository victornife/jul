// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"strings"
	"testing"
	"time"

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

func TestTranslateListenHTTP2CleartextEnablesH2C(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 8080 http2;
    location / { grpc_pass grpc://backend:9090; }
  }
}`)
	s := onlyServer(t, cfg)
	if !s.H2C {
		t.Error("expected H2C to be enabled for a cleartext http2 listener")
	}
}

func TestTranslateListenHTTP2OverTLSDoesNotEnableH2C(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 443 ssl http2;
    ssl_certificate /etc/ssl/cert.pem;
    ssl_certificate_key /etc/ssl/key.pem;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.H2C {
		t.Error("expected H2C to stay disabled for http2 negotiated via TLS ALPN")
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

func TestTranslateUpstreamConsistentMaxFailsAndFailTimeout(t *testing.T) {
	cfg, rep := translate(t, `
http {
  upstream pool {
    server a:80 max_fails=2 fail_timeout=5s;
    server b:80 max_fails=2 fail_timeout=5s;
  }
}`)
	u := cfg.Upstreams[0]
	if u.Resilience == nil {
		t.Fatalf("expected a Resilience block, got nil")
	}
	if u.Resilience.MaxFails != 2 {
		t.Errorf("MaxFails: got %d want 2", u.Resilience.MaxFails)
	}
	if u.Resilience.FailTimeout.Std() != 5*time.Second {
		t.Errorf("FailTimeout: got %s want 5s", u.Resilience.FailTimeout.Std())
	}
	for _, n := range rep.Notes {
		if strings.Contains(n, "declare different") {
			t.Errorf("unexpected disagreement note for consistent backends: %q", n)
		}
	}
}

func TestTranslateUpstreamInconsistentMaxFailsAndFailTimeoutKeepsDefault(t *testing.T) {
	cfg, rep := translate(t, `
http {
  upstream pool {
    server a:80 max_fails=2 fail_timeout=5s;
    server b:80 max_fails=5 fail_timeout=9s;
  }
}`)
	u := cfg.Upstreams[0]
	if u.Resilience != nil {
		t.Errorf("expected no Resilience block when backends disagree, got %+v", u.Resilience)
	}
	found := 0
	for _, n := range rep.Notes {
		if strings.Contains(n, "declare different") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("expected 2 disagreement notes (max_fails and fail_timeout), got %d: %+v", found, rep.Notes)
	}
}

func TestTranslateUpstreamFailTimeoutOnlyConsistent(t *testing.T) {
	cfg, _ := translate(t, `
http {
  upstream pool {
    server a:80 fail_timeout=5s;
    server b:80 fail_timeout=5s;
  }
}`)
	u := cfg.Upstreams[0]
	if u.Resilience == nil || u.Resilience.FailTimeout.Std() != 5*time.Second {
		t.Fatalf("expected FailTimeout=5s with no max_fails set, got %+v", u.Resilience)
	}
	if u.Resilience.MaxFails != 0 {
		t.Errorf("expected MaxFails unset (0), got %d", u.Resilience.MaxFails)
	}
}

func TestServerSpecFromParamsMaxFailsAndFailTimeout(t *testing.T) {
	s := serverSpecFromParams([]string{"10.0.0.1:80", "weight=2", "max_fails=3", "fail_timeout=15s"}, 12)
	if s.addr != "10.0.0.1:80" || s.weight != 2 {
		t.Fatalf("addr/weight: got %+v", s)
	}
	if !s.hasMaxFails || s.maxFails != 3 {
		t.Errorf("maxFails: got %+v", s)
	}
	if !s.hasFailTimeout || s.failTimeout.Std() != 15*time.Second {
		t.Errorf("failTimeout: got %+v", s)
	}
	down := serverSpecFromParams([]string{"10.0.0.1:80", "down", "max_fails=bad", "fail_timeout=bad"}, 1)
	if !down.down {
		t.Errorf("expected down=true, got %+v", down)
	}
	if down.hasMaxFails || down.hasFailTimeout {
		t.Errorf("malformed max_fails/fail_timeout must not set has*, got %+v", down)
	}
}

func TestTranslateCacheSingleZoneConsistent(t *testing.T) {
	cfg, rep := translate(t, `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m max_size=100m;
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_cache z;
      proxy_cache_valid 200 302 10m;
    }
  }
}`)
	if !cfg.Cache.Enabled || cfg.Cache.DiskPath != "/var/cache/jul" {
		t.Fatalf("cache: got %+v", cfg.Cache)
	}
	if cfg.Cache.DiskMaxSize != config.Size(100<<20) {
		t.Errorf("DiskMaxSize: got %d want %d", cfg.Cache.DiskMaxSize, config.Size(100<<20))
	}
	if cfg.Cache.DefaultTTL.Std() != 10*time.Minute {
		t.Errorf("DefaultTTL: got %s want 10m", cfg.Cache.DefaultTTL.Std())
	}
	if !onlyServer(t, cfg).Locations[0].Cache {
		t.Error("expected the location's cache toggle to be enabled")
	}
	if len(rep.Skipped) != 0 {
		t.Errorf("expected no skips, got %+v", rep.Skipped)
	}
}

func TestTranslateCacheProxyCacheValidAtServerLevel(t *testing.T) {
	cfg, _ := translate(t, `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 80;
    proxy_cache_valid 5m;
    location / {
      proxy_pass http://backend;
      proxy_cache z;
    }
  }
}`)
	if cfg.Cache.DefaultTTL.Std() != 5*time.Minute {
		t.Errorf("DefaultTTL: got %s want 5m (from the server-level proxy_cache_valid)", cfg.Cache.DefaultTTL.Std())
	}
}

func TestTranslateCacheUndeclaredZoneIsSkipped(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_cache undeclared;
    }
  }
}`)
	if cfg.Cache.Enabled {
		t.Fatalf("expected cache disabled, got %+v", cfg.Cache)
	}
	if onlyServer(t, cfg).Locations[0].Cache {
		t.Error("expected the location's cache toggle to stay disabled")
	}
	if !hasSkip(rep, "no matching proxy_cache_path declaration") {
		t.Errorf("expected an undeclared-zone skip, got %+v", rep.Skipped)
	}
}

func TestTranslateCacheOffAndDynamicAreIgnored(t *testing.T) {
	cfg, _ := translate(t, `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 80;
    location /off {
      proxy_pass http://backend;
      proxy_cache off;
    }
    location /dyn {
      proxy_pass http://backend;
      proxy_cache $cache_zone;
    }
  }
}`)
	for _, l := range onlyServer(t, cfg).Locations {
		if l.Cache {
			t.Errorf("location %s: expected cache disabled, got enabled", l.Match.Path)
		}
	}
}

func TestTranslateCacheDuplicateZoneDeclarationSkipped(t *testing.T) {
	_, rep := translate(t, `
http {
  proxy_cache_path /var/cache/a keys_zone=z:10m;
  proxy_cache_path /var/cache/b keys_zone=z:5m;
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_cache z;
    }
  }
}`)
	if !hasSkip(rep, "duplicate proxy_cache_path") {
		t.Errorf("expected a duplicate-zone skip, got %+v", rep.Skipped)
	}
}

func TestTranslateCacheInvalidZoneDeclarationSkipped(t *testing.T) {
	_, rep := translate(t, `
http {
  proxy_cache_path /var/cache/jul;
  server { listen 80; location / { proxy_pass http://backend; } }
}`)
	if !hasSkip(rep, "proxy_cache_path is missing a path or a valid keys_zone") {
		t.Errorf("expected an invalid-declaration skip, got %+v", rep.Skipped)
	}
}

func TestTranslateClientAuthOnConsistent(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 443 ssl;
    ssl_certificate /etc/ssl/server.pem;
    ssl_certificate_key /etc/ssl/server.key;
    ssl_verify_client on;
    ssl_client_certificate /etc/ssl/ca.pem;
    ssl_crl /etc/ssl/ca.crl;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.TLS == nil || s.TLS.ClientAuth == nil {
		t.Fatalf("expected a ClientAuth block, got %+v", s.TLS)
	}
	ca := s.TLS.ClientAuth
	if ca.Mode != "require" || ca.CAFile != "/etc/ssl/ca.pem" || ca.CRLFile != "/etc/ssl/ca.crl" {
		t.Errorf("ClientAuth: got %+v", ca)
	}
}

func TestTranslateClientAuthOptional(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 443 ssl;
    ssl_certificate /etc/ssl/server.pem;
    ssl_certificate_key /etc/ssl/server.key;
    ssl_verify_client optional;
    ssl_client_certificate /etc/ssl/ca.pem;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.TLS == nil || s.TLS.ClientAuth == nil || s.TLS.ClientAuth.Mode != "request" {
		t.Fatalf("expected ClientAuth.Mode=request, got %+v", s.TLS)
	}
}

func TestTranslateClientAuthMissingCAFileSkipped(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 443 ssl;
    ssl_certificate /etc/ssl/server.pem;
    ssl_certificate_key /etc/ssl/server.key;
    ssl_verify_client on;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.TLS != nil && s.TLS.ClientAuth != nil {
		t.Fatalf("expected no ClientAuth without ssl_client_certificate, got %+v", s.TLS.ClientAuth)
	}
	if !hasSkip(rep, "requires a non-empty ssl_client_certificate") {
		t.Errorf("expected a skip finding, got %+v", rep.Skipped)
	}
}

func TestTranslateClientAuthOptionalNoCASkipped(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 443 ssl;
    ssl_certificate /etc/ssl/server.pem;
    ssl_certificate_key /etc/ssl/server.key;
    ssl_verify_client optional_no_ca;
    ssl_client_certificate /etc/ssl/ca.pem;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.TLS != nil && s.TLS.ClientAuth != nil {
		t.Fatalf("expected no ClientAuth for optional_no_ca, got %+v", s.TLS.ClientAuth)
	}
	if !hasSkip(rep, "optional_no_ca accepts a client certificate without validating it against any CA") {
		t.Errorf("expected an optional_no_ca skip, got %+v", rep.Skipped)
	}
}

func TestTranslateClientAuthUnrecognizedValueSkipped(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 443 ssl;
    ssl_certificate /etc/ssl/server.pem;
    ssl_certificate_key /etc/ssl/server.key;
    ssl_verify_client maybe;
    ssl_client_certificate /etc/ssl/ca.pem;
    location / { return 200; }
  }
}`)
	if !hasSkip(rep, "ssl_verify_client value is not recognized") {
		t.Errorf("expected an unrecognized-value skip, got %+v", rep.Skipped)
	}
}

func TestTranslateClientAuthOffAndTuningKnobsIgnored(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 443 ssl;
    ssl_certificate /etc/ssl/server.pem;
    ssl_certificate_key /etc/ssl/server.key;
    ssl_verify_client off;
    ssl_verify_depth 2;
    ssl_trusted_certificate /etc/ssl/trust.pem;
    location / { return 200; }
  }
}`)
	s := onlyServer(t, cfg)
	if s.TLS == nil {
		t.Fatal("expected TLS to still be enabled")
	}
	if s.TLS.ClientAuth != nil {
		t.Errorf("expected no ClientAuth for ssl_verify_client off, got %+v", s.TLS.ClientAuth)
	}
}

func TestTranslateCacheHTTPLevelProxyCacheValid(t *testing.T) {
	cfg, _ := translate(t, `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  proxy_cache_valid 15m;
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_cache z;
    }
  }
}`)
	if cfg.Cache.DefaultTTL.Std() != 15*time.Minute {
		t.Errorf("DefaultTTL: got %s want 15m (from the http-level proxy_cache_valid)", cfg.Cache.DefaultTTL.Std())
	}
}

func TestTranslateCacheBareProxyCacheAndInvalidValidAreIgnoredByCollectors(t *testing.T) {
	// A bare proxy_cache with no argument and an unrepresentable
	// proxy_cache_valid form must not panic or register as a use/TTL
	// candidate; the location's own directive loop still reports the bare
	// proxy_cache generically since it has no zone name to act on.
	cfg, _ := translate(t, `
http {
  proxy_cache_path /var/cache/jul keys_zone=z:10m;
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_cache;
      proxy_cache_valid any 1m;
    }
  }
}`)
	if cfg.Cache.Enabled {
		t.Fatalf("expected cache disabled (no valid proxy_cache use), got %+v", cfg.Cache)
	}
	if onlyServer(t, cfg).Locations[0].Cache {
		t.Error("expected the location's cache toggle to stay disabled")
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
	if !cfg.Compression.IsEnabled() {
		t.Error("expected compression to be enabled by `gzip on`")
	}
}

func TestTranslateStreamBasicTCP(t *testing.T) {
	cfg, rep := translate(t, `
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
  }
}`)
	if len(cfg.Streams) != 1 {
		t.Fatalf("want 1 stream, got %d: %+v", len(cfg.Streams), cfg.Streams)
	}
	st := cfg.Streams[0]
	if st.Listen != ":5353" || st.ProxyPass != "127.0.0.1:6000" || st.Protocol != "" {
		t.Errorf("unexpected stream: %+v", st)
	}
	if rep.Streams != 1 {
		t.Errorf("report.Streams = %d, want 1", rep.Streams)
	}
}

func TestTranslateMailModuleSkipped(t *testing.T) {
	_, rep := translate(t, `
mail {
  server { listen 25; }
}`)
	if !hasSkip(rep, "mail") {
		t.Errorf("expected a skip for the mail module, got %+v", rep.Skipped)
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

func TestTranslateProxyPassRetainedPathWarnsAboutPrependSemantics(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location /api { proxy_pass http://backend/v2; }
  }
}`)
	s := onlyServer(t, cfg)
	if got := s.Locations[0].ProxyPass; got != "http://backend/v2" {
		t.Errorf("proxy_pass: got %q want http://backend/v2", got)
	}
	if !hasNote(rep, "prepends it to the client's full incoming request path") {
		t.Errorf("expected a prepend-semantics note, notes=%v", rep.Notes)
	}
}

func TestTranslateLocationProxyTimeouts(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_connect_timeout 2s;
      proxy_read_timeout 5s;
      proxy_send_timeout 7s;
    }
  }
}`)
	l := onlyServer(t, cfg).Locations[0]
	if l.ProxyConnectTimeout.Std() != 2*time.Second {
		t.Errorf("ProxyConnectTimeout: got %s want 2s", l.ProxyConnectTimeout.Std())
	}
	if l.ProxyReadTimeout.Std() != 5*time.Second {
		t.Errorf("ProxyReadTimeout: got %s want 5s", l.ProxyReadTimeout.Std())
	}
	if l.ProxySendTimeout.Std() != 7*time.Second {
		t.Errorf("ProxySendTimeout: got %s want 7s", l.ProxySendTimeout.Std())
	}
}

func TestTranslateLocationProxyTimeoutsMalformedAreSkipped(t *testing.T) {
	for _, tt := range []struct {
		directive string
		skipWant  string
	}{
		{"proxy_connect_timeout", "proxy_connect_timeout is not a representable duration"},
		{"proxy_read_timeout", "proxy_read_timeout is not a representable duration"},
		{"proxy_send_timeout", "proxy_send_timeout is not a representable duration"},
	} {
		t.Run(tt.directive, func(t *testing.T) {
			_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      `+tt.directive+` nope;
    }
  }
}`)
			if !hasSkip(rep, tt.skipWant) {
				t.Errorf("expected a skip finding, got %+v", rep.Skipped)
			}
		})
	}
}

func TestTranslateProxyNextUpstreamTriesExplicitBound(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_next_upstream_tries 3;
    }
  }
}`)
	l := onlyServer(t, cfg).Locations[0]
	if l.Resilience == nil || l.Resilience.RetryAttempts != 2 {
		t.Errorf("Resilience: got %+v want RetryAttempts=2", l.Resilience)
	}
}

func TestTranslateProxyNextUpstreamTriesAmbiguousFormsSkipped(t *testing.T) {
	for _, n := range []string{"0", "1"} {
		cfg, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      proxy_pass http://backend;
      proxy_next_upstream_tries `+n+`;
    }
  }
}`)
		l := onlyServer(t, cfg).Locations[0]
		if l.Resilience != nil {
			t.Errorf("tries=%s: expected no Resilience block, got %+v", n, l.Resilience)
		}
		if !hasSkip(rep, "proxy_next_upstream_tries") {
			t.Errorf("tries=%s: expected a skip finding, got %+v", n, rep.Skipped)
		}
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

func TestTranslateGRPCPassSchemes(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location /a { grpc_pass grpc://backend:9090; }
    location /b { grpc_pass grpcs://backend:9443; }
    location /c { grpc_pass grpc_pool; }
  }
}`)
	s := onlyServer(t, cfg)
	if len(s.Locations) != 3 {
		t.Fatalf("want 3 locations, got %d", len(s.Locations))
	}
	if got := s.Locations[0]; got.ProxyPass != "http://backend:9090" || !got.GRPC {
		t.Errorf("grpc:// location: got proxy_pass=%q grpc=%v", got.ProxyPass, got.GRPC)
	}
	if got := s.Locations[1]; got.ProxyPass != "https://backend:9443" || !got.GRPC {
		t.Errorf("grpcs:// location: got proxy_pass=%q grpc=%v", got.ProxyPass, got.GRPC)
	}
	if got := s.Locations[2]; got.ProxyPass != "http://grpc_pool" || !got.GRPC {
		t.Errorf("bare upstream location: got proxy_pass=%q grpc=%v", got.ProxyPass, got.GRPC)
	}
}

func TestTranslateGRPCPassUnrecognizedFormsSkipped(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location /a { grpc_pass https://backend; }
    location /b { grpc_pass grpc://unix:/run/grpc.sock; }
    location /c { grpc_pass grpc://$backend; }
    location /d { grpc_pass; }
  }
}`)
	if !hasSkip(rep, "grpc_pass scheme is not representable") {
		t.Errorf("expected unrecognized-scheme skip, got %+v", rep.Skipped)
	}
	if !hasSkip(rep, "direct Unix grpc_pass is not representable") {
		t.Errorf("expected direct-Unix skip, got %+v", rep.Skipped)
	}
	if !hasSkip(rep, "variable-derived grpc_pass targets are not translated") {
		t.Errorf("expected variable-derived skip, got %+v", rep.Skipped)
	}
	if !hasSkip(rep, "grpc_pass target is missing") {
		t.Errorf("expected missing-target skip, got %+v", rep.Skipped)
	}
}

func TestTranslateUWSGIPass(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / { uwsgi_pass 127.0.0.1:3031; }
  }
}`)
	s := onlyServer(t, cfg)
	if got := s.Locations[0].UWSGIPass; got != "127.0.0.1:3031" {
		t.Errorf("uwsgi_pass: got %q want 127.0.0.1:3031", got)
	}
}

func TestTranslateUWSGIParamSkipped(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      uwsgi_pass 127.0.0.1:3031;
      uwsgi_param UWSGI_SCRIPT app;
    }
  }
}`)
	if !hasSkip(rep, "Jul has no per-parameter uWSGI configuration equivalent") {
		t.Errorf("expected uwsgi_param skip, got %+v", rep.Skipped)
	}
}

func TestTranslateFastCGIParamLiteral(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80;
    location / {
      fastcgi_pass 127.0.0.1:9000;
      fastcgi_param SCRIPT_NAME /index.php;
      fastcgi_param PATH_INFO $fastcgi_path_info;
    }
  }
}`)
	s := onlyServer(t, cfg)
	loc := s.Locations[0]
	if got := loc.FastCGIParams["SCRIPT_NAME"]; got != "/index.php" {
		t.Errorf("fastcgi_params[SCRIPT_NAME]: got %q want /index.php", got)
	}
	if _, ok := loc.FastCGIParams["PATH_INFO"]; ok {
		t.Errorf("variable-derived fastcgi_param PATH_INFO should not be translated, got %+v", loc.FastCGIParams)
	}
}

func TestTranslateFastCGIParamMissingValueSkipped(t *testing.T) {
	_, rep := translate(t, `
http {
  server {
    listen 80;
    location / {
      fastcgi_pass 127.0.0.1:9000;
      fastcgi_param SCRIPT_NAME;
    }
  }
}`)
	if !hasSkip(rep, "fastcgi_param requires a name and a value") {
		t.Errorf("expected missing-value skip, got %+v", rep.Skipped)
	}
}
