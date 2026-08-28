// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"testing"
)

func TestCORSValidation(t *testing.T) {
	t.Run("valid enabled policy accepted", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:          true,
			AllowedOrigins:   []string{"https://app.example.test"},
			AllowedMethods:   []string{"GET", "POST"},
			AllowedHeaders:   []string{"Content-Type", "Authorization"},
			ExposedHeaders:   []string{"X-Request-Id"},
			AllowCredentials: true,
			MaxAge:           durationPtr(10 * 60),
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("enabled without allowed_origins is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{Enabled: true}}), "allowed_origins is required")
	})

	t.Run("disabled without allowed_origins is accepted", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{Enabled: false}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("wildcard is unconditional and exclusive", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"*", "https://app.example.test"},
		}}), "may not be combined")
	})

	t.Run("wildcard forbids credentials, even when disabled", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:          false,
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
		}}), "forbids allow_credentials")
	})

	t.Run("wildcard alone is accepted", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"*"},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("null origin is accepted literally", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"null"},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	for _, bad := range []string{
		"app.example.test",             // missing scheme
		"ftp://app.example.test",       // wrong scheme
		"https://app.example.test/",    // trailing path
		"https://app.example.test/api", // path
		"https://app.example.test?x=1", // query
		"https://app.example.test:443", // explicit default port
		"http://app.example.test:80",   // explicit default port
		"https://user@app.example.test",
	} {
		t.Run("malformed origin "+bad+" is rejected", func(t *testing.T) {
			requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
				Enabled:        true,
				AllowedOrigins: []string{bad},
			}}), "is not a valid origin")
		})
	}

	t.Run("explicit default port on a non-default-scheme case is fine", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test:8443"},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("allowed_methods explicitly empty is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
			AllowedMethods: []string{},
		}}), "allowed_methods is explicitly empty")
	})

	t.Run("omitted allowed_methods is accepted", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	for _, field := range []string{"allowed_methods", "allowed_headers", "exposed_headers"} {
		t.Run("wildcard rejected in "+field, func(t *testing.T) {
			cors := &CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}}
			switch field {
			case "allowed_methods":
				cors.AllowedMethods = []string{"*"}
			case "allowed_headers":
				cors.AllowedHeaders = []string{"*"}
			case "exposed_headers":
				cors.ExposedHeaders = []string{"*"}
			}
			requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: cors}), "not permitted here")
		})
	}

	t.Run("max_age must be whole seconds", func(t *testing.T) {
		d := Duration(500 * 1e6) // 500ms
		requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
			MaxAge:         &d,
		}}), "not a whole number of seconds")
	})

	t.Run("max_age zero is legal", func(t *testing.T) {
		d := Duration(0)
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
			MaxAge:         &d,
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("max_age over 24h is rejected", func(t *testing.T) {
		requirePolicyError(t, locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
			MaxAge:         durationPtr(25 * 3600),
		}}), "over the limit of 24h")
	})

	t.Run("max_age omitted emits no header at validation time (no error)", func(t *testing.T) {
		cfg := locationPolicyConfig(LocationConfig{CORS: &CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://app.example.test"},
		}})
		if err := Validate(cfg); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// durationPtr returns a *Duration for n seconds.
func durationPtr(seconds int64) *Duration {
	d := Duration(seconds * 1e9)
	return &d
}
