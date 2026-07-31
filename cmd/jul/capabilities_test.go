// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCmdCapabilitiesReportsEveryOptionalTag guards the capabilities contract:
// every optional build tag must appear as a feature key so operators and
// automation can confirm exactly what a binary supports (F-05).
func TestCmdCapabilitiesReportsEveryOptionalTag(t *testing.T) {
	code, out, _ := capture(t, func() int { return cmdCapabilities([]string{"-json"}) })
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, out)
	}
	var got capabilitiesOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("capabilities -json is not valid JSON: %v\n%s", err, out)
	}
	for _, key := range []string{
		`"waf"`, `"stream_proxy"`, `"wasm_plugins"`, `"acme"`, `"grpc"`,
		`"http3"`, `"otel"`, `"console"`, `"brotli"`, `"zstd"`,
		`"importer"`, `"consul"`, `"kubernetes"`,
	} {
		if !strings.Contains(out, key) {
			t.Errorf("capabilities -json missing feature key %s\n%s", key, out)
		}
	}
	if len(got.ExitCodes) == 0 {
		t.Error("exit_codes table is empty")
	}
}
