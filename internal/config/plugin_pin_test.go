// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestParseSHA256Pin(t *testing.T) {
	hex := strings.Repeat("0123456789abcdef", 4)
	for in, want := range map[string]string{
		"":                                      "",
		"  ":                                    "",
		hex:                                     hex,
		strings.ToUpper(hex):                    hex,
		"sha256:" + hex:                         hex,
		" SHA256:" + strings.ToUpper(hex) + " ": hex,
	} {
		got, err := ParseSHA256Pin(in)
		if err != nil || got != want {
			t.Errorf("ParseSHA256Pin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", hex + "0", strings.Repeat("z", 64), "sha512:" + hex, "sha256:" + hex[:63] + "g"} {
		if _, err := ParseSHA256Pin(bad); err == nil {
			t.Errorf("ParseSHA256Pin(%q) accepted", bad)
		}
	}
}

func TestValidatePluginsRejectsMalformedPin(t *testing.T) {
	errs := validatePlugins(map[string]PluginConfig{"p": {Inline: "AGFzbQEAAAA=", SHA256: "not-a-digest"}})
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "[plugins.p]: sha256") {
		t.Fatalf("errs = %v", errs)
	}
	if errs := validatePlugins(map[string]PluginConfig{"p": {Inline: "AGFzbQEAAAA=", SHA256: strings.Repeat("a", 64)}}); len(errs) != 0 {
		t.Fatalf("valid pin rejected: %v", errs)
	}
}
