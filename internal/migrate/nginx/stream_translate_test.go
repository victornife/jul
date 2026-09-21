// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"testing"

	"jul/internal/config"
)

func TestTranslateStreamIncludeAndUnknownTopLevelBlocked(t *testing.T) {
	_, rep := translate(t, `
stream {
  include conf.d/*.conf;
}`)
	if !hasSkip(rep, "include not followed") {
		t.Errorf("expected stream include to be skipped, got %+v", rep.Skipped)
	}

	_, rep = translate(t, `
stream {
  resolver 8.8.8.8;
}`)
	if !hasFinding(rep, "resolver", "unsupported stream-level directive") {
		t.Errorf("expected unknown stream-level directive to be skipped, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamServerNameWithoutProxyPassBlocksGroup(t *testing.T) {
	cfg, rep := translate(t, `
stream {
  server { listen 5353; server_name host1.example.com; ssl_preread on; }
  server { listen 5353; ssl_preread on; proxy_pass 10.0.0.2:6000; }
}`)
	if len(cfg.Streams) != 0 {
		t.Fatalf("group with a targetless server_name must not translate: %+v", cfg.Streams)
	}
	if !hasFinding(rep, "listen", "has no proxy_pass target") {
		t.Errorf("expected a missing-target conflict, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamOutboundProxyProtocolOff(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_protocol off;
  }
}`)
	if cfg.Streams[0].ProxyProtocol != "" {
		t.Errorf("proxy_protocol off must not set outbound propagation: %+v", cfg.Streams[0])
	}
}

func TestTranslateStreamProxyProtocolMalformedValue(t *testing.T) {
	_, rep := translate(t, `
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_protocol maybe;
  }
}`)
	if !hasFinding(rep, "proxy_protocol", "requires an on or off value") {
		t.Errorf("expected a malformed proxy_protocol value to be skipped, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamConnectTimeoutUnsupportedUnit(t *testing.T) {
	_, rep := translate(t, `
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_connect_timeout 1w;
  }
}`)
	if !hasSkip(rep, "duration value is not representable") {
		t.Errorf("expected unsupported connect-timeout unit to be skipped, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamTimeoutNegativeRejected(t *testing.T) {
	_, rep := translate(t, `
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_timeout -5s;
  }
}`)
	if !hasSkip(rep, "duration value is not representable") {
		t.Errorf("expected a negative duration to be rejected, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamNamedUpstream(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  upstream pool {
    server 10.0.0.1:6000 weight=5;
    server 10.0.0.2:6000;
  }
  server {
    listen 5353;
    proxy_pass pool;
  }
}`)
	if len(cfg.Upstreams) != 1 || len(cfg.Upstreams[0].Servers) != 2 {
		t.Fatalf("upstream not reused: %+v", cfg.Upstreams)
	}
	if len(cfg.Streams) != 1 || cfg.Streams[0].ProxyPass != "pool" {
		t.Fatalf("stream did not reference named upstream: %+v", cfg.Streams)
	}
	if rep.Upstreams != 1 || rep.Streams != 1 {
		t.Errorf("report counts = upstreams:%d streams:%d, want 1/1", rep.Upstreams, rep.Streams)
	}
	validateGenerated(t, cfg)
}

func TestTranslateStreamUDP(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  server {
    listen 5353 udp;
    proxy_pass 127.0.0.1:6000;
  }
}`)
	if len(cfg.Streams) != 1 || cfg.Streams[0].Protocol != "udp" {
		t.Fatalf("udp stream not translated: %+v", cfg.Streams)
	}
	if !hasNote(rep, "udp") || !hasNote(rep, "not QUIC Connection-ID-aware") {
		t.Errorf("expected a UDP session-model note, got: %+v", rep.Notes)
	}
	validateGenerated(t, cfg)
}

func TestTranslateStreamTimeouts(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_timeout 45s;
    proxy_connect_timeout 2s;
  }
}`)
	st := cfg.Streams[0]
	if st.IdleTimeout.Std().String() != "45s" || st.ConnectTimeout.Std().String() != "2s" {
		t.Errorf("timeouts not translated: idle=%v connect=%v", st.IdleTimeout, st.ConnectTimeout)
	}
	validateGenerated(t, cfg)
}

func TestTranslateStreamTimeoutBareSeconds(t *testing.T) {
	cfg, _ := translate(t, `
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_timeout 30;
  }
}`)
	if cfg.Streams[0].IdleTimeout.Std().String() != "30s" {
		t.Errorf("bare-seconds duration not translated: %v", cfg.Streams[0].IdleTimeout)
	}
}

