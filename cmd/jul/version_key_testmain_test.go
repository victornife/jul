// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"jul/internal/versionid"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jul-cli-version-key-")
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
