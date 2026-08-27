// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// These tests pin what ADR 0018 makes a *validation* error, as opposed to a
// lint finding. Jul does not reject a valid HTTP construct for being unusual,
// but it never lets an unusual one be silent.

func sp(s string) *string { return &s }

// matchConfig returns a minimal configuration whose single location carries m.
func matchConfig(m MatchConfig, clientAddress *ClientAddressConfig) *Config {
	m.Type = firstNonEmpty(m.Type, "prefix")
	m.Path = firstNonEmpty(m.Path, "/")
	return &Config{Servers: []ServerConfig{{
		Listen:        ":8080",
		ClientAddress: clientAddress,
		Locations:     []LocationConfig{{Match: m, Return: 200}},
	}}}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// requireMatchError asserts that a configuration is rejected with a message
// containing want.
func requireMatchError(t *testing.T, cfg *Config, want string) {
	t.Helper()
	err := Validate(cfg)
	if err == nil {
		t.Fatalf("configuration was accepted; want an error mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to mention %q", err, want)
	}
}

func TestMatchMethodsValidation(t *testing.T) {
	t.Run("omitted leaves the method unconstrained", func(t *testing.T) {
		if err := Validate(matchConfig(MatchConfig{}, nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// An empty list is a route that can never match, which is a mistake rather
	// than a way to disable a route. Omitted and explicit-empty never collapse.
	t.Run("explicitly empty is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Methods: []string{}}, nil), "match.methods is empty")
	})

	t.Run("a lowercase spelling of a registered method is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Methods: []string{"get"}}, nil), `must be spelled "GET"`)
	})

	// Mechanical uppercasing would silently break a genuinely lowercase
	// extension method, so only registered names are corrected.
	t.Run("a lowercase extension method is accepted", func(t *testing.T) {
		if err := Validate(matchConfig(MatchConfig{Methods: []string{"purge-cache"}}, nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("duplicates are rejected rather than collapsed", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Methods: []string{"GET", "GET"}}, nil), "duplicate method")
	})

	t.Run("a non-token is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Methods: []string{"GET POST"}}, nil), "not a valid HTTP method token")
	})

	// Jul implements no tunnelling, and Go gives an authority-form CONNECT an
	// empty URL path, which matches no tier — so the predicate could never fire.
	t.Run("CONNECT is rejected with its reason", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Methods: []string{"CONNECT"}}, nil), "CONNECT cannot be matched")
	})

	t.Run("OPTIONS and extension methods are accepted", func(t *testing.T) {
		if err := Validate(matchConfig(MatchConfig{Methods: []string{"OPTIONS", "PURGE"}}, nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("the count bound is enforced", func(t *testing.T) {
		methods := make([]string, 0, MaxMatchMethods+1)
		for _, m := range []string{"GET", "HEAD", "POST", "PUT", "DELETE", "PATCH", "OPTIONS", "TRACE"} {
			methods = append(methods, m)
		}
		for i := len(methods); i <= MaxMatchMethods; i++ {
			methods = append(methods, "EXT"+strings.Repeat("X", i))
		}
		requireMatchError(t, matchConfig(MatchConfig{Methods: methods}, nil), "over the limit of 16")
	})
}

func TestMatchHeaderValidation(t *testing.T) {
	trusted := &ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}, ForwardedHeaders: []string{"x-forwarded-for"}}

	t.Run("op is required", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant"}}}, nil), "op is required")
	})

	t.Run("an unknown op is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "prefix", Value: sp("a")}}}, nil), `invalid op "prefix"`)
	})

	t.Run("exact requires a value and present forbids one", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact"}}}, nil), "value is required")
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "present", Value: sp("a")}}}, nil), "takes no value")
	})

	// An explicitly empty exact value is legal and meaningful: it matches only
	// a present-but-empty field.
	t.Run("an explicitly empty exact value is accepted", func(t *testing.T) {
		if err := Validate(matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp("")}}}, nil)); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	// Go moves Host out of r.Header, so the predicate could never match — the
	// worst possible failure mode for a routing rule.
	t.Run("Host is rejected and points at server_names", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "host", Op: "present"}}}, nil), "server_names")
	})

	t.Run("a pseudo-header is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: ":authority", Op: "present"}}}, nil), "pseudo-header")
	})

	t.Run("an invalid field name is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X Tenant", Op: "present"}}}, nil), "not a valid header field name")
	})

	t.Run("an uncompilable regex is rejected", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp("(")}}}, nil), "invalid regex")
	})

	t.Run("the regex pattern length bound is enforced", func(t *testing.T) {
		pattern := strings.Repeat("a", MaxMatchHeaderPatternBytes+1)
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "regex", Value: sp(pattern)}}}, nil), "over the limit of 512")
	})

	t.Run("the regex count bound is enforced", func(t *testing.T) {
		var headers []HeaderMatch
		for i := 0; i <= MaxMatchHeaderRegexes; i++ {
			headers = append(headers, HeaderMatch{Name: "X-Tenant", Op: "regex", Value: sp("a")})
		}
		requireMatchError(t, matchConfig(MatchConfig{Headers: headers}, nil), "regex predicates, over the limit of 8")
	})

	t.Run("the value length bound is enforced", func(t *testing.T) {
		value := strings.Repeat("v", MaxMatchHeaderValueBytes+1)
		requireMatchError(t, matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: sp(value)}}}, nil), "over the limit of 1024")
	})

	t.Run("the entry count bound is enforced", func(t *testing.T) {
		var headers []HeaderMatch
		for i := 0; i <= MaxMatchHeaders; i++ {
			headers = append(headers, HeaderMatch{Name: "X-Tenant", Op: "present"})
		}
		requireMatchError(t, matchConfig(MatchConfig{Headers: headers}, nil), "match.headers has 17 entries")
	})

	// A forwarded-header predicate reads as a security control and is trivially
	// forged: matching runs before the forwarded chain is rebuilt. A lint
	// finding alone still admits the configuration, so validation rejects it
	// unless the listener declares the trust boundary ADR 0016 already requires.
	t.Run("a forwarded predicate is rejected without trusted_proxies", func(t *testing.T) {
		for _, name := range []string{"X-Forwarded-For", "X-Forwarded-Proto", "Forwarded", "Client-Cert"} {
			cfg := matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: name, Op: "present"}}}, nil)
			requireMatchError(t, cfg, "trusted_proxies")
		}
	})

	t.Run("a forwarded predicate is accepted with trusted_proxies", func(t *testing.T) {
		cfg := matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "X-Forwarded-Proto", Op: "exact", Value: sp("https")}}}, trusted)
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// It is accepted, and still reported: the trust extends to the declared
		// proxy, not to the client behind it.
		requireDiagnostic(t, Lint(cfg), SeverityError, "match.headers[0]", "client can set")
	})

	// Hop-by-hop names are accepted and warned: they are connection-scoped, so
	// the predicate behaves differently per protocol version for reasons the
	// operator did not choose.
	t.Run("a hop-by-hop predicate is accepted with a warning", func(t *testing.T) {
		cfg := matchConfig(MatchConfig{Headers: []HeaderMatch{{Name: "Connection", Op: "present"}}}, nil)
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "match.headers[0]", "connection-scoped")
	})
}

