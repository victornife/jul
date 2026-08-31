// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package buildcaps

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"jul/internal/plugins"
	"jul/internal/stream"
	"jul/internal/waf"
)

// TestNamedCoversEveryFlag is the property this package exists to guarantee:
// `jul capabilities` and GET /api/v1/capabilities read one source, so a flag
// added to the struct and forgotten in the display order would silently vanish
// from the CLI's human output while still appearing in the API.
func TestNamedCoversEveryFlag(t *testing.T) {
	rt := reflect.TypeFor[Flags]()
	jsonNames := make(map[string]bool, rt.NumField())
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Fatalf("Flags.%s has no json name; it is a published contract key", rt.Field(i).Name)
		}
		jsonNames[name] = true
	}

	named := Compiled().Named()
	if len(named) != rt.NumField() {
		t.Fatalf("Named() returns %d rows for %d fields", len(named), rt.NumField())
	}
	seen := make(map[string]bool, len(named))
	for _, f := range named {
		if !jsonNames[f.Name] {
			t.Errorf("Named() reports %q, which is not a json key of Flags", f.Name)
		}
		if seen[f.Name] {
			t.Errorf("Named() reports %q twice", f.Name)
		}
		seen[f.Name] = true
	}
	for name := range jsonNames {
		if !seen[name] {
			t.Errorf("flag %q is absent from Named(); it would vanish from the CLI's human output", name)
		}
	}
}

// TestNamedReportsTheSameValuesAsTheStruct: the display list must not be a
// second, hand-maintained copy of the values.
func TestNamedReportsTheSameValuesAsTheStruct(t *testing.T) {
	f := Flags{WAF: true, GRPC: true, Kubernetes: true}
	byName := map[string]bool{}
	for _, n := range f.Named() {
		byName[n.Name] = n.Enabled
	}
	if !byName["waf"] || !byName["grpc"] || !byName["kubernetes"] {
		t.Fatalf("set flags did not survive Named(): %v", byName)
	}
	if byName["acme"] || byName["console"] {
		t.Fatalf("unset flags reported enabled: %v", byName)
	}
}

// TestFlagsMarshalWithStableKeys pins the wire keys, which appear in two
// published surfaces and in every script that reads them.
func TestFlagsMarshalWithStableKeys(t *testing.T) {
	b, err := json.Marshal(Flags{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"waf":false,"stream_proxy":false,"wasm_plugins":false,"acme":false,` +
		`"grpc":false,"http3":false,"otel":false,"console":false,"brotli":false,` +
		`"zstd":false,"importer":false,"consul":false,"kubernetes":false}`
	if string(b) != want {
		t.Fatalf("the published flag keys changed.\n got: %s\nwant: %s", b, want)
	}
}

// TestCompiledReadsTheSourcePackagesRatherThanACopy. Three flags come from
// constants their own packages export; restating them here is how the report
// and the runtime would come to disagree about the same build.
func TestCompiledReadsTheSourcePackagesRatherThanACopy(t *testing.T) {
	got := Compiled()
	if got.WAF != waf.Compiled {
		t.Errorf("waf = %v, waf.Compiled = %v", got.WAF, waf.Compiled)
	}
	if got.StreamProxy != stream.Compiled {
		t.Errorf("stream_proxy = %v, stream.Compiled = %v", got.StreamProxy, stream.Compiled)
	}
	if got.WASMPlugins != plugins.Compiled {
		t.Errorf("wasm_plugins = %v, plugins.Compiled = %v", got.WASMPlugins, plugins.Compiled)
	}
}

// TestCompiledIsStable: the answer is a property of the binary, so repeated
// calls must agree. A flag flipped at runtime would make the two published
// surfaces disagree depending on when each was read.
func TestCompiledIsStable(t *testing.T) {
	first := Compiled()
	for range 5 {
		if got := Compiled(); got != first {
			t.Fatalf("Compiled() returned %+v then %+v for the same binary", first, got)
		}
	}
}
