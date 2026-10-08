// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

func TestLintExposedWithoutTimeouts(t *testing.T) {
	const msg = "sets no read_timeout, write_timeout, send_timeout, proxy_read_timeout or proxy_send_timeout"
	proxy := func(read, send time.Duration) LocationConfig {
		return LocationConfig{
			Match:            MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass:        "http://backend",
			ProxyReadTimeout: Duration(read),
			ProxySendTimeout: Duration(send),
		}
	}
	for _, tc := range []struct {
		name string
		srv  ServerConfig
		want bool
	}{
		{"all interfaces, nothing bounded", ServerConfig{Listen: ":80", Locations: []LocationConfig{proxy(0, 0)}}, true},
		{"wildcard ipv4, static only", ServerConfig{Listen: "0.0.0.0:80", Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}}}, true},
		{"public address", ServerConfig{Listen: "203.0.113.7:443", Locations: []LocationConfig{proxy(0, 0)}}, true},
		{"loopback", ServerConfig{Listen: "127.0.0.1:80", Locations: []LocationConfig{proxy(0, 0)}}, false},
		{"read_timeout set", ServerConfig{Listen: ":80", ReadTimeout: Duration(time.Minute), Locations: []LocationConfig{proxy(0, 0)}}, false},
		{"write_timeout set", ServerConfig{Listen: ":80", WriteTimeout: Duration(time.Minute), Locations: []LocationConfig{proxy(0, 0)}}, false},
		{"send_timeout set", ServerConfig{Listen: ":80", SendTimeout: Duration(time.Minute), Locations: []LocationConfig{proxy(0, 0)}}, false},
		{"proxy_read_timeout set", ServerConfig{Listen: ":80", Locations: []LocationConfig{proxy(30*time.Second, 0)}}, false},
		{"proxy_send_timeout set", ServerConfig{Listen: ":80", Locations: []LocationConfig{proxy(0, 30*time.Second)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Config{Servers: []ServerConfig{tc.srv}, Compression: CompressionConfig{Enabled: Bool(true)}}
			if got := hasWarning(Lint(c), msg); got != tc.want {
				t.Fatalf("warning = %v, want %v:\n%s", got, tc.want, lintMessages(Lint(c)))
			}
		})
	}
}

func TestLintSourceEmptyEncoders(t *testing.T) {
	for _, tc := range []struct {
		name, toml string
		want       bool
	}{
		{"enabled with empty encoders", "[compression]\nenabled = true\nencoders = []\n", true},
		{"enabled, encoders omitted", "[compression]\nenabled = true\n", false},
		{"enabled with gzip", "[compression]\nenabled = true\nencoders = [\"gzip\"]\n", false},
		{"disabled with empty encoders", "[compression]\nenabled = false\nencoders = []\n", false},
		{"auto (no enabled) with empty encoders", "[compression]\nencoders = []\n", false},
		{"no compression block", "[global]\nlog_level = \"info\"\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			diags := LintSource([]byte(tc.toml))
			got := len(diags) == 1 && diags[0].Field == "[compression].encoders" &&
				strings.Contains(diags[0].Message, `treated as ["gzip"]`)
			if got != tc.want {
				t.Fatalf("LintSource = %+v, want warning=%v", diags, tc.want)
			}
		})
	}
	// The parsed config really is defaulted, which is why the check must read
	// the source.
	c, err := Parse([]byte("[compression]\nenabled = true\nencoders = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Compression.Encoders) != 1 || c.Compression.Encoders[0] != "gzip" {
		t.Fatalf("encoders = %v, want the documented [gzip] default", c.Compression.Encoders)
	}
}
