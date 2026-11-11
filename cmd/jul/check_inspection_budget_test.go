// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"strings"
	"testing"
)

func TestCmdCheckUninspectedSmallInspectionBudget(t *testing.T) {
	path := writeTemp(t, `
[global.inspection]
mode = "enforce"
max_buffered_bytes = "1m"

[[servers]]
listen = "127.0.0.1:0"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
`)
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", path, "-json"}) })
	if code != 0 || !strings.Contains(out, `"ok":true`) || strings.Contains(out, "warnings") || errOut != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
