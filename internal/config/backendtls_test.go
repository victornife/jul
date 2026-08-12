// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// backendTLSFixture writes a readable PEM-looking file and returns its path.
// Validation checks readability, not contents; the resolver parses the bytes.
func backendTLSFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// tlsBackendConfig builds a config whose single location proxies over https, so
// a backend_tls block is meaningful.
func tlsBackendConfig(t *testing.T, up *BackendTLSConfig, loc *BackendTLSConfig) *Config {
	t.Helper()
	cfg := validKnownValueConfig()
	cfg.Servers[0].Locations[0].ProxyPass = "https://api"
	cfg.Servers[0].Locations[0].BackendTLS = loc
	cfg.Upstreams = []UpstreamConfig{{
		Name:       "api",
		Servers:    []UpstreamServer{{Address: "127.0.0.1:8443", Weight: 1}},
		BackendTLS: up,
	}}
	return cfg
}

func TestValidateAcceptsBackendTLS(t *testing.T) {
	ca := backendTLSFixture(t, "ca.pem")
	cert := backendTLSFixture(t, "client.pem")
	key := backendTLSFixture(t, "client.key")

	tests := []struct {
		name   string
		policy *BackendTLSConfig
	}{
		{name: "omitted", policy: nil},
		{name: "system roots with a verified name", policy: &BackendTLSConfig{ServerName: "api.internal"}},
		{name: "private CA replacing the system roots", policy: &BackendTLSConfig{CAMode: "file_only", CAFile: ca}},
		{name: "private CA augmenting the system roots", policy: &BackendTLSConfig{CAMode: "system_and_file", CAFile: ca}},
		{name: "mutual TLS", policy: &BackendTLSConfig{ClientCert: cert, ClientKey: key}},
		{name: "explicit peer identities", policy: &BackendTLSConfig{PeerIdentities: []string{"dns:api.internal", "uri:spiffe://example/api"}}},
		{name: "tls 1.3 floor", policy: &BackendTLSConfig{MinVersion: "1.3"}},
		// Accepted by Validate on purpose: a field that exists to opt into an
		// insecure mode cannot be a validation rejection. jul lint fails on it.
		{name: "insecure bypass alone", policy: &BackendTLSConfig{InsecureSkipVerify: true}},
	}

	for _, tt := range tests {
		t.Run("upstream/"+tt.name, func(t *testing.T) {
			if err := Validate(tlsBackendConfig(t, tt.policy, nil)); err != nil {
				t.Fatalf("Validate rejected a valid upstream policy: %v", err)
			}
		})
		t.Run("location/"+tt.name, func(t *testing.T) {
			if err := Validate(tlsBackendConfig(t, nil, tt.policy)); err != nil {
				t.Fatalf("Validate rejected a valid location policy: %v", err)
			}
		})
	}
}

func TestValidateRejectsInvalidBackendTLS(t *testing.T) {
	ca := backendTLSFixture(t, "ca.pem")

	tests := []struct {
		name   string
		policy *BackendTLSConfig
		want   string
	}{
		{name: "unknown ca mode", policy: &BackendTLSConfig{CAMode: "trust_everything"}, want: "ca_mode"},
		{name: "file mode without a file", policy: &BackendTLSConfig{CAMode: "file_only"}, want: "ca_file: required"},
		{name: "ca file that would be ignored", policy: &BackendTLSConfig{CAFile: ca}, want: "set ca_mode"},
		{name: "unreadable ca file", policy: &BackendTLSConfig{CAMode: "file_only", CAFile: "/nonexistent/ca.pem"}, want: "is not readable"},
		{name: "ca file that is a directory", policy: &BackendTLSConfig{CAMode: "file_only", CAFile: t.TempDir()}, want: "is a directory"},
		{name: "certificate without key", policy: &BackendTLSConfig{ClientCert: ca}, want: "both are required"},
		{name: "server name with a port", policy: &BackendTLSConfig{ServerName: "api.internal:8443"}, want: "must not carry a port"},
		{name: "invalid min version", policy: &BackendTLSConfig{MinVersion: "1.0"}, want: "min_version"},
		{name: "unprefixed peer identity", policy: &BackendTLSConfig{PeerIdentities: []string{"api.internal"}}, want: "must start with"},
		{name: "insecure with peer identities", policy: &BackendTLSConfig{InsecureSkipVerify: true, PeerIdentities: []string{"dns:api.internal"}}, want: "cannot be combined with peer_identities"},
		{name: "insecure with a private CA", policy: &BackendTLSConfig{InsecureSkipVerify: true, CAMode: "file_only", CAFile: ca}, want: "cannot be combined with ca_mode"},
	}

	for _, tt := range tests {
		t.Run("upstream/"+tt.name, func(t *testing.T) {
			requireValidationError(t, tlsBackendConfig(t, tt.policy, nil), tt.want)
		})
		t.Run("location/"+tt.name, func(t *testing.T) {
			requireValidationError(t, tlsBackendConfig(t, nil, tt.policy), tt.want)
		})
	}
}

