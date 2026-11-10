// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"errors"
	"fmt"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// Guest exports named by the ABIs.
const (
	exportHandleRequest  = "handle_request"
	exportHandleResponse = "handle_response"
	// exportABIV2Marker is the jul-abi/v2 declaration: a () -> () export the
	// host never calls.
	exportABIV2Marker = ABIJulV2
)

// abiDecl is what negotiateABI proved about a module.
type abiDecl struct {
	// hasResponse reports that a v2 module exports handle_response.
	hasResponse bool
	// readsBody reports that the module imports read_request_body from its
	// ABI's host module. It is static capability, not per-request demand: a
	// module that imports the function may still not call it (#699).
	readsBody bool
}

// importRequestBody is the host import whose presence makes a module's
// request body prepared before each handle_request (#699).
const importRequestBody = "read_request_body"

// negotiateABI verifies, statically and before instantiation, that the
// compiled module declares the configured ABI (ADR 0020 §1). No guest code
// runs to decide it.
func negotiateABI(abi string, compiled wazero.CompiledModule) (abiDecl, error) {
	exports := compiled.ExportedFunctions()
	importsV1, importsV2 := false, false
	readsV1, readsV2 := false, false
	for _, def := range compiled.ImportedFunctions() {
		mod, name, _ := def.Import()
		switch mod {
		case hostModuleV1:
			importsV1 = true
			readsV1 = readsV1 || name == importRequestBody
		case hostModuleV2:
			importsV2 = true
			readsV2 = readsV2 || name == importRequestBody
		}
	}
	_, marker := exports[exportABIV2Marker]
	switch abi {
	case ABIJulV1:
		if marker || importsV2 {
			return abiDecl{}, fmt.Errorf("module declares %s but the plugin is configured for %s (set abi = %q)", ABIJulV2, ABIJulV1, ABIJulV2)
		}
		if _, ok := exports[exportHandleRequest]; !ok {
			return abiDecl{}, errors.New("module does not export handle_request (build it against the Jul.IA plugin SDK)")
		}
		return abiDecl{readsBody: readsV1}, nil
	case ABIJulV2:
		if !marker {
			return abiDecl{}, fmt.Errorf("module does not declare %s (no %s export); it is a %s module or was not built against the v2 SDK", ABIJulV2, exportABIV2Marker, ABIJulV1)
		}
		if importsV1 {
			return abiDecl{}, fmt.Errorf("%s module imports the %s host module %q; v2 guests import only %q", ABIJulV2, ABIJulV1, hostModuleV1, hostModuleV2)
		}
		if err := checkSignature(exports, exportABIV2Marker, nil, nil); err != nil {
			return abiDecl{}, err
		}
		if err := checkSignature(exports, exportHandleRequest, nil, []api.ValueType{api.ValueTypeI32}); err != nil {
			return abiDecl{}, err
		}
		_, hasResponse := exports[exportHandleResponse]
		if hasResponse {
			if err := checkSignature(exports, exportHandleResponse, nil, []api.ValueType{api.ValueTypeI32}); err != nil {
				return abiDecl{}, err
			}
		}
		return abiDecl{hasResponse: hasResponse, readsBody: readsV2}, nil
	default:
		return abiDecl{}, fmt.Errorf("unknown ABI %q", abi)
	}
}

func checkSignature(exports map[string]api.FunctionDefinition, name string, params, results []api.ValueType) error {
	def, ok := exports[name]
	if !ok {
		return fmt.Errorf("%s module does not export %s", ABIJulV2, name)
	}
	if !sameTypes(def.ParamTypes(), params) || !sameTypes(def.ResultTypes(), results) {
		return fmt.Errorf("%s export %s has type (%s)->(%s), want (%s)->(%s)", ABIJulV2, name,
			typeNames(def.ParamTypes()), typeNames(def.ResultTypes()), typeNames(params), typeNames(results))
	}
	return nil
}

func sameTypes(a, b []api.ValueType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func typeNames(ts []api.ValueType) string {
	out := ""
	for i, t := range ts {
		if i > 0 {
			out += ","
		}
		out += api.ValueTypeName(t)
	}
	return out
}