func TestTranslateStreamTimeoutUnsupportedUnit(t *testing.T) {
	_, rep := translate(t, `
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_timeout 1d;
  }
}`)
	if !hasSkip(rep, "duration value is not representable") {
		t.Errorf("expected unsupported duration unit to be skipped, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamOutboundProxyProtocolTCP(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  server {
    listen 5353;
    proxy_pass 127.0.0.1:6000;
    proxy_protocol on;
  }
}`)
	if cfg.Streams[0].ProxyProtocol != "out" {
		t.Errorf("outbound proxy_protocol not translated: %+v", cfg.Streams[0])
	}
	validateGenerated(t, cfg)
}

func TestTranslateStreamOutboundProxyProtocolBlockedOnUDP(t *testing.T) {
	cfg, rep := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  server {
    listen 5353 udp;
    proxy_pass 127.0.0.1:6000;
    proxy_protocol on;
  }
}`)
	if cfg.Streams[0].ProxyProtocol != "" {
		t.Errorf("proxy_protocol must not be set for udp: %+v", cfg.Streams[0])
	}
	if !hasFinding(rep, "proxy_protocol", "only supported for tcp") {
		t.Errorf("expected a udp+proxy_protocol finding, got %+v", rep.Skipped)
	}
	validateGenerated(t, cfg)
}

