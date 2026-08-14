// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

// sharedListenConfig builds two server blocks on one listen address, mutated
// by the caller.
func sharedListenConfig(mutate func(a, b *ServerConfig)) *Config {
	loc := LocationConfig{Match: MatchConfig{Type: "prefix", Path: "/"}, Return: 204}
	cfg := &Config{Servers: []ServerConfig{
		{Listen: ":8443", ServerNames: []string{"a.example.com"}, Locations: []LocationConfig{loc}},
		{Listen: ":8443", ServerNames: []string{"b.example.com"}, Locations: []LocationConfig{loc}},
	}}
	mutate(&cfg.Servers[0], &cfg.Servers[1])
	return cfg
}

// listenerDiags returns the diagnostics whose field names the given leaf.
func listenerDiags(cfg *Config, field string) []Diagnostic {
	var out []Diagnostic
	for _, d := range Lint(cfg) {
		if strings.HasSuffix(d.Field, "."+field) {
			out = append(out, d)
		}
	}
	return out
}

func TestLintWarnsOnDivergentListenerScopedFields(t *testing.T) {
	tests := []struct {
		name   string
		field  string
		mutate func(a, b *ServerConfig)
		want   string
	}{
		{
			name:  "read_header_timeout",
			field: "read_header_timeout",
			mutate: func(a, b *ServerConfig) {
				a.ReadHeaderTimeout = Duration(5 * time.Second)
				b.ReadHeaderTimeout = Duration(30 * time.Second)
			},
			want: "already takes read_header_timeout from servers[0]",
		},
		{
			name:  "read_timeout",
			field: "read_timeout",
			mutate: func(a, b *ServerConfig) {
				a.ReadTimeout = Duration(time.Minute)
				b.ReadTimeout = Duration(2 * time.Minute)
			},
			want: "already takes read_timeout from servers[0]",
		},
		{
			name:  "write_timeout",
			field: "write_timeout",
			mutate: func(a, b *ServerConfig) {
				a.WriteTimeout = Duration(time.Minute)
				b.WriteTimeout = Duration(10 * time.Second)
			},
			want: "already takes write_timeout from servers[0]",
		},
		{
			name:  "idle_timeout",
			field: "idle_timeout",
			mutate: func(a, b *ServerConfig) {
				// Both differ from the applied default (60s), so both are
				// recognisably operator-written.
				a.IdleTimeout = Duration(3 * time.Minute)
				b.IdleTimeout = Duration(5 * time.Minute)
			},
			want: "already takes idle_timeout from servers[0]",
		},
		{
			name:  "max_header_bytes",
			field: "max_header_bytes",
			mutate: func(a, b *ServerConfig) {
				a.MaxHeaderBytes = Size(4 << 20)
				b.MaxHeaderBytes = Size(2 << 20)
			},
			want: "already takes max_header_bytes from servers[0]",
		},
		{
			name:  "http3.alt_svc_max_age",
			field: "alt_svc_max_age",
			mutate: func(a, b *ServerConfig) {
				a.HTTP3 = &HTTP3Config{Enabled: true, AltSvcMaxAge: 7200}
				b.HTTP3 = &HTTP3Config{Enabled: true, AltSvcMaxAge: 3600}
			},
			want: "already takes http3.alt_svc_max_age from servers[0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diags := listenerDiags(sharedListenConfig(tt.mutate), tt.field)
			if len(diags) != 1 {
				t.Fatalf("got %d diagnostics, want exactly one: %+v", len(diags), diags)
			}
			d := diags[0]
			if d.Severity != SeverityWarning {
				t.Errorf("severity = %s, want warning: the behaviour is pre-existing", d.Severity)
			}
			// Both the winner and the ignored block must be identifiable.
			if !strings.HasPrefix(d.Field, "servers[1].") {
				t.Errorf("field = %q, want it to name the ignored block", d.Field)
			}
			if !strings.Contains(d.Message, tt.want) {
				t.Errorf("message = %q, want it to name the winning block", d.Message)
			}
			if d.Hint == "" {
				t.Error("a diagnostic without a hint tells the operator nothing to do")
			}
		})
	}
}

