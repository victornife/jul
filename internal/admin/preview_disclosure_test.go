// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

const disclosureBase = `[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
root = "."
[servers.locations.match]
type = "prefix"
path = "/"
`

const previewWriterToken = "preview-writer-credential-32-characters"

func disclosureServer(t *testing.T, raw []byte, permissions []string) *Server {
	t.Helper()
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	p, err := rbac.Build(true, "admin", map[string][]string{"custom": permissions}, []rbac.PrincipalDef{
		{Name: "admin", Role: rbac.RoleAdmin, Token: rbAdminTok},
		{Name: "preview-writer", Role: "custom", Token: previewWriterToken},
	}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, config.AdminConfig{Listen: "127.0.0.1:9090"}, Deps{
		LoadConfig:    func() (*config.Config, error) { return cfg, nil },
		ReadConfigRaw: func() ([]byte, error) { return raw, nil },
	})
	s.UpdatePolicy(p)
	return s
}

func disclosureRequest(h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.Contains(path, "/plan") || strings.Contains(path, "/diff") || path == "/api/config/preview" {
		req.Header.Set("Content-Type", "application/toml")
	}
	req.Header.Set(rawPreviewBaseVersionHeader, "stale-test-version")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// Decode the entire DTO. The only normalization is the server-generated v1
// request ID, sampled independently of config/input and echoed in its header.
// Versions, lifecycle classes, status flags, errors and row counts are retained.
func disclosureResponse(t *testing.T, rr *httptest.ResponseRecorder) any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if e, ok := out["error"].(map[string]any); ok {
		if id, exists := e["request_id"]; exists {
			if id != rr.Header().Get("X-Request-ID") {
				t.Fatal("request ID is not independently generated correlation metadata")
			}
			delete(e, "request_id")
		}
	}
	return out
}

func TestPreviewDisclosureOriginalOracle(t *testing.T) {
	// Real HTTP authentication uses a separate named writer, never the admin
	// token being guessed. The same baseline is used for both guesses.
	for _, field := range []string{"admin", "plugin"} {
		t.Run(field, func(t *testing.T) {
			raw := disclosureBase + "\n[admin]\ntoken = \"synthetic-admin-secret-32-characters\"\n[plugins.auth]\npath = \"test.wasm\"\n[plugins.auth.config]\ncredential = \"synthetic-plugin-secret\"\n"
			h := disclosureServer(t, []byte(raw), []string{"config:write"}).routes()
			wrong := strings.Replace(raw, "synthetic-"+field+"-secret", "incorrect-"+field+"-guess", 1)
			equal := disclosureRequest(h, "POST", "/api/config/diff", raw, previewWriterToken)
			unequal := disclosureRequest(h, "POST", "/api/config/diff", wrong, previewWriterToken)
			if equal.Code != http.StatusForbidden || unequal.Code != http.StatusForbidden || !reflect.DeepEqual(disclosureResponse(t, equal), disclosureResponse(t, unequal)) {
				t.Fatalf("hidden equality is observable: equal=%d %s unequal=%d %s", equal.Code, equal.Body.String(), unequal.Code, unequal.Body.String())
			}
		})
	}
}

