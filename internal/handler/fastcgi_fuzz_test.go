// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"strings"
	"testing"
)

// FuzzScriptName ensures the FastCGI SCRIPT_NAME computed from an attacker-
// controlled URL path can never contain a ".." traversal segment and always
// stays absolute, regardless of input. A fixed, trusted index file models the
// realistic threat surface (only the request path is attacker-controlled).
func FuzzScriptName(f *testing.F) {
	seeds := []string{
		"/index.php", "/app/", "/../../etc/passwd", "/a/../../b",
		"/%2e%2e/x", "/", "", "/dir/..//..//evil", "\x00", "/foo/./bar",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, urlPath string) {
		got := scriptNameFor(urlPath, []string{"index.php"})
		if !strings.HasPrefix(got, "/") {
			t.Fatalf("scriptNameFor(%q) = %q, not absolute", urlPath, got)
		}
		for _, seg := range strings.Split(got, "/") {
			if seg == ".." {
				t.Fatalf("scriptNameFor(%q) = %q leaks a %q traversal segment", urlPath, got, "..")
			}
		}
	})
}

// FuzzParseSocketAddress ensures upstream socket address parsing never panics
// and only ever yields a known network type.
func FuzzParseSocketAddress(f *testing.F) {
	seeds := []string{
		"unix:/run/php.sock", "tcp://127.0.0.1:9000", "127.0.0.1:9000",
		"unix:", "tcp://", "", ":::", "unix:tcp://x", "\x00",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, pass string) {
		network, _ := parseSocketAddress(pass)
		switch network {
		case "unix", "tcp":
		default:
			t.Fatalf("parseSocketAddress(%q) network = %q, want unix or tcp", pass, network)
		}
	})
}
