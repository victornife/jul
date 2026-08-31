// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"testing"
	"time"

	"jul/internal/config"
)

// strPtr returns a pointer to s, for the schema's optional string fields where
// an omitted value and an explicitly empty one are different configurations.
func strPtr(s string) *string { return &s }

// durationPtr returns a pointer to a config.Duration of d, for optional
// duration fields with the same omitted-vs-zero distinction as strPtr.
func durationPtr(d time.Duration) *config.Duration {
	cd := config.Duration(d)
	return &cd
}

// fullConfig returns a configuration that populates every public schema leaf
// with a non-zero value. It is the reachability fixture for the closed-world
// contract: TestFullyPopulatedFixtureReachesEveryEntry fails when a registered
// path cannot be extracted from it, which is what happens when a new field is
// registered without an extractor or with a mistyped path.
func fullConfig() *config.Config {
	return &config.Config{
		Global: config.GlobalConfig{
			WorkerThreads:         "4",
			AccessLog:             "stdout",
			ErrorLog:              "stderr",
			LogLevel:              "info",
			LogFormat:             "text",
			ConfigAuthority:       "managed",
			ShutdownTimeout:       config.Duration(30 * time.Second),
			ReloadTimeout:         config.Duration(10 * time.Second),
			RedactMinSecretLength: 4,
		},
		Admin: config.AdminConfig{
			Enabled:              true,
			Listen:               "127.0.0.1:2019",
			Token:                "shared-token",
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
			TLS: &config.AdminTLSConfig{
				Enabled:    true,
				Cert:       "admin-cert.pem",
				Key:        "admin-key.pem",
				MinVersion: "1.3",
			},
			RBAC: config.AdminRBACConfig{
				Enabled:     true,
				DefaultRole: "admin",
				Roles: []config.AdminRole{{
					Name:        "release",
					Permissions: []string{"config:read", "config:apply"},
				}},
				Principals: []config.AdminPrincipal{{
					Name:      "alice",
					Role:      "release",
					Token:     "principal-token",
					Disabled:  false,
					ExpiresAt: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC),
				}},
			},
		},
		Servers: []config.ServerConfig{{
			Name:              "default",
			Listen:            ":8443",
			ServerNames:       []string{"example.com"},
			ClientMaxBodySize: config.Size(1024),
			ReadHeaderTimeout: config.Duration(time.Second),
			ReadTimeout:       config.Duration(5 * time.Second),
			WriteTimeout:      config.Duration(5 * time.Second),
			IdleTimeout:       config.Duration(120 * time.Second),
			MaxHeaderBytes:    config.Size(1024),
			H2C:               true,
			AccessLog:         "legacy-access.log",
			ErrorLog:          "legacy-error.log",
			RedirectHTTPS:     308,
			ErrorPages:        map[string]string{"404": "/404.html"},
			Plugins:           []string{"p"},
			TLS: &config.TLSConfig{
				Enabled:    true,
				Cert:       "-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----",
				Key:        "-----BEGIN PRIVATE KEY-----\nMIIE...\n-----END PRIVATE KEY-----",
				MinVersion: "1.3",
				ClientAuth: &config.ClientAuthConfig{
					Mode:               "require",
					CAFile:             "/etc/ca.pem",
					VerifySAN:          []string{"example.com"},
					CRLFile:            "/etc/crl.pem",
					ForwardCertificate: "chain",
				},
				ACME: &config.ACMEConfig{
					Enabled:      true,
					Email:        "ops@example.com",
					CA:           "letsencrypt-staging",
					Domains:      []string{"example.com"},
					Challenge:    "http-01",
					DNSProvider:  "",
					CacheDir:     "./jul-data/certs",
					OCSPStapling: config.Bool(true),
				},
			},
			HTTP3: &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 86400},
			// The fixture maximises field coverage, not validity: proxy_protocol
			// and http3 are mutually exclusive in a real configuration.
			ProxyProtocol: "in",
			ClientAddress: &config.ClientAddressConfig{
				TrustedProxies:   []string{"10.0.0.0/8"},
				ForwardedHeaders: []string{"forwarded", "x-forwarded-for"},
				MaxHops:          8,
			},
			Locations: []config.LocationConfig{{
				Match: config.MatchConfig{
					Type:    "prefix",
					Path:    "/",
					Methods: []string{"GET", "POST"},
					Headers: []config.HeaderMatch{{
						Name:  "X-Tenant",
						Op:    "exact",
						Value: strPtr("public"),
					}},
					Query: []config.QueryMatch{{
						Name:  "version",
						Op:    "exact",
						Value: strPtr("v2"),
					}},
				},
				RouteID: strPtr("fixture-route"),

				Root:             "/srv",
				Index:            []string{"index.html"},
				TryFiles:         []string{"$uri", "$uri/", "/index.html"},
				DirectoryListing: true,
				AllowHidden:      true,
				CacheControl:     "public, max-age=3600",

				ProxyPass:           "http://app",
				ProxyConnectTimeout: config.Duration(time.Second),
				ProxyReadTimeout:    config.Duration(5 * time.Second),
				ProxySendTimeout:    config.Duration(5 * time.Second),
				ProxyRetries:        2,
				GRPC:                true,
				BackendTLS: &config.BackendTLSConfig{
					CAFile:             "/etc/jul/route-ca.pem",
					CAMode:             "file_only",
					ClientCert:         "/etc/jul/route-client.pem",
					ClientKey:          "/etc/jul/route-client.key",
					ServerName:         "route.internal",
					MinVersion:         "1.2",
					PeerIdentities:     []string{"uri:spiffe://example/route"},
					InsecureSkipVerify: false,
				},
				Resilience: &config.LocationResilienceConfig{
					MaxConnectionsPerBackend: 64,
				},
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
					Enabled:           true,
					Mode:              "block",
					BlockStatus:       403,
					CRSEnabled:        true,
					DirectivesFiles:   []string{"/etc/loc-crs.conf"},
					InlineRules:       "SecRule ENGINE Off",
					Paranoia:          2,
					RequestBodyLimit:  config.Size(64 << 10),
					ResponseBodyCheck: true,
				},
				Plugins: []string{"p"},
				Plugin:  "handler",
				GRPCTranscode: &config.GRPCTranscodeConfig{
					Target:         "grpc://app",
					DescriptorSet:  "/etc/descriptor.pb",
					UseReflection:  true,
					TLS:            true,
					PreserveNames:  true,
					Streaming:      true,
					StreamMode:     "ndjson",
					MaxMessageSize: config.Size(4 << 20),
				},
				Deny: true,
				ResponseHeaders: []config.ResponseHeaderOp{{
					Op:    "set",
					Name:  "X-Frame-Options",
					Value: strPtr("DENY"),
				}},
				CORS: &config.CORSConfig{
					Enabled:          true,
					AllowedOrigins:   []string{"https://app.example.test"},
					AllowedMethods:   []string{"GET", "POST"},
					AllowedHeaders:   []string{"Content-Type"},
					ExposedHeaders:   []string{"X-Request-Id"},
					AllowCredentials: true,
					MaxAge:           durationPtr(10 * time.Minute),
				},
			}},
		}},
		Upstreams: []config.UpstreamConfig{{
			Name:     "app",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:3000", Weight: 1},
			},
			Resilience: &config.ResilienceConfig{
				MaxFails:            3,
				FailTimeout:         config.Duration(10 * time.Second),
				MaxActiveRequests:   1000,
				MaxActivePerBackend: 250,
				MaxPendingRequests:  100,
				PendingTimeout:      config.Duration(2 * time.Second),

				MaxConnectionsPerBackend: 128,

				CircuitHalfOpenProbes: config.Int(2),
			},
			BackendTLS: &config.BackendTLSConfig{
				CAFile:             "/etc/jul/backend-ca.pem",
				CAMode:             "system_and_file",
				ClientCert:         "/etc/jul/client.pem",
				ClientKey:          "/etc/jul/client.key",
				ServerName:         "app.internal",
				MinVersion:         "1.3",
				PeerIdentities:     []string{"dns:app.internal"},
				InsecureSkipVerify: false,
			},
			HealthCheck: &config.HealthCheckConfig{
				Enabled:            true,
				Type:               "http",
				Path:               "/healthz",
				Interval:           config.Duration(5 * time.Second),
				Timeout:            config.Duration(2 * time.Second),
				HealthyThreshold:   2,
				UnhealthyThreshold: 3,
				ExpectStatus:       []int{200},
				ExpectBody:         "ok",
			},
			Discovery: &config.DiscoveryConfig{
				Type:    "dns",
				Target:  "app.internal:8080",
				Refresh: config.Duration(30 * time.Second),
				Consul: &config.ConsulDiscovery{
					Address:     "http://127.0.0.1:8500",
					Service:     "app",
					Tag:         "v1",
					Datacenter:  "dc1",
					Token:       "consul-token",
					PassingOnly: config.Bool(true),
					TLS: &config.BackendTLSConfig{
						CAFile:             "/etc/jul/consul-ca.pem",
						CAMode:             "file_only",
						ClientCert:         "/etc/jul/consul-client.pem",
						ClientKey:          "/etc/jul/consul-client.key",
						ServerName:         "consul.service.consul",
						MinVersion:         "1.3",
						PeerIdentities:     []string{"dns:consul.service.consul"},
						InsecureSkipVerify: true,
					},
				},
				Kubernetes: &config.KubernetesDiscovery{
					Namespace:             "default",
					Service:               "app",
					Port:                  "8080",
					APIServer:             "https://kubernetes.default",
					Token:                 "kubernetes-token",
					CAFile:                "/var/run/secrets/ca.crt",
					InsecureSkipTLSVerify: true,
				},
			},
		}},
		Cache: config.CacheConfig{
			Enabled:              true,
			MemoryMaxSize:        config.Size(64 << 20),
			DiskPath:             "./jul-data/cache",
			DiskMaxSize:          config.Size(1 << 30),
			DefaultTTL:           config.Duration(300 * time.Second),
			StaleWhileRevalidate: config.Duration(60 * time.Second),
			StaleIfError:         config.Duration(300 * time.Second),
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
				Enabled:     config.Bool(true),
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
				Timeout:          config.Duration(100 * time.Millisecond),
				KV:               true,
				Fetch:            true,
				AllowedHosts:     []string{"example.com"},
				MaxRequestBody:   config.Size(1 << 20),
				MaxResponseBody:  config.Size(8 << 20),
				FetchTimeout:     config.Duration(5 * time.Second),
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
			TrustedProxies: []string{"10.0.0.0/8"},
			ConnectTimeout: config.Duration(10 * time.Second),
			IdleTimeout:    config.Duration(300 * time.Second),
			MaxUDPSessions: 10000,
		}},
	}
}

// mustValue fails the test when a registry path has no extractor.
func mustValue(t *testing.T, cfg *config.Config, path string) any {
	t.Helper()
	v, ok := EffectiveValue(cfg, path)
	if !ok {
		t.Fatalf("no extractor for %q", path)
	}
	return v
}
