// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"jul/internal/versionid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionKeyCommandLifecycle(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private", "key")
	t.Setenv(versionid.Environment, path)
	for _, args := range [][]string{{}, {"rotate"}, {"init", "extra"}, {"init", "--unknown"}} {
		code, _, _ := capture(t, func() int { return cmdVersionKey(args) })
		if code != 2 {
			t.Fatalf("invalid args accepted: %v", args)
		}
	}
	code, out, _ := capture(t, func() int { return cmdVersionKey([]string{"init"}) })
	if code != 0 || !strings.Contains(out, path) {
		t.Fatal("provisioning failed")
	}
	before, _ := os.ReadFile(path)
	if strings.Contains(out, string(before)) {
		t.Fatal("key contents printed")
	}
	code, _, _ = capture(t, func() int { return cmdVersionKey([]string{"init", "--file", path}) })
	after, _ := os.ReadFile(path)
	if code != 0 || !bytes.Equal(before, after) {
		t.Fatal("existing key replaced")
	}
	os.WriteFile(path, []byte("corrupt"), 0600)
	code, _, _ = capture(t, func() int { return cmdVersionKey([]string{"init", "--file", path}) })
	if code != 1 {
		t.Fatal("corrupt established key repaired")
	}
}

func TestServingCommandsSelectExplicitKeyPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	cfg := writeTemp(t, validConfig)
	for _, runCommand := range []func() int{
		func() int { return cmdServe([]string{"--config", cfg, "--effective-version-key-file", missing}) },
		func() int {
			return cmdRun([]string{"--proxy", "http://127.0.0.1:9000", "--effective-version-key-file", missing})
		},
	} {
		code, _, _ := capture(t, runCommand)
		if code != 1 {
			t.Fatal("explicit missing key fell back to environment/default key")
		}
	}
}