func TestMatchQueryValidation(t *testing.T) {
	t.Run("name is required", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Op: "present"}}}, nil), "name is required")
	})

	t.Run("op is required and bounded to two operations", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Name: "v"}}}, nil), "op is required")
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Name: "v", Op: "regex", Value: sp("a")}}}, nil), `invalid op "regex"`)
	})

	t.Run("exact requires a value and present forbids one", func(t *testing.T) {
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Name: "v", Op: "exact"}}}, nil), "value is required")
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Name: "v", Op: "present", Value: sp("a")}}}, nil), "takes no value")
	})

	t.Run("the bounds are enforced", func(t *testing.T) {
		var query []QueryMatch
		for i := 0; i <= MaxMatchQuery; i++ {
			query = append(query, QueryMatch{Name: "v", Op: "present"})
		}
		requireMatchError(t, matchConfig(MatchConfig{Query: query}, nil), "match.query has 17 entries")

		long := strings.Repeat("v", MaxMatchQueryValueBytes+1)
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Name: "v", Op: "exact", Value: sp(long)}}}, nil), "over the limit of 1024")
		requireMatchError(t, matchConfig(MatchConfig{Query: []QueryMatch{{Name: long, Op: "present"}}}, nil), "name is 1025 bytes")
	})
}

// TestMatchPredicatesRoundTripThroughTOML is the property that makes the
// pointer-valued Value field necessary rather than merely tidy: the typed patch
// API re-serializes the parsed config, so a predicate that cannot survive
// Marshal → Parse would break every edit made to a route that carries one.
func TestMatchPredicatesRoundTripThroughTOML(t *testing.T) {
	cfg := matchConfig(MatchConfig{
		Methods: []string{"GET", "POST"},
		Headers: []HeaderMatch{
			{Name: "X-Tenant", Op: "present"},
			{Name: "X-Tenant", Op: "exact", Value: sp("")},
			{Name: "X-Trace", Op: "regex", Value: sp("^[0-9a-f]{32}$")},
		},
		Query: []QueryMatch{
			{Name: "version", Op: "exact", Value: sp("v2")},
			{Name: "debug", Op: "present"},
		},
	}, nil)

	raw, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("reparse:\n%s\nerror: %v", raw, err)
	}
	if err := Validate(back); err != nil {
		t.Fatalf("the re-parsed configuration no longer validates:\n%s\nerror: %v", raw, err)
	}

	got := back.Servers[0].Locations[0].Match
	if len(got.Methods) != 2 || got.Methods[0] != "GET" {
		t.Errorf("methods = %v, want [GET POST]", got.Methods)
	}
	if len(got.Headers) != 3 {
		t.Fatalf("headers = %+v, want 3 entries in declaration order", got.Headers)
	}
	// An omitted value and an explicitly empty one must not collapse in either
	// direction.
	if got.Headers[0].Value != nil {
		t.Errorf(`present predicate round-tripped with value %q, want none`, *got.Headers[0].Value)
	}
	if got.Headers[1].Value == nil || *got.Headers[1].Value != "" {
		t.Errorf("exact-empty predicate round-tripped as %v, want an explicit empty value", got.Headers[1].Value)
	}
	if len(got.Query) != 2 || got.Query[0].Name != "version" {
		t.Errorf("query = %+v, want the declaration order preserved", got.Query)
	}
}

// TestMatchPredicatesRejectUnknownFields keeps the strict-decoding contract:
// a typo in a predicate must not silently become a no-op predicate.
func TestMatchPredicatesRejectUnknownFields(t *testing.T) {
	_, err := Parse([]byte(`
[[servers]]
listen = ":8080"

[[servers.locations]]
return = 200

[servers.locations.match]
type = "prefix"
path = "/"

[[servers.locations.match.headers]]
name = "X-Tenant"
op = "exact"
vaule = "public"
`))
	if err == nil {
		t.Fatal("a misspelled predicate field was accepted")
	}
	if !strings.Contains(err.Error(), "vaule") {
		t.Fatalf("error = %v, want it to name the unknown field", err)
	}
}
