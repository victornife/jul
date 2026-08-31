// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"jul/internal/admin"
	"jul/internal/adminapi"
)

// probeRoute is a well-formed external route the branch tests can vary one
// field at a time from.
func probeRoute(method string) admin.ExternalRoute {
	return admin.ExternalRoute{
		Pattern:   "/healthz",
		Method:    method,
		Stability: admin.StabilityPublic,
		Public:    true,
		Operation: admin.ExternalOperation{
			ID:       "probeOperation",
			Summary:  "A probe.",
			Response: "HealthResponse",
		},
	}
}

// TestAddPathsBindsEveryModelledMethod. A method silently dropped here would
// leave an external route enforced by the server and absent from the contract,
// which the catalog/document guard test would then report as a stale artifact
// rather than as the missing branch it is.
func TestAddPathsBindsEveryModelledMethod(t *testing.T) {
	methods := map[string]func(*PathItem) *Operation{
		http.MethodGet:    func(p *PathItem) *Operation { return p.Get },
		http.MethodPost:   func(p *PathItem) *Operation { return p.Post },
		http.MethodPut:    func(p *PathItem) *Operation { return p.Put },
		http.MethodPatch:  func(p *PathItem) *Operation { return p.Patch },
		http.MethodDelete: func(p *PathItem) *Operation { return p.Delete },
		http.MethodHead:   func(p *PathItem) *Operation { return p.Head },
	}
	for method, get := range methods {
		doc := build(t)
		if err := addPaths(doc, []admin.ExternalRoute{probeRoute(method)}); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		op := get(doc.Paths["/healthz"])
		if op == nil {
			t.Fatalf("%s was not bound onto the path item", method)
		}
		if op.OperationID != "probeOperation" {
			t.Fatalf("%s bound the wrong operation: %q", method, op.OperationID)
		}
	}
}

// TestAddPathsRefusesAnUnmodelledMethod: the document models the methods the
// contract uses and nothing else, so an exotic verb stops the generator instead
// of vanishing from the published document.
func TestAddPathsRefusesAnUnmodelledMethod(t *testing.T) {
	doc := build(t)
	err := addPaths(doc, []admin.ExternalRoute{probeRoute(http.MethodOptions)})
	if err == nil {
		t.Fatal("OPTIONS was accepted")
	}
	if !strings.Contains(err.Error(), http.MethodOptions) {
		t.Fatalf("the error does not name the method: %v", err)
	}
}

// TestAddPathsPropagatesAnOperationError.
func TestAddPathsPropagatesAnOperationError(t *testing.T) {
	r := probeRoute(http.MethodGet)
	r.Operation.Response = ""
	if err := addPaths(build(t), []admin.ExternalRoute{r}); err == nil {
		t.Fatal("a route naming no response schema was accepted")
	}
}

// TestAddPathsSharesOnePathItemAcrossMethods: two methods on one pattern must
// land on the same path item, or the second overwrites the first.
func TestAddPathsSharesOnePathItemAcrossMethods(t *testing.T) {
	doc := build(t)
	get := probeRoute(http.MethodGet)
	patch := probeRoute(http.MethodPatch)
	patch.Operation.ID = "probeOperationPatch"
	if err := addPaths(doc, []admin.ExternalRoute{get, patch}); err != nil {
		t.Fatalf("addPaths: %v", err)
	}
	item := doc.Paths["/healthz"]
	if item.Get == nil || item.Patch == nil {
		t.Fatalf("both methods must share one path item: %+v", item)
	}
}

// TestBuildOperationRejectsAnUncataloguedErrorCode. An operation listing a code
// with no catalogue entry would reference a response component that does not
// exist, producing a dangling $ref in a published document.
func TestBuildOperationRejectsAnUncataloguedErrorCode(t *testing.T) {
	r := probeRoute(http.MethodGet)
	r.Operation.Errors = []string{"not_a_real_code"}
	_, err := buildOperation(r, build(t))
	if err == nil {
		t.Fatal("an uncatalogued error code was accepted")
	}
	if !strings.Contains(err.Error(), "not_a_real_code") {
		t.Fatalf("the error does not name the code: %v", err)
	}
}

