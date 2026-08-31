// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"jul/internal/admin"
	"jul/internal/adminapi"
)

func build(t *testing.T) *Document {
	t.Helper()
	doc, err := Build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return doc
}

// operationsOf flattens a document into pattern+method keys so the tests can
// compare it with the route catalog without walking PathItem by hand.
func operationsOf(doc *Document) map[string]*Operation {
	out := map[string]*Operation{}
	for pattern, item := range doc.Paths {
		for method, op := range map[string]*Operation{
			http.MethodGet:    item.Get,
			http.MethodPost:   item.Post,
			http.MethodPut:    item.Put,
			http.MethodPatch:  item.Patch,
			http.MethodDelete: item.Delete,
			http.MethodHead:   item.Head,
		} {
			if op != nil {
				out[method+" "+pattern] = op
			}
		}
	}
	return out
}

// TestEveryExternalRouteAppearsInOpenAPIAndViceVersa is ADR 0019 §24–§25's
// required test, in both directions. It is what makes the route catalog and the
// published document incapable of disagreeing: a route promoted to external and
// not regenerated fails, and a path in the document with no external route
// behind it fails too.
func TestEveryExternalRouteAppearsInOpenAPIAndViceVersa(t *testing.T) {
	doc := build(t)
	ops := operationsOf(doc)

	for _, r := range admin.ExternalRoutes() {
		key := r.Method + " " + r.Pattern
		op, ok := ops[key]
		if !ok {
			t.Errorf("external route %s is absent from the generated document; run `make api-contract-generate`", key)
			continue
		}
		if op.OperationID != r.Operation.ID {
			t.Errorf("%s: operationId is %q in the document and %q in the catalog", key, op.OperationID, r.Operation.ID)
		}
		delete(ops, key)
	}
	for key := range ops {
		t.Errorf("the generated document publishes %s, which is not an external route in the catalog", key)
	}
}

// TestPermissionsAgreeOnBothSides. A published permission that does not match
// the one the server enforces is worse than no permission at all: it tells an
// operator to issue a token that will not work, or one wider than necessary.
func TestPermissionsAgreeOnBothSides(t *testing.T) {
	ops := operationsOf(build(t))
	for _, r := range admin.ExternalRoutes() {
		op := ops[r.Method+" "+r.Pattern]
		if op == nil {
			continue // reported by the test above
		}
		if len(op.Permissions) != len(r.Permissions) {
			t.Errorf("%s %s: document publishes permissions %v, catalog enforces %v", r.Method, r.Pattern, op.Permissions, r.Permissions)
			continue
		}
		for i := range op.Permissions {
			if op.Permissions[i] != r.Permissions[i] {
				t.Errorf("%s %s: document publishes permissions %v, catalog enforces %v", r.Method, r.Pattern, op.Permissions, r.Permissions)
				break
			}
		}
	}
}

// TestNoInternalRouteIsPublished is the negative half, driven by the
// classification inventory rather than by the catalog, so the two independent
// records of "this is internal" have to agree.
func TestNoInternalRouteIsPublished(t *testing.T) {
	doc := build(t)
	for pattern := range admin.InternalRouteReasons() {
		if _, ok := doc.Paths[pattern]; ok {
			t.Errorf("internal route %q is published in the generated document", pattern)
		}
	}
}

// TestAnUnclassifiedRouteIsNotExternal pins the fail-closed zero value. It is
// the property that makes "no route becomes external merely because the Console
// calls it" hold for routes that do not exist yet.
func TestAnUnclassifiedRouteIsNotExternal(t *testing.T) {
	var unclassified admin.RouteStability
	if unclassified != admin.StabilityInternal {
		t.Fatal("the zero RouteStability must be StabilityInternal")
	}
	if unclassified.External() {
		t.Fatal("an unclassified route must not be external: the zero value is the fail-closed default")
	}
}

// TestDeprecatedOperationsAreMarked. §29 includes deprecated routes in the
// document deliberately — an endpoint still served, still supported and under a
// Sunset header has to be described somewhere a client can find it — so the
// marking is what distinguishes it from a supported one.
func TestDeprecatedOperationsAreMarked(t *testing.T) {
	ops := operationsOf(build(t))
	for _, r := range admin.ExternalRoutes() {
		op := ops[r.Method+" "+r.Pattern]
		if op == nil {
			continue
		}
		want := r.Stability == admin.StabilityDeprecated
		if op.Deprecated != want {
			t.Errorf("%s %s: deprecated = %v, want %v", r.Method, r.Pattern, op.Deprecated, want)
		}
		if want && op.Sunset == "" {
			t.Errorf("%s %s is deprecated but publishes no sunset date", r.Method, r.Pattern)
		}
	}
}

