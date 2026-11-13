// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package app

import (
	"context"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/inspection"
)

func wafBudgetConfig(t *testing.T, mode, bodyLimit string) *config.Config {
	t.Helper()
	src := `
[global.inspection]
mode = "` + mode + `"

[waf]
enabled = true
crs_enabled = true
`
	if bodyLimit != "" {
		src += `request_body_limit = "` + bodyLimit + `"
`
	}
	src += `
[[servers]]
listen = "127.0.0.1:0"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
`
	cfg, err := config.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The default CRS policy (128 KiB request body limit, 1 MiB request headers,
// 64 KiB response-header ceiling) fits the 256 MiB default budget, so an
// upgrade with defaults keeps starting in enforce mode.
func TestDefaultWAFRouteFitsDefaultBudget(t *testing.T) {
	cfg := wafBudgetConfig(t, "enforce", "")
	if warning, err := CheckInspectionBudget(context.Background(), cfg); err != nil || warning != nil {
		t.Fatalf("check: warning=%v err=%v", warning, err)
	}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	_, genID, commit, _, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	commit()
	minimum, route, ok := f.InspectionRequirement(genID)
	if !ok || minimum != 258654208 || !strings.Contains(route, "servers[0].locations[0]") {
		t.Fatalf("requirement = %d %q %v", minimum, route, ok)
	}
	limits, err := inspection.LimitsFromConfig(cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	if minimum > limits.MaxBytes {
		t.Fatalf("default WAF route %d B exceeds default budget %d B", minimum, limits.MaxBytes)
	}
}

// A WAF request_body_limit of 1 MiB needs about 288.6 MiB for one
// worst-case request: enforce refuses it consistently in check and in the
// handler build that startup, reload and admin apply share; observe warns.
func TestWAFRouteAboveDefaultBudget(t *testing.T) {
	enforce := wafBudgetConfig(t, "enforce", "1m")
	if _, err := CheckInspectionBudget(context.Background(), enforce); err == nil || !strings.Contains(err.Error(), "needs at least 302694400 B") {
		t.Fatalf("check err = %v", err)
	}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	if _, _, _, _, err := f.Prepare(context.Background(), enforce); err == nil || !strings.Contains(err.Error(), "max_buffered_bytes") {
		t.Fatalf("Prepare err = %v", err)
	}
	if _, _, err := f.Build(context.Background(), enforce, false); err == nil {
		t.Fatal("preflight Build accepted an insufficient enforce budget")
	}

	observe := wafBudgetConfig(t, "observe", "1m")
	warning, err := CheckInspectionBudget(context.Background(), observe)
	if err != nil || warning == nil || !strings.Contains(warning.Error(), "observe") {
		t.Fatalf("observe check: warning=%v err=%v", warning, err)
	}
	_, genID, commit, _, err := f.Prepare(context.Background(), observe)
	if err != nil {
		t.Fatalf("observe Prepare: %v", err)
	}
	commit()
	if minimum, _, ok := f.InspectionRequirement(genID); !ok || minimum != 302694400 {
		t.Fatalf("observe requirement = %d %v", minimum, ok)
	}
}
