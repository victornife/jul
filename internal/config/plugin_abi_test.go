// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

func TestValidatePluginsABI(t *testing.T) {
	const module = "AGFzbQEAAAA="
	for abi, ok := range map[string]bool{"": true, PluginABIV1: true, PluginABIV2: true, "jul-abi/v3": false, "JUL-ABI/V2": false} {
		errs := validatePlugins(map[string]PluginConfig{"p": {Inline: module, ABI: abi}})
		if (len(errs) == 0) != ok {
			t.Errorf("abi %q: errs = %v", abi, errs)
		}
		if !ok && !strings.Contains(errs[0].Error(), "invalid abi") {
			t.Errorf("abi %q: %v", abi, errs)
		}
	}
	big := PluginConfig{Inline: module, ABI: PluginABIV2, MaxResponseBody: Size(MaxPluginV2ResponseBody + 1)}
	if errs := validatePlugins(map[string]PluginConfig{"p": big}); len(errs) != 1 || !strings.Contains(errs[0].Error(), "1g") {
		t.Fatalf("oversized v2 max_response_body: %v", errs)
	}
	big.ABI = ""
	if errs := validatePlugins(map[string]PluginConfig{"p": big}); len(errs) != 0 {
		t.Fatalf("v1 max_response_body is not bounded by the v2 rule: %v", errs)
	}
	if EffectivePluginABI(PluginConfig{}) != PluginABIV1 || EffectivePluginABI(PluginConfig{ABI: PluginABIV2}) != PluginABIV2 {
		t.Fatal("EffectivePluginABI")
	}
}
