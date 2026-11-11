// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package main

import (
	"strings"
	"testing"
)

func wafBudgetTOML(bodyLimit string) string {
	return `
[waf]
enabled = true
crs_enabled = true
request_body_limit = "` + bodyLimit + `"

[[servers]]
listen = "127.0.0.1:0"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 204
`
}

func TestCmdCheckWAFRouteAgainstDefaultInspectionBudget(t *testing.T) {
	code, out, errOut := capture(t, func() int { return cmdCheck([]string{"-config", writeTemp(t, wafBudgetTOML("128k"))}) })
	if code != 0 || errOut != "" {
		t.Fatalf("default WAF: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
	code, out, errOut = capture(t, func() int { return cmdCheck([]string{"-config", writeTemp(t, wafBudgetTOML("1m"))}) })
	if code != 1 || !strings.Contains(errOut, "needs at least 302596096 B") {
		t.Fatalf("1 MiB WAF body: code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}
