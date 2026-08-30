// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package diagnostics

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestSanitizeErrorStringRemovesURLsAndAbsolutePaths(t *testing.T) {
	t.Parallel()
	input := `open /private/var/folders/operator/server.toml: fetch https://user:pass@example.test/private?token=fixture: read C:\Users\Alice\server.toml and \\server\share\secret.toml`
	got := SanitizeErrorString(input)
	for _, forbidden := range []string{"/private/var", "example.test", `C:\Users`, `\\server\share`, "fixture", "user", "pass"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("%q survived error sanitization: %s", forbidden, got)
		}
	}
	if !strings.Contains(got, "[PATH REDACTED]") || !strings.Contains(got, "[URL REDACTED]") {
		t.Fatalf("expected path and URL markers: %s", got)
	}
	if got := SanitizeString("request path /healthz remains useful"); !strings.Contains(got, "/healthz") {
		t.Fatalf("ordinary text path was unexpectedly removed: %s", got)
	}
}

func TestSanitizeResultTreatsErrorEvidenceStructurally(t *testing.T) {
	t.Parallel()
	result := SanitizeResult(Result{Evidence: map[string]any{
		"error":  "open /tmp/operator/config.toml: password=hunter2",
		"errors": []string{"dial https://example.test/private", `read C:\Users\Alice\key.pem`},
		"reason": fmt.Errorf("stat /var/lib/jul/cache"),
		"safe":   "/api/v1/status",
	}})
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"/tmp/operator", "hunter2", "example.test", `C:\\Users`, "/var/lib/jul"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("%q survived structural error sanitization: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "/api/v1/status") {
		t.Fatalf("non-error evidence was unexpectedly redacted: %s", text)
	}
}
