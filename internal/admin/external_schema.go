// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "reflect"

// ExternalSchemaTypes deliberately registers canonical admin-package wire types
// used by the v1 adapters. This avoids Go -> OpenAPI -> duplicate Go models and
// preserves pointer/omitempty/null presence semantics of the authoritative
// operations. Registration is the publication boundary; arbitrary Console
// structs remain absent.
func ExternalSchemaTypes() map[string]reflect.Type {
	return map[string]reflect.Type{
		"ConfigValidationResponse":     reflect.TypeFor[v1ValidationResponse](),
		"ConfigPlanResponse":           reflect.TypeFor[rawConfigPreviewResponse](),
		"RouteTestRequest":             reflect.TypeFor[routeTestRequest](),
		"RouteTestResponse":            reflect.TypeFor[routeTestResult](),
		"PatchApplyRequest":            reflect.TypeFor[patchApplyRequest](),
		"PatchPreviewResponse":         reflect.TypeFor[patchPreviewResponse](),
		"ConfigRollbackRequest":        reflect.TypeFor[v1RollbackRequest](),
		"AdoptExternalRequest":         reflect.TypeFor[AdoptExternalRequest](),
		"AdoptPreviewResult":           reflect.TypeFor[AdoptPreviewResult](),
		"ListenerClientAddressRequest": reflect.TypeFor[listenerClientAddressRequest](),
	}
}