func TestPreviewDisclosureSurfaces(t *testing.T) {
	// Independent route inventory: do not derive expected protection from the
	// catalog flag whose accidental omission this test must detect.
	paths := []struct{ method, path string }{
		{"POST", "/api/config/diff"}, {"POST", "/api/config/preview"},
		{"POST", "/api/config/patch"}, {"POST", "/api/config/patch/preview"},
		{"POST", "/api/config/patch/candidate"},
		{"POST", "/api/v1/config/plan"}, {"POST", "/api/v1/config/patch"},
		{"GET", "/api/config/history/synthetic/diff"}, {"GET", "/api/v1/config/history/synthetic/diff"},
		{"GET", "/api/config/adopt-external/preview"}, {"POST", "/api/config/adopt-external/preview"},
		{"POST", "/api/v1/config/adopt-external/preview"},
	}
	for _, path := range paths {
		t.Run(path.method+path.path, func(t *testing.T) {
			var previous any
			for _, hidden := range []string{"first-hidden-secret", "second-hidden-secret-with-new-length"} {
				raw := disclosureBase + "\n[admin]\ntoken = \"" + hidden + "\"\n"
				s := disclosureServer(t, []byte(raw), []string{"config:write", "history:rollback", "config:adopt"})
				// A denied comparison must not even consult configuration, history,
				// resolution, adoption, persistence or apply. This also covers absent
				// callbacks and invalid/stale candidates without secret-based errors.
				s.deps.LoadConfig = func() (*config.Config, error) { t.Fatal("denied preview read config"); return nil, nil }
				s.deps.ReadConfigRaw = func() ([]byte, error) { t.Fatal("denied preview read source"); return nil, nil }
				h := s.routes()
				for _, body := range []string{raw, "invalid TOML [", `{}`, `{"base_version":"stale","ops":[{"op":"plugin_set","plugin":{"config":{"guess":"value"}}}]}`} {
					rr := disclosureRequest(h, path.method, path.path, body, previewWriterToken)
					if rr.Code != http.StatusForbidden {
						t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
					}
					out := disclosureResponse(t, rr)
					if previous != nil && !reflect.DeepEqual(previous, out) {
						t.Fatalf("baseline/candidate dependent response: %v / %v", previous, out)
					}
					previous = out
				}
			}
		})
	}
}

func TestPreviewDisclosurePrivilegedControl(t *testing.T) {
	raw := disclosureBase + "\n[admin]\ntoken = \"synthetic-admin-secret-32-characters\"\n[plugins.auth]\npath = \"test.wasm\"\n[plugins.auth.config]\ncredential = \"synthetic-plugin-secret\"\n"
	h := disclosureServer(t, []byte(raw), []string{"config:write", "config:raw"}).routes()
	for _, field := range []string{"admin", "plugin"} {
		wrong := strings.Replace(raw, "synthetic-"+field+"-secret", "incorrect-"+field+"-guess", 1)
		equal := disclosureRequest(h, "POST", "/api/config/diff", raw, previewWriterToken)
		unequal := disclosureRequest(h, "POST", "/api/config/diff", wrong, previewWriterToken)
		if equal.Code != 200 || unequal.Code != 200 || reflect.DeepEqual(disclosureResponse(t, equal), disclosureResponse(t, unequal)) {
			t.Fatalf("authorised comparison detail lost: %s / %s", equal.Body.String(), unequal.Body.String())
		}
		if strings.Contains(unequal.Body.String(), "synthetic-admin-secret") || strings.Contains(unequal.Body.String(), "synthetic-plugin-secret") {
			t.Fatal("comparison added plaintext secrets")
		}
	}
}

