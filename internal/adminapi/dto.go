// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import "reflect"

// This file is the source of every schema in the generated OpenAPI document.
//
// ADR 0019 §29 forbids a hand-maintained parallel schema: the Go types below
// are reflected into JSON Schema by internal/apicontract, so a field added to a
// DTO reaches the published contract by regeneration and a field removed from
// one cannot linger in the document. There is no second place to edit.
//
// A type reaches the document only by being registered here under the name an
// operation refers to. Registering is the deliberate act; defining a struct in
// this package is not enough.

// HealthResponse is the liveness and readiness probe body. `reason` and
// `detail` are present only on a failing readiness check and name the
// subsystem class that is not ready — never a path, a credential or a
// configuration value.
type HealthResponse struct {
	// Status is "ok" for a passing check and "not ready" for a failing one.
	Status string `json:"status"`
	// Reason is a bounded class identifier for a failed readiness check.
	Reason string `json:"reason,omitempty"`
	// Detail is a human-readable elaboration of Reason. It is not a machine
	// contract and may change in any release.
	Detail string `json:"detail,omitempty"`
}

// schemaTypes maps an OpenAPI component name to the Go type that defines it.
// The contract test asserts that every name an operation refers to resolves
// here, so a typo in an ExternalOperation fails the build rather than
// publishing an operation with no schema.
var schemaTypes = map[string]reflect.Type{
	"HealthResponse":    reflect.TypeFor[HealthResponse](),
	"ErrorEnvelope":     reflect.TypeFor[Envelope](),
	"ErrorBody":         reflect.TypeFor[Body](),
	"ErrorDetails":      reflect.TypeFor[Details](),
	"ValidationFinding": reflect.TypeFor[Finding](),

	// The /api/v1 read surface.
	"StatusResponse":       reflect.TypeFor[StatusResponse](),
	"CapabilitiesResponse": reflect.TypeFor[CapabilitiesResponse](),
	"ApplySummary":         reflect.TypeFor[ApplySummary](),
	"Degradation":          reflect.TypeFor[Degradation](),
	"DriftState":           reflect.TypeFor[DriftState](),
	"PendingRestartState":  reflect.TypeFor[PendingRestartState](),
	"LedgerRetention":      reflect.TypeFor[LedgerRetention](),
	"EndpointAvailability": reflect.TypeFor[EndpointAvailability](),
}

// SchemaTypes returns the registered external DTOs by component name.
func SchemaTypes() map[string]reflect.Type {
	out := make(map[string]reflect.Type, len(schemaTypes))
	for k, v := range schemaTypes {
		out[k] = v
	}
	return out
}

// ComponentNameFor returns the registered component name for a Go type, so the
// reflector can emit a $ref instead of inlining a type that is already a named
// component. It reports false for an unregistered type, which the reflector
// then inlines.
func ComponentNameFor(t reflect.Type) (string, bool) {
	for name, rt := range schemaTypes {
		if rt == t {
			return name, true
		}
	}
	return "", false
}

// NonJSONSchemas are the external responses that are not JSON and therefore
// have no Go DTO to reflect. They are declared explicitly rather than being
// invented by the generator, and each names the media type it is served as.
//
// /metrics is the only member: Prometheus exposition is a released text format
// owned by Prometheus, and restating it as a JSON Schema would claim a
// structure Jul does not define.
var NonJSONSchemas = map[string]struct {
	MediaType   string
	Description string
}{
	"PrometheusExposition": {
		MediaType: "text/plain; version=0.0.4; charset=utf-8",
		Description: "Prometheus text exposition format. The format is owned by Prometheus and is not restated here; " +
			"the set of metric names and labels Jul emits is documented in docs/metrics-contract.json.",
	},
}

// UniversalErrorCodes are the codes any external operation may return, so an
// operation lists only the conditions specific to it. A generated client must
// handle all of these on every call.
//
// insecure_transport is universal on every authenticated route because §28.1's
// gate runs before route lookup: it is not a property of the operation.
// rate_limited is universal because the admin rate limiter wraps the whole mux.
func UniversalErrorCodes(public bool) []Code {
	if public {
		// A public route consumes no credential, so neither the transport gate
		// nor authentication applies to it.
		return []Code{CodeRateLimited, CodeInternalError}
	}
	return []Code{
		CodeInsecureTransport,
		CodeUnauthenticated,
		CodeForbidden,
		CodeRateLimited,
		CodeInternalError,
	}
}
