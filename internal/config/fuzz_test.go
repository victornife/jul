// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import "testing"

// FuzzParse exercises the TOML config parser with arbitrary input.  It is the
// fuzz target for criterion ⑧ of the Y1-08 Zero-config + jul lint GA bundle.
// go-toml/v2 handles the heavy lifting; this target verifies that our Config
// struct, custom UnmarshalText implementations, and applyDefaults() never
// panic on malformed or edge-case input.
func FuzzParse(f *testing.F) {
	// Seed with a minimal valid config, a config with every block populated,
	// and a few malformed edge cases.
	f.Add([]byte(`
[global]
log_level = "info"

[[servers]]
listen = ":8080"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv"
`))
	f.Add([]byte(`
[global]
log_level = "debug"
log_format = "json"

[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/ssl/cert.pem"
  key = "/etc/ssl/key.pem"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://127.0.0.1:3000"

    [servers.locations.rate_limit]
    enabled = true
    key = "ip"
    rate = 100
    burst = 200

[[upstreams]]
name = "api"
strategy = "round_robin"
servers = [{ address = "127.0.0.1:3001" }]

  [upstreams.health_check]
  enabled = true
  type = "http"
  path = "/health"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_ADMIN_TOKEN}"

[compression]
enabled = true

[rate_limit]
enabled = true
key = "ip"
rate = 1000
burst = 1000
max_conns = 1024
`))
	f.Add([]byte(`[global]
log_level = 42`)) // wrong type
	f.Add([]byte(`[[servers]]`)) // minimal
	f.Add([]byte(`[servers]`))   // wrong table type
	f.Add([]byte(`[[servers]]
listen = ":80"
server_names = "not-a-list"`)) // wrong type for slice
	f.Add([]byte(``))             // empty
	f.Add([]byte(`\x00\x01\x02`)) // binary garbage
	f.Add([]byte(`[global]
log_level = "` + string(make([]byte, 1<<16)) + `"`)) // huge string

	f.Fuzz(func(t *testing.T, data []byte) {
		// We only care that Parse does not panic.  A decode error is expected
		// for malformed input; we ignore it.  We also exercise Marshal on the
		// result when parsing succeeds, to catch round-trip panics.
		cfg, err := Parse(data)
		if err != nil {
			return
		}
		_ = Validate(cfg) // should not panic
		_, _ = Marshal(cfg)
	})
}
