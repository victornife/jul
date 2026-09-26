// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"

	"jul/internal/config"
)

// goldenABIV2Path pins the whole jul-abi/v2 contract: host imports, guest
// exports and every numeric value. It is additive-only (ADR 0020 §13).
const goldenABIV2Path = "../../testdata/plugins/abi-v2.golden"

func currentABIV2Contract(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = r.Close(ctx) })
	p := &plugin{name: "golden", kvUsage: newKVLedger()}
	if err := registerJulV2HostModule(ctx, r, p); err != nil {
		t.Fatalf("register v2 host module: %v", err)
	}
	mod := r.Module(hostModuleV2)
	if mod == nil {
		t.Fatal("jul-abi/v2 host module not instantiated")
	}
	var imports []string
	for name, d := range mod.ExportedFunctionDefinitions() {
		imports = append(imports, fmt.Sprintf("import %s %s(%s)->%s", hostModuleV2, name, joinTypes(d.ParamTypes()), joinTypes(d.ResultTypes())))
	}
	sort.Strings(imports)
	lines := []string{"abi " + ABIJulV2}
	lines = append(lines, imports...)
	lines = append(lines,
		"export "+exportABIV2Marker+" ()-> required",
		"export "+exportHandleRequest+" ()->i32 required",
		"export "+exportHandleResponse+" ()->i32 optional",
	)
	consts := []struct {
		name string
		v    int64
	}{
		{"handle_request.stop", int64(actionStop)},
		{"handle_request.continue", int64(actionContinue)},
		{"handle_response.continue", int64(responseContinue)},
		{"handle_response.reject", int64(responseReject)},
		{"subscribe_response.metadata", int64(responseModeMetadata)},
		{"subscribe_response.body", int64(responseModeBody)},
		{"body_state.available", int64(bodyAvailable)},
		{"body_state.not_requested", int64(bodyNotRequested)},
		{"body_state.none", int64(bodyNone)},
		{"body_state.too_large", int64(bodyTooLarge)},
		{"body_state.streaming", int64(bodyStreaming)},
		{"body_state.encoded", int64(bodyEncoded)},
		{"body_state.partial", int64(bodyPartial)},
		{"body_state.upgraded", int64(bodyUpgraded)},
		{"code.ok", int64(codeOK)},
		{"code.not_found", int64(codeNotFound)},
		{"code.invalid", int64(codeInvalid)},
		{"code.forbidden", int64(codeForbidden)},
		{"code.unavailable", int64(codeUnavailable)},
		{"code.too_large", int64(codeTooLarge)},
		{"code.committed", int64(codeCommitted)},
		{"code.wrong_phase", int64(codeWrongPhase)},
		{"code.unsupported", int64(codeUnsupported)},
		{"limit.request_state_bytes", maxRequestState},
		{"limit.added_header_bytes", maxAddedHeaderBytes},
	}
	for _, c := range consts {
		lines = append(lines, fmt.Sprintf("const %s=%d", c.name, c.v))
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestABIV2Golden(t *testing.T) {
	got := currentABIV2Contract(t)
	if os.Getenv("UPDATE_ABI_GOLDEN") == "1" {
		if err := os.WriteFile(goldenABIV2Path, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(goldenABIV2Path)
	if err != nil {
		t.Fatalf("read golden (run with UPDATE_ABI_GOLDEN=1 to seed): %v", err)
	}
	if got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Fatalf("jul-abi/v2 contract changed.\n--- got\n%s--- want\n%s\nAdditive changes require UPDATE_ABI_GOLDEN=1; anything else requires a new ABI id.", got, want)
	}
}

// TestABIV1HostModuleHasNoV2Surface proves the v2 work did not leak into the
// frozen v1 module: no response-phase function is importable as "jul".
func TestABIV1HostModuleHasNoV2Surface(t *testing.T) {
	v1 := currentABISurface(t)
	for _, name := range []string{"resp_", "subscribe_response", "request_state"} {
		if strings.Contains(v1, name) {
			t.Fatalf("v1 surface exposes %q:\n%s", name, v1)
		}
	}
}

// ---- hand-assembled modules for the negotiation matrix ---------------------

type wasmFunc struct {
	export  string
	params  []byte
	results []byte
	body    []byte // instructions, without the trailing end
}

type wasmImport struct{ module, name string }

func leb(n int) []byte {
	var out []byte
	for {
		b := byte(n & 0x7f)
		n >>= 7
		if n != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func wasmName(s string) []byte { return append(leb(len(s)), s...) }

func wasmSection(id byte, items [][]byte) []byte {
	body := leb(len(items))
	for _, it := range items {
		body = append(body, it...)
	}
	return append(append([]byte{id}, leb(len(body))...), body...)
}

// assemble builds a minimal module: every import is a () -> () function, and
// each function gets its own type.
func assemble(imports []wasmImport, funcs []wasmFunc) []byte {
	var types, imps, fns, exps, code [][]byte
	types = append(types, []byte{0x60, 0x00, 0x00})
	for _, im := range imports {
		imps = append(imps, append(append(wasmName(im.module), wasmName(im.name)...), 0x00, 0x00))
	}
	for i, f := range funcs {
		ty := append([]byte{0x60}, leb(len(f.params))...)
		ty = append(ty, f.params...)
		ty = append(ty, leb(len(f.results))...)
		ty = append(ty, f.results...)
		types = append(types, ty)
		fns = append(fns, leb(i+1))
		exps = append(exps, append(wasmName(f.export), append([]byte{0x00}, leb(len(imports)+i)...)...))
		fb := append([]byte{0x00}, f.body...)
		fb = append(fb, 0x0b)
		code = append(code, append(leb(len(fb)), fb...))
	}
	out := append([]byte(nil), wasmHeader...)
	out = append(out, wasmSection(1, types)...)
	if len(imps) > 0 {
		out = append(out, wasmSection(2, imps)...)
	}
	out = append(out, wasmSection(3, fns)...)
	out = append(out, wasmSection(7, exps)...)
	return append(out, wasmSection(10, code)...)
}

const i32 = 0x7f

var (
	fnMarker    = wasmFunc{export: exportABIV2Marker}
	fnRequest   = wasmFunc{export: exportHandleRequest, results: []byte{i32}, body: []byte{0x41, 0x01}}
	fnResponse  = wasmFunc{export: exportHandleResponse, results: []byte{i32}, body: []byte{0x41, 0x00}}
	fnReqNoRes  = wasmFunc{export: exportHandleRequest}
	fnMarkerBad = wasmFunc{export: exportABIV2Marker, results: []byte{i32}, body: []byte{0x41, 0x00}}
	fnRespBad   = wasmFunc{export: exportHandleResponse, params: []byte{i32}, results: []byte{i32}, body: []byte{0x41, 0x00}}
)

func TestNegotiateABIMatrix(t *testing.T) {
	m := testManager(t)
	cases := []struct {
		name    string
		abi     string
		src     func(*config.PluginConfig)
		wantErr string
		resp    bool
	}{
		{"v1 historical fixture", "", fixtureSrc("header-inject"), "", false},
		{"v1 current fixture", ABIJulV1, fixtureSrc("v1-current-header-inject"), "", false},
		{"v2 fixture", ABIJulV2, fixtureSrc("testguest-v2"), "", true},
		{"v2 module under v1", ABIJulV1, fixtureSrc("testguest-v2"), "declares jul-abi/v2", false},
		{"v1 module under v2", ABIJulV2, fixtureSrc("header-inject"), "does not declare jul-abi/v2", false},
		{"v2 minimal request-only", ABIJulV2, inlineSrc(assemble(nil, []wasmFunc{fnMarker, fnRequest})), "", false},
		{"v2 minimal with response", ABIJulV2, inlineSrc(assemble(nil, []wasmFunc{fnMarker, fnRequest, fnResponse})), "", true},
		{"v2 missing handle_request", ABIJulV2, inlineSrc(assemble(nil, []wasmFunc{fnMarker})), "does not export handle_request", false},
		{"v2 handle_request wrong type", ABIJulV2, inlineSrc(assemble(nil, []wasmFunc{fnMarker, fnReqNoRes})), "export handle_request has type", false},
		{"v2 marker wrong type", ABIJulV2, inlineSrc(assemble(nil, []wasmFunc{fnMarkerBad, fnRequest})), "export jul-abi/v2 has type", false},
		{"v2 handle_response wrong type", ABIJulV2, inlineSrc(assemble(nil, []wasmFunc{fnMarker, fnRequest, fnRespBad})), "export handle_response has type", false},
		{"v2 importing v1 host", ABIJulV2, inlineSrc(assemble([]wasmImport{{"jul", "log"}}, []wasmFunc{fnMarker, fnRequest})), `imports the jul-abi/v1 host module "jul"`, false},
		{"v2 importing an unknown v2 function", ABIJulV2, inlineSrc(assemble([]wasmImport{{hostModuleV2, "resp_stream_chunk"}}, []wasmFunc{fnMarker, fnRequest})), "resp_stream_chunk", false},
		{"v1 spoofing the v2 marker", ABIJulV1, inlineSrc(assemble(nil, []wasmFunc{fnMarker, fnRequest})), "declares jul-abi/v2", false},
		{"v1 importing the v2 host", ABIJulV1, inlineSrc(assemble([]wasmImport{{hostModuleV2, "log"}}, []wasmFunc{fnRequest})), "declares jul-abi/v2", false},
		{"unknown abi", "jul-abi/v9", fixtureSrc("header-inject"), "unknown ABI", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pc := config.PluginConfig{ABI: tc.abi}
			tc.src(&pc)
			s, err := m.Build(context.Background(), map[string]config.PluginConfig{"p": pc})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			defer s.Close()
			if got := s.ResponsePoint("p") != nil; got != tc.resp {
				t.Fatalf("response point = %v, want %v", got, tc.resp)
			}
			want := tc.abi
			if want == "" {
				want = ABIJulV1
			}
			if s.ABIs()["p"] != want {
				t.Fatalf("ABIs = %v", s.ABIs())
			}
		})
	}
}

func fixtureSrc(name string) func(*config.PluginConfig) {
	return func(pc *config.PluginConfig) { pc.Path = fixturePath(name) }
}

func inlineSrc(b []byte) func(*config.PluginConfig) {
	return func(pc *config.PluginConfig) { pc.Inline = base64.StdEncoding.EncodeToString(b) }
}

// fixturePath resolves a guest fixture. JUL_PLUGIN_FIXTURES points the suite
// at freshly built examples (CI builds them from source).
func fixturePath(name string) string {
	if dir := os.Getenv("JUL_PLUGIN_FIXTURES"); dir != "" {
		if _, err := os.Stat(dir + "/" + name + ".wasm"); err == nil {
			return dir + "/" + name + ".wasm"
		}
	}
	return testdataDir + name + ".wasm"
}

// TestHandlerTypeV2HasNoResponsePoint: a handler-type plugin never gets the
// response point even when its module exports handle_response.
func TestHandlerTypeV2HasNoResponsePoint(t *testing.T) {
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"h": {Path: fixturePath("testguest-v2"), ABI: ABIJulV2, Type: "handler"}})
	if s.ResponsePoint("h") != nil || s.ResponsePoint("missing") != nil {
		t.Fatal("handler-type or unknown plugin installed a response point")
	}
}
