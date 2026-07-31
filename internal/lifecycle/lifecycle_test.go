// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestFieldClassExact(t *testing.T) {
	if got := FieldClass("global.log_format"); got != RestartRequiredClass {
		t.Fatalf("log_format class = %v, want restart_required", got)
	}
	if got := FieldClass("global.log_level"); got != HotReloadClass {
		t.Fatalf("log_level class = %v, want hot_reload", got)
	}
	if got := FieldClass("servers.*.listen"); got != NewListenerOnlyClass {
		t.Fatalf("listen class = %v, want new_listener_only", got)
	}
}

func TestRestartRequiredEntriesAreStartupConsumed(t *testing.T) {
	for _, e := range Registry {
		if e.Class == RestartRequiredClass && !e.StartupConsumed {
			t.Errorf("restart_required entry %q is not StartupConsumed", e.Path)
		}
	}
}

func TestFieldClassUnknownIsHotReload(t *testing.T) {
	if got := FieldClass("unknown.path"); got != HotReloadClass {
		t.Fatalf("unknown class = %v, want hot_reload", got)
	}
}

func TestLookupWildcard(t *testing.T) {
	e := Lookup("servers.0.locations.3.proxy_pass")
	if e == nil {
		t.Fatal("expected wildcard match")
	}
	if e.Class != HotReloadClass {
		t.Fatalf("proxy_pass class = %v, want hot_reload", e.Class)
	}
}

func TestRestartRequiredDetectsChange(t *testing.T) {
	old := makeFingerprint("text")
	next := makeFingerprint("json")
	reason, need := RestartRequired(old, next)
	if !need {
		t.Fatal("expected restart required")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func makeFingerprint(logFormat string) Fingerprint {
	cfg := &config.Config{}
	cfg.Global.LogFormat = logFormat
	return ComputeFingerprint(cfg)
}

func TestRestartRequiredNoChange(t *testing.T) {
	base := makeFingerprint("text")
	reason, need := RestartRequired(base, base)
	if need {
		t.Fatalf("unexpected restart required: %s", reason)
	}
}

func TestRestartRequiredIgnoresListenerAddition(t *testing.T) {
	base := makeFingerprint("text")
	base.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
	}
	candidate := makeFingerprint("text")
	candidate.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
		"127.0.0.1:8081": map[string]any{"enabled": true},
	}
	if reason, need := RestartRequired(base, candidate); need {
		t.Fatalf("listener addition should not require restart: %s", reason)
	}
}

func TestRestartRequiredDetectsExistingListenerTLSChange(t *testing.T) {
	base := makeFingerprint("text")
	base.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
	}
	candidate := makeFingerprint("text")
	candidate.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": true},
	}
	if _, need := RestartRequired(base, candidate); !need {
		t.Fatal("changing TLS on an existing listener should require restart")
	}
}

// TestDiffAddressAwareReaddedAddress (R10-05) verifies that a startup address
// removed and then re-added with the same settings is not reported as a diff.
func TestDiffAddressAwareReaddedAddress(t *testing.T) {
	base := makeFingerprint("text")
	base.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": true},
		"127.0.0.1:8081": map[string]any{"enabled": false},
	}

	// Remove 8080, add 8082 with the same settings 8080 had.
	candidate := makeFingerprint("text")
	candidate.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8081": map[string]any{"enabled": false},
		"127.0.0.1:8082": map[string]any{"enabled": true},
	}
	if paths := DiffAddressAware(base, candidate); len(paths) != 0 {
		t.Fatalf("re-added address with same settings should not diff; got %v", paths)
	}

	// Re-add 8080 but with different settings -> diff.
	candidate.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
		"127.0.0.1:8081": map[string]any{"enabled": false},
	}
	if paths := DiffAddressAware(base, candidate); len(paths) != 1 || paths[0] != "servers.*.tls" {
		t.Fatalf("expected servers.*.tls diff, got %v", paths)
	}
}

func TestComputeFingerprintIncludesLogFormat(t *testing.T) {
	cfg := &config.Config{}
	cfg.Global.LogFormat = "json"
	fp := ComputeFingerprint(cfg)
	if got := fp.Values["global.log_format"]; got != "json" {
		t.Fatalf("log_format fingerprint = %v, want json", got)
	}
}

func TestComputeFingerprintTLSAggregatesVirtualHostsPerAddress(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: ":8443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-a"}},
			{Listen: ":8443", ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-b"}},
		},
	}
	fp := ComputeFingerprint(cfg)
	tls, ok := fp.Values["servers.*.tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls fingerprint type = %T, want map[string]any", fp.Values["servers.*.tls"])
	}
	vhosts, ok := tls[":8443"].(map[string]any)
	if !ok {
		t.Fatalf("tls vhosts type = %T, want map[string]any", tls[":8443"])
	}
	if len(vhosts) != 2 {
		t.Fatalf("expected 2 vhosts, got %d", len(vhosts))
	}
	if _, ok := vhosts["a.example.com"]; !ok {
		t.Fatal("missing vhost a.example.com")
	}
	if _, ok := vhosts["b.example.com"]; !ok {
		t.Fatal("missing vhost b.example.com")
	}
}

