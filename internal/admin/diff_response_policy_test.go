// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// ADR 0018 §13/§15: response_headers and cors are registered lifecycle paths
// (SubHeaders, SubCORS). diffResponseHeaders/diffCORS (diff_helpers.go) give
// them the same granular, operator-facing modification entries the other
// route fields get (#147 requirement); the registry-driven completeness pass
// still backstops any registered leaf they do not name explicitly.

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

func TestDiffResponseHeaderModificationNamesOpAndHeaderNotValue(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{Return: 200})
	after := responsePolicyServer(config.LocationConfig{
		Return: 200,
		ResponseHeaders: []config.ResponseHeaderOp{
			{Op: "set", Name: "X-Frame-Options", Value: diffStrPtr("super-secret-value")},
		},
	})
	d := diffConfigs(before, after)
	if len(d.Modifications) == 0 {
		t.Fatalf("expected a granular modification entry, got none: %+v", d)
	}
	found := false
	for _, m := range d.Modifications {
		if m.Kind != "response_headers" {
			continue
		}
		found = true
		if !strings.Contains(m.After, "set X-Frame-Options") {
			t.Errorf("modification After = %q, want it to name the op and header", m.After)
		}
		if strings.Contains(m.After, "super-secret-value") || strings.Contains(m.Before, "super-secret-value") {
			t.Errorf("modification leaked the header value: %+v", m)
		}
	}
	if !found {
		t.Fatal("expected a response_headers-kind modification entry")
	}
}

func TestDiffCORSModificationNamesEnabledToggle(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{Return: 200})
	after := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS:   &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	})
	d := diffConfigs(before, after)
	found := false
	for _, m := range d.Modifications {
		if m.Kind == "cors" && strings.Contains(m.Detail, "Add CORS policy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cors-kind modification entry naming the policy addition, got: %+v", d.Modifications)
	}
}

func TestDiffCORSModificationNamesChangedFields(t *testing.T) {
	loc := config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}}
	before := responsePolicyServer(config.LocationConfig{Return: 200, CORS: &loc})
	changed := loc
	changed.AllowCredentials = true
	changed.ExposedHeaders = []string{"X-Request-Id"}
	after := responsePolicyServer(config.LocationConfig{Return: 200, CORS: &changed})

	d := diffConfigs(before, after)
	var fields []string
	for _, m := range d.Modifications {
		if m.Kind == "cors" {
			fields = append(fields, m.Detail)
		}
	}
	joined := strings.Join(fields, " | ")
	if !strings.Contains(joined, "allow_credentials") {
		t.Errorf("modifications = %q, want a mention of allow_credentials", joined)
	}
	if !strings.Contains(joined, "exposed_headers") {
		t.Errorf("modifications = %q, want a mention of exposed_headers", joined)
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
