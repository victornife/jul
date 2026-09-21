// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import "testing"

func TestClassifyStreamListen(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"empty address", []string{""}, AssessmentBlocking},
		{"unix socket", []string{"unix:/tmp/s.sock"}, AssessmentBlocking},
		{"plain tcp", []string{"5353"}, AssessmentSupported},
		{"udp", []string{"5353", "udp"}, AssessmentSupported},
		{"ssl termination", []string{"5353", "ssl"}, AssessmentBlocking},
		{"inbound proxy_protocol", []string{"5353", "proxy_protocol"}, AssessmentBlocking},
		{"operational tokens", []string{"5353", "bind", "reuseport", "backlog=511", "ipv6only=on", "so_keepalive=on"}, AssessmentSupported},
		{"unknown option", []string{"5353", "fastopen=10"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyStreamListen(tt.params), tt.want)
		})
	}
}

func TestClassifyStreamProxyPass(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"empty", []string{""}, AssessmentBlocking},
		{"literal", []string{"10.0.0.1:80"}, AssessmentSupported},
		{"named upstream", []string{"pool"}, AssessmentSupported},
		{"variable", []string{"$backend"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyStreamProxyPass(tt.params), tt.want)
		})
	}
}

func TestClassifyStreamProxyProtocol(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		isUDP  bool
		want   AssessmentClass
	}{
		{"missing", nil, false, AssessmentBlocking},
		{"on tcp", []string{"on"}, false, AssessmentSupported},
		{"on udp", []string{"on"}, true, AssessmentBlocking},
		{"off", []string{"off"}, false, AssessmentIgnored},
		{"malformed", []string{"maybe"}, false, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyStreamProxyProtocol(tt.params, tt.isUDP), tt.want)
		})
	}
}

func TestClassifyStreamDuration(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"bare seconds", []string{"30"}, AssessmentSupported},
		{"explicit unit", []string{"45s"}, AssessmentSupported},
		{"unsupported unit", []string{"1d"}, AssessmentBlocking},
		{"malformed", []string{"nope"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyStreamDuration("proxy_timeout", "NGX_STREAM_PROXY_TIMEOUT", tt.params), tt.want)
		})
	}
}

func TestClassifyStreamSSLPreread(t *testing.T) {
	tests := []struct {
		name   string
		params []string
		want   AssessmentClass
	}{
		{"missing", nil, AssessmentBlocking},
		{"on", []string{"on"}, AssessmentSupported},
		{"off", []string{"off"}, AssessmentSupported},
		{"malformed", []string{"maybe"}, AssessmentBlocking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertCapabilityClass(t, classifyStreamSSLPreread(tt.params), tt.want)
		})
	}
}

func TestServerHasUsableHTTPProxyProtocolIdentity(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{
			name: "complete trio",
			source: `http { server {
				listen 80 proxy_protocol;
				set_real_ip_from 10.0.0.0/8;
				real_ip_header proxy_protocol;
			} }`,
			want: true,
		},
		{
			name: "missing listen token",
			source: `http { server {
				listen 80;
				set_real_ip_from 10.0.0.0/8;
				real_ip_header proxy_protocol;
			} }`,
			want: false,
		},
		{
			name: "missing trusted source",
			source: `http { server {
				listen 80 proxy_protocol;
				real_ip_header proxy_protocol;
			} }`,
			want: false,
		},
		{
			name: "invalid trusted source",
			source: `http { server {
				listen 80 proxy_protocol;
				set_real_ip_from unix:;
				real_ip_header proxy_protocol;
			} }`,
			want: false,
		},
		{
			name: "not proxy_protocol header",
			source: `http { server {
				listen 80 proxy_protocol;
				set_real_ip_from 10.0.0.0/8;
				real_ip_header X-Forwarded-For;
			} }`,
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := firstDirectiveNamed(t, tt.source, "server")
			if got := serverHasUsableHTTPProxyProtocolIdentity(orderedChildren(server)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStreamServerListenIsUDP(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{"tcp", `stream { server { listen 5353; proxy_pass 1.2.3.4:80; } }`, false},
		{"udp", `stream { server { listen 5353 udp; proxy_pass 1.2.3.4:80; } }`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := firstDirectiveNamed(t, tt.source, "server")
			if got := streamServerListenIsUDP(orderedChildren(server)); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
