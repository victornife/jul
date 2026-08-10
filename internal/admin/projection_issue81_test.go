// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func parseIssue81Config(t *testing.T, raw string) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

func TestIssue81TrafficProjectionRoundTripsDormantValues(t *testing.T) {
	cfg := parseIssue81Config(t, `
[global]
worker_threads = "7"
log_level = "debug"
log_format = "json"
shutdown_timeout = "41s"
reload_timeout = "17s"
redact_min_secret_length = 9

[compression]
enabled = false
encoders = ["zstd", "gzip"]
level = 6
min_size = "3k"
types = ["application/json", "text/plain"]
precompressed = true

[rate_limit]
enabled = false
key = "header:X-Tenant"
rate = 23
burst = 31
max_conns = 19

[cache]
enabled = false
memory_max_size = "73m"
disk_path = "/var/lib/jul/cache"
disk_max_size = "5g"
default_ttl = "71s"
stale_while_revalidate = "13s"
stale_if_error = "29s"

[[servers]]
listen = "127.0.0.1:8080"
client_max_body_size = "17m"
read_timeout = "21s"
write_timeout = "22s"
idle_timeout = "91s"

[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`)

	got := projectIssue81TrafficControls(cfg)
	if got.Global == nil || got.Global.WorkerThreads != "7" || got.Global.LogFormat != "json" {
		t.Fatalf("global projection mismatch: %#v", got.Global)
	}
	if got.Compression == nil || got.Compression.Enabled || got.Compression.Level != 6 ||
		got.Compression.MinSize != "3k" || !got.Compression.Precompressed ||
		len(got.Compression.Encoders) != 2 || len(got.Compression.Types) != 2 {
		t.Fatalf("compression projection dropped dormant values: %#v", got.Compression)
	}
	if got.RateLimit == nil || got.RateLimit.Enabled || got.RateLimit.MaxConns != 19 ||
		got.RateLimit.Key != "header:X-Tenant" || got.RateLimit.Rate != 23 || got.RateLimit.Burst != 31 {
		t.Fatalf("global rate-limit projection dropped dormant values: %#v", got.RateLimit)
	}
	if got.Cache == nil || got.Cache.Enabled || got.Cache.MemoryMaxSize != "73m" ||
		got.Cache.MemoryMax != "73m" || got.Cache.DiskPath != "/var/lib/jul/cache" ||
		got.Cache.DiskMaxSize != "5g" || got.Cache.DefaultTTL != "1m11s" ||
		got.Cache.StaleWhileRevalidate != "13s" || got.Cache.StaleIfError != "29s" {
		t.Fatalf("cache projection is incomplete: %#v", got.Cache)
	}

	servers := got.Servers
	if len(servers) != 1 {
		t.Fatalf("servers=%d want 1", len(servers))
	}
	if servers[0].ClientMaxBodySize != "17m" || servers[0].ReadTimeout != "21s" ||
		servers[0].WriteTimeout != "22s" || servers[0].IdleTimeout != "1m31s" {
		t.Fatalf("server limits projection mismatch: %#v", servers[0])
	}

	encoded, err := json.Marshal(got.Global)
	if err != nil {
		t.Fatalf("marshal projection: %v", err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"access_log\"", "error_log\"", "token", "credential", "secret:"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("projection leaked forbidden field/value %q: %s", forbidden, text)
		}
	}
}

func TestIssue81GlobalLifecycleProjectionComesFromRegistry(t *testing.T) {
	cfg := parseIssue81Config(t, `[global]
worker_threads = "auto"
log_level = "info"
log_format = "text"
shutdown_timeout = "30s"
reload_timeout = "10s"
redact_min_secret_length = 8
`)
	got := projectIssue81TrafficControls(cfg)
	if got.Global == nil {
		t.Fatal("global projection is nil")
	}
	paths := map[string]string{
		"worker_threads":           "global.worker_threads",
		"log_level":                "global.log_level",
		"log_format":               "global.log_format",
		"shutdown_timeout":         "global.shutdown_timeout",
		"reload_timeout":           "global.reload_timeout",
		"redact_min_secret_length": "global.redact_min_secret_length",
	}
	for field, path := range paths {
		entry, ok := lifecycle.Lookup(path)
		if !ok {
			t.Fatalf("registry missing %s", path)
		}
		metadata, ok := got.Global.Lifecycle[field]
		if !ok {
			t.Fatalf("projection missing lifecycle metadata for %s", field)
		}
		if metadata.Class != entry.Class.String() || metadata.Subsystem != string(entry.Subsystem) ||
			metadata.Reason != entry.Reason {
			t.Fatalf("metadata for %s is not registry-derived: %#v vs %#v", field, metadata, entry)
		}
	}
}

func TestIssue81LocationRateLimitProjectionDoesNotExposeMaxConns(t *testing.T) {
	cfg := parseIssue81Config(t, `
[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
[servers.locations.rate_limit]
enabled = true
rate = 4
burst = 8
key = "ip"
`)
	encoded, err := json.Marshal(projectRoutes(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "max_conns") {
		t.Fatalf("location projection widened global-only max_conns semantics: %s", encoded)
	}
}

func TestIssue81ProjectionIsCompleteByDefaultAndJSONCompatible(t *testing.T) {
	cfg := parseIssue81Config(t, `
[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`)
	projection := projectIssue81TrafficControls(cfg)
	if projection.Global == nil || projection.Compression == nil || projection.RateLimit == nil || projection.Cache == nil {
		t.Fatalf("complete projection contains nil section: %#v", projection)
	}
	if projection.Compression.Encoders == nil || projection.Compression.Types == nil {
		t.Fatalf("empty compression arrays must encode as [] rather than null: %#v", projection.Compression)
	}
	encoded, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	cache, ok := wire["cache"].(map[string]any)
	if !ok {
		t.Fatalf("cache wire shape=%T", wire["cache"])
	}
	if cache["memory_max_size"] != cache["memory_max"] {
		t.Fatalf("compatibility alias mismatch: %v", cache)
	}
}

func TestIssue81ProjectionNeverCarriesRawSecrets(t *testing.T) {
	const marker = "ISSUE81_LITERAL_ADMIN_SECRET"
	cfg := parseIssue81Config(t, `
[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "`+marker+`"

[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`)
	encoded, err := json.Marshal(projectIssue81TrafficControls(cfg))
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	if strings.Contains(wire, marker) || strings.Contains(wire, `"token"`) {
		t.Fatalf("safe projection leaked secret-bearing admin state: %s", wire)
	}
}
