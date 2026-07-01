package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
)

func TestDurationUnmarshal(t *testing.T) {
	cases := map[string]time.Duration{
		`x = "30s"`:   30 * time.Second,
		`x = "5m"`:    5 * time.Minute,
		`x = "1h30m"`: 90 * time.Minute,
		`x = ""`:      0,
	}
	for input, want := range cases {
		var v struct {
			X Duration `toml:"x"`
		}
		if err := toml.Unmarshal([]byte(input), &v); err != nil {
			t.Fatalf("%q: unexpected error: %v", input, err)
		}
		if v.X.Std() != want {
			t.Errorf("%q: got %v, want %v", input, v.X.Std(), want)
		}
	}
}

func TestDurationInvalid(t *testing.T) {
	var v struct {
		X Duration `toml:"x"`
	}
	if err := toml.Unmarshal([]byte(`x = "nope"`), &v); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestSizeUnmarshal(t *testing.T) {
	cases := map[string]int64{
		`x = "1024"`: 1024,
		`x = "1k"`:   1024,
		`x = "1kb"`:  1024,
		`x = "1m"`:   1 << 20,
		`x = "2mb"`:  2 << 20,
		`x = "1g"`:   1 << 30,
		`x = "512b"`: 512,
		`x = ""`:     0,
	}
	for input, want := range cases {
		var v struct {
			X Size `toml:"x"`
		}
		if err := toml.Unmarshal([]byte(input), &v); err != nil {
			t.Fatalf("%q: unexpected error: %v", input, err)
		}
		if v.X.Bytes() != want {
			t.Errorf("%q: got %d, want %d", input, v.X.Bytes(), want)
		}
	}
}

func TestSizeInvalid(t *testing.T) {
	for _, input := range []string{`x = "-1"`, `x = "abc"`, `x = "1x"`} {
		var v struct {
			X Size `toml:"x"`
		}
		if err := toml.Unmarshal([]byte(input), &v); err == nil {
			t.Errorf("%q: expected error", input)
		}
	}
}

func TestUpstreamServerUnmarshal(t *testing.T) {
	input := `servers = ["127.0.0.1:3000", "10.0.0.1:80 weight=5"]`
	var v struct {
		Servers []UpstreamServer `toml:"servers"`
	}
	if err := toml.Unmarshal([]byte(input), &v); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(v.Servers) != 2 {
		t.Fatalf("got %d servers, want 2", len(v.Servers))
	}
	if v.Servers[0].Address != "127.0.0.1:3000" || v.Servers[0].Weight != 1 {
		t.Errorf("server[0] = %+v", v.Servers[0])
	}
	if v.Servers[1].Address != "10.0.0.1:80" || v.Servers[1].Weight != 5 {
		t.Errorf("server[1] = %+v", v.Servers[1])
	}
}

func TestValidateRejectsReserved(t *testing.T) {
	cfg := &Config{
		Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
		Mail:    []map[string]any{{"listen": "1.2.3.4:25"}},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for reserved [[mail]] table")
	}
}

func TestValidateRejectsNegativeRedactFloor(t *testing.T) {
	cfg := &Config{
		Global:  GlobalConfig{RedactMinSecretLength: -1},
		Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
	}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "redact_min_secret_length") {
		t.Fatalf("expected redact_min_secret_length validation error, got %v", err)
	}
}

func TestValidateStreams(t *testing.T) {
	base := func() *Config {
		return &Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
			Upstreams: []UpstreamConfig{{
				Name:    "db",
				Servers: []UpstreamServer{{Address: "127.0.0.1:5432", Weight: 1}},
			}},
		}
	}

	t.Run("valid tcp to upstream", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{Listen: "0.0.0.0:6432", Protocol: "tcp", ProxyPass: "db"}}
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid sni routes to literal addr", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{
			Listen:    "0.0.0.0:443",
			Protocol:  "tcp",
			SNIRoutes: map[string]string{"a.example.com": "127.0.0.1:8443", "*": "db"},
		}}
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("valid udp", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{Listen: "0.0.0.0:53", Protocol: "udp", ProxyPass: "127.0.0.1:5353"}}
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("missing listen", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{ProxyPass: "db"}}
		if err := Validate(c); err == nil {
			t.Error("expected error for missing listen")
		}
	})

	t.Run("no target", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{Listen: "0.0.0.0:6432"}}
		if err := Validate(c); err == nil {
			t.Error("expected error for missing proxy_pass and sni_routes")
		}
	})

	t.Run("unknown upstream target", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{Listen: "0.0.0.0:6432", ProxyPass: "nope"}}
		if err := Validate(c); err == nil {
			t.Error("expected error: target neither upstream nor host:port")
		}
	})

	t.Run("bad protocol", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{Listen: "0.0.0.0:6432", Protocol: "sctp", ProxyPass: "db"}}
		if err := Validate(c); err == nil {
			t.Error("expected error for invalid protocol")
		}
	})

	t.Run("bad proxy_protocol", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{Listen: "0.0.0.0:6432", ProxyPass: "db", ProxyProtocol: "sideways"}}
		if err := Validate(c); err == nil {
			t.Error("expected error for invalid proxy_protocol")
		}
	})

	t.Run("udp rejects tcp-only features", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{{
			Listen:    "0.0.0.0:53",
			Protocol:  "udp",
			SNIRoutes: map[string]string{"a": "db"},
		}}
		if err := Validate(c); err == nil {
			t.Error("expected error: sni_routes on udp stream")
		}
	})

	t.Run("duplicate listener", func(t *testing.T) {
		c := base()
		c.Streams = []StreamServer{
			{Listen: "0.0.0.0:6432", Protocol: "tcp", ProxyPass: "db"},
			{Listen: "0.0.0.0:6432", Protocol: "tcp", ProxyPass: "db"},
		}
		if err := Validate(c); err == nil {
			t.Error("expected error for duplicate tcp listener")
		}
	})
}

func TestValidateDiscovery(t *testing.T) {
	base := func(d *DiscoveryConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{Listen: "127.0.0.1:80"}},
			Upstreams: []UpstreamConfig{{
				Name:      "api",
				Discovery: d,
			}},
		}
	}

	t.Run("dns valid with no static servers", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "dns", Target: "svc.local:8080"})
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("dns_srv valid", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "dns_srv", Target: "_grpc._tcp.svc.local"})
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("consul valid", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "consul", Consul: &ConsulDiscovery{Service: "web", Address: "http://127.0.0.1:8500"}})
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("kubernetes valid", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "kubernetes", Kubernetes: &KubernetesDiscovery{Namespace: "default", Service: "web"}})
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("static type with servers still valid", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "static"})
		c.Upstreams[0].Servers = []UpstreamServer{{Address: "127.0.0.1:80", Weight: 1}}
		if err := Validate(c); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("dns without port rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "dns", Target: "svc.local"})
		if err := Validate(c); err == nil {
			t.Error("expected error: dns target without port")
		}
	})

	t.Run("dns without target rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "dns"})
		if err := Validate(c); err == nil {
			t.Error("expected error: dns without target")
		}
	})

	t.Run("dns_srv without target rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "dns_srv"})
		if err := Validate(c); err == nil {
			t.Error("expected error: dns_srv without target")
		}
	})

	t.Run("consul without service rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "consul", Consul: &ConsulDiscovery{}})
		if err := Validate(c); err == nil {
			t.Error("expected error: consul without service")
		}
	})

	t.Run("consul bad address rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "consul", Consul: &ConsulDiscovery{Service: "web", Address: "not-a-url"}})
		if err := Validate(c); err == nil {
			t.Error("expected error: consul address not http(s)")
		}
	})

	t.Run("kubernetes without namespace rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "kubernetes", Kubernetes: &KubernetesDiscovery{Service: "web"}})
		if err := Validate(c); err == nil {
			t.Error("expected error: kubernetes without namespace")
		}
	})

	t.Run("unknown type rejected", func(t *testing.T) {
		c := base(&DiscoveryConfig{Type: "etcd", Target: "x"})
		if err := Validate(c); err == nil {
			t.Error("expected error: unknown discovery type")
		}
	})

	t.Run("static upstream without discovery or servers rejected", func(t *testing.T) {
		c := base(nil)
		if err := Validate(c); err == nil {
			t.Error("expected error: upstream with neither servers nor discovery")
		}
	})
}

