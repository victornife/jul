// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"testing"

	"jul/internal/config"
)

// ADR 0018 §13/§15: response_headers and cors are registered lifecycle paths
// (SubHeaders, SubCORS), so a change to either is reported by the registry-
// driven completeness pass without needing a dedicated high-level comparator —
// the same mechanism #145's match predicates already rely on for everything
// this file does not special-case.

func responsePolicyServer(loc config.LocationConfig) *config.Config {
	loc.Match = config.MatchConfig{Type: "prefix", Path: "/"}
	return &config.Config{Servers: []config.ServerConfig{{Listen: ":8080", Locations: []config.LocationConfig{loc}}}}
}

func TestDiffReportsResponseHeaderChanges(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{Return: 200})
	after := responsePolicyServer(config.LocationConfig{
		Return: 200,
		ResponseHeaders: []config.ResponseHeaderOp{
			{Op: "set", Name: "X-Frame-Options", Value: diffStrPtr("DENY")},
		},
	})
	d := diffConfigs(before, after)
	if len(d.Affected) == 0 {
		t.Fatalf("expected the diff to report the response_headers change, got none: %+v", d)
	}
}

func TestDiffReportsCORSChanges(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{Return: 200})
	after := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS:   &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	})
	d := diffConfigs(before, after)
	if len(d.Affected) == 0 {
		t.Fatalf("expected the diff to report the cors change, got none: %+v", d)
	}
}

func TestDiffNoChangeWhenPolicyIdentical(t *testing.T) {
	loc := config.LocationConfig{
		Return: 200,
		ResponseHeaders: []config.ResponseHeaderOp{
			{Op: "set", Name: "X-Frame-Options", Value: diffStrPtr("DENY")},
		},
		CORS: &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	}
	before := responsePolicyServer(loc)
	after := responsePolicyServer(loc)
	d := diffConfigs(before, after)
	if len(d.Affected) != 0 {
		t.Fatalf("expected no diff for identical policies, got: %+v", d.Affected)
	}
}
