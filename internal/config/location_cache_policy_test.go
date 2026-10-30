// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

func TestLocationCachePolicyParseAndValidate(t *testing.T) {
	validTOML := `
[cache]
enabled = true
default_ttl = "5m"
stale_while_revalidate = "30s"
stale_if_error = "10m"

[[servers]]
listen = ":8080"
server_names = ["example.com"]

  [[servers.locations]]
  route_id = "api-route"
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:9000"
  cache = true
    [servers.locations.cache_policy]
    default_ttl = "0s"
    max_ttl = "2m"
    stale_while_revalidate = "15s"
    stale_if_error = "0s"
    bypass_cookies = ["Session_ID", "__Host-auth"]
`
	cfg, err := Parse([]byte(validTOML))
	if err != nil {
		t.Fatalf("Parse valid TOML failed: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate valid TOML failed: %v", err)
	}
	cp := cfg.Servers[0].Locations[0].CachePolicy
	if cp == nil {
		t.Fatal("expected non-nil CachePolicy")
	}
	if cp.DefaultTTL == nil || cp.DefaultTTL.Std() != 0 {
		t.Fatalf("expected DefaultTTL=0s, got %+v", cp.DefaultTTL)
	}
	if cp.MaxTTL == nil || cp.MaxTTL.Std() != 2*time.Minute {
		t.Fatalf("expected MaxTTL=2m, got %+v", cp.MaxTTL)
	}
	if cp.StaleWhileRevalidate == nil || cp.StaleWhileRevalidate.Std() != 15*time.Second {
		t.Fatalf("expected SWR=15s, got %+v", cp.StaleWhileRevalidate)
	}
	if cp.StaleIfError == nil || cp.StaleIfError.Std() != 0 {
		t.Fatalf("expected SIE=0s, got %+v", cp.StaleIfError)
	}
	if len(cp.BypassCookies) != 2 {
		t.Fatalf("expected 2 bypass cookies, got %v", cp.BypassCookies)
	}
}

func TestLocationCachePolicyValidationRejections(t *testing.T) {
	tooManyCookies := make([]string, 33)
	for i := range tooManyCookies {
		tooManyCookies[i] = `"c` + string(rune('a'+(i%26))) + string(rune('0'+(i/26))) + `"`
	}
	longCookie := `"` + strings.Repeat("a", 129) + `"`

	cases := []struct {
		name    string
		policy  string
		wantSub string
	}{
		{
			name:    "negative_default_ttl",
			policy:  `default_ttl = "-1s"`,
			wantSub: "default_ttl",
		},
		{
			name:    "negative_max_ttl",
			policy:  `max_ttl = "-5s"`,
			wantSub: "max_ttl",
		},
		{
			name:    "negative_swr",
			policy:  `stale_while_revalidate = "-1s"`,
			wantSub: "stale_while_revalidate",
		},
		{
			name:    "negative_sie",
			policy:  `stale_if_error = "-1s"`,
			wantSub: "stale_if_error",
		},
		{
			name: "bypass_any_and_named_mutually_exclusive",
			policy: `
bypass_any_cookie = true
bypass_cookies = ["sid"]
`,
			wantSub: "cannot be combined with bypass_cookies",
		},
		{
			name:    "too_many_bypass_cookies",
			policy:  `bypass_cookies = [` + strings.Join(tooManyCookies, ", ") + `]`,
			wantSub: "exceeds maximum of 32",
		},
		{
			name:    "oversized_cookie_name",
			policy:  `bypass_cookies = [` + longCookie + `]`,
			wantSub: "128 bytes",
		},
		{
			name:    "empty_cookie_name",
			policy:  `bypass_cookies = [""]`,
			wantSub: "must be a non-empty HTTP cookie-name token",
		},
		{
			name:    "cookie_name_with_equals",
			policy:  `bypass_cookies = ["sid=123"]`,
			wantSub: "must be a non-empty HTTP cookie-name token",
		},
		{
			name:    "cookie_name_with_semicolon",
			policy:  `bypass_cookies = ["sid;Path=/"]`,
			wantSub: "must be a non-empty HTTP cookie-name token",
		},
		{
			name:    "cookie_name_with_space",
			policy:  `bypass_cookies = ["bad name"]`,
			wantSub: "must be a non-empty HTTP cookie-name token",
		},
		{
			name:    "duplicate_cookie_names_case_insensitive",
			policy:  `bypass_cookies = ["Session_ID", "session_id"]`,
			wantSub: "duplicates",
		},
		{
			name:    "duration_exceeds_100_years",
			policy:  `max_ttl = "880000h"`,
			wantSub: "100 years",
		},
		{
			name:    "default_ttl_exceeds_max_ttl",
			policy:  "default_ttl = \"10m\"\nmax_ttl = \"1m\"",
			wantSub: "must not exceed max_ttl",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := `
[cache]
enabled = true

[[servers]]
listen = ":8080"
server_names = ["example.com"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api" }
  proxy_pass = "http://127.0.0.1:9000"
  cache = true
    [servers.locations.cache_policy]
    ` + tc.policy + "\n"
			cfg, err := Parse([]byte(doc))
			if err != nil {
				if strings.Contains(err.Error(), tc.wantSub) {
					return
				}
				t.Fatalf("unexpected parse error: %v", err)
			}
			err = Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantSub, err)
			}
		})
	}

	// Non-cacheable action with cache_policy is rejected; handler plugin with cache_policy is accepted.
	oneMin := Duration(time.Minute)
	if errs := validateLocationCachePolicy(LocationConfig{
		Return:      200,
		CachePolicy: &LocationCachePolicy{MaxTTL: &oneMin},
	}, "servers[0].locations[0]"); len(errs) == 0 || !strings.Contains(errs[0].Error(), "cacheable location actions") {
		t.Fatalf("expected non-cacheable action error, got %v", errs)
	}
	if errs := validateLocationCachePolicy(LocationConfig{
		Plugin:      "act",
		CachePolicy: &LocationCachePolicy{MaxTTL: &oneMin},
	}, "servers[0].locations[0]"); len(errs) != 0 {
		t.Fatalf("expected handler plugin with cache_policy to pass, got %v", errs)
	}
}