func TestValidatePluginsValid(t *testing.T) {
	cfg := &Config{
		Servers: []ServerConfig{{
			Listen:  "127.0.0.1:80",
			Plugins: []string{"mw"},
			Locations: []LocationConfig{
				{Match: MatchConfig{Type: "prefix", Path: "/"}, Plugin: "act"},
			},
		}},
		Plugins: map[string]PluginConfig{
			"mw":  {Path: "../../testdata/plugins/header-inject.wasm", Type: "middleware"},
			"act": {Path: "../../testdata/plugins/request-block.wasm", Type: "handler"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePluginsErrors(t *testing.T) {
	good := "../../testdata/plugins/header-inject.wasm"
	cases := map[string]*Config{
		"bad type": {
			Servers: []ServerConfig{{Listen: ":80"}},
			Plugins: map[string]PluginConfig{"p": {Path: good, Type: "bogus"}},
		},
		"no source": {
			Servers: []ServerConfig{{Listen: ":80"}},
			Plugins: map[string]PluginConfig{"p": {Type: "middleware"}},
		},
		"both sources": {
			Servers: []ServerConfig{{Listen: ":80"}},
			Plugins: map[string]PluginConfig{"p": {Path: good, Inline: "AAA=", Type: "middleware"}},
		},
		"missing file": {
			Servers: []ServerConfig{{Listen: ":80"}},
			Plugins: map[string]PluginConfig{"p": {Path: "nope.wasm", Type: "middleware"}},
		},
		"fetch without allowed_hosts": {
			Servers: []ServerConfig{{Listen: ":80"}},
			Plugins: map[string]PluginConfig{"p": {Path: good, Fetch: true}},
		},
		"unknown ref": {
			Servers: []ServerConfig{{
				Listen:    ":80",
				Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Plugins: []string{"ghost"}}},
			}},
		},
		"wrong type ref": {
			Servers: []ServerConfig{{
				Listen:    ":80",
				Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Plugin: "mw"}},
			}},
			Plugins: map[string]PluginConfig{"mw": {Path: good, Type: "middleware"}},
		},
		"action conflict": {
			Servers: []ServerConfig{{
				Listen: ":80",
				Locations: []LocationConfig{{
					Match:  MatchConfig{Type: "prefix", Path: "/"},
					Plugin: "act", Root: "/var/www",
				}},
			}},
			Plugins: map[string]PluginConfig{"act": {Path: good, Type: "handler"}},
		},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := Validate(cfg); err == nil {
				t.Fatalf("expected a validation error for %q", name)
			}
		})
	}
}

func acmeBlock(listen, email, ca, challenge, cacheDir string) ServerConfig {
	return ServerConfig{
		Listen:      listen,
		ServerNames: []string{"example.com"},
		TLS: &TLSConfig{
			Enabled: true,
			ACME: &ACMEConfig{
				Enabled: true, Email: email, CA: ca,
				Challenge: challenge, CacheDir: cacheDir,
				Domains: []string{"example.com"},
			},
		},
	}
}

func TestValidateACMEDNS01Reserved(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{acmeBlock(":443", "a@b.com", "letsencrypt-staging", "dns-01", "./c")}}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "reserved for a future release") {
		t.Fatalf("expected dns-01 reserved error, got %v", err)
	}
}

func TestValidateACMEDNSProviderReserved(t *testing.T) {
	s := acmeBlock(":443", "a@b.com", "letsencrypt-staging", "http-01", "./c")
	s.TLS.ACME.DNSProvider = "cloudflare"
	cfg := &Config{Servers: []ServerConfig{s}}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "dns_provider is reserved") {
		t.Fatalf("expected dns_provider reserved error, got %v", err)
	}
}

func TestValidateACMEMultiBlockDivergent(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{
		acmeBlock(":443", "a@b.com", "letsencrypt-staging", "http-01", "./c"),
		acmeBlock(":8443", "x@y.com", "letsencrypt", "http-01", "./c"),
	}}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "all ACME server blocks share one issuer") {
		t.Fatalf("expected divergent multi-block error, got %v", err)
	}
}

func TestValidateACMEMultiBlockConsistent(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{
		acmeBlock(":443", "a@b.com", "letsencrypt-staging", "http-01", "./c"),
		acmeBlock(":8443", "a@b.com", "letsencrypt-staging", "http-01", "./c"),
	}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error for consistent ACME blocks: %v", err)
	}
}

func TestValidateRequiresServerAndListen(t *testing.T) {
	if err := Validate(&Config{}); err == nil {
		t.Fatal("expected error when no servers configured")
	}
	cfg := &Config{Servers: []ServerConfig{{}}}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error when listen is empty")
	}
}

func TestValidateMatch(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{{
		Listen: "127.0.0.1:80",
		Locations: []LocationConfig{
			{Match: MatchConfig{Type: "bogus", Path: "/"}},
		},
	}}}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected error for invalid match type")
	}
}

func TestCompressionDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[compression]\nenabled = true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := cfg.Compression
	if len(c.Encoders) != 1 || c.Encoders[0] != "gzip" {
		t.Errorf("default encoders = %v, want [gzip]", c.Encoders)
	}
	if c.MinSize != Size(1<<10) {
		t.Errorf("default min_size = %d, want 1024", c.MinSize.Bytes())
	}
	if len(c.Types) == 0 {
		t.Error("default types should be non-empty when enabled")
	}
}

