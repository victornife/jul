// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !importer

package main

import (
	"strings"
	"testing"
)

func TestCmdImportStubMessage(t *testing.T) {
	code, _, errOut := capture(t, func() int {
		return cmdImport([]string{"nginx", "x.conf"})
	})
	if code != 1 {
		t.Errorf("stub exit = %d, want 1", code)
	}
	if !strings.Contains(errOut, "importer tag") {
		t.Errorf("expected the stub to mention the importer tag:\n%s", errOut)
	}
}
