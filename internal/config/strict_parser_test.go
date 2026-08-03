// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestParseRejectsUnknownFields(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "top-level field",
			doc: `unexpected = true

[[servers]]
listen = "127.0.0.1:8080"
`,
			want: "unexpected",
		},
		{
			name: "nested global field",
			doc: `[global]
log_levle = "debug"

[[servers]]
listen = "127.0.0.1:8080"
`,
			want: "log_levle",
		},
		{
			name: "nested server field",
			doc: `[[servers]]
listen = "127.0.0.1:8080"
read_header_timout = "5s"
`,
			want: "read_header_timout",
		},
		{
			name: "nested client-auth field",
			doc: `[[servers]]
listen = "127.0.0.1:8443"

  [servers.tls]
  enabled = true
  cert = "server.pem"
  key = "server.key"

    [servers.tls.client_auth]
    mdoe = "require"
`,
			want: "mdoe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatalf("Parse accepted unknown field %q: %#v", tt.want, cfg)
			}
			if !strings.Contains(err.Error(), "parse config:") {
				t.Fatalf("error = %q, want parse config prefix", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want unknown field %q", err, tt.want)
			}
		}
	}
}

func TestParseStrictModePreservesValidDynamicMaps(t *testing.T) {
	cfg, err := Parse([]byte(`[plugins.example]
path = "./example.wasm"

[plugins.example.config]
region = "eu-west"
tenant = "test"

[[servers]]
listen = "127.0.0.1:8080"
`))
	if err != nil {
		t.Fatalf("Parse valid dynamic plugin map: %v", err)
	}
	plugin, ok := cfg.Plugins["example"]
	if !ok {
		t.Fatal("expected plugins.example to be decoded")
	}
	if plugin.Path != "./example.wasm" || plugin.Config["region"] != "eu-west" || plugin.Config["tenant"] != "test" {
		t.Fatalf("plugin = %#v, want path and arbitrary config map", plugin)
	}
}

func TestParseAcceptsDeprecatedServerNameAlias(t *testing.T) {
	cfg, err := Parse([]byte(`[[servers]]
listen = "127.0.0.1:8080"
server_name = "example.com"
`))
	if err != nil {
		t.Fatalf("Parse deprecated server_name alias: %v", err)
	}
	if got, want := cfg.Servers[0].ServerNames, []string{"example.com"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("server_names = %#v, want %#v", got, want)
	}
	if cfg.Servers[0].DeprecatedServerName != "" {
		t.Fatalf("deprecated alias remained populated: %q", cfg.Servers[0].DeprecatedServerName)
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal canonicalized config: %v", err)
	}
	if strings.Contains(string(out), "server_name =") {
		t.Fatalf("canonical output re-emitted deprecated alias:\n%s", out)
	}
	if !strings.Contains(string(out), "server_names =") {
		t.Fatalf("canonical output omitted server_names:\n%s", out)
	}
}

func TestParseRejectsServerNameAliasWithCanonicalField(t *testing.T) {
	_, err := Parse([]byte(`[[servers]]
listen = "127.0.0.1:8080"
server_name = "legacy.example.com"
server_names = ["canonical.example.com"]
`))
	if err == nil {
		t.Fatal("expected conflicting server_name and server_names to fail")
	}
	if !strings.Contains(err.Error(), "cannot set both") {
		t.Fatalf("error = %q, want conflicting alias diagnostic", err)
	}
}