func TestCompressionDefaultsSkippedWhenDisabled(t *testing.T) {
	cfg, err := Parse([]byte("[compression]\nenabled = false\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(cfg.Compression.Encoders) != 0 || cfg.Compression.MinSize != 0 {
		t.Error("defaults must not be applied when compression is disabled")
	}
}

func TestAdminConsoleDefaultsEnabled(t *testing.T) {
	cfg, err := Parse([]byte("[admin]\nenabled = true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Admin.Console == nil {
		t.Fatal("admin console default not applied")
	}
	if !cfg.Admin.ConsoleEnabled() {
		t.Error("admin console should default to enabled")
	}
}

func TestAdminConsoleExplicitFalse(t *testing.T) {
	cfg, err := Parse([]byte("[admin]\nenabled = true\nconsole = false\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Admin.ConsoleEnabled() {
		t.Error("explicit console = false must disable the console")
	}
}

func TestAdminConsoleEnabledHelperZeroValue(t *testing.T) {
	// A zero-value AdminConfig (Console nil) must report enabled so the helper
	// is safe to call on configs that never went through applyDefaults.
	var a AdminConfig
	if !a.ConsoleEnabled() {
		t.Error("zero-value AdminConfig should report console enabled")
	}
}

func TestAdminHistoryDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[admin]\nenabled = true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Admin.HistoryDir != "./jul-data/config-history" {
		t.Errorf("history_dir default = %q, want ./jul-data/config-history", cfg.Admin.HistoryDir)
	}
	if cfg.Admin.HistoryKeep != 50 {
		t.Errorf("history_keep default = %d, want 50", cfg.Admin.HistoryKeep)
	}
}

func TestAdminHistoryExplicit(t *testing.T) {
	cfg, err := Parse([]byte("[admin]\nenabled = true\nhistory_dir = \"/var/lib/jul/hist\"\nhistory_keep = 10\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Admin.HistoryDir != "/var/lib/jul/hist" {
		t.Errorf("history_dir = %q", cfg.Admin.HistoryDir)
	}
	if cfg.Admin.HistoryKeep != 10 {
		t.Errorf("history_keep = %d, want 10", cfg.Admin.HistoryKeep)
	}
}

func TestAdminHistoryKeepNegativeRejected(t *testing.T) {
	cfg, err := Parse([]byte("[admin]\nenabled = true\nhistory_keep = -1\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected validation error for negative history_keep")
	}
}

func TestValidateCompression(t *testing.T) {
	base := func(c CompressionConfig) *Config {
		return &Config{
			Servers:     []ServerConfig{{Listen: "127.0.0.1:80"}},
			Compression: c,
		}
	}
	if err := Validate(base(CompressionConfig{Enabled: true, Encoders: []string{"gzip", "br"}, Types: []string{"text/*"}})); err != nil {
		t.Fatalf("valid compression rejected: %v", err)
	}
	if err := Validate(base(CompressionConfig{Enabled: true, Encoders: []string{"snappy"}, Types: []string{"text/*"}})); err == nil {
		t.Error("expected error for invalid encoder")
	}
	if err := Validate(base(CompressionConfig{Enabled: true, Encoders: []string{"gzip"}, Level: 99, Types: []string{"text/*"}})); err == nil {
		t.Error("expected error for out-of-range level")
	}
	if err := Validate(base(CompressionConfig{Enabled: false, Encoders: []string{"snappy"}})); err != nil {
		t.Errorf("disabled compression must not be validated: %v", err)
	}
}

func TestRateLimitDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[rate_limit]\nenabled = true\nrate = 100\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	c := cfg.RateLimit
	if c.Key != "ip" {
		t.Errorf("default key = %q, want ip", c.Key)
	}
	if c.Burst != 100 {
		t.Errorf("default burst = %d, want 100 (= rate)", c.Burst)
	}
}

func TestRateLimitDefaultsSkippedWhenDisabled(t *testing.T) {
	cfg, err := Parse([]byte("[rate_limit]\nenabled = false\nrate = 100\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.RateLimit.Key != "" || cfg.RateLimit.Burst != 0 {
		t.Error("defaults must not be applied when rate limiting is disabled")
	}
}

func TestRateLimitLocationOverrideDefaults(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "127.0.0.1:8080"

[[servers.locations]]
match = { type = "prefix", path = "/api" }
proxy_pass = "http://127.0.0.1:3000"
rate_limit = { enabled = true, rate = 50 }
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rl := cfg.Servers[0].Locations[0].RateLimit
	if rl == nil {
		t.Fatal("location rate_limit override not parsed")
	}
	if rl.Key != "ip" || rl.Burst != 50 {
		t.Errorf("location override defaults not applied: key=%q burst=%d", rl.Key, rl.Burst)
	}
}

func TestValidateRateLimit(t *testing.T) {
	base := func(c RateLimitConfig) *Config {
		return &Config{
			Servers:   []ServerConfig{{Listen: "127.0.0.1:80"}},
			RateLimit: c,
		}
	}
	if err := Validate(base(RateLimitConfig{Enabled: true, Key: "ip", Rate: 100, Burst: 200})); err != nil {
		t.Fatalf("valid rate limit rejected: %v", err)
	}
	if err := Validate(base(RateLimitConfig{Enabled: true, Key: "ip", Rate: 0, Burst: 0})); err == nil {
		t.Error("expected error for rate <= 0")
	}
	if err := Validate(base(RateLimitConfig{Enabled: true, Key: "ip", Rate: 100, Burst: 50})); err == nil {
		t.Error("expected error for burst < rate")
	}
	if err := Validate(base(RateLimitConfig{Enabled: true, Key: "header:", Rate: 10, Burst: 10})); err == nil {
		t.Error("expected error for malformed header key")
	}
	if err := Validate(base(RateLimitConfig{Enabled: true, Key: "jwt:sub", Rate: 10, Burst: 10})); err != nil {
		t.Errorf("jwt key rejected: %v", err)
	}
	if err := Validate(base(RateLimitConfig{Enabled: false, Rate: -1})); err != nil {
		t.Errorf("disabled rate limit must not be validated: %v", err)
	}
}

func TestValidateRateLimitLocationRejectsMaxConns(t *testing.T) {
	cfg := &Config{
		Servers: []ServerConfig{{
			Listen: "127.0.0.1:80",
			Locations: []LocationConfig{{
				Match:     MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: "http://127.0.0.1:3000",
				RateLimit: &RateLimitConfig{Enabled: true, Key: "ip", Rate: 10, Burst: 10, MaxConns: 5},
			}},
		}},
	}
	if err := Validate(cfg); err == nil {
		t.Error("expected error: max_conns on a location override is not allowed")
	}
}

func TestAuthDefaults(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "127.0.0.1:8080"

[[servers.locations]]
match = { type = "prefix", path = "/api" }
proxy_pass = "http://127.0.0.1:3000"

[servers.locations.auth]
[servers.locations.auth.basic]
file = "/etc/jul/htpasswd"

[servers.locations.auth.jwt]
jwks_url = "https://issuer.example/.well-known/jwks.json"
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	a := cfg.Servers[0].Locations[0].Auth
	if a == nil {
		t.Fatal("auth block not parsed")
	}
	if a.Basic == nil || a.Basic.Realm != "Restricted" {
		t.Errorf("basic realm default not applied: %+v", a.Basic)
	}
	if a.JWT == nil || len(a.JWT.Algorithms) == 0 {
		t.Fatal("jwt algorithm default not applied")
	}
	for _, alg := range a.JWT.Algorithms {
		if alg == "none" || strings.HasPrefix(alg, "HS") {
			t.Errorf("default algorithms must be asymmetric, got %q", alg)
		}
	}
}

func TestValidateAuth(t *testing.T) {
	withAuth := func(a AuthConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen: "127.0.0.1:80",
				Locations: []LocationConfig{{
					Match:     MatchConfig{Type: "prefix", Path: "/"},
					ProxyPass: "http://127.0.0.1:3000",
					Auth:      &a,
				}},
			}},
		}
	}

	t.Run("valid cidr-only", func(t *testing.T) {
		if err := Validate(withAuth(AuthConfig{Allow: []string{"10.0.0.0/8"}, Deny: []string{"10.9.0.0/16"}})); err != nil {
			t.Errorf("valid CIDR auth rejected: %v", err)
		}
	})
	t.Run("empty auth block enforces nothing", func(t *testing.T) {
		// An auth block with no CIDR gate and no credential method builds an
		// authenticator that falls through and permits every request, while the
		// Console reports the location as protected. Validation must reject it
		// so a guided "Require auth" toggle cannot emit silently-inert "auth = {}".
		err := Validate(withAuth(AuthConfig{}))
		if err == nil {
			t.Fatal("expected error for an auth block that enforces nothing")
		}
		if !strings.Contains(err.Error(), "enforces nothing") {
			t.Errorf("error = %q, want it to mention 'enforces nothing'", err.Error())
		}
	})
	t.Run("invalid allow cidr", func(t *testing.T) {
		if err := Validate(withAuth(AuthConfig{Allow: []string{"not-a-cidr"}})); err == nil {
			t.Error("expected error for invalid allow CIDR")
		}
	})
	t.Run("invalid deny cidr", func(t *testing.T) {
		if err := Validate(withAuth(AuthConfig{Deny: []string{"10.0.0.1"}})); err == nil {
			t.Error("expected error for CIDR missing prefix length")
		}
	})
	t.Run("basic requires file", func(t *testing.T) {
		if err := Validate(withAuth(AuthConfig{Basic: &BasicAuthConfig{Realm: "r"}})); err == nil {
			t.Error("expected error: basic auth requires a file")
		}
	})
	t.Run("jwt requires https jwks_url", func(t *testing.T) {
		if err := Validate(withAuth(AuthConfig{JWT: &JWTAuthConfig{JWKSURL: "http://issuer/jwks", Algorithms: []string{"RS256"}}})); err == nil {
			t.Error("expected error: jwks_url must be https")
		}
	})
	t.Run("jwt rejects none algorithm", func(t *testing.T) {
		a := AuthConfig{JWT: &JWTAuthConfig{JWKSURL: "https://issuer/jwks", Algorithms: []string{"none"}}}
		if err := Validate(withAuth(a)); err == nil {
			t.Error("expected error: 'none' algorithm must be rejected")
		}
	})
	t.Run("jwt rejects symmetric algorithm", func(t *testing.T) {
		a := AuthConfig{JWT: &JWTAuthConfig{JWKSURL: "https://issuer/jwks", Algorithms: []string{"HS256"}}}
		if err := Validate(withAuth(a)); err == nil {
			t.Error("expected error: symmetric HS256 must be rejected")
		}
	})
	t.Run("valid jwt", func(t *testing.T) {
		a := AuthConfig{JWT: &JWTAuthConfig{JWKSURL: "https://issuer/jwks", Algorithms: []string{"RS256", "ES256"}}}
		if err := Validate(withAuth(a)); err != nil {
			t.Errorf("valid jwt auth rejected: %v", err)
		}
	})
	t.Run("forward_auth requires http url", func(t *testing.T) {
		if err := Validate(withAuth(AuthConfig{ForwardAuth: &ForwardAuthConfig{URL: "::bad"}})); err == nil {
			t.Error("expected error: forward_auth url must be http(s)")
		}
	})
	t.Run("at most one credential method", func(t *testing.T) {
		a := AuthConfig{
			Basic: &BasicAuthConfig{File: "/etc/jul/htpasswd"},
			JWT:   &JWTAuthConfig{JWKSURL: "https://issuer/jwks", Algorithms: []string{"RS256"}},
		}
		if err := Validate(withAuth(a)); err == nil {
			t.Error("expected error: only one of basic/jwt/forward_auth may be set")
		}
	})
}

