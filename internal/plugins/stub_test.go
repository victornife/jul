// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !wasmplugins

package plugins

import (
	"context"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestLeanBuildRejectsConfiguredPlugins(t *testing.T) {
	if Compiled {
		t.Fatal("lean plugin test compiled with Compiled=true")
	}

	manager, err := NewManager(Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer func() {
		if err := manager.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	_, err = manager.Build(context.Background(), map[string]config.PluginConfig{
		"example": {Path: "example.wasm"},
	})
	if err == nil {
		t.Fatal("configured plugin accepted by a build without wasmplugins")
	}
	if !strings.Contains(err.Error(), "wasmplugins") {
		t.Fatalf("Build error = %q, want actionable wasmplugins tag guidance", err)
	}
}

func TestLeanBuildNoPluginSurfaceIsSafe(t *testing.T) {
	manager, err := NewManager(Options{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	set, err := manager.Build(context.Background(), nil)
	if err != nil {
		t.Fatalf("Build(empty): %v", err)
	}
	if set.Has("missing") {
		t.Fatal("lean empty set unexpectedly reports a plugin")
	}
	if set.Middleware("missing") != nil {
		t.Fatal("lean empty set unexpectedly returns middleware")
	}
	if set.Handler("missing") != nil {
		t.Fatal("lean empty set unexpectedly returns a handler")
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Set.Close: %v", err)
	}
}
