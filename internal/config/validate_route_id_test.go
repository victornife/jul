// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// routeIDConfig returns a minimal single-location configuration whose location
// carries id (nil means omitted).
func routeIDConfig(id *string) *Config {
	return &Config{Servers: []ServerConfig{{
		Listen: ":8080",
		Locations: []LocationConfig{{
			Match:   MatchConfig{Type: "prefix", Path: "/"},
			RouteID: id,
			Return:  200,
		}},
	}}}
}

func TestValidateRouteIDGrammar(t *testing.T) {
	cases := []struct {
		name    string
		id      *string
		wantErr string
	}{
		{"omitted is valid", nil, ""},
		{"simple valid id", sp("public-api"), ""},
		{"valid id with digits and underscore", sp("r-k7m2q9x4vb8nfp3jd6ths5wzy0"), ""},
		{"single character valid id", sp("a"), ""},
		{"present-empty is rejected", sp(""), "present and empty"},
		{"uppercase is rejected", sp("Public-Api"), "want lowercase"},
		{"invalid leading character (hyphen)", sp("-abc"), "must start with"},
		{"invalid leading character (underscore)", sp("_abc"), "must start with"},
		{"invalid punctuation", sp("abc.def"), "want lowercase"},
		{"invalid space", sp("abc def"), "want lowercase"},
		{"exactly 64 bytes is valid", sp(strings.Repeat("a", 64)), ""},
		{"65 bytes is rejected", sp(strings.Repeat("a", 65)), "want at most 64"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(routeIDConfig(tc.id))
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected an error mentioning %q, got none", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestValidateRouteIDGlobalUniqueness proves a duplicate route_id is rejected
// and the error names both conflicting locations (ADR 0019 §4).
func TestValidateRouteIDGlobalUniqueness(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{
		{
			Listen: ":8080",
			Locations: []LocationConfig{
				{Match: MatchConfig{Type: "prefix", Path: "/a"}, RouteID: sp("dup"), Return: 200},
				{Match: MatchConfig{Type: "prefix", Path: "/b"}, RouteID: sp("dup"), Return: 200},
			},
		},
		{
			Listen: ":8081",
			Locations: []LocationConfig{
				{Match: MatchConfig{Type: "prefix", Path: "/c"}, RouteID: sp("unique"), Return: 200},
			},
		},
	}}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected a duplicate route_id error")
	}
	msg := err.Error()
	if !strings.Contains(msg, `duplicate route_id "dup"`) {
		t.Fatalf("error = %v, want it to name the duplicate route_id", err)
	}
	if !strings.Contains(msg, "servers[0].locations[0]") || !strings.Contains(msg, "servers[0].locations[1]") {
		t.Fatalf("error = %v, want it to identify both conflicting locations", err)
	}
}

// TestValidateRouteIDUniquenessIsGlobalNotPerServer proves two different
// server blocks are not allowed to share a route_id either.
func TestValidateRouteIDUniquenessIsGlobalNotPerServer(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{
		{Listen: ":8080", Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/a"}, RouteID: sp("shared"), Return: 200}}},
		{Listen: ":8081", Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/b"}, RouteID: sp("shared"), Return: 200}}},
	}}
	err := Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), `duplicate route_id "shared"`) {
		t.Fatalf("Validate() = %v, want a duplicate route_id error across server blocks", err)
	}
}

// TestValidateRouteIDTwoIDLessRoutesRemainLegal proves routes without an ID
// never collide with each other, however many there are.
func TestValidateRouteIDTwoIDLessRoutesRemainLegal(t *testing.T) {
	cfg := &Config{Servers: []ServerConfig{{
		Listen: ":8080",
		Locations: []LocationConfig{
			{Match: MatchConfig{Type: "prefix", Path: "/a"}, Return: 200},
			{Match: MatchConfig{Type: "prefix", Path: "/b"}, Return: 200},
			{Match: MatchConfig{Type: "prefix", Path: "/c"}, Return: 200},
		},
	}}}
	if err := Validate(cfg); err != nil {
		t.Fatalf("unexpected error for ID-less routes: %v", err)
	}
}

// TestParseRouteIDPresenceRoundTrips proves omitted, present, and
// present-empty survive the strict TOML decoder distinctly.
func TestParseRouteIDPresenceRoundTrips(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		cfg, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		if cfg.Servers[0].Locations[0].RouteID != nil {
			t.Fatalf("RouteID = %v, want nil (omitted)", cfg.Servers[0].Locations[0].RouteID)
		}
	})

	t.Run("present", func(t *testing.T) {
		cfg, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\nroute_id = \"public-api\"\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got := cfg.Servers[0].Locations[0].RouteID
		if got == nil || *got != "public-api" {
			t.Fatalf("RouteID = %v, want \"public-api\"", got)
		}
	})

	t.Run("present-empty is invalid at validation, not parse", func(t *testing.T) {
		cfg, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\nroute_id = \"\"\n"))
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		got := cfg.Servers[0].Locations[0].RouteID
		if got == nil || *got != "" {
			t.Fatalf("RouteID = %v, want a present-but-empty pointer", got)
		}
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "present and empty") {
			t.Fatalf("Validate() = %v, want a present-and-empty route_id error", err)
		}
	})
}

// TestMarshalRouteIDOmittedAddsNothing proves a canonical rewrite never
// injects a route_id for a location that did not have one (ADR 0019 §4:
// "config.Parse contains no randomness source, proven structurally").
func TestMarshalRouteIDOmittedAddsNothing(t *testing.T) {
	cfg, err := Parse([]byte("[[servers]]\nlisten = \"127.0.0.1:8080\"\n[[servers.locations]]\nmatch = { type = \"prefix\", path = \"/\" }\nreturn = 200\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), "route_id") {
		t.Fatalf("canonical rewrite injected a route_id where none was configured:\n%s", out)
	}
}