func TestHealthCheckDefaults(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "127.0.0.1:8080"

[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://backend"

[[upstreams]]
name = "backend"
servers = ["127.0.0.1:3000"]
  [upstreams.health_check]
  enabled = true
  path = "/healthz"
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	h := cfg.Upstreams[0].HealthCheck
	if h == nil {
		t.Fatal("health_check block not parsed")
	}
	if h.Type != "http" {
		t.Errorf("type default = %q, want http", h.Type)
	}
	if h.Interval.Std() != 5*time.Second {
		t.Errorf("interval default = %s, want 5s", h.Interval.Std())
	}
	if h.Timeout.Std() != 2*time.Second {
		t.Errorf("timeout default = %s, want 2s", h.Timeout.Std())
	}
	if h.HealthyThreshold != 2 {
		t.Errorf("healthy_threshold default = %d, want 2", h.HealthyThreshold)
	}
	if h.UnhealthyThreshold != 3 {
		t.Errorf("unhealthy_threshold default = %d, want 3", h.UnhealthyThreshold)
	}
	if len(h.ExpectStatus) != 1 || h.ExpectStatus[0] != 200 {
		t.Errorf("expect_status default = %v, want [200]", h.ExpectStatus)
	}
}

func TestHealthCheckDefaultsSkippedWhenDisabled(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "127.0.0.1:8080"

[[servers.locations]]
match = { type = "prefix", path = "/" }
proxy_pass = "http://backend"

[[upstreams]]
name = "backend"
servers = ["127.0.0.1:3000"]
  [upstreams.health_check]
  enabled = false
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	h := cfg.Upstreams[0].HealthCheck
	if h == nil {
		t.Fatal("health_check block not parsed")
	}
	if h.Type != "" || h.Interval != 0 || h.HealthyThreshold != 0 || len(h.ExpectStatus) != 0 {
		t.Errorf("defaults must not be applied to a disabled health_check: %+v", h)
	}
}

func TestValidateHealthCheck(t *testing.T) {
	withHealth := func(h HealthCheckConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen: "127.0.0.1:80",
				Locations: []LocationConfig{{
					Match:     MatchConfig{Type: "prefix", Path: "/"},
					ProxyPass: "http://backend",
				}},
			}},
			Upstreams: []UpstreamConfig{{
				Name:        "backend",
				Strategy:    "round_robin",
				Servers:     []UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}},
				MaxFails:    3,
				HealthCheck: &h,
			}},
		}
	}
	validHTTP := func() HealthCheckConfig {
		return HealthCheckConfig{
			Enabled: true, Type: "http", Path: "/healthz",
			Interval: Duration(5 * time.Second), Timeout: Duration(2 * time.Second),
			HealthyThreshold: 2, UnhealthyThreshold: 3, ExpectStatus: []int{200},
		}
	}

	t.Run("valid http", func(t *testing.T) {
		if err := Validate(withHealth(validHTTP())); err != nil {
			t.Errorf("valid http health check rejected: %v", err)
		}
	})
	t.Run("valid tcp", func(t *testing.T) {
		h := HealthCheckConfig{Enabled: true, Type: "tcp", Interval: Duration(5 * time.Second), Timeout: Duration(2 * time.Second), HealthyThreshold: 1, UnhealthyThreshold: 1}
		if err := Validate(withHealth(h)); err != nil {
			t.Errorf("valid tcp health check rejected: %v", err)
		}
	})
	t.Run("disabled skips validation", func(t *testing.T) {
		h := HealthCheckConfig{Enabled: false, Type: "bogus"}
		if err := Validate(withHealth(h)); err != nil {
			t.Errorf("disabled health check should not be validated: %v", err)
		}
	})
	t.Run("invalid type", func(t *testing.T) {
		h := validHTTP()
		h.Type = "icmp"
		if err := Validate(withHealth(h)); err == nil {
			t.Error("expected error for invalid type")
		}
	})
	t.Run("http requires path", func(t *testing.T) {
		h := validHTTP()
		h.Path = ""
		if err := Validate(withHealth(h)); err == nil {
			t.Error("expected error: http probe requires a path")
		}
	})
	t.Run("timeout must be less than interval", func(t *testing.T) {
		h := validHTTP()
		h.Timeout = Duration(5 * time.Second)
		if err := Validate(withHealth(h)); err == nil {
			t.Error("expected error: timeout must be < interval")
		}
	})
	t.Run("interval must be positive", func(t *testing.T) {
		h := validHTTP()
		h.Interval = 0
		if err := Validate(withHealth(h)); err == nil {
			t.Error("expected error: interval must be > 0")
		}
	})
	t.Run("thresholds at least one", func(t *testing.T) {
		h := validHTTP()
		h.HealthyThreshold = 0
		if err := Validate(withHealth(h)); err == nil {
			t.Error("expected error: healthy_threshold must be >= 1")
		}
	})
	t.Run("invalid expect_status", func(t *testing.T) {
		h := validHTTP()
		h.ExpectStatus = []int{99}
		if err := Validate(withHealth(h)); err == nil {
			t.Error("expected error: expect_status out of range")
		}
	})
}

