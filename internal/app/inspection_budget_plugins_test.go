// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

func pluginBudgetConfig(t *testing.T, mode string) *config.Config {
	t.Helper()
	guest, err := filepath.Abs(v2GuestPath())
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(`
[global.inspection]
mode = "` + mode + `"
max_buffered_bytes = "4m"

[plugins.p]
path = "` + filepath.ToSlash(guest) + `"
abi = "jul-abi/v2"
max_request_body = "16m"

[[servers]]
listen = "127.0.0.1:0"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  plugins = ["p"]
  return = 204
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// A plugin allowed a 16 MiB request body cannot fit a 4 MiB aggregate
// budget: enforce refuses it in check and in the shared handler build, and
// observe only warns.
func TestPluginBodyAboveBudget(t *testing.T) {
	enforce := pluginBudgetConfig(t, "enforce")
	_, err := CheckInspectionBudget(context.Background(), enforce)
	if err == nil || !strings.Contains(err.Error(), "plugin request bodies") {
		t.Fatalf("check err = %v", err)
	}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	if _, _, _, _, err := f.Prepare(context.Background(), enforce); err == nil || !strings.Contains(err.Error(), "plugin request bodies") {
		t.Fatalf("Prepare err = %v", err)
	}

	observe := pluginBudgetConfig(t, "observe")
	warning, err := CheckInspectionBudget(context.Background(), observe)
	if err != nil || warning == nil {
		t.Fatalf("observe check: warning=%v err=%v", warning, err)
	}
	_, genID, commit, _, err := f.Prepare(context.Background(), observe)
	if err != nil {
		t.Fatalf("observe Prepare: %v", err)
	}
	commit()
	// base + 2 × (16 MiB + 1) for the body reader + v2 response buffers.
	minimum, _, ok := f.InspectionRequirement(genID)
	if !ok || minimum < 2*(16<<20+1) {
		t.Fatalf("observe requirement = %d %v", minimum, ok)
	}
}