func TestTranslateStreamInboundProxyProtocolBlocked(t *testing.T) {
	cfg, rep := translate(t, `
stream {
  server {
    listen 5353 proxy_protocol;
    proxy_pass 127.0.0.1:6000;
  }
}`)
	if len(cfg.Streams) != 0 {
		t.Errorf("inbound stream proxy_protocol must not translate: %+v", cfg.Streams)
	}
	if !hasFinding(rep, "listen", "trusted_proxies allow-list") {
		t.Errorf("expected inbound stream proxy_protocol to block, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamTLSTerminationBlocked(t *testing.T) {
	cfg, rep := translate(t, `
stream {
  server {
    listen 5353 ssl;
    proxy_pass 127.0.0.1:6000;
  }
}`)
	if len(cfg.Streams) != 0 {
		t.Errorf("TLS termination must not translate: %+v", cfg.Streams)
	}
	if !hasFinding(rep, "listen", "not representable") {
		t.Errorf("expected TLS termination to block, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamMapBlocked(t *testing.T) {
	_, rep := translate(t, `
stream {
  map $ssl_preread_server_name $backend {
    host1.example.com backend1;
    default backend2;
  }
  server {
    listen 5353;
    ssl_preread on;
    proxy_pass $backend;
  }
}`)
	if !hasSkip(rep, "variable maps") {
		t.Errorf("expected the stream map to block, got %+v", rep.Skipped)
	}
	// proxy_pass referencing an nginx variable is never resolved to the map.
	if !hasFinding(rep, "proxy_pass", "variable-derived") {
		t.Errorf("expected the variable proxy_pass to block too, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamSNIBoundedMerge(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server { listen 8080; location / { return 200; } }
}
stream {
  server {
    listen 5353;
    server_name host1.example.com;
    ssl_preread on;
    proxy_pass 10.0.0.1:6000;
  }
  server {
    listen 5353;
    server_name host2.example.com;
    ssl_preread on;
    proxy_pass 10.0.0.2:6000;
  }
  server {
    listen 5353;
    ssl_preread on;
    proxy_pass 10.0.0.3:6000;
  }
}`)
	if len(cfg.Streams) != 1 {
		t.Fatalf("expected the three listeners to merge into one, got %d: %+v", len(cfg.Streams), cfg.Streams)
	}
	st := cfg.Streams[0]
	if !st.TLSPassthrough || len(st.SNIRoutes) != 2 || st.SNIRoutes["host1.example.com"] != "10.0.0.1:6000" || st.SNIRoutes["host2.example.com"] != "10.0.0.2:6000" {
		t.Errorf("sni_routes not merged correctly: %+v", st)
	}
	if st.ProxyPass != "10.0.0.3:6000" {
		t.Errorf("fallback proxy_pass not preserved: %+v", st)
	}
	validateGenerated(t, cfg)
}

func TestTranslateStreamSNIGroupConflicts(t *testing.T) {
	tests := []struct {
		name     string
		source   string
		fragment string
	}{
		{
			name: "missing ssl_preread",
			source: `
stream {
  server { listen 5353; server_name host1.example.com; proxy_pass 10.0.0.1:6000; }
  server { listen 5353; server_name host2.example.com; ssl_preread on; proxy_pass 10.0.0.2:6000; }
}`,
			fragment: "without ssl_preread",
		},
		{
			name: "duplicate server_name",
			source: `
stream {
  server { listen 5353; server_name host1.example.com; ssl_preread on; proxy_pass 10.0.0.1:6000; }
  server { listen 5353; server_name host1.example.com; ssl_preread on; proxy_pass 10.0.0.2:6000; }
}`,
			fragment: "duplicate server_name",
		},
		{
			name: "ambiguous fallback",
			source: `
stream {
  server { listen 5353; ssl_preread on; proxy_pass 10.0.0.1:6000; }
  server { listen 5353; ssl_preread on; proxy_pass 10.0.0.2:6000; }
}`,
			fragment: "more than one server block",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, rep := translate(t, tt.source)
			if len(cfg.Streams) != 0 {
				t.Fatalf("conflicting group must not translate: %+v", cfg.Streams)
			}
			if !hasFinding(rep, "listen", tt.fragment) {
				t.Errorf("expected conflict fragment %q, got %+v", tt.fragment, rep.Skipped)
			}
		})
	}
}

func TestTranslateStreamUnsupportedListenOption(t *testing.T) {
	_, rep := translate(t, `
stream {
  server {
    listen 5353 fastopen=10;
    proxy_pass 127.0.0.1:6000;
  }
}`)
	if !hasFinding(rep, "listen", "listen option is not translated") {
		t.Errorf("expected unsupported listen option to block, got %+v", rep.Skipped)
	}
}

func TestTranslateStreamOperationalListenTokensAccepted(t *testing.T) {
	cfg, _ := translate(t, `
stream {
  server {
    listen 5353 bind reuseport backlog=511 ipv6only=on so_keepalive=on;
    proxy_pass 127.0.0.1:6000;
  }
}`)
	if len(cfg.Streams) != 1 {
		t.Fatalf("operational listen tokens must not block translation: %+v", cfg.Streams)
	}
}

func TestTranslateHTTPProxyProtocolIdentityFullTrio(t *testing.T) {
	cfg, _ := translate(t, `
http {
  server {
    listen 80 proxy_protocol;
    set_real_ip_from 10.0.0.0/8;
    real_ip_header proxy_protocol;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`)
	srv := onlyServer(t, cfg)
	if srv.ProxyProtocol != "in" {
		t.Fatalf("ProxyProtocol = %q, want \"in\"", srv.ProxyProtocol)
	}
	if srv.ClientAddress == nil || len(srv.ClientAddress.TrustedProxies) != 1 || srv.ClientAddress.TrustedProxies[0] != "10.0.0.0/8" {
		t.Fatalf("client address not translated: %+v", srv.ClientAddress)
	}
	validateGenerated(t, cfg)
}

func TestTranslateHTTPProxyProtocolIdentityIncompleteTrio(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{
			name: "missing listen token",
			source: `
http {
  server {
    listen 80;
    set_real_ip_from 10.0.0.0/8;
    real_ip_header proxy_protocol;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`,
		},
		{
			name: "missing trusted source",
			source: `
http {
  server {
    listen 80 proxy_protocol;
    real_ip_header proxy_protocol;
    location / { proxy_pass http://127.0.0.1:3000; }
  }
}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, rep := translate(t, tt.source)
			srv := onlyServer(t, cfg)
			if srv.ProxyProtocol == "in" {
				t.Fatalf("incomplete trio must not enable proxy_protocol: %+v", srv)
			}
			if srv.ClientAddress != nil {
				t.Fatalf("incomplete trio must not emit a client_address policy: %+v", srv.ClientAddress)
			}
			if !hasFinding(rep, "real_ip_header", "proxy_protocol requires") {
				t.Errorf("expected a blocking finding, got %+v", rep.Skipped)
			}
		})
	}
}

// validateGenerated marshals, reparses, and strictly validates a translated
// candidate - the same normal authoritative path every generated config must
// pass, per the migration invariant that no unsupported semantics may hide
// behind output that merely "looks" valid.
func validateGenerated(t *testing.T, cfg *config.Config) {
	t.Helper()
	toml, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	loaded, err := config.Parse(toml)
	if err != nil {
		t.Fatalf("parse candidate:\n%s\nerror: %v", toml, err)
	}
	if err := config.Validate(loaded); err != nil {
		t.Fatalf("validate candidate:\n%s\nerror: %v", toml, err)
	}
}
