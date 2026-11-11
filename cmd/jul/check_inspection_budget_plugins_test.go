// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func pluginBudgetTOML(t *testing.T, mode string) string {
	t.Helper()
	guest, err := filepath.Abs(filepath.Join("..", "..", "testdata", "plugins", "testguest-v2.wasm"))
	if err != nil {
		t.Fatal(err)
	}
	return `
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
`
}

func TestCmdCheckPluginBodyAboveInspectionBudget(t *testing.T) {
	path := writeTemp(t, pluginBudgetTOML(t, "enforce"))
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path}) })
	if code != 1 || !strings.Contains(errOut, "plugin request bodies") || !strings.Contains(errOut, "max_buffered_bytes") {
		t.Fatalf("enforce: code=%d stdout=%q stderr=%q", code, out, errOut)
	}

	path = writeTemp(t, pluginBudgetTOML(t, "observe"))
	code, out, errOut = capture(t, func() int { return cmdCheck([]string{"-config", path}) })
	if code != 0 || !strings.Contains(errOut, "warning: inspection (observe)") || !strings.Contains(out, "is valid") {
		t.Fatalf("observe: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = capture(t, func() int { return cmdCheck([]string{"-config", path, "-json"}) })
	if code != 0 || !strings.Contains(out, `"warnings":["inspection (observe)`) || errOut != "" {
		t.Fatalf("observe json: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