func TestValidateGRPCTranscode(t *testing.T) {
	descFile := filepath.Join(t.TempDir(), "api.pb")
	if err := os.WriteFile(descFile, []byte("not-a-real-descriptor"), 0o600); err != nil {
		t.Fatalf("write temp descriptor: %v", err)
	}
	withGRPC := func(g *GRPCTranscodeConfig, extra func(*LocationConfig)) *Config {
		loc := LocationConfig{
			Match:         MatchConfig{Type: "prefix", Path: "/v1/"},
			GRPCTranscode: g,
		}
		if extra != nil {
			extra(&loc)
		}
		return &Config{
			Servers: []ServerConfig{{
				Listen:    "127.0.0.1:80",
				Locations: []LocationConfig{loc},
			}},
			Upstreams: []UpstreamConfig{{
				Name:     "grpcbackend",
				Strategy: "round_robin",
				Servers:  []UpstreamServer{{Address: "127.0.0.1:50051", Weight: 1}},
				MaxFails: 3,
			}},
		}
	}

	t.Run("valid with descriptor_set", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "grpcbackend", DescriptorSet: descFile}
		if err := Validate(withGRPC(g, nil)); err != nil {
			t.Errorf("valid grpc_transcode rejected: %v", err)
		}
	})
	t.Run("valid with reflection and host:port", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "127.0.0.1:50051", UseReflection: true}
		if err := Validate(withGRPC(g, nil)); err != nil {
			t.Errorf("valid reflection grpc_transcode rejected: %v", err)
		}
	})
	t.Run("target required", func(t *testing.T) {
		g := &GRPCTranscodeConfig{DescriptorSet: descFile}
		if err := Validate(withGRPC(g, nil)); err == nil {
			t.Error("expected error: target is required")
		}
	})
	t.Run("unknown upstream target", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "nope", DescriptorSet: descFile}
		if err := Validate(withGRPC(g, nil)); err == nil {
			t.Error("expected error: target neither upstream nor host:port")
		}
	})
	t.Run("needs a descriptor source", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "grpcbackend"}
		if err := Validate(withGRPC(g, nil)); err == nil {
			t.Error("expected error: one of descriptor_set or use_reflection")
		}
	})
	t.Run("descriptor sources mutually exclusive", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "grpcbackend", DescriptorSet: descFile, UseReflection: true}
		if err := Validate(withGRPC(g, nil)); err == nil {
			t.Error("expected error: descriptor_set and use_reflection are mutually exclusive")
		}
	})
	t.Run("descriptor_set must exist", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "grpcbackend", DescriptorSet: filepath.Join(t.TempDir(), "missing.pb")}
		if err := Validate(withGRPC(g, nil)); err == nil {
			t.Error("expected error: descriptor_set file does not exist")
		}
	})
	t.Run("conflicts with another action", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "grpcbackend", DescriptorSet: descFile}
		cfg := withGRPC(g, func(l *LocationConfig) { l.Root = "/var/www" })
		if err := Validate(cfg); err == nil {
			t.Error("expected error: conflicting actions (grpc_transcode + root)")
		}
	})
	t.Run("valid stream modes accepted", func(t *testing.T) {
		for _, mode := range []string{"", "ndjson", "sse", "SSE", "NDJSON"} {
			g := &GRPCTranscodeConfig{Target: "grpcbackend", DescriptorSet: descFile, Streaming: true, StreamMode: mode}
			if err := Validate(withGRPC(g, nil)); err != nil {
				t.Errorf("stream_mode %q: unexpected error: %v", mode, err)
			}
		}
	})
	t.Run("invalid stream mode rejected", func(t *testing.T) {
		g := &GRPCTranscodeConfig{Target: "grpcbackend", DescriptorSet: descFile, StreamMode: "xml"}
		if err := Validate(withGRPC(g, nil)); err == nil {
			t.Error("expected error: invalid stream_mode")
		}
	})
}

func TestValidateGRPCPassthrough(t *testing.T) {
	base := func(loc LocationConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen:    "127.0.0.1:80",
				H2C:       true,
				Locations: []LocationConfig{loc},
			}},
		}
	}

	t.Run("grpc passthrough with proxy_pass is valid", func(t *testing.T) {
		cfg := base(LocationConfig{
			Match:     MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://127.0.0.1:50051",
			GRPC:      true,
		})
		if err := Validate(cfg); err != nil {
			t.Errorf("valid grpc passthrough rejected: %v", err)
		}
	})
	t.Run("grpc without proxy_pass is rejected", func(t *testing.T) {
		cfg := base(LocationConfig{
			Match: MatchConfig{Type: "prefix", Path: "/"},
			GRPC:  true,
		})
		if err := Validate(cfg); err == nil {
			t.Error("expected error: grpc = true requires proxy_pass")
		}
	})
}