// TestPublicOperationsDeclareNoCredential. An OpenAPI operation with an empty
// security requirement says "no credential"; one that inherits a default says
// the opposite. Getting this backwards on a probe would tell every monitoring
// system to send a token it does not have.
func TestPublicOperationsDeclareNoCredential(t *testing.T) {
	ops := operationsOf(build(t))
	for _, r := range admin.ExternalRoutes() {
		op := ops[r.Method+" "+r.Pattern]
		if op == nil {
			continue
		}
		if r.Public {
			if len(op.Security) != 1 || len(op.Security[0]) != 0 {
				t.Errorf("%s %s is public but publishes security %v", r.Method, r.Pattern, op.Security)
			}
			continue
		}
		if len(op.Security) != 1 {
			t.Errorf("%s %s is authenticated but publishes security %v", r.Method, r.Pattern, op.Security)
			continue
		}
		if _, ok := op.Security[0][bearerSchemeName]; !ok {
			t.Errorf("%s %s does not reference the %s security scheme", r.Method, r.Pattern, bearerSchemeName)
		}
	}
}

// TestAuthenticatedOperationsDocumentTheTransportGate. §28.1's refusal reaches
// every authenticated operation, and a client that does not expect a 403 on a
// read will report it as a bug in Jul.
func TestAuthenticatedOperationsDocumentTheTransportGate(t *testing.T) {
	ops := operationsOf(build(t))
	for _, r := range admin.ExternalRoutes() {
		op := ops[r.Method+" "+r.Pattern]
		if op == nil || r.Public {
			continue
		}
		resp, ok := op.Responses[strconv.Itoa(http.StatusForbidden)]
		if !ok {
			t.Errorf("%s %s is authenticated but documents no 403; §28.1's refusal reaches every authenticated route", r.Method, r.Pattern)
			continue
		}
		if !strings.Contains(resp.Description, string(adminapi.CodeInsecureTransport)) &&
			!strings.Contains(resp.Description, string(adminapi.CodeForbidden)) {
			t.Errorf("%s %s: the 403 response describes neither forbidden nor insecure_transport: %q", r.Method, r.Pattern, resp.Description)
		}
	}
}

// TestErrorCatalogueIsFullyPublished. §26 rule 4 makes a new code an additive
// API change that must reach OpenAPI; this is where forgetting fails.
func TestErrorCatalogueIsFullyPublished(t *testing.T) {
	doc := build(t)
	enum, ok := doc.Components.Schemas["ErrorCode"]
	if !ok {
		t.Fatal("the document publishes no ErrorCode component")
	}
	published := make(map[string]bool, len(enum.Enum))
	for _, c := range enum.Enum {
		published[c] = true
	}
	for _, c := range adminapi.Codes() {
		if !published[string(c)] {
			t.Errorf("code %q is in the catalogue but absent from the published enum", c)
		}
		if _, ok := doc.Components.Responses[errorResponseName(c)]; !ok {
			t.Errorf("code %q has no reusable response component", c)
		}
	}
	if len(enum.Enum) != len(adminapi.Codes()) {
		t.Errorf("the published enum has %d codes, the catalogue has %d", len(enum.Enum), len(adminapi.Codes()))
	}
}

// TestEveryErrorResponseUsesTheEnvelope. One shape for every failure is the
// whole point of §26; a heterogeneous error response in the published document
// would document the problem this issue exists to fix.
func TestEveryErrorResponseUsesTheEnvelope(t *testing.T) {
	const envelopeRef = "#/components/schemas/ErrorEnvelope"
	for key, op := range operationsOf(build(t)) {
		for status, resp := range op.Responses {
			code, err := strconv.Atoi(status)
			if err != nil {
				t.Errorf("%s: response key %q is not a status", key, status)
				continue
			}
			if code < 400 {
				continue
			}
			media, ok := resp.Content["application/json"]
			if !ok {
				t.Errorf("%s %s: error response is not JSON", key, status)
				continue
			}
			if media.Schema == nil || media.Schema.Ref != envelopeRef {
				t.Errorf("%s %s: error response does not reference the shared envelope", key, status)
			}
		}
	}
}

