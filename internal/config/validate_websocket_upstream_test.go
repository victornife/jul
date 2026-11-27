// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestValidateWebSocketUpstreamProtocol(t *testing.T) {
	tests := []struct {
		name string
		up   UpstreamConfig
		want string
	}{
		{
			name: "default remains valid",
			up:   UpstreamConfig{Name: "inventory"},
		},
		{
			name: "explicit HTTP1 remains valid",
			up:   UpstreamConfig{WebSocketUpstreamProtocol: "http1"},
		},
		{
			name: "HTTP3 accepts UDP backends",
			up:   UpstreamConfig{WebSocketUpstreamProtocol: "http3", Servers: []UpstreamServer{{Address: "127.0.0.1:443"}}},
		},
		{
			name: "HTTP3 excludes Unix sockets",
			up:   UpstreamConfig{WebSocketUpstreamProtocol: "http3", Servers: []UpstreamServer{{Address: "unix:/tmp/backend.sock"}}},
			want: "requires UDP backends",
		},
		{
			name: "unsupported protocol rejected",
			up:   UpstreamConfig{WebSocketUpstreamProtocol: "http2"},
			want: "unsupported value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateWebSocketUpstreamProtocol(tt.up, "upstreams[0]")
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected validation errors: %v", errs)
				}

				return
			}
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), tt.want) {
				t.Fatalf("validation errors = %v, want one containing %q", errs, tt.want)
			}
		})
	}
}

func TestValidateWebSocketUpstreamConsumers(t *testing.T) {
	for _, tt := range []struct {
		name string
		loc  LocationConfig
		want bool
	}{
		{name: "HTTPS", loc: LocationConfig{ProxyPass: "https://inventory/ws"}},
		{name: "plaintext", loc: LocationConfig{ProxyPass: "http://inventory"}, want: true},
		{name: "unrelated pool", loc: LocationConfig{ProxyPass: "http://other"}},
		{name: "ordinary gRPC unaffected", loc: LocationConfig{ProxyPass: "http://inventory", GRPC: true}},
		{name: "static unaffected", loc: LocationConfig{Root: "/tmp"}},
		{name: "malformed URL handled by proxy validation", loc: LocationConfig{ProxyPass: "http://%"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := &Config{
				Upstreams: []UpstreamConfig{{Name: "inventory", WebSocketUpstreamProtocol: "http3"}},
				Servers:   []ServerConfig{{Locations: []LocationConfig{tt.loc}}},
			}
			errs := validateWebSocketUpstreamConsumers(c)
			if (len(errs) != 0) != tt.want {
				t.Fatalf("consumer validation errors=%v, want error=%t", errs, tt.want)
			}
			if tt.want && !strings.Contains(errs[0].Error(), "requires an https:// proxy_pass") {
				t.Fatalf("unexpected error: %v", errs)
			}
		})
	}
}
