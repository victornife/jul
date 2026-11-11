// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"testing"

	"jul/internal/config"
	"jul/internal/inspection"
)

// provisionedInspection gives a fixture whose WAF or plugin limits exceed the
// default aggregate budget an explicit budget, as an operator must (ADR 0032 §7).
func provisionedInspection(cfg *config.Config) *config.Config {
	budget := config.Size(16 << 30)
	cfg.Global.Inspection.MaxBufferedBytes = &budget
	return cfg
}

const uninspectedSmallBudget = `
[global.inspection]
mode = "enforce"
max_buffered_bytes = "1m"

[[servers]]
listen = "127.0.0.1:0"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
`

func TestUninspectedConfigNeedsNoInspectionBudget(t *testing.T) {
	cfg, err := config.Parse([]byte(uninspectedSmallBudget))
	if err != nil {
		t.Fatal(err)
	}
	warning, err := CheckInspectionBudget(context.Background(), cfg)
	if err != nil || warning != nil {
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
	if !ok || minimum != 0 || route != "" {
		t.Fatalf("requirement = %d %q %v, want 0 for an uninspected generation", minimum, route, ok)
	}
	limits, err := inspection.LimitsFromConfig(cfg, 1)
	if err != nil {
		t.Fatal(err)
	}
	limits.MinimumBytes = minimum
	if err := limits.Validate(); err != nil {
		t.Fatalf("1 MiB budget on an uninspected config rejected: %v", err)
	}
}

func TestInspectionRequirementIsPerGeneration(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	f.publishInspectionRequirement(3, inspection.Requirement{RouteRequirement: inspection.RouteRequirement{Route: "r", Bytes: 9}})
	if _, _, ok := f.InspectionRequirement(2); ok {
		t.Fatal("another generation's requirement was returned")
	}
	if minimum, route, ok := f.InspectionRequirement(3); !ok || minimum != 9 || route != "r" {
		t.Fatalf("requirement = %d %q %v", minimum, route, ok)
	}
}