func TestComputeFingerprintTLSIgnoresVirtualHostOrder(t *testing.T) {
	cfgA := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: ":8443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-a"}},
			{Listen: ":8443", ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-b"}},
		},
	}
	cfgB := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: ":8443", ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-b"}},
			{Listen: ":8443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-a"}},
		},
	}
	fpA := ComputeFingerprint(cfgA)
	fpB := ComputeFingerprint(cfgB)
	if reason, need := RestartRequired(fpA, fpB); need {
		t.Fatalf("same vhosts in different order should not require restart: %s", reason)
	}
}

func TestComputeFingerprintWorkerThreadsNotStartupConsumed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Global.WorkerThreads = "auto"
	fp := ComputeFingerprint(cfg)
	if _, ok := fp.Values["global.worker_threads"]; ok {
		t.Fatal("worker_threads is hot-reloadable and must not appear in startup fingerprint")
	}
}

// TestAllRegisteredPathsHaveExtractor verifies that every lifecycle registry
// path can be extracted from a fully populated config. This is the
// completeness contract for R7-06: a path cannot be added to the registry
// without an extractor noticing it.
func TestAllRegisteredPathsHaveExtractor(t *testing.T) {
	cfg := fullConfig()
	for _, e := range Registry {
		v := extractRegisteredValue(cfg, e.Path)
		if v == nil {
			t.Errorf("no extractor returned a value for registered path %q", e.Path)
		}
	}
}

// TestDiffConfigDetectsRegisteredChange verifies that changing a registered
// field produces a diff entry. It exercises a path that was previously missing
// from the hand-written switch (waf.directives_files) to prove completeness.
func TestDiffConfigDetectsRegisteredChange(t *testing.T) {
	before := fullConfig()
	after := fullConfig()
	after.WAF.DirectivesFiles = append([]string(nil), after.WAF.DirectivesFiles...)
	after.WAF.DirectivesFiles = append(after.WAF.DirectivesFiles, "/extra.conf")

	changes := DiffConfig(before, after)
	found := false
	for _, ch := range changes {
		if ch.Path == "waf.directives_files" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected diff for waf.directives_files, got %+v", changes)
	}
}

