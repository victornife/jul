// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/plugins/wasmtest"
)

// The offline budget check must learn a plugin's facts without running guest
// code. The fixture's WebAssembly start function never terminates, so any
// instantiation would spin until its context ends.
func TestInspectionFactsDoesNotInstantiate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spin.wasm")
	if err := os.WriteFile(path, wasmtest.StartLoopV2(), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]config.PluginConfig{"spin": {
		Path: path, Type: "middleware", ABI: config.PluginABIV2,
		MaxRequestBody: config.Size(16 << 20), MemoryLimit: config.Size(32 << 20),
	}}
	m := testManager(t)

	// Control: Build instantiates, so the start function runs until the
	// deadline interrupts it. This proves the fixture really executes code.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	start := time.Now()
	set, err := m.Build(ctx, cfg)
	cancel()
	if err == nil {
		_ = set.Close()
		t.Fatal("Build of a module with a nonterminating start function succeeded")
	}
	if !strings.Contains(err.Error(), "instantiate module") || !strings.Contains(err.Error(), "deadline exceeded") || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("control Build did not spin in the start function until its deadline: %v", err)
	}

	// A generous deadline that the inspection path must never approach: it
	// only bounds the test if instantiation is ever reintroduced.
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start = time.Now()
	facts, err := m.InspectionFacts(ctx, cfg)
	if err != nil {
		t.Fatalf("InspectionFacts: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second || ctx.Err() != nil {
		t.Fatalf("InspectionFacts took %s; it must not run the start function", elapsed)
	}
	want := inspection.PluginFacts{
		Name: "spin", MaxRequestBody: 16 << 20, MaxResponseBody: maxResponseBodyBuffer,
		MaxFetchResponse: 1 << 20, MemoryLimit: 32 << 20, ReadsBody: true,
	}
	if got := facts["spin"]; got != want {
		t.Fatalf("facts = %+v, want %+v", got, want)
	}
}

func TestPluginBuildAndInspectionRejectInvalidMemoryLimit(t *testing.T) {
	for name, limit := range map[string]config.Size{
		"sub-page": 32 << 10,
		"over-max": (1 << 32) + (1 << 16),
		"overflow": 1 << 48,
	} {
		t.Run(name, func(t *testing.T) {
			cfg := map[string]config.PluginConfig{"invalid": {
				Path: testdataDir + "header-inject.wasm", Type: "middleware", MemoryLimit: limit,
			}}
			m := testManager(t)
			if set, err := m.Build(context.Background(), cfg); err == nil {
				_ = set.Close()
				t.Fatal("Build accepted an invalid memory limit")
			} else if !strings.Contains(err.Error(), "memory_limit") {
				t.Fatalf("Build error = %v, want memory_limit error", err)
			}
			if _, err := m.InspectionFacts(context.Background(), cfg); err == nil {
				t.Fatal("InspectionFacts accepted an invalid memory limit")
			} else if !strings.Contains(err.Error(), "memory_limit") {
				t.Fatalf("InspectionFacts error = %v, want memory_limit error", err)
			}
		})
	}
}

func TestInspectionFactsRoundsMaximumMemoryLimit(t *testing.T) {
	cfg := map[string]config.PluginConfig{"p": {
		Path: testdataDir + "header-inject.wasm", Type: "middleware",
		MemoryLimit: (1 << 32) + (1 << 16) - 1,
	}}
	facts, err := testManager(t).InspectionFacts(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := facts["p"].MemoryLimit, uint64(1<<32); got != want {
		t.Fatalf("effective memory limit = %d, want %d", got, want)
	}
}

// Build and InspectionFacts share module reading, limits and ABI negotiation,
// so they must report identical facts for every fixture shape.
func TestInspectionFactsMatchBuild(t *testing.T) {
	cfg := map[string]config.PluginConfig{
		"v2":      {Path: testdataDir + "testguest-v2.wasm", Type: "middleware", ABI: config.PluginABIV2, Fetch: true},
		"redact":  {Path: testdataDir + "v2-redact.wasm", Type: "middleware", ABI: config.PluginABIV2, MaxResponseBody: config.Size(2 << 20)},
		"handler": {Path: testdataDir + "v2-status-header.wasm", Type: "handler", ABI: config.PluginABIV2},
		"v1":      {Path: testdataDir + "header-inject.wasm", Type: "middleware"},
		"rounded": {Path: testdataDir + "header-inject.wasm", Type: "middleware", MemoryLimit: (16 << 20) + 1},
	}
	m := testManager(t)
	set, err := m.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer set.Close()
	facts, err := m.InspectionFacts(context.Background(), cfg)
	if err != nil {
		t.Fatalf("InspectionFacts: %v", err)
	}
	for name := range cfg {
		built, ok := set.InspectionFacts(name)
		if !ok || facts[name] != built {
			t.Errorf("%s: inspected %+v, built %+v", name, facts[name], built)
		}
	}
	if got, want := facts["rounded"].MemoryLimit, uint64(16<<20); got != want {
		t.Errorf("rounded effective memory limit = %d, want %d", got, want)
	}
}

func TestInspectionFactsRejectsInvalidModules(t *testing.T) {
	m := testManager(t)
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage.wasm")
	if err := os.WriteFile(garbage, []byte("\x00asm\x01\x00\x00\x00\xff"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]config.PluginConfig{
		"missing":      {Path: filepath.Join(dir, "missing.wasm"), Type: "middleware"},
		"garbage":      {Path: garbage, Type: "middleware"},
		"abi mismatch": {Path: testdataDir + "testguest-v2.wasm", Type: "middleware", ABI: config.PluginABIV1},
		"unknown abi":  {Path: testdataDir + "testguest-v2.wasm", Type: "middleware", ABI: "jul-abi/v9"},
	}
	for name, pc := range cases {
		if _, err := m.InspectionFacts(context.Background(), map[string]config.PluginConfig{"p": pc}); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := m.InspectionFacts(ctx, map[string]config.PluginConfig{"p": {Path: testdataDir + "testguest-v2.wasm", Type: "middleware", ABI: config.PluginABIV2}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}
