// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"
	"time"

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

func diffDuration(seconds int) config.Duration {
	return config.Duration(time.Duration(seconds) * time.Second)
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

func TestDiffResponseHeadersNoChangeCoversARemoveOp(t *testing.T) {
	// A "remove" op has a nil Value on both sides — this is the only way
	// responseHeaderOpsEqual's per-field comparison (via stringPtrsEqual)
	// exercises the "at least one side is nil" branch instead of always
	// comparing two present values.
	loc := config.LocationConfig{
		Return:          200,
		ResponseHeaders: []config.ResponseHeaderOp{{Op: "remove", Name: "Server"}},
	}
	before := responsePolicyServer(loc)
	after := responsePolicyServer(loc)
	d := diffConfigs(before, after)
	for _, m := range d.Modifications {
		if m.Kind == "response_headers" {
			t.Fatalf("expected no response_headers modification for an identical remove-op list, got %+v", m)
		}
	}
}

// TestResponseHeaderOpsEqualDirect exercises responseHeaderOpsEqual and
// stringPtrsEqual directly, independent of whether diffConfigs' server/location
// correlation happens to reach this comparator for a given fixture.
func TestResponseHeaderOpsEqualDirect(t *testing.T) {
	set := func(v string) []config.ResponseHeaderOp {
		return []config.ResponseHeaderOp{{Op: "set", Name: "X", Value: diffStrPtr(v)}}
	}
	remove := []config.ResponseHeaderOp{{Op: "remove", Name: "X"}}

	if !responseHeaderOpsEqual(set("v"), set("v")) {
		t.Error("identical set ops should be equal")
	}
	if responseHeaderOpsEqual(set("v"), set("other")) {
		t.Error("different values should not be equal")
	}
	if !responseHeaderOpsEqual(remove, remove) {
		t.Error("identical remove ops (both nil Value) should be equal")
	}
	if responseHeaderOpsEqual(remove, set("v")) {
		t.Error("a remove op (nil Value) and a set op (non-nil Value) should not be equal")
	}
	if responseHeaderOpsEqual(set("v"), remove) {
		t.Error("a set op (non-nil Value) and a remove op (nil Value) should not be equal")
	}
}

func TestDiffCORSRemoval(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS:   &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	})
	after := responsePolicyServer(config.LocationConfig{Return: 200})
	d := diffConfigs(before, after)
	found := false
	for _, m := range d.Modifications {
		if m.Kind == "cors" && strings.Contains(m.Detail, "Remove CORS policy") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cors-kind modification naming the policy removal, got: %+v", d.Modifications)
	}
}

func TestDiffCORSEnabledToggleWithBothPresent(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS:   &config.CORSConfig{Enabled: true, AllowedOrigins: []string{"https://app.example.test"}},
	})
	after := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS:   &config.CORSConfig{Enabled: false, AllowedOrigins: []string{"https://app.example.test"}},
	})
	d := diffConfigs(before, after)
	found := false
	for _, m := range d.Modifications {
		if m.Kind == "cors" && strings.Contains(m.Detail, "Disable CORS") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a cors-kind modification naming the disable, got: %+v", d.Modifications)
	}
}

func TestDiffCORSAllOriginMethodHeaderMaxAgeFieldsNamed(t *testing.T) {
	before := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS: &config.CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://a.example.test"},
			AllowedMethods: []string{"GET"},
			AllowedHeaders: []string{"Content-Type"},
		},
	})
	maxAge := diffDuration(10 * 60)
	after := responsePolicyServer(config.LocationConfig{
		Return: 200,
		CORS: &config.CORSConfig{
			Enabled:        true,
			AllowedOrigins: []string{"https://b.example.test"},
			AllowedMethods: []string{"GET", "POST"},
			AllowedHeaders: []string{"Content-Type", "Authorization"},
			MaxAge:         &maxAge,
		},
	})
	d := diffConfigs(before, after)
	var fields []string
	for _, m := range d.Modifications {
		if m.Kind == "cors" {
			fields = append(fields, m.Detail)
		}
	}
	joined := strings.Join(fields, " | ")
	for _, want := range []string{"allowed_origins", "allowed_methods", "allowed_headers", "max_age"} {
		if !strings.Contains(joined, want) {
			t.Errorf("modifications = %q, want a mention of %s", joined, want)
		}
	}
}