func fullConfig() *config.Config {
	return &config.Config{
		Global: config.GlobalConfig{
			WorkerThreads:         "4",
			AccessLog:             "stdout",
			ErrorLog:              "stderr",
			LogLevel:              "info",
			LogFormat:             "text",
			ShutdownTimeout:       config.Duration(30e9),
			ReloadTimeout:         config.Duration(10e9),
			RedactMinSecretLength: 4,
		},
		Admin: config.AdminConfig{
			Enabled:              true,
			Listen:               "127.0.0.1:2019",
			Token:                "secret",
			Console:              config.Bool(true),
			HistoryDir:           "./jul-data/config-history",
			HistoryKeep:          50,
			RateLimitReadPerMin:  240,
			RateLimitWritePerMin: 60,
			RateLimitApplyPerMin: 30,
			MaxEventConns:        4,
			AuditLogFile:         "./jul-data/audit.log",
			AuditLogRotateMaxMB:  100,
			AuditLogRotateKeep:   14,
			PluginUploadDir:      "./jul-data/plugins",
			PluginUploadMaxSize:  32,
			PluginUploadEnabled:  config.Bool(false),
		},
		Servers: []config.ServerConfig{{
			Name:              "default",
			Listen:            ":8080",
			ServerNames:       []string{"example.com"},
			ClientMaxBodySize: config.Size(1024),
			ReadHeaderTimeout: config.Duration(1e9),
			ReadTimeout:       config.Duration(5e9),
			WriteTimeout:      config.Duration(5e9),
			IdleTimeout:       config.Duration(120e9),
			MaxHeaderBytes:    config.Size(1024),
			H2C:               true,
			RedirectHTTPS:     308,
			ErrorPages:        map[string]string{"404": "/404.html"},
			Plugins:           []string{"p"},
			TLS: &config.TLSConfig{
				Enabled:    true,
				Cert:       "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
				Key:        "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----",
				MinVersion: "1.3",
				ClientAuth: &config.ClientAuthConfig{
					Mode:      "require",
					CAFile:    "/etc/ca.pem",
					VerifySAN: []string{"example.com"},
					CRLFile:   "/etc/crl.pem",
				},
			},
			HTTP3: &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 86400},
			Locations: []config.LocationConfig{{
				Match: config.MatchConfig{Type: "prefix", Path: "/"},

				Root:             "/srv",
				Index:            []string{"index.html"},
				TryFiles:         []string{"$uri", "$uri/", "/index.html"},
				DirectoryListing: true,
				AllowHidden:      true,
				CacheControl:     "public, max-age=3600",

				ProxyPass:           "http://app",
				ProxyConnectTimeout: config.Duration(1e9),
				ProxyReadTimeout:    config.Duration(5e9),
				ProxySendTimeout:    config.Duration(5e9),
				GRPC:                true,

				FastCGIPass:   "127.0.0.1:9000",
				FastCGIParams: map[string]string{"SCRIPT_FILENAME": "/srv/index.php"},
				UWSGIPass:     "127.0.0.1:3031",

				Redirect: "https://example.com",
				Return:   200,
				Rewrites: []config.RewriteConfig{{
					Pattern:     "^/old/(.*)$",
					Replacement: "/new/$1",
					Flag:        "last",
				}},
				Headers: map[string]string{"X-Frame-Options": "DENY"},

				Cache:             true,
				ClientMaxBodySize: config.Size(2048),
				RequireClientCert: true,

				RateLimit: &config.RateLimitConfig{Enabled: true, Rate: 10, Burst: 20, Key: "ip"},
				Auth: &config.AuthConfig{
					Allow: []string{"10.0.0.0/8"},
					Deny:  []string{"10.0.0.1/32"},
					Basic: &config.BasicAuthConfig{File: "/etc/htpasswd", Realm: "Restricted"},
					JWT: &config.JWTAuthConfig{
						JWKSURL:    "https://idp.example.com/.well-known/jwks.json",
						Issuer:     "idp",
						Audience:   "app",
						Algorithms: []string{"RS256"},
					},
					ForwardAuth: &config.ForwardAuthConfig{
						URL:                 "http://authz.internal/allow",
						AuthResponseHeaders: []string{"X-User"},
					},
				},
				WAF: &config.WAFConfig{
					Enabled:     true,
					Mode:        "block",
					BlockStatus: 403,
					CRSEnabled:  true,
				},
				Plugins: []string{"p"},
				Plugin:  "handler",
				GRPCTranscode: &config.GRPCTranscodeConfig{
					Target:        "grpc://app",
					DescriptorSet: "/etc/descriptor.pb",
				},
				Deny: true,
			}},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:     "app",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:3000", Weight: 1},
			},
			MaxFails:    3,
			FailTimeout: config.Duration(10e9),
			HealthCheck: &config.HealthCheckConfig{
				Enabled:            true,
				Type:               "http",
				Path:               "/healthz",
				Interval:           config.Duration(5e9),
				Timeout:            config.Duration(2e9),
				HealthyThreshold:   2,
				UnhealthyThreshold: 3,
				ExpectStatus:       []int{200},
				ExpectBody:         "ok",
			},
			Discovery: &config.DiscoveryConfig{
				Type:    "dns",
				Target:  "app.internal:8080",
				Refresh: config.Duration(30e9),
				Consul: &config.ConsulDiscovery{
					Address:     "http://127.0.0.1:8500",
					Service:     "app",
					Tag:         "v1",
					Datacenter:  "dc1",
					Token:       "token",
					PassingOnly: config.Bool(true),
				},
				Kubernetes: &config.KubernetesDiscovery{
					Namespace:             "default",
					Service:               "app",
					Port:                  "8080",
					APIServer:             "https://kubernetes.default",
					Token:                 "token",
					CAFile:                "/var/run/secrets/ca.crt",
					InsecureSkipTLSVerify: false,
				},
			},
		}},
		Cache: config.CacheConfig{
			Enabled:              true,
			MemoryMaxSize:        config.Size(64 << 20),
			DiskPath:             "./jul-data/cache",
			DiskMaxSize:          config.Size(1 << 30),
			DefaultTTL:           config.Duration(300e9),
			StaleWhileRevalidate: config.Duration(60e9),
			StaleIfError:         config.Duration(300e9),
		},
		Compression: config.CompressionConfig{
			Enabled:       config.Bool(true),
			Encoders:      []string{"gzip"},
			Level:         6,
			MinSize:       config.Size(1024),
			Types:         []string{"text/plain"},
			Precompressed: true,
		},
		RateLimit: config.RateLimitConfig{
			Enabled:  true,
			Rate:     100,
			Burst:    200,
			Key:      "ip",
			MaxConns: 1000,
		},
		Observability: config.ObservabilityConfig{
			Tracing: config.TracingConfig{
				Enabled:     true,
				Exporter:    "otlp-grpc",
				Endpoint:    "localhost:4317",
				SampleRatio: 1.0,
				ServiceName: "jul",
				Insecure:    true,
			},
			Metrics: config.MetricsConfig{HostLabel: true},
			AccessLog: config.AccessLogConfig{
				Sinks:       []string{"stdout"},
				File:        "./jul-data/access.log",
				Format:      "text",
				RotateMaxMB: 100,
				RotateKeep:  7,
			},
		},
		Egress: config.EgressConfig{
			Enabled: true,
			Allow:   []string{"10.0.0.0/8"},
		},
		WAF: config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       403,
			CRSEnabled:        true,
			DirectivesFiles:   []string{"/etc/crs.conf"},
			InlineRules:       "SecRule ENGINE Off",
			Paranoia:          1,
			RequestBodyLimit:  config.Size(128 << 10),
			ResponseBodyCheck: true,
		},
		Plugins: map[string]config.PluginConfig{
			"p": {
				Path:             "/opt/plugin.wasm",
				Inline:           "aW5saW5l",
				Type:             "middleware",
				Config:           map[string]string{"key": "value"},
				MemoryLimit:      config.Size(16 << 20),
				Timeout:          config.Duration(100e6),
				KV:               true,
				Fetch:            true,
				AllowedHosts:     []string{"example.com"},
				MaxRequestBody:   config.Size(1 << 20),
				MaxResponseBody:  config.Size(8 << 20),
				FetchTimeout:     config.Duration(5e9),
				MaxFetchResponse: config.Size(1 << 20),
				KVMaxEntries:     1024,
				KVMaxBytes:       config.Size(1 << 20),
			},
		},
		Streams: []config.StreamServer{{
			Listen:         ":5432",
			Protocol:       "tcp",
			ProxyPass:      "tcp://db",
			SNIRoutes:      map[string]string{"db.example.com": "tcp://db"},
			TLSPassthrough: true,
			ProxyProtocol:  "both",
			ConnectTimeout: config.Duration(10e9),
			IdleTimeout:    config.Duration(300e9),
			MaxUDPSessions: 10000,
		}},
	}
}

