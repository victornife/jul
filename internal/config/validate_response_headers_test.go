// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// locationPolicyConfig returns a minimal configuration whose single location
// carries loc, with a default prefix "/" match.
func locationPolicyConfig(loc LocationConfig) *Config {
	loc.Match = MatchConfig{Type: "prefix", Path: "/"}
	if loc.Return == 0 {
		loc.Return = 200
	}
	return &Config{Servers: []ServerConfig{{Listen: ":8080", Locations: []LocationConfig{loc}}}}
}

func requirePolicyError(t *testing.T, cfg *Config, want string) {
	t.Helper()
	err := Validate(cfg)
	if err == nil {
		t.Fatalf("configuration was accepted; want an error mentioning %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to mention %q", err, want)
	}
}

func TestResponseHeaderOpValidation(t *testing.T) {
	t.Run("valid add/set/remove accepted", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "X-Frame-Options", Value: sp("DENY")},
			{Op: "add", Name: "Set-Cookie", Value: sp("a=b")},
			{Op: "remove", Name: "X-Powered-By"},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("empty value on add/set is legal", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "X-Test", Value: sp("")},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("omitted value on add/set is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "X-Test"},
		}}), "value is required")
	})

	t.Run("value on remove is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "remove", Name: "X-Test", Value: sp("x")},
		}}), "takes no value")
	})

	t.Run("invalid op rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "delete", Name: "X-Test", Value: sp("x")},
		}}), "invalid op")
	})

	t.Run("pseudo-header rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: ":status", Value: sp("x")},
		}}), "pseudo-header")
	})

	for _, name := range []string{"Connection", "Content-Length", "Transfer-Encoding", "Upgrade", "Keep-Alive", "Proxy-Connection", "TE", "Trailer", "Proxy-Authenticate", "Proxy-Authorization"} {
		t.Run("hop-by-hop "+name+" rejected", func(t *testing.T) {
			requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
				{Op: "set", Name: name, Value: sp("x")},
			}}), "hop-by-hop")
		})
	}

	t.Run("Content-Encoding rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "remove", Name: "Content-Encoding"},
		}}), "compression layer")
	})

	t.Run("value with a C0 control other than CRLF is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "X-Test", Value: sp("a\x01b")},
		}}), "field-value grammar")
	})

	t.Run("value over the byte limit is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "X-Test", Value: sp(strings.Repeat("a", MaxResponseHeaderValueBytes+1))},
		}}), "over the limit")
	})

	t.Run("too many operations rejected", func(t *testing.T) {
		ops := make([]ResponseHeaderOp, MaxResponseHeaderOps+1)
		for i := range ops {
			ops[i] = ResponseHeaderOp{Op: "remove", Name: "X-Test"}
		}
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: ops}), "over the limit")
	})
}

func TestVaryOperationValidation(t *testing.T) {
	t.Run("add on Vary at a non-cached location is permitted", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "add", Name: "Vary", Value: sp("X-Tenant")},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("any Vary operation on a cached location is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{
			Cache: true,
			ResponseHeaders: []ResponseHeaderOp{
				{Op: "add", Name: "Vary", Value: sp("X-Tenant")},
			},
		}), "cache = true")
	})

	t.Run("set on Vary at a non-cached location is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "set", Name: "Vary", Value: sp("X-Tenant")},
		}}), "only op = \"add\" is permitted")
	})

	t.Run("remove on Vary at a non-cached location is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{ResponseHeaders: []ResponseHeaderOp{
			{Op: "remove", Name: "Vary"},
		}}), "only op = \"add\" is permitted")
	})
}

func TestAccessControlOperationOwnership(t *testing.T) {
	t.Run("Access-Control-* operation rejected when cors.enabled", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{
			CORS: &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
			ResponseHeaders: []ResponseHeaderOp{
				{Op: "set", Name: "Access-Control-Allow-Origin", Value: sp("*")},
			},
		}), "owned by cors")
	})

	t.Run("Access-Control-* operation permitted when cors.enabled = false", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{
			ResponseHeaders: []ResponseHeaderOp{
				{Op: "set", Name: "Access-Control-Allow-Origin", Value: sp("*")},
			},
		})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