// TestValidateRejectsBackendTLSOnPlaintextTarget proves the block cannot sit
// where it would never apply: an operator who writes one believes the backend
// is verified.
func TestValidateRejectsBackendTLSOnPlaintextTarget(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
	}{
		{
			name: "http proxy_pass",
			set: func(c *Config) {
				c.Servers[0].Locations[0].ProxyPass = "http://api"
			},
		},
		{
			name: "plaintext transcoding target",
			set: func(c *Config) {
				c.Servers[0].Locations[0].ProxyPass = ""
				c.Servers[0].Locations[0].GRPCTranscode = &GRPCTranscodeConfig{
					Target: "api", DescriptorSet: backendTLSFixture(t, "api.pb"), TLS: false,
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tlsBackendConfig(t, nil, &BackendTLSConfig{ServerName: "api.internal"})
			tt.set(cfg)
			requireValidationError(t, cfg, "the backend is not reached over TLS")
		})
	}

	t.Run("tls transcoding target is accepted", func(t *testing.T) {
		cfg := tlsBackendConfig(t, nil, &BackendTLSConfig{ServerName: "api.internal"})
		cfg.Servers[0].Locations[0].ProxyPass = ""
		cfg.Servers[0].Locations[0].GRPCTranscode = &GRPCTranscodeConfig{
			Target: "api", DescriptorSet: backendTLSFixture(t, "api.pb"), TLS: true,
		}
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate rejected a TLS transcoding target: %v", err)
		}
	})
}

func TestLintBackendTLS(t *testing.T) {
	ca := backendTLSFixture(t, "ca.pem")

	t.Run("insecure_skip_verify is an error, not a warning", func(t *testing.T) {
		cfg := tlsBackendConfig(t, &BackendTLSConfig{InsecureSkipVerify: true}, nil)
		var found bool
		for _, d := range Lint(cfg) {
			if !strings.Contains(d.Field, "backend_tls") {
				continue
			}
			found = true
			if d.Severity != SeverityError {
				t.Errorf("severity = %s, want error so `jul lint` fails without -strict", d.Severity)
			}
			if !strings.Contains(d.Message, "not authenticated") {
				t.Errorf("message = %q", d.Message)
			}
		}
		if !found {
			t.Fatal("no diagnostic for insecure_skip_verify")
		}
	})

	t.Run("https health target without a policy warns", func(t *testing.T) {
		cfg := tlsBackendConfig(t, nil, nil)
		cfg.Upstreams[0].HealthCheck = &HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz"}
		var found bool
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Field, "health_check") && strings.Contains(d.Message, "probed over https") {
				found = true
				if d.Severity != SeverityWarning {
					t.Errorf("severity = %s, want warning", d.Severity)
				}
			}
		}
		if !found {
			t.Fatal("no warning for an https health target without backend_tls")
		}
	})

	t.Run("https health target with a policy does not warn", func(t *testing.T) {
		cfg := tlsBackendConfig(t, &BackendTLSConfig{CAMode: "file_only", CAFile: ca}, nil)
		cfg.Upstreams[0].HealthCheck = &HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz"}
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "probed over https") {
				t.Fatalf("warned despite a configured policy: %+v", d)
			}
		}
	})

	t.Run("plaintext health target does not warn", func(t *testing.T) {
		cfg := tlsBackendConfig(t, nil, nil)
		cfg.Servers[0].Locations[0].ProxyPass = "http://api"
		cfg.Upstreams[0].HealthCheck = &HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz"}
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "probed over https") {
				t.Fatalf("warned about a plaintext pool: %+v", d)
			}
		}
	})

	t.Run("tcp probe does not warn", func(t *testing.T) {
		cfg := tlsBackendConfig(t, nil, nil)
		cfg.Upstreams[0].HealthCheck = &HealthCheckConfig{Enabled: true, Type: "tcp"}
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "probed over https") {
				t.Fatalf("warned about a TCP probe, which never verifies identity: %+v", d)
			}
		}
	})

	t.Run("overlapping policies are reported", func(t *testing.T) {
		cfg := tlsBackendConfig(t, &BackendTLSConfig{ServerName: "pool.internal"}, &BackendTLSConfig{ServerName: "route.internal"})
		var found bool
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "both define backend_tls") {
				found = true
			}
		}
		if !found {
			t.Fatal("an overriding location policy was not reported")
		}
	})
}