// TestBuildOperationAddsDeclaredErrorsWithoutDuplicating.
func TestBuildOperationAddsDeclaredErrorsWithoutDuplicating(t *testing.T) {
	r := probeRoute(http.MethodGet)
	r.Public = false
	r.Stability = admin.StabilityExternal
	r.Permissions = []string{"status:read"}
	// stale_base_version is new; rate_limited is already universal, so listing
	// it must not produce a second entry.
	r.Operation.Errors = []string{string(adminapi.CodeStaleBaseVersion), string(adminapi.CodeRateLimited)}

	op, err := buildOperation(r, build(t))
	if err != nil {
		t.Fatalf("buildOperation: %v", err)
	}
	if _, ok := op.Responses[strconv.Itoa(http.StatusConflict)]; !ok {
		t.Fatal("the declared 409 is missing")
	}
	if got := op.Responses[strconv.Itoa(http.StatusTooManyRequests)]; got == nil {
		t.Fatal("the universal 429 is missing")
	}
	if !slices.Equal(op.Permissions, []string{"status:read"}) {
		t.Fatalf("permissions = %v", op.Permissions)
	}
	if op.Security[0][bearerSchemeName] == nil {
		t.Fatal("an authenticated operation must reference the bearer scheme")
	}
	if op.Responses["200"].Headers["X-Request-ID"].Schema == nil {
		t.Fatal("an authenticated success response must document X-Request-ID")
	}
}

// TestDeprecatedOperationDescribesItsSunset. §25 keeps a deprecated endpoint
// working for at least one minor release under a Sunset header; a client can
// only act on that if the date is in the document.
func TestDeprecatedOperationDescribesItsSunset(t *testing.T) {
	r := probeRoute(http.MethodGet)
	r.Stability = admin.StabilityDeprecated
	r.Sunset = "2027-01-01"

	op, err := buildOperation(r, build(t))
	if err != nil {
		t.Fatalf("buildOperation: %v", err)
	}
	if !op.Deprecated {
		t.Fatal("a deprecated route did not set deprecated: true")
	}
	if op.Sunset != "2027-01-01" || !strings.Contains(op.Description, "2027-01-01") {
		t.Fatalf("the sunset date is not published: sunset=%q description=%q", op.Sunset, op.Description)
	}
}

// TestPathParametersAreDerivedFromThePattern, so a renamed segment cannot leave
// a stale parameter behind.
func TestPathParametersAreDerivedFromThePattern(t *testing.T) {
	cases := []struct {
		pattern string
		want    []string
	}{
		{"/healthz", nil},
		{"/api/v1/routes/{route_id}", []string{"route_id"}},
		{"/api/v1/listeners/{addr}/client_address", []string{"addr"}},
		{"/a/{one}/b/{two}/c/{three}", []string{"one", "two", "three"}},
		// A malformed pattern yields what it can rather than panicking.
		{"/a/{unclosed", nil},
		{"/a/}stray{", nil},
	}
	for _, tc := range cases {
		got := pathParameters(tc.pattern)
		if !slices.Equal(got, tc.want) {
			t.Errorf("pathParameters(%q) = %v, want %v", tc.pattern, got, tc.want)
		}
	}
}

func TestBuildOperationDeclaresPathParameters(t *testing.T) {
	r := probeRoute(http.MethodGet)
	r.Pattern = "/api/v1/routes/{route_id}"
	op, err := buildOperation(r, build(t))
	if err != nil {
		t.Fatalf("buildOperation: %v", err)
	}
	if len(op.Parameters) != 1 {
		t.Fatalf("parameters = %+v", op.Parameters)
	}
	p := op.Parameters[0]
	if p.Name != "route_id" || p.In != "path" || !p.Required || p.Schema.Type != "string" {
		t.Fatalf("parameter = %+v", p)
	}
}