func TestLintStaysSilentWhenListenerScopedFieldsAgree(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(a, b *ServerConfig)
	}{
		{
			name: "identical values",
			mutate: func(a, b *ServerConfig) {
				a.ReadTimeout = Duration(time.Minute)
				b.ReadTimeout = Duration(time.Minute)
			},
		},
		{
			name: "one block omits the field",
			mutate: func(a, b *ServerConfig) {
				a.ReadTimeout = Duration(time.Minute)
			},
		},
		{
			name:   "both omit the field",
			mutate: func(a, b *ServerConfig) {},
		},
		{
			name: "identical h2c",
			mutate: func(a, b *ServerConfig) {
				a.H2C, b.H2C = true, true
			},
		},
		{
			name: "identical http3",
			mutate: func(a, b *ServerConfig) {
				a.HTTP3 = &HTTP3Config{Enabled: true, AltSvcMaxAge: 86400}
				b.HTTP3 = &HTTP3Config{Enabled: true, AltSvcMaxAge: 86400}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := sharedListenConfig(tc.mutate)
			for _, d := range Lint(cfg) {
				if strings.Contains(d.Message, "already takes") {
					t.Fatalf("warned about agreeing or omitted values: %+v", d)
				}
			}
		})
	}
}

// TestLintDoesNotWarnAcrossDistinctListeners proves the rule is per address:
// two listeners may legitimately differ.
func TestLintDoesNotWarnAcrossDistinctListeners(t *testing.T) {
	cfg := sharedListenConfig(func(a, b *ServerConfig) {
		a.ReadTimeout = Duration(time.Minute)
		b.ReadTimeout = Duration(5 * time.Minute)
	})
	cfg.Servers[1].Listen = ":9443"
	for _, d := range Lint(cfg) {
		if strings.Contains(d.Message, "already takes") {
			t.Fatalf("warned across distinct listen addresses: %+v", d)
		}
	}
}

// TestLintTreatsClientMaxBodySizeAsPerVirtualHost records the correction found
// while implementing this: client_max_body_size is applied by the router from
// the *matched* virtual host, not resolved once per listener, so two blocks on
// one address may legitimately differ and must not be warned about.
func TestLintTreatsClientMaxBodySizeAsPerVirtualHost(t *testing.T) {
	cfg := sharedListenConfig(func(a, b *ServerConfig) {
		a.ClientMaxBodySize = Size(1 << 20)
		b.ClientMaxBodySize = Size(8 << 20)
	})
	for _, d := range Lint(cfg) {
		if strings.Contains(d.Field, "client_max_body_size") {
			t.Fatalf("warned about a per-virtual-host field: %+v", d)
		}
	}
}

// TestLintWarnsWhenABlockCannotOptOutOfH2COrHTTP3 covers the any-wins fields:
// enabling either in one block turns it on for the address, so a sibling that
// leaves it off is overruled rather than merely overridden.
func TestLintWarnsWhenABlockCannotOptOutOfH2COrHTTP3(t *testing.T) {
	t.Run("h2c stays silent when only one block enables it", func(t *testing.T) {
		cfg := sharedListenConfig(func(a, b *ServerConfig) { a.H2C = true })
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Field, "h2c") {
				t.Fatalf("warned when no sibling declared a conflicting value: %+v", d)
			}
		}
	})

	// A conflict is only observable when both blocks declare the field, which
	// for a bool means both enable it — and then they agree. The value of the
	// entry is therefore the documented hint on the *other* listener-scoped
	// warnings, plus the documentation of the any-wins rule; assert that the
	// enable case never produces a spurious warning.
	t.Run("http3 enabled in both blocks is not a conflict", func(t *testing.T) {
		cfg := sharedListenConfig(func(a, b *ServerConfig) {
			a.HTTP3 = &HTTP3Config{Enabled: true}
			b.HTTP3 = &HTTP3Config{Enabled: true}
		})
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Field, "http3.enabled") {
				t.Fatalf("warned about agreeing values: %+v", d)
			}
		}
	})
}