func TestParseBackendTLS(t *testing.T) {
	doc := `
[[servers]]
listen = "127.0.0.1:8080"

  [[servers.locations]]
  proxy_pass = "https://inventory"
  [servers.locations.match]
  type = "prefix"
  path = "/"
  [servers.locations.backend_tls]
  server_name = "route.internal"

[[upstreams]]
name = "inventory"
servers = ["10.0.0.5:8443"]

  [upstreams.backend_tls]
  ca_file = "/etc/jul/backend-ca.pem"
  ca_mode = "system_and_file"
  client_cert = "/etc/jul/client.pem"
  client_key = "/etc/jul/client.key"
  server_name = "inventory.internal"
  min_version = "1.2"
  peer_identities = ["dns:inventory.internal"]
  insecure_skip_verify = false
`
	cfg, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	up := cfg.Upstreams[0].BackendTLS
	if up == nil {
		t.Fatal("upstream backend_tls was not decoded")
	}
	if up.CAMode != "system_and_file" || up.ServerName != "inventory.internal" || up.MinVersion != "1.2" {
		t.Errorf("upstream policy = %+v", up)
	}
	if len(up.PeerIdentities) != 1 || up.PeerIdentities[0] != "dns:inventory.internal" {
		t.Errorf("peer_identities = %v", up.PeerIdentities)
	}
	if loc := cfg.Servers[0].Locations[0].BackendTLS; loc == nil || loc.ServerName != "route.internal" {
		t.Errorf("location policy = %+v", loc)
	}
}

func TestParseRejectsRejectedBackendTLSAliases(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want string
	}{
		{
			name: "upstreams.tls is not the outbound key",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[upstreams]]\nname = \"api\"\nservers = [\"10.0.0.1:443\"]\n[upstreams.tls]\nca_file = \"ca.pem\"\n",
			want: "tls",
		},
		{
			name: "locations.proxy_tls is not the outbound key",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[servers.locations]]\nproxy_pass = \"https://api\"\n[servers.locations.proxy_tls]\nca_file = \"ca.pem\"\n",
			want: "proxy_tls",
		},
		{
			name: "misspelled leaf",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[upstreams]]\nname = \"api\"\nservers = [\"10.0.0.1:443\"]\n[upstreams.backend_tls]\nca_mdoe = \"file_only\"\n",
			want: "ca_mdoe",
		},
		{
			name: "rejected neighbour",
			doc:  "[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[upstreams]]\nname = \"api\"\nservers = [\"10.0.0.1:443\"]\n[upstreams.backend_tls]\nverify = false\n",
			want: "verify",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Parse([]byte(tt.doc))
			if err == nil {
				t.Fatalf("Parse accepted %q: %#v", tt.want, cfg)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %q, want it to name %q", err, tt.want)
			}
		})
	}
}
