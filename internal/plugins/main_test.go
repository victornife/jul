// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"
)

var testCompilationCacheDir string

type pluginTestSuite struct {
	*testing.M
}

func (suite pluginTestSuite) Run() int {
	code := suite.M.Run()
	if err := os.RemoveAll(testCompilationCacheDir); err != nil {
		fmt.Fprintln(os.Stderr, "remove plugin test compilation cache:", err)
		return 1
	}
	return code
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "jul-plugin-test-cache-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create plugin test compilation cache:", err)
		os.Exit(1)
	}
	testCompilationCacheDir = dir
	goleak.VerifyTestMain(pluginTestSuite{M: m})
}
