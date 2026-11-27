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
		loc  LocationConfig
		want string
	}{
		{
			name: "default remains valid",
			loc:  LocationConfig{ProxyPass: "http://inventory"},
		},
		{
			name: "explicit HTTP1 remains valid",
			loc:  LocationConfig{ProxyPass: "http://inventory", WebSocketUpstreamProtocol: "http1"},
		},
		{
			name: "HTTP3 requires HTTPS",
			loc:  LocationConfig{ProxyPass: "http://inventory", WebSocketUpstreamProtocol: "http3"},
			want: "http3 requires an https:// proxy_pass",
		},
		{
			name: "HTTP3 requires a proxy route",
			loc:  LocationConfig{WebSocketUpstreamProtocol: "http3"},
			want: "applies only to HTTP proxy routes",
		},
		{
			name: "HTTP3 excludes native gRPC",
			loc: LocationConfig{
				ProxyPass: "https://inventory", GRPC: true, WebSocketUpstreamProtocol: "http3",
			},
			want: "applies only to HTTP proxy routes",
		},
		{
			name: "unsupported protocol rejected",
			loc:  LocationConfig{ProxyPass: "https://inventory", WebSocketUpstreamProtocol: "http2"},
			want: "unsupported value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateWebSocketUpstreamProtocol(tt.loc, "servers[0].locations[0]")
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