func TestValidateRedirectReturnCombination(t *testing.T) {
	base := func(loc string) []byte {
		return []byte("[[servers]]\nlisten = \":8080\"\n\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\n" + loc + "\n")
	}
	t.Run("redirect with a 3xx return is valid", func(t *testing.T) {
		cfg, err := Parse(base("redirect = \"https://example.com/\"\nreturn = 301"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("redirect + return 301 should be valid, got: %v", err)
		}
	})
	t.Run("redirect with a non-3xx return is invalid", func(t *testing.T) {
		cfg, err := Parse(base("redirect = \"https://example.com/\"\nreturn = 200"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := Validate(cfg); err == nil {
			t.Error("redirect + return 200 should be invalid (non-3xx status)")
		}
	})
	t.Run("a bare return is valid", func(t *testing.T) {
		cfg, err := Parse(base("return = 404"))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("bare return should be valid, got: %v", err)
		}
	})
}

func TestTracingDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[observability.tracing]\nenabled = true\nendpoint = \"localhost:4317\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tr := cfg.Observability.Tracing
	if tr.Exporter != "otlp-grpc" {
		t.Errorf("default exporter = %q, want otlp-grpc", tr.Exporter)
	}
	if tr.SampleRatio != 1.0 {
		t.Errorf("default sample_ratio = %g, want 1.0", tr.SampleRatio)
	}
	if tr.ServiceName != "jul" {
		t.Errorf("default service_name = %q, want jul", tr.ServiceName)
	}
	if tr.Insecure {
		t.Error("default insecure = true, want false (TLS by default)")
	}
}

func TestTracingDefaultsSkippedWhenDisabled(t *testing.T) {
	cfg, err := Parse([]byte("[observability.tracing]\nenabled = false\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tr := cfg.Observability.Tracing
	if tr.Exporter != "" || tr.SampleRatio != 0 || tr.ServiceName != "" {
		t.Errorf("defaults must not be applied when tracing is disabled: %+v", tr)
	}
}

func TestTracingExplicitValues(t *testing.T) {
	cfg, err := Parse([]byte("[observability.tracing]\nenabled = true\nexporter = \"otlp-http\"\nendpoint = \"http://collector:4318\"\nsample_ratio = 0.25\nservice_name = \"edge\"\ninsecure = true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tr := cfg.Observability.Tracing
	if tr.Exporter != "otlp-http" {
		t.Errorf("exporter = %q, want otlp-http", tr.Exporter)
	}
	if tr.SampleRatio != 0.25 {
		t.Errorf("sample_ratio = %g, want 0.25", tr.SampleRatio)
	}
	if tr.ServiceName != "edge" {
		t.Errorf("service_name = %q, want edge", tr.ServiceName)
	}
	if !tr.Insecure {
		t.Error("insecure = false, want true")
	}
}

func TestValidateTracing(t *testing.T) {
	base := func(tr TracingConfig) *Config {
		return &Config{
			Servers:       []ServerConfig{{Listen: "127.0.0.1:80"}},
			Observability: ObservabilityConfig{Tracing: tr},
		}
	}
	if err := Validate(base(TracingConfig{Enabled: true, Exporter: "otlp-grpc", Endpoint: "localhost:4317", SampleRatio: 1})); err != nil {
		t.Fatalf("valid tracing rejected: %v", err)
	}
	if err := Validate(base(TracingConfig{Enabled: true, Exporter: "jaeger", Endpoint: "localhost:4317"})); err == nil {
		t.Error("expected error for invalid exporter")
	}
	if err := Validate(base(TracingConfig{Enabled: true, Exporter: "otlp-grpc", Endpoint: ""})); err == nil {
		t.Error("expected error for missing endpoint")
	}
	if err := Validate(base(TracingConfig{Enabled: true, Exporter: "otlp-grpc", Endpoint: "x", SampleRatio: 1.5})); err == nil {
		t.Error("expected error for sample_ratio out of range")
	}
	if err := Validate(base(TracingConfig{Enabled: false, Exporter: "jaeger"})); err != nil {
		t.Errorf("disabled tracing must not be validated: %v", err)
	}
}

func TestAccessLogDefaults(t *testing.T) {
	cfg, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:80\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	al := cfg.Observability.AccessLog
	if len(al.Sinks) != 1 || al.Sinks[0] != "stdout" {
		t.Errorf("default sinks = %v, want [stdout]", al.Sinks)
	}
	if al.Format != "text" {
		t.Errorf("default format = %q, want text", al.Format)
	}
	if al.RotateMaxMB != 100 {
		t.Errorf("default rotate_max_mb = %d, want 100", al.RotateMaxMB)
	}
	if al.RotateKeep != 7 {
		t.Errorf("default rotate_keep = %d, want 7", al.RotateKeep)
	}
}

func TestAccessLogExplicitValues(t *testing.T) {
	cfg, err := Parse([]byte("[observability.access_log]\nsinks = [\"stdout\", \"file\"]\nfile = \"/var/log/jul/access.log\"\nformat = \"json\"\nrotate_max_mb = 50\nrotate_keep = 3\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	al := cfg.Observability.AccessLog
	if len(al.Sinks) != 2 || al.Sinks[0] != "stdout" || al.Sinks[1] != "file" {
		t.Errorf("sinks = %v", al.Sinks)
	}
	if al.File != "/var/log/jul/access.log" || al.Format != "json" || al.RotateMaxMB != 50 || al.RotateKeep != 3 {
		t.Errorf("explicit access-log values not preserved: %+v", al)
	}
}

func TestValidateAccessLog(t *testing.T) {
	base := func(al AccessLogConfig) *Config {
		return &Config{
			Servers:       []ServerConfig{{Listen: "127.0.0.1:80"}},
			Observability: ObservabilityConfig{AccessLog: al},
		}
	}
	if err := Validate(base(AccessLogConfig{Sinks: []string{"stdout", "file"}, File: "/tmp/a.log", Format: "json"})); err != nil {
		t.Fatalf("valid access_log rejected: %v", err)
	}
	if err := Validate(base(AccessLogConfig{Sinks: []string{"bogus"}})); err == nil {
		t.Error("expected error for unknown sink")
	}
	if err := Validate(base(AccessLogConfig{Sinks: []string{"file"}})); err == nil {
		t.Error("expected error for file sink without path")
	}
	if err := Validate(base(AccessLogConfig{Sinks: []string{"stdout"}, Format: "xml"})); err == nil {
		t.Error("expected error for invalid format")
	}
	if err := Validate(base(AccessLogConfig{Sinks: []string{"stdout"}, RotateMaxMB: -1})); err == nil {
		t.Error("expected error for negative rotate_max_mb")
	}
	if err := Validate(base(AccessLogConfig{Sinks: []string{"stdout"}, RotateKeep: -1})); err == nil {
		t.Error("expected error for negative rotate_keep")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	src := []byte(`
[global]
log_level = "warn"
log_format = "json"
shutdown_timeout = "45s"

[cache]
enabled = true
default_ttl = "2m"
memory_max_size = "128m"

[[upstreams]]
name = "app"
servers = ["127.0.0.1:3000", "10.0.0.1:80 weight=5"]

[[servers]]
listen = "127.0.0.1:8080"
server_name = "example.com"
max_header_bytes = "1m"
`)
	first, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out, err := Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	second, err := Parse(out)
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, out)
	}

	if second.Global.LogLevel != "warn" || second.Global.LogFormat != "json" {
		t.Errorf("global mismatch: %+v", second.Global)
	}
	if second.Global.ShutdownTimeout.Std() != 45*time.Second {
		t.Errorf("shutdown_timeout = %v, want 45s", second.Global.ShutdownTimeout.Std())
	}
	if !second.Cache.Enabled || second.Cache.DefaultTTL.Std() != 2*time.Minute {
		t.Errorf("cache mismatch: %+v", second.Cache)
	}
	if int64(second.Cache.MemoryMaxSize) != 128*1024*1024 {
		t.Errorf("memory_max_size = %d, want %d", int64(second.Cache.MemoryMaxSize), 128*1024*1024)
	}
	if len(second.Upstreams) != 1 || len(second.Upstreams[0].Servers) != 2 {
		t.Fatalf("upstreams mismatch: %+v", second.Upstreams)
	}
	if second.Upstreams[0].Servers[1].Address != "10.0.0.1:80" || second.Upstreams[0].Servers[1].Weight != 5 {
		t.Errorf("upstream server weight not preserved: %+v", second.Upstreams[0].Servers[1])
	}
	if len(second.Servers) != 1 || int64(second.Servers[0].MaxHeaderBytes) != 1024*1024 {
		t.Errorf("server mismatch: %+v", second.Servers)
	}
}

func TestACMEDefaults(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com", "www.example.com"]
  [servers.tls.acme]
  enabled = true
  email = "ops@example.com"
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	a := cfg.Servers[0].TLS.ACME
	if a == nil {
		t.Fatal("acme block not parsed")
	}
	if !cfg.Servers[0].TLS.Enabled {
		t.Error("enabling acme must imply tls.enabled = true")
	}
	if a.CA != "letsencrypt-staging" {
		t.Errorf("default ca = %q, want letsencrypt-staging (safe default)", a.CA)
	}
	if a.Challenge != "http-01" {
		t.Errorf("default challenge = %q, want http-01", a.Challenge)
	}
	if a.CacheDir != "./jul-data/certs" {
		t.Errorf("default cache_dir = %q", a.CacheDir)
	}
	if len(a.Domains) != 2 || a.Domains[0] != "example.com" {
		t.Errorf("domains should default to server_names, got %v", a.Domains)
	}
	if a.OCSPStapling == nil || !*a.OCSPStapling {
		t.Errorf("ocsp_stapling should default to enabled (materialized true), got %v", a.OCSPStapling)
	}
	if !a.OCSPStaplingEnabled() {
		t.Error("OCSPStaplingEnabled() should be true by default")
	}
}

func TestACMEOCSPDisabledAndDNSProviderParsed(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com"]
  [servers.tls.acme]
  enabled = true
  email = "ops@example.com"
  ocsp_stapling = false
  dns_provider = "cloudflare"
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	a := cfg.Servers[0].TLS.ACME
	if a.OCSPStapling == nil || *a.OCSPStapling {
		t.Errorf("explicit ocsp_stapling = false must be preserved, got %v", a.OCSPStapling)
	}
	if a.OCSPStaplingEnabled() {
		t.Error("OCSPStaplingEnabled() should be false when explicitly disabled")
	}
	if a.DNSProvider != "cloudflare" {
		t.Errorf("dns_provider = %q, want cloudflare", a.DNSProvider)
	}
}

func TestACMEDefaultsSkippedWhenDisabled(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:443"
server_names = ["example.com"]
  [servers.tls.acme]
  enabled = false
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if a := cfg.Servers[0].TLS.ACME; a.CA != "" || a.Challenge != "" || len(a.Domains) != 0 {
		t.Errorf("defaults must not be applied when acme is disabled: %+v", a)
	}
}

func acmeServer(a *ACMEConfig, names ...string) *Config {
	return &Config{
		Servers: []ServerConfig{{
			Listen:      "0.0.0.0:443",
			ServerNames: names,
			TLS:         &TLSConfig{Enabled: true, ACME: a},
			Locations:   []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
		}},
	}
}

func TestHTTP3Defaults(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:443"

[servers.tls]
enabled = true
cert = "/etc/cert.pem"
key = "/etc/key.pem"

[servers.http3]
enabled = true

[[servers.locations]]
match = { type = "prefix", path = "/" }
root = "/srv"
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	h := cfg.Servers[0].HTTP3
	if h == nil || !h.Enabled {
		t.Fatal("http3 block not parsed/enabled")
	}
	if h.AltSvcMaxAge != 86400 {
		t.Errorf("alt_svc_max_age default = %d, want 86400", h.AltSvcMaxAge)
	}
}

func TestHTTP3DefaultsKeepExplicitMaxAge(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:443"

[servers.tls]
enabled = true
cert = "/etc/cert.pem"
key = "/etc/key.pem"

[servers.http3]
enabled = true
alt_svc_max_age = 3600
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Servers[0].HTTP3.AltSvcMaxAge; got != 3600 {
		t.Errorf("alt_svc_max_age = %d, want 3600 (explicit value kept)", got)
	}
}

func TestHTTP3DefaultsNoopWhenDisabled(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:80"

[servers.http3]
enabled = false

[[servers.locations]]
match = { type = "prefix", path = "/" }
root = "/srv"
`)
	cfg, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := cfg.Servers[0].HTTP3.AltSvcMaxAge; got != 0 {
		t.Errorf("alt_svc_max_age = %d, want 0 (no defaults for a disabled block)", got)
	}
}

func TestValidateHTTP3(t *testing.T) {
	http3Server := func(h *HTTP3Config, tlsCfg *TLSConfig) *Config {
		return &Config{
			Servers: []ServerConfig{{
				Listen:    "0.0.0.0:443",
				TLS:       tlsCfg,
				HTTP3:     h,
				Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
			}},
		}
	}
	tlsOn := &TLSConfig{Enabled: true, Cert: "/etc/cert.pem", Key: "/etc/key.pem"}

	t.Run("valid with static TLS", func(t *testing.T) {
		if err := Validate(http3Server(&HTTP3Config{Enabled: true, AltSvcMaxAge: 86400}, tlsOn)); err != nil {
			t.Errorf("http3 with TLS rejected: %v", err)
		}
	})
	t.Run("rejected without TLS", func(t *testing.T) {
		cfg := &Config{
			Servers: []ServerConfig{{
				Listen:    "0.0.0.0:80",
				HTTP3:     &HTTP3Config{Enabled: true, AltSvcMaxAge: 86400},
				Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
			}},
		}
		if err := Validate(cfg); err == nil {
			t.Error("expected error: http3 requires TLS on the same server block")
		}
	})
	t.Run("rejected when TLS present but disabled", func(t *testing.T) {
		if err := Validate(http3Server(&HTTP3Config{Enabled: true}, &TLSConfig{Enabled: false})); err == nil {
			t.Error("expected error: http3 requires TLS enabled, not merely present")
		}
	})
	t.Run("rejected negative max age", func(t *testing.T) {
		if err := Validate(http3Server(&HTTP3Config{Enabled: true, AltSvcMaxAge: -1}, tlsOn)); err == nil {
			t.Error("expected error: alt_svc_max_age must be >= 0")
		}
	})
	t.Run("disabled block is a no-op on a plain listener", func(t *testing.T) {
		cfg := &Config{
			Servers: []ServerConfig{{
				Listen:    "0.0.0.0:80",
				HTTP3:     &HTTP3Config{Enabled: false},
				Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
			}},
		}
		if err := Validate(cfg); err != nil {
			t.Errorf("disabled http3 on plain listener rejected: %v", err)
		}
	})
}

func TestValidateACMEValid(t *testing.T) {
	for _, ch := range []string{"http-01", "tls-alpn-01"} {
		cfg := acmeServer(&ACMEConfig{Enabled: true, Email: "ops@example.com", CA: "letsencrypt", Challenge: ch, Domains: []string{"example.com"}}, "example.com")
		if err := Validate(cfg); err != nil {
			t.Fatalf("valid acme config (challenge %q) rejected: %v", ch, err)
		}
	}
}

func TestValidateACMERequiresEmail(t *testing.T) {
	cfg := acmeServer(&ACMEConfig{Enabled: true, CA: "letsencrypt-staging", Challenge: "http-01", Domains: []string{"example.com"}}, "example.com")
	if err := Validate(cfg); err == nil {
		t.Error("expected error: acme requires email")
	}
}

func TestValidateACMERejectsStaticCert(t *testing.T) {
	cfg := acmeServer(&ACMEConfig{Enabled: true, Email: "ops@example.com", CA: "letsencrypt", Challenge: "http-01", Domains: []string{"example.com"}}, "example.com")
	cfg.Servers[0].TLS.Cert = "/etc/cert.pem"
	cfg.Servers[0].TLS.Key = "/etc/key.pem"
	if err := Validate(cfg); err == nil {
		t.Error("expected error: acme and static cert/key are mutually exclusive")
	}
}

func TestValidateACMERejectsUnsupportedChallenge(t *testing.T) {
	// dns-01 is reserved for a future release (rejected today); bogus is invalid.
	// tls-alpn-01 is intentionally absent here — it is now a supported challenge.
	for _, ch := range []string{"dns-01", "bogus"} {
		cfg := acmeServer(&ACMEConfig{Enabled: true, Email: "ops@example.com", CA: "letsencrypt", Challenge: ch, Domains: []string{"example.com"}}, "example.com")
		if err := Validate(cfg); err == nil {
			t.Errorf("expected error for challenge %q (only http-01 and tls-alpn-01 are supported)", ch)
		}
	}
}

func TestValidateACMERejectsBadCA(t *testing.T) {
	cfg := acmeServer(&ACMEConfig{Enabled: true, Email: "ops@example.com", CA: "http://insecure", Challenge: "http-01", Domains: []string{"example.com"}}, "example.com")
	if err := Validate(cfg); err == nil {
		t.Error("expected error: ca must be a known name or an https directory URL")
	}
}

func TestValidateACMERejectsMixedListener(t *testing.T) {
	cfg := &Config{
		Servers: []ServerConfig{
			{
				Listen:      "0.0.0.0:443",
				ServerNames: []string{"acme.example.com"},
				TLS:         &TLSConfig{Enabled: true, ACME: &ACMEConfig{Enabled: true, Email: "ops@example.com", CA: "letsencrypt", Challenge: "http-01", Domains: []string{"acme.example.com"}}},
				Locations:   []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
			},
			{
				Listen:      "0.0.0.0:443",
				ServerNames: []string{"static.example.com"},
				TLS:         &TLSConfig{Enabled: true, Cert: "/etc/cert.pem", Key: "/etc/key.pem"},
				Locations:   []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
			},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Error("expected error: a listener cannot mix ACME and static TLS")
	}
}

// clientAuthServer builds a TLS-enabled static-cert server with the given
// client_auth block and an optional require_client_cert on its single location.
func clientAuthServer(ca *ClientAuthConfig, requireLoc bool) *Config {
	return &Config{
		Servers: []ServerConfig{{
			Listen:      "0.0.0.0:443",
			ServerNames: []string{"mtls.example.com"},
			TLS:         &TLSConfig{Enabled: true, Cert: "/etc/cert.pem", Key: "/etc/key.pem", ClientAuth: ca},
			Locations: []LocationConfig{{
				Match:             MatchConfig{Type: "prefix", Path: "/"},
				Root:              "/srv",
				RequireClientCert: requireLoc,
			}},
		}},
	}
}

func TestValidateClientAuthValid(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"request", "require"} {
		cfg := clientAuthServer(&ClientAuthConfig{Mode: mode, CAFile: caFile}, true)
		if err := Validate(cfg); err != nil {
			t.Errorf("mode %q: unexpected error: %v", mode, err)
		}
	}
}

func TestValidateClientAuthBadMode(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(caFile, []byte("ca"), 0o600)
	cfg := clientAuthServer(&ClientAuthConfig{Mode: "bogus", CAFile: caFile}, false)
	if err := Validate(cfg); err == nil {
		t.Error("expected error for an invalid client_auth mode")
	}
}

func TestValidateClientAuthRequiresCAFile(t *testing.T) {
	cfg := clientAuthServer(&ClientAuthConfig{Mode: "require"}, false)
	if err := Validate(cfg); err == nil {
		t.Error("expected error: require mode needs a ca_file")
	}
}

func TestValidateClientAuthMissingCAFile(t *testing.T) {
	cfg := clientAuthServer(&ClientAuthConfig{Mode: "require", CAFile: "/no/such/ca.pem"}, false)
	if err := Validate(cfg); err == nil {
		t.Error("expected error: ca_file must be readable")
	}
}

func TestValidateClientAuthRequiresTLS(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	_ = os.WriteFile(caFile, []byte("ca"), 0o600)
	cfg := &Config{
		Servers: []ServerConfig{{
			Listen: "0.0.0.0:8080",
			TLS:    &TLSConfig{Enabled: false, ClientAuth: &ClientAuthConfig{Mode: "require", CAFile: caFile}},
		}},
	}
	if err := Validate(cfg); err == nil {
		t.Error("expected error: client_auth requires tls.enabled")
	}
}

func TestValidateRequireClientCertNeedsClientAuth(t *testing.T) {
	cfg := &Config{
		Servers: []ServerConfig{{
			Listen:      "0.0.0.0:443",
			ServerNames: []string{"x.example.com"},
			TLS:         &TLSConfig{Enabled: true, Cert: "/etc/cert.pem", Key: "/etc/key.pem"},
			Locations: []LocationConfig{{
				Match:             MatchConfig{Type: "prefix", Path: "/"},
				Root:              "/srv",
				RequireClientCert: true,
			}},
		}},
	}
	if err := Validate(cfg); err == nil {
		t.Error("expected error: require_client_cert needs the server's client_auth enabled")
	}
}

func TestParseClientAuthTOML(t *testing.T) {
	src := []byte(`
[[servers]]
listen = "0.0.0.0:443"
server_names = ["mtls.example.com"]

[servers.tls]
enabled = true
cert = "/etc/cert.pem"
key = "/etc/key.pem"

[servers.tls.client_auth]
mode = "require"
ca_file = "/etc/clients-ca.pem"
verify_san = ["svc.example.com"]
crl_file = "/etc/clients.crl"

[[servers.locations]]
match = { type = "prefix", path = "/secure" }
proxy_pass = "http://app"
require_client_cert = true
`)
	var cfg Config
	if err := toml.Unmarshal(src, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ca := cfg.Servers[0].TLS.ClientAuth
	if ca == nil {
		t.Fatal("client_auth not parsed")
	}
	if ca.Mode != "require" || ca.CAFile != "/etc/clients-ca.pem" || ca.CRLFile != "/etc/clients.crl" {
		t.Errorf("client_auth = %+v", ca)
	}
	if len(ca.VerifySAN) != 1 || ca.VerifySAN[0] != "svc.example.com" {
		t.Errorf("verify_san = %v", ca.VerifySAN)
	}
	if !ca.Active() {
		t.Error("Active() should be true for mode require")
	}
	if !cfg.Servers[0].Locations[0].RequireClientCert {
		t.Error("require_client_cert not parsed")
	}
}

func TestAdminPluginUploadDisabled(t *testing.T) {
	cfg, err := Parse([]byte("[admin]\nenabled = true\nplugin_upload_enabled = false\nplugin_upload_max_size = 32\n\n[[servers]]\nlisten = \":8080\"\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("validate rejected valid upload-disabled config: %v", err)
	}
	if cfg.Admin.PluginUploadEnabled == nil || *cfg.Admin.PluginUploadEnabled {
		t.Fatal("PluginUploadEnabled should be false")
	}
	if cfg.Admin.PluginUploadMaxSize != 32 {
		t.Fatalf("PluginUploadMaxSize = %d, want 32", cfg.Admin.PluginUploadMaxSize)
	}
}