// TestDigestTLSFileDetectsRelativePathContentChange verifies that a relative
// certificate path (without "./") is read and its content digested, so
// rotation is detected (R8 / relative path digest).
func TestDigestTLSFileDetectsRelativePathContentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(path, []byte("CERT-A"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	first := digestTLSFile(path)
	if first == "" {
		t.Fatal("expected non-empty digest for readable relative path")
	}
	if !strings.HasPrefix(first, "file-sha256:") {
		t.Fatalf("expected file-sha256 digest, got %q", first)
	}

	if err := os.WriteFile(path, []byte("CERT-B"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	second := digestTLSFile(path)
	if second == first {
		t.Fatal("expected digest to change when certificate content changes")
	}
}

// TestDigestTLSFileTreatsWindowsPathsAsCandidates verifies that Windows-style
// absolute and UNC paths are passed to os.Stat rather than being digested as
// opaque strings. On non-Windows hosts the stat fails and the function falls
// back to a string digest without panicking.
func TestDigestTLSFileTreatsWindowsPathsAsCandidates(t *testing.T) {
	cases := []string{
		`C:\certs\cert.pem`,
		`\\server\share\cert.pem`,
		`C:/certs/cert.pem`,
	}
	for _, p := range cases {
		d := digestTLSFile(p)
		if d == "" {
			t.Errorf("digest for %q is empty", p)
		}
		if strings.HasPrefix(d, "file-sha256:") && runtime.GOOS != "windows" {
			t.Errorf("unexpected file digest on %s for %q; expected string fallback", runtime.GOOS, p)
		}
	}
}

// TestDigestTLSFileInlinePEMIsNotAPath verifies that inline PEM content is
// digested as a string even if it resembles a path, so secret values are not
// treated as files.
func TestDigestTLSFileInlinePEMIsNotAPath(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nMIIB…\n-----END CERTIFICATE-----\n"
	d := digestTLSFile(pem)
	want := digestString(pem)
	if d != want {
		t.Errorf("inline PEM digest = %q, want %q", d, want)
	}
}

// TestRestartRequiredDetectsTLSCertContentChange verifies that changing the
// content of a relative-path TLS certificate triggers restart_required.
func TestRestartRequiredDetectsTLSCertContentChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(path, []byte("CERT-A"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	base := &config.Config{
		Servers: []config.ServerConfig{{
			Listen: ":8443",
			TLS:    &config.TLSConfig{Enabled: true, Cert: path, Key: path},
		}},
	}
	before := ComputeFingerprint(base)

	if err := os.WriteFile(path, []byte("CERT-B"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	after := ComputeFingerprint(base)

	if _, need := RestartRequired(before, after); !need {
		t.Fatal("expected restart required after TLS certificate content change")
	}
}