// TestSchemasComeFromTheGoDTOs proves the reflection actually ran, by checking
// a property no hand-written schema would reproduce by accident: the envelope's
// required fields are exactly the Go fields that are neither omitempty,
// omitzero nor pointers.
func TestSchemasComeFromTheGoDTOs(t *testing.T) {
	doc := build(t)
	body, ok := doc.Components.Schemas["ErrorBody"]
	if !ok {
		t.Fatal("ErrorBody is not published")
	}
	wantRequired := map[string]bool{"code": true, "message": true, "request_id": true}
	if len(body.Required) != len(wantRequired) {
		t.Fatalf("ErrorBody required = %v, want %v", body.Required, wantRequired)
	}
	for _, f := range body.Required {
		if !wantRequired[f] {
			t.Errorf("ErrorBody publishes %q as required", f)
		}
	}
	if body.Properties["details"] == nil {
		t.Error("ErrorBody publishes no details property")
	}
	if body.AdditionalProperties == nil || *body.AdditionalProperties {
		t.Error("ErrorBody must set additionalProperties: false — unknown request fields are rejected (ADR 0019 §24a)")
	}
}

// TestNoCredentialLikeExample. §29 forbids a security scheme example that
// resembles a credential, and forbids a local path or a real host anywhere.
func TestNoCredentialLikeExample(t *testing.T) {
	b, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	text := string(b)
	for _, bad := range []string{`"example"`, `"examples"`, `"servers"`, "127.0.0.1", "localhost", "/Users/", "/home/"} {
		if strings.Contains(text, bad) {
			t.Errorf("the generated document contains %q", bad)
		}
	}
}

// TestGenerationIsDeterministic. The artifact is committed and CI compares it
// byte for byte, so a non-deterministic generator would fail a lane at random
// rather than when something changed.
func TestGenerationIsDeterministic(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	for range 8 {
		next, err := Generate()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if string(next) != string(first) {
			t.Fatal("two generations produced different bytes; the document must be deterministic")
		}
	}
}

// TestCommittedArtifactIsCurrent is the in-test half of `make generated-check`.
// It fails in `go test` as well as in the generated-check lane, so the mistake
// is caught by whichever the developer runs first.
//
// The comparison is byte-exact, including line endings, and stays that way
// deliberately: normalising here would let a CRLF-committed artifact pass on
// Windows and then fail the Linux gate, which is a worse failure than this one.
// `.gitattributes` pins docs/generated/** to LF so every checkout agrees.
func TestCommittedArtifactIsCurrent(t *testing.T) {
	want, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	path := filepath.Join("..", "..", "docs", filepath.FromSlash(ArtifactPath))
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read committed artifact: %v", err)
	}
	if string(got) == string(want) {
		return
	}
	if strings.ReplaceAll(string(got), "\r\n", "\n") == string(want) {
		t.Fatalf("docs/%s differs from the generator only in line endings.\n"+
			"The generator emits LF; this checkout has CRLF. Confirm .gitattributes pins docs/generated/** to LF "+
			"and re-check out the file.", ArtifactPath)
	}
	t.Fatalf("docs/%s is stale. Regenerate it with:\n\n    %s\n", ArtifactPath, RegenerateCommand)
}

// TestDocumentIsValidJSONAndDeclaresOpenAPI31 is the cheap structural
// validation the repository can run without a network fetch.
func TestDocumentIsValidJSONAndDeclaresOpenAPI31(t *testing.T) {
	b, err := Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(b, &parsed); err != nil {
		t.Fatalf("the generated document is not valid JSON: %v", err)
	}
	if parsed["openapi"] != "3.1.0" {
		t.Fatalf("openapi = %v, want 3.1.0", parsed["openapi"])
	}
	if _, ok := parsed["paths"]; !ok {
		t.Fatal("the document declares no paths")
	}
}

// TestEveryRefResolves. A dangling $ref makes a generated client fail to build
// with an error that points at the client generator rather than at the source.
func TestEveryRefResolves(t *testing.T) {
	doc := build(t)
	b, err := Render(doc)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if ref, ok := x["$ref"].(string); ok {
				name, found := strings.CutPrefix(ref, "#/components/schemas/")
				if !found {
					t.Errorf("unsupported $ref %q", ref)
				} else if _, ok := doc.Components.Schemas[name]; !ok {
					t.Errorf("$ref %q does not resolve", ref)
				}
			}
			for _, e := range x {
				walk(e)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(raw)
}
