// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import (
	"errors"
	"net/http"
	"reflect"
	"slices"
	"testing"
)

// TestSchemaTypesIsACopy: the registry is the source of every published schema,
// so handing a caller the live map would let one mutate the contract.
func TestSchemaTypesIsACopy(t *testing.T) {
	got := SchemaTypes()
	if len(got) != len(schemaTypes) {
		t.Fatalf("SchemaTypes returned %d entries, the registry has %d", len(got), len(schemaTypes))
	}
	for name, rt := range schemaTypes {
		if got[name] != rt {
			t.Errorf("SchemaTypes()[%q] = %v, want %v", name, got[name], rt)
		}
	}

	delete(got, "HealthResponse")
	got["Injected"] = reflect.TypeFor[string]()
	if _, ok := schemaTypes["Injected"]; ok {
		t.Fatal("mutating the returned map changed the registry")
	}
	if _, ok := schemaTypes["HealthResponse"]; !ok {
		t.Fatal("deleting from the returned map removed a registered schema")
	}
}

// TestComponentNameForResolvesRegisteredTypesOnly. The reflector emits a $ref
// for a registered type and inlines everything else, so a wrong answer here
// either produces a dangling reference or duplicates a shape in the document.
func TestComponentNameForResolvesRegisteredTypesOnly(t *testing.T) {
	cases := []struct {
		typ  reflect.Type
		want string
	}{
		{reflect.TypeFor[HealthResponse](), "HealthResponse"},
		{reflect.TypeFor[Envelope](), "ErrorEnvelope"},
		{reflect.TypeFor[Body](), "ErrorBody"},
		{reflect.TypeFor[Details](), "ErrorDetails"},
		{reflect.TypeFor[Finding](), "ValidationFinding"},
	}
	for _, tc := range cases {
		got, ok := ComponentNameFor(tc.typ)
		if !ok {
			t.Errorf("%v is registered but ComponentNameFor reported it unknown", tc.typ)
			continue
		}
		if got != tc.want {
			t.Errorf("ComponentNameFor(%v) = %q, want %q", tc.typ, got, tc.want)
		}
	}

	for _, unregistered := range []reflect.Type{
		reflect.TypeFor[string](),
		reflect.TypeFor[Code](),
		reflect.TypeFor[CodeSpec](),
		reflect.TypeFor[struct{ A int }](),
	} {
		if name, ok := ComponentNameFor(unregistered); ok {
			t.Errorf("ComponentNameFor(%v) resolved to %q; only registered types become components", unregistered, name)
		}
	}
}

// TestUniversalErrorCodes pins the distinction that decides what a generated
// client has to handle on every call. A public route consumes no credential, so
// neither the transport gate nor authentication applies to it — publishing
// those on a probe would tell every monitoring system to handle a 401 it can
// never receive.
func TestUniversalErrorCodes(t *testing.T) {
	pub := UniversalErrorCodes(true)
	for _, forbidden := range []Code{CodeInsecureTransport, CodeUnauthenticated, CodeForbidden} {
		if slices.Contains(pub, forbidden) {
			t.Errorf("a public route publishes %q, which cannot happen without a credential", forbidden)
		}
	}
	for _, required := range []Code{CodeRateLimited, CodeInternalError} {
		if !slices.Contains(pub, required) {
			t.Errorf("a public route does not publish %q", required)
		}
	}

	auth := UniversalErrorCodes(false)
	for _, required := range []Code{CodeInsecureTransport, CodeUnauthenticated, CodeForbidden, CodeRateLimited, CodeInternalError} {
		if !slices.Contains(auth, required) {
			t.Errorf("an authenticated route does not publish %q", required)
		}
	}

	// Every universal code must be a catalogue member, or an operation would
	// reference a response component that does not exist.
	for _, c := range append(append([]Code{}, pub...), auth...) {
		if _, ok := Spec(c); !ok {
			t.Errorf("universal code %q is not in the catalogue", c)
		}
	}
}

func TestCodeString(t *testing.T) {
	if got := CodeStaleBaseVersion.String(); got != "stale_base_version" {
		t.Fatalf("String() = %q", got)
	}
	if got := Code("anything").String(); got != "anything" {
		t.Fatalf("String() = %q", got)
	}
}