// TestAddSuccessResponseResolvesSchemasAndMediaTypes covers the three ways a
// success response can be described, and the two ways it can be wrong.
func TestAddSuccessResponseResolvesSchemasAndMediaTypes(t *testing.T) {
	doc := build(t)

	t.Run("a registered JSON DTO", func(t *testing.T) {
		op := &Operation{Responses: map[string]*Response{}}
		if err := addSuccessResponse(op, probeRoute(http.MethodGet), doc); err != nil {
			t.Fatalf("addSuccessResponse: %v", err)
		}
		media, ok := op.Responses["200"].Content["application/json"]
		if !ok || media.Schema.Ref != "#/components/schemas/HealthResponse" {
			t.Fatalf("response = %+v", op.Responses["200"])
		}
	})

	t.Run("a non-JSON declared schema", func(t *testing.T) {
		r := probeRoute(http.MethodGet)
		r.Operation.Response = "PrometheusExposition"
		op := &Operation{Responses: map[string]*Response{}}
		if err := addSuccessResponse(op, r, doc); err != nil {
			t.Fatalf("addSuccessResponse: %v", err)
		}
		want := adminapi.NonJSONSchemas["PrometheusExposition"].MediaType
		if _, ok := op.Responses["200"].Content[want]; !ok {
			t.Fatalf("the exposition response is not served as %q: %+v", want, op.Responses["200"].Content)
		}
	})

	t.Run("a non-default success status", func(t *testing.T) {
		r := probeRoute(http.MethodGet)
		r.Operation.SuccessStatus = http.StatusAccepted
		op := &Operation{Responses: map[string]*Response{}}
		if err := addSuccessResponse(op, r, doc); err != nil {
			t.Fatalf("addSuccessResponse: %v", err)
		}
		if _, ok := op.Responses["202"]; !ok {
			t.Fatalf("responses = %v", op.Responses)
		}
	})

	t.Run("no response schema named", func(t *testing.T) {
		r := probeRoute(http.MethodGet)
		r.Operation.Response = ""
		if err := addSuccessResponse(&Operation{Responses: map[string]*Response{}}, r, doc); err == nil {
			t.Fatal("an operation naming no response schema was accepted")
		}
	})

	t.Run("an unregistered response schema", func(t *testing.T) {
		r := probeRoute(http.MethodGet)
		r.Operation.Response = "NotRegisteredAnywhere"
		err := addSuccessResponse(&Operation{Responses: map[string]*Response{}}, r, doc)
		if err == nil {
			t.Fatal("an unregistered schema name was accepted; a typo must fail the build, not publish an operation with no schema")
		}
		if !strings.Contains(err.Error(), "internal/adminapi") {
			t.Fatalf("the error does not say where to register it: %v", err)
		}
	})
}

func TestTagsForGroupsEveryPublishedPrefix(t *testing.T) {
	cases := map[string]string{
		"/healthz":                  "health",
		"/readyz":                   "health",
		"/metrics":                  "observability",
		"/api/v1/config":            "configuration",
		"/api/v1/config/history":    "configuration",
		"/api/v1/routes":            "routes",
		"/api/v1/routes/{route_id}": "routes",
		"/api/v1/upstreams":         "upstreams",
		"/api/v1/listeners/{addr}/client_address": "listeners",
		"/api/v1/streams":                         "streams",
		"/api/v1/status":                          "status",
		"/api/v1/capabilities":                    "status",
	}
	for pattern, want := range cases {
		got := tagsFor(pattern)
		if len(got) != 1 || got[0] != want {
			t.Errorf("tagsFor(%q) = %v, want [%s]", pattern, got, want)
		}
	}
}

func TestIsResourceAddressPath(t *testing.T) {
	cases := map[string]bool{
		"/api/v1/routes/{route_id}":               true,
		"/api/v1/upstreams/{name}":                true,
		"/api/v1/routes":                          false,
		"/api/v1/config/applies/{apply_id}":       false, // an operation identity, not a resource
		"/api/v1/listeners/{addr}/client_address": false, // a sub-resource
		"/api/routes/{route_id}":                  false, // not versioned
		"/healthz":                                false,
		"/api/v1/routes/test":                     false,
	}
	for pattern, want := range cases {
		if got := isResourceAddressPath(pattern); got != want {
			t.Errorf("isResourceAddressPath(%q) = %v, want %v", pattern, got, want)
		}
	}
}

