// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// corsLintConfig builds one server block with the given single location.
func corsLintConfig(loc LocationConfig) *Config {
	if loc.Match.Path == "" {
		loc.Match = MatchConfig{Type: "prefix", Path: "/"}
	}
	if loc.Return == 0 && loc.ProxyPass == "" && !loc.GRPC {
		loc.Return = 200
	}
	return &Config{Servers: []ServerConfig{{Listen: ":8080", Locations: []LocationConfig{loc}}}}
}

func TestCORSNullOriginLint(t *testing.T) {
	t.Run("null in a non-wildcard policy warns", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test", "null"},
		}})
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "cors.allowed_origins[1]", `"null"`)
	})

	t.Run("null under an unconditional wildcard does not warn", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"*"},
		}})
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "null") {
				t.Fatalf("unexpected null-origin diagnostic under a wildcard policy: %+v", d)
			}
		}
	})
}

func TestCORSOnNativeGRPCLint(t *testing.T) {
	cfg := corsLintConfig(LocationConfig{
		GRPC:      true,
		ProxyPass: "http://backend",
		CORS:      &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	})
	requireDiagnostic(t, Lint(cfg), SeverityWarning, "locations[0].cors", "native gRPC")
}

func TestContentTypeAtGRPCLocationLint(t *testing.T) {
	cfg := corsLintConfig(LocationConfig{
		GRPC:      true,
		ProxyPass: "http://backend",
		ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "Content-Type", Value: sp("application/json")},
		},
	})
	requireDiagnostic(t, Lint(cfg), SeverityWarning, "response_headers[0]", "framing content type")
}

func TestAccessControlWithoutCORSEnabledLint(t *testing.T) {
	t.Run("warns when cors.enabled = false", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{
			ResponseHeaders: []ResponseHeaderOp{
				{Op: "set", Name: "Access-Control-Allow-Origin", Value: sp("*")},
			},
		})
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "response_headers[0]", "cors.enabled = false")
	})

	t.Run("no warning when cors.enabled = true (rejected at validation instead)", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{
			CORS: &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
			ResponseHeaders: []ResponseHeaderOp{
				{Op: "set", Name: "Access-Control-Allow-Origin", Value: sp("*")},
			},
		})
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "cors.enabled = false") {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
		}
	})
}

func TestVaryAddAtNonCachedLocationLint(t *testing.T) {
	t.Run("warns at a non-cached location", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{
			ResponseHeaders: []ResponseHeaderOp{
				{Op: "add", Name: "Vary", Value: sp("X-Tenant")},
			},
		})
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "response_headers[0]", "downstream caches only")
	})

	t.Run("no warning at a cached location (rejected at validation instead)", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{Cache: true})
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "downstream caches only") {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
		}
	})
}

func TestCORSWithHeaderPredicatesLint(t *testing.T) {
	t.Run("warns", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{
			Match:  MatchConfig{Type: "prefix", Path: "/", Headers: []HeaderMatch{{Name: "X-Tenant", Op: "present"}}},
			CORS:   &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
			Return: 200,
		})
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "match.headers", "will not be selected for its own preflight")
	})

	t.Run("no warning without header predicates", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{
			CORS: &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
		})
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "will not be selected for its own preflight") {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
		}
	})

	t.Run("no warning for method predicates alone", func(t *testing.T) {
		cfg := corsLintConfig(LocationConfig{
			Match:  MatchConfig{Type: "prefix", Path: "/", Methods: []string{"GET"}},
			CORS:   &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
			Return: 200,
		})
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Message, "will not be selected for its own preflight") {
				t.Fatalf("unexpected diagnostic: %+v", d)
			}
		}
	})
}