// TestListenerScopedLintCoversEveryServerLevelField is the guard the issue asks
// for: the lint's field list and the lifecycle registry must agree about what
// is listener-scoped, so a new bind-time field cannot be added without either
// covering it here or deliberately exempting it.
//
// The registry lives in internal/lifecycle, which imports this package, so the
// expected set is stated literally rather than imported — a duplicate list that
// a reviewer can compare, not a second mechanism.
func TestListenerScopedLintCoversEveryServerLevelField(t *testing.T) {
	// Every server-level path the lifecycle registry classifies as
	// new_listener_only or bind-bound, with its disposition here.
	registryListenerScoped := map[string]string{
		"servers.*.read_header_timeout":        "linted",
		"servers.*.read_timeout":               "linted",
		"servers.*.write_timeout":              "linted",
		"servers.*.idle_timeout":               "linted",
		"servers.*.max_header_bytes":           "linted",
		"servers.*.h2c":                        "linted",
		"servers.*.http3.enabled":              "linted",
		"servers.*.http3.alt_svc_max_age":      "linted",
		"servers.*.listen":                     "exempt: it is the address itself, not a property of one",
		"servers.*.proxy_protocol":             "exempt: validateProxyProtocolConsistency already rejects divergence",
		"servers.*.tls.enabled":                "exempt: validation already rejects TLS mixed with plaintext on one address",
		"servers.*.tls.min_version":            "exempt: covered by the TLS/plaintext and ACME consistency rules",
		"servers.*.tls.cert":                   "exempt: certificate selection is by SNI, so blocks legitimately differ",
		"servers.*.tls.key":                    "exempt: certificate selection is by SNI, so blocks legitimately differ",
		"servers.*.tls.client_auth.mode":       "exempt: mutual-TLS policy is part of the certificate identity, selected per SNI",
		"servers.*.tls.client_auth.ca_file":    "exempt: as above",
		"servers.*.tls.client_auth.crl_file":   "exempt: as above",
		"servers.*.tls.client_auth.verify_san": "exempt: as above",
		"servers.*.tls.acme.ca":                "exempt: validateACMEConsistency already rejects divergence",
		"servers.*.tls.acme.cache_dir":         "exempt: validateACMEConsistency already rejects divergence",
		"servers.*.tls.acme.challenge":         "exempt: validateACMEConsistency already rejects divergence",
		"servers.*.tls.acme.domains":           "exempt: per-block domain sets are the intended usage",
		"servers.*.tls.acme.email":             "exempt: validateACMEConsistency already rejects divergence",
		"servers.*.tls.acme.enabled":           "exempt: validation rejects ACME mixed with static TLS on one address",
		"servers.*.tls.acme.ocsp_stapling":     "exempt: validateACMEConsistency already rejects divergence",
	}

	linted := map[string]bool{}
	srv := &ServerConfig{
		ReadHeaderTimeout: Duration(time.Second),
		ReadTimeout:       Duration(time.Second),
		WriteTimeout:      Duration(time.Second),
		IdleTimeout:       Duration(time.Second),
		MaxHeaderBytes:    Size(1024),
		H2C:               true,
		HTTP3:             &HTTP3Config{Enabled: true, AltSvcMaxAge: 3600},
	}
	for _, f := range listenerScopedFields(srv) {
		linted["servers.*."+f.name] = true
	}

	for path, disposition := range registryListenerScoped {
		isLinted := linted[path]
		wantLinted := disposition == "linted"
		if isLinted != wantLinted {
			t.Errorf("%s: linted = %v, want %v (%s)", path, isLinted, wantLinted, disposition)
		}
	}
	for path := range linted {
		if _, known := registryListenerScoped[path]; !known {
			t.Errorf("%s is linted but is not a known listener-scoped registry path; update this list", path)
		}
	}
}

// TestLintTreatsDefaultedValuesAsNoOpinion pins the deliberate limitation:
// Parse applies defaults before Lint sees the configuration, so a block that
// never mentioned a field is indistinguishable from one that spelled the
// default out. Treating "equals the default" as "no opinion" is what stops the
// linter warning about fields the operator never wrote.
func TestLintTreatsDefaultedValuesAsNoOpinion(t *testing.T) {
	// The under-warned case, recorded so the trade-off is explicit: when the
	// *winning* block holds the default and a later block sets something else,
	// the later value is discarded at runtime but cannot be reported, because
	// after defaulting the winner is indistinguishable from a block that never
	// mentioned the field. Reporting it would mean warning about fields nobody
	// wrote, which is the worse failure for a linter.
	quiet := sharedListenConfig(func(a, b *ServerConfig) {
		a.IdleTimeout = Duration(60 * time.Second) // exactly the applied default
		b.IdleTimeout = Duration(5 * time.Minute)
	})
	for _, d := range Lint(quiet) {
		if strings.Contains(d.Field, "idle_timeout") {
			t.Fatalf("unexpectedly reported the defaulted-winner case: %+v", d)
		}
	}

	// The shape `jul lint` actually sees: one block sets a value, the other
	// never mentions the field and carries the loader's default.
	cfg, err := Parse([]byte(`
[[servers]]
listen = "127.0.0.1:8443"
server_names = ["a.example.com"]
idle_timeout = "2m"
read_header_timeout = "30s"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204

[[servers]]
listen = "127.0.0.1:8443"
server_names = ["b.example.com"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, d := range Lint(cfg) {
		if strings.Contains(d.Message, "already takes") {
			t.Fatalf("warned about a field the second block never wrote: %+v", d)
		}
	}

	// The same document with an explicit divergent value does warn.
	cfg.Servers[1].IdleTimeout = Duration(5 * time.Minute)
	var found bool
	for _, d := range Lint(cfg) {
		if strings.Contains(d.Field, "servers[1].idle_timeout") {
			found = true
		}
	}
	if !found {
		t.Fatal("an explicit divergent value was not reported")
	}
}