// TestCheckResourcePathsRejectsAnUnclaimedResourceURI. A per-resource URI that
// no resource claims is a second, unchecked identity model — exactly what
// ADR 0019 §21 exists to prevent.
func TestCheckResourcePathsRejectsAnUnclaimedResourceURI(t *testing.T) {
	doc := &Document{Paths: map[string]*PathItem{
		"/api/v1/widgets/{widget_id}": {Get: &Operation{}},
	}}
	err := checkResourcePaths(doc)
	if err == nil {
		t.Fatal("an unclaimed per-resource path was accepted")
	}
	if !strings.Contains(err.Error(), "ResourceCatalog") {
		t.Fatalf("the error does not name the remedy: %v", err)
	}

	// A path the catalog does claim passes.
	ok := &Document{Paths: map[string]*PathItem{
		"/api/v1/routes/{route_id}": {Get: &Operation{}},
		"/api/v1/upstreams/{name}":  {Get: &Operation{}},
		"/healthz":                  {Get: &Operation{}},
	}}
	if err := checkResourcePaths(ok); err != nil {
		t.Fatalf("a catalogued path was rejected: %v", err)
	}
}

func TestErrorResponseNameIsCamelCase(t *testing.T) {
	cases := map[adminapi.Code]string{
		adminapi.CodeStaleBaseVersion:      "StaleBaseVersion",
		adminapi.CodeInsecureTransport:     "InsecureTransport",
		adminapi.CodeConfigAuthorityRO:     "ConfigAuthorityReadOnly",
		adminapi.CodeAdminReachabilityConf: "AdminReachabilityConfirmationRequired",
		// Defensive: a doubled or trailing separator must not produce an empty
		// segment in the component name.
		adminapi.Code("a__b_"): "AB",
	}
	for code, want := range cases {
		if got := errorResponseName(code); got != want {
			t.Errorf("errorResponseName(%q) = %q, want %q", code, got, want)
		}
	}
}

// TestCheckNoLeakedHostOrPath is the last gate before the bytes are committed.
// It runs on the rendered document rather than the model so a leak introduced
// through any field is caught, not only the fields someone remembered.
func TestCheckNoLeakedHostOrPath(t *testing.T) {
	leaks := map[string]string{
		"a loopback URL":            "reach it at http://127.0.0.1:9090",
		"a loopback hostname URL":   "reach it at http://localhost:9090",
		"a TLS loopback URL":        "reach it at https://127.0.0.1:9090",
		"a macOS absolute path":     "see /Users/operator/server.toml",
		"a Linux absolute path":     "see /home/operator/server.toml",
		"a Windows absolute path":   `see C:\jul\server.toml`,
		"a JWT-shaped token":        "send Bearer eyJhbGciOiJIUzI1NiJ9",
		"a credential-like example": "use example-token here",
	}
	for name, leak := range leaks {
		t.Run(name, func(t *testing.T) {
			doc := build(t)
			doc.Info.Description = leak
			err := checkNoLeakedHostOrPath(doc)
			if err == nil {
				t.Fatalf("the document published %q", leak)
			}
			if !strings.Contains(err.Error(), "credential") && !strings.Contains(err.Error(), "path") && !strings.Contains(err.Error(), "host") {
				t.Fatalf("the error does not explain the rule: %v", err)
			}
		})
	}

	t.Run("the real document is clean", func(t *testing.T) {
		if err := checkNoLeakedHostOrPath(build(t)); err != nil {
			t.Fatalf("the published document trips its own leak check: %v", err)
		}
	})
}

// TestDocumentCannotDeclareAServersBlock. A `servers` block is the usual way a
// host reaches an OpenAPI document, and a server URL is either a local address
// or a fabricated host — neither belongs in a published contract. The
// invariant is asserted against the type rather than the output, because a
// field the type does not have cannot be scanned for.
func TestDocumentCannotDeclareAServersBlock(t *testing.T) {
	rt := reflect.TypeFor[Document]()
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "servers" {
			t.Fatalf("Document.%s publishes a servers block", rt.Field(i).Name)
		}
	}
}

// TestRenderIsIndentedAndDoesNotEscapeHTML: the section signs in the
// descriptions must stay readable, and the artifact is reviewed as a diff.
func TestRenderIsIndentedAndDoesNotEscapeHTML(t *testing.T) {
	b, err := Render(build(t))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(string(b), "\n  \"info\": {") {
		t.Fatal("the document is not indented for review as a diff")
	}
	if strings.Contains(string(b), `\u0026`) || strings.Contains(string(b), `\u003c`) {
		t.Fatal("HTML escaping is on; the descriptions would be unreadable")
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatal("the artifact does not end with a newline")
	}
}