// TestNewUsesTheCataloguedMeaning: New exists so an error carries a sentence
// rather than an empty message when the call site has nothing better to say.
func TestNewUsesTheCataloguedMeaning(t *testing.T) {
	e := New(CodeStorageUnavailable)
	spec, _ := Spec(CodeStorageUnavailable)
	if e.Code != CodeStorageUnavailable {
		t.Fatalf("code = %q", e.Code)
	}
	if e.Message != spec.Meaning {
		t.Fatalf("message = %q, want the catalogued meaning %q", e.Message, spec.Meaning)
	}
	if e.Status() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", e.Status())
	}
}

func TestErrorf(t *testing.T) {
	e := Errorf(CodeInvalidRequest, "the %s parameter is required", "base_version")
	if e.Code != CodeInvalidRequest {
		t.Fatalf("code = %q", e.Code)
	}
	if e.Message != "the base_version parameter is required" {
		t.Fatalf("message = %q", e.Message)
	}
	if got, want := e.Error(), "invalid_request: the base_version parameter is required"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	// It must satisfy error so it can travel through ordinary Go plumbing.
	var err error = e
	var target *Error
	if !errors.As(err, &target) || target.Code != CodeInvalidRequest {
		t.Fatal("*Error does not round-trip through errors.As")
	}
}

// TestWithDetailsCopies is the property the method exists for: specialising a
// package-level sentinel at a call site must not mutate the sentinel, or the
// next caller inherits the previous caller's details.
func TestWithDetailsCopies(t *testing.T) {
	base := New(CodeForbidden)
	first := base.WithDetails(Details{RequiredPermission: "config:apply"})
	second := base.WithDetails(Details{RequiredPermission: "history:rollback"})

	if !reflect.DeepEqual(base.Details, Details{}) {
		t.Fatalf("WithDetails mutated the receiver: %+v", base.Details)
	}
	if first.Details.RequiredPermission != "config:apply" {
		t.Fatalf("first = %q", first.Details.RequiredPermission)
	}
	if second.Details.RequiredPermission != "history:rollback" {
		t.Fatalf("second = %q", second.Details.RequiredPermission)
	}
	if first == base || second == base {
		t.Fatal("WithDetails returned the receiver rather than a copy")
	}
	if first.Code != base.Code || first.Message != base.Message {
		t.Fatal("WithDetails changed something other than the details")
	}
}

// TestStatusForEveryCatalogueMember complements the unknown-code test: the
// lookup branch has to work for all 22, not only the fallback.
func TestStatusForEveryCatalogueMember(t *testing.T) {
	for _, c := range Codes() {
		spec, _ := Spec(c)
		e := &Error{Code: c, Message: "x"}
		if got := e.Status(); got != spec.Status {
			t.Errorf("Error{%q}.Status() = %d, want %d", c, got, spec.Status)
		}
		if got := c.Status(); got != spec.Status {
			t.Errorf("%q.Status() = %d, want %d", c, got, spec.Status)
		}
	}
	// A code outside the catalogue has no status at all, which is how a caller
	// distinguishes "not a contract member" from "500".
	if got := Code("not_a_real_code").Status(); got != 0 {
		t.Fatalf("an uncatalogued code reported status %d; only Error.Status falls back to 500", got)
	}
}

func TestSpecReportsMembership(t *testing.T) {
	if _, ok := Spec(CodeDriftDetected); !ok {
		t.Fatal("drift_detected is not reported as a catalogue member")
	}
	if spec, ok := Spec(Code("")); ok {
		t.Fatalf("the empty code resolved to %+v", spec)
	}
}

// TestNonJSONSchemasAreDeclaredNotInvented: /metrics has no Go DTO, and the
// generator must not fabricate a JSON Schema for a text format Prometheus owns.
func TestNonJSONSchemasAreDeclaredNotInvented(t *testing.T) {
	ns, ok := NonJSONSchemas["PrometheusExposition"]
	if !ok {
		t.Fatal("PrometheusExposition is not declared")
	}
	if ns.MediaType == "" || ns.Description == "" {
		t.Fatalf("PrometheusExposition is incompletely declared: %+v", ns)
	}
	for name := range NonJSONSchemas {
		if _, clash := schemaTypes[name]; clash {
			t.Errorf("%q is declared both as a Go DTO and as a non-JSON schema", name)
		}
	}
}
