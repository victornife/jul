// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"jul/internal/versionid"
	"os"
	"path/filepath"
	"testing"
)

// All production Serve tests use a disposable provisioned key. Coordinator-only
// tests may explicitly inject deterministic contexts without filesystem state.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jul-app-version-key-")
	if err != nil {
		panic(err)
	}
	if resolved, e := filepath.EvalSymlinks(dir); e == nil {
		dir = resolved
	}
	path := filepath.Join(dir, "private", "effective-version.key")
	if _, err := versionid.Init(path); err != nil {
		panic(err)
	}
	if err := os.Setenv(versionid.Environment, path); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