func TestPreviewDisclosurePermissionMatrix(t *testing.T) {
	for _, tc := range []struct {
		name        string
		permissions []string
		token       string
		want        int
	}{
		{"writer", []string{"config:write"}, previewWriterToken, 403},
		{"raw_without_write", []string{"config:raw"}, previewWriterToken, 403},
		{"read_only", []string{"config:read"}, previewWriterToken, 403},
		{"writer_and_raw", []string{"config:write", "config:raw"}, previewWriterToken, 200},
		{"resource_wildcard", []string{"config:*"}, previewWriterToken, 200},
		{"global_wildcard", []string{"*"}, previewWriterToken, 200},
		{"unauthenticated", []string{"config:write", "config:raw"}, "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := disclosureServer(t, []byte(disclosureBase), tc.permissions)
			rr := disclosureRequest(s.routes(), "POST", "/api/config/diff", disclosureBase, tc.token)
			if rr.Code != tc.want {
				t.Fatalf("status=%d expected=%d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
	// The existing legacy shared token has wildcard authority. No artificial
	// write permission is added to endpoints that historically require raw only.
	s := newTestServer(t, config.AdminConfig{Token: previewWriterToken}, Deps{LoadConfig: func() (*config.Config, error) { return config.Parse([]byte(disclosureBase)) }})
	if rr := disclosureRequest(s.routes(), "POST", "/api/config/diff", disclosureBase, previewWriterToken); rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	s = disclosureServer(t, []byte(disclosureBase), []string{"config:raw"})
	rr := disclosureRequest(s.routes(), "POST", "/api/config/patch/candidate", `{}`, previewWriterToken)
	if rr.Code != 400 {
		t.Fatalf("raw-only candidate semantics changed: %d %s", rr.Code, rr.Body.String())
	}
}

func TestPreviewDisclosurePinnedIdentity(t *testing.T) {
	for _, initiallyRaw := range []bool{false, true} {
		permissions := []string{"config:write"}
		if initiallyRaw {
			permissions = append(permissions, "config:raw")
		}
		s := disclosureServer(t, []byte(disclosureBase), permissions)
		replacement := disclosureServer(t, []byte(disclosureBase), []string{"config:*"}).currentPolicy()
		if initiallyRaw {
			replacement = disclosureServer(t, []byte(disclosureBase), []string{"config:write"}).currentPolicy()
		}
		next := requireConfigComparisonRead(okHandler())
		h := s.requirePermission(rbac.ConfigWrite, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.UpdatePolicy(replacement)
			next.ServeHTTP(w, r)
		}))
		rr := disclosureRequest(h, "POST", "/", "", previewWriterToken)
		want := 403
		if initiallyRaw {
			want = 200
		}
		if rr.Code != want {
			t.Fatalf("disclosure consulted a newer policy: %d want %d", rr.Code, want)
		}
	}
	rr := disclosureRequest(requireConfigComparisonRead(okHandler()), "POST", "/", "", "")
	if rr.Code != 403 {
		t.Fatal("missing request identity must fail closed")
	}
}

func TestPreviewDisclosureFieldAndIntentInventory(t *testing.T) {
	fields := map[string]string{
		"admin_token":             "[admin]\ntoken = %q\n",
		"named_principal":         "[admin.rbac]\nenabled = true\n[[admin.rbac.principals]]\nname = \"victim\"\nrole = \"admin\"\ntoken = %q\n",
		"opaque_plugin":           "[plugins.auth]\npath = \"auth.wasm\"\n[plugins.auth.config]\nprivate_key_name = %q\n",
		"health_headers":          "[[upstreams]]\nname = \"pool\"\n[upstreams.health_check.headers]\nAuthorization = %q\n",
		"request_headers":         "[servers.locations.headers]\nAuthorization = %q\n",
		"response_headers":        "[[servers.locations.response_headers]]\nop = \"set\"\nname = \"X-Private\"\nvalue = %q\n",
		"oidc_secret":             "[admin.oidc]\nclient_secret = %q\n",
		"consul_token":            "[[upstreams]]\nname = \"pool\"\n[upstreams.discovery]\ntype = \"consul\"\n[upstreams.discovery.consul]\ntoken = %q\n",
		"kubernetes_token":        "[[upstreams]]\nname = \"pool\"\n[upstreams.discovery]\ntype = \"kubernetes\"\n[upstreams.discovery.kubernetes]\ntoken = %q\n",
		"forward_auth_url":        "[servers.locations.auth.forward_auth]\nurl = %q\n",
		"generic_reference_field": "[global]\naccess_log = %q\n",
	}
	for name, fragment := range fields {
		t.Run(name, func(t *testing.T) {
			// The exact SAME request is sent to baselines differing only in the
			// hidden value. Config.Parse verifies actual supported field shapes.
			candidate := disclosureBase + fmt.Sprintf(fragment, "synthetic-hidden-a")
			var previous any
			for _, baseline := range []string{"synthetic-hidden-a", "synthetic-hidden-b"} {
				raw := disclosureBase + fmt.Sprintf(fragment, baseline)
				s := disclosureServer(t, []byte(raw), []string{"config:write"})
				for _, input := range []string{candidate, disclosureBase, disclosureBase + fmt.Sprintf(fragment, ""), disclosureBase + fmt.Sprintf(fragment, "${env:NEVER_RESOLVE_645}"), disclosureBase + fmt.Sprintf(fragment, "prefix-${env:FIRST_645}-${file:/never-read-645}-suffix")} {
					rr := disclosureRequest(s.routes(), "POST", "/api/v1/config/plan", input, previewWriterToken)
					if rr.Code != 403 {
						t.Fatalf("field comparison escaped gate: %d %s", rr.Code, rr.Body.String())
					}
					out := disclosureResponse(t, rr)
					if previous != nil && !reflect.DeepEqual(previous, out) {
						t.Fatalf("field/intent affected disclosure: %v / %v", previous, out)
					}
					previous = out
				}
			}
		})
	}
}
