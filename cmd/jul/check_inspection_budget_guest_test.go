// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/plugins/wasmtest"
)

// spinPluginTOML configures a body-reading plugin whose WebAssembly start
// function never terminates, so the check passes only if the guest is
// inspected without being instantiated.
func spinPluginTOML(t *testing.T, mode, budget string) string {
	t.Helper()
	guest := filepath.Join(t.TempDir(), "spin.wasm")
	if err := os.WriteFile(guest, wasmtest.StartLoopV2(), 0o600); err != nil {
		t.Fatal(err)
	}
	return `
[global.inspection]
mode = "` + mode + `"
max_buffered_bytes = "` + budget + `"

[plugins.spin]
path = "` + filepath.ToSlash(guest) + `"
abi = "jul-abi/v2"
max_request_body = "16m"

[[servers]]
listen = "127.0.0.1:0"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  plugins = ["spin"]
  return = 204
`
}

// withinDeadline runs fn and fails the test, instead of hanging the suite,
// if it does not return promptly.
func withinDeadline(t *testing.T, fn func() int) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- fn() }()
	select {
	case code := <-done:
		return code
	case <-time.After(60 * time.Second):
		t.Fatal("check did not finish: plugin guest code is being executed")
		return -1
	}
}

func TestCmdCheckInspectsPluginWithoutRunningGuestCode(t *testing.T) {
	path := writeTemp(t, spinPluginTOML(t, "enforce", "4m"))
	code, out, errOut := capture(t, func() int {
		return withinDeadline(t, func() int { return cmdCheck([]string{"-config", path}) })
	})
	if code != 1 || !strings.Contains(errOut, "plugin request bodies") {
		t.Fatalf("enforce shortfall: code=%d stdout=%q stderr=%q", code, out, errOut)
	}

	path = writeTemp(t, spinPluginTOML(t, "enforce", "256m"))
	code, out, errOut = capture(t, func() int {
		return withinDeadline(t, func() int { return cmdCheck([]string{"-config", path}) })
	})
	if code != 0 || !strings.Contains(out, "is valid") {
		t.Fatalf("enforce fits: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestLegacyCheckInspectsPluginWithoutRunningGuestCode(t *testing.T) {
	args, flags, usage := os.Args, flag.CommandLine, flag.Usage
	t.Cleanup(func() { os.Args, flag.CommandLine, flag.Usage = args, flags, usage })
	for _, tc := range []struct {
		mode, budget string
		want         int
	}{
		{"observe", "4m", 0},
		{"enforce", "4m", 1},
		{"enforce", "256m", 0},
	} {
		flag.CommandLine = flag.NewFlagSet("jul", flag.ContinueOnError)
		os.Args = []string{"jul", "--check", "--config", writeTemp(t, spinPluginTOML(t, tc.mode, tc.budget))}
		if code := withinDeadline(t, run); code != tc.want {
			t.Fatalf("jul --check %s %s: code=%d, want %d", tc.mode, tc.budget, code, tc.want)
		}
	}
}
