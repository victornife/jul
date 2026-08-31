// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// secretBearingConfig plants a distinct, recognizable literal in every place a
// configuration can hold one. Each value appears exactly once, so a leak names
// the field that leaked it.
const secretBearingConfig = `
[global]
log_level = "debug"
log_format = "json"
access_log = "/var/log/jul/access.log"

[admin]
enabled = true
listen = "127.0.0.1:9099"
token = "PLANTED-ADMIN-TOKEN-aaaaaaaaaaaa"

  [admin.rbac]
  enabled = true
  [[admin.rbac.principals]]
  name = "ops"
  role = "admin"
  token = "PLANTED-PRINCIPAL-TOKEN-bbbbbbbb"

[[upstreams]]
name = "api"
  [[upstreams.servers]]
  address = "127.0.0.1:9001"

[[servers]]
listen = "127.0.0.1:8443"
server_names = ["app.example.com"]

  [servers.tls]
  enabled = true
  cert = "/etc/jul/tls/PLANTED-CERT-PATH.pem"
  key = "/etc/jul/tls/PLANTED-KEY-PATH.pem"

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "api"
    [servers.locations.auth]
    [servers.locations.auth.basic]
    file = "/etc/jul/PLANTED-HTPASSWD-PATH"
`

// plantedSecrets are the values that must never reach a v1 response body.
var plantedSecrets = []string{
	"PLANTED-ADMIN-TOKEN-aaaaaaaaaaaa",
	"PLANTED-PRINCIPAL-TOKEN-bbbbbbbb",
	"PLANTED-CERT-PATH",
	"PLANTED-KEY-PATH",
	"PLANTED-HTPASSWD-PATH",
}

// historyDiffServer wires a real history store so the diff reads a snapshot the
// way the handler does.
func historyDiffServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := exportServer(t)
	dir := t.TempDir()
	s.hist = newHistory(dir, 50)
	return s, dir
}

func writeHistorySnapshot(t *testing.T, dir, body string) string {
	t.Helper()
	id, err := newHistory(dir, 50).snapshot([]byte(body))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return id
}

func exportServer(t *testing.T) *Server {
	t.Helper()
	cfg, err := config.Parse([]byte(secretBearingConfig))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
}

// TestV1ExportCarriesNoPlantedSecret. The export is an allow-list, so a leak
// here means a field was published that should not have been.
func TestV1ExportCarriesNoPlantedSecret(t *testing.T) {
	s := exportServer(t)
	body := getV1(t, s, "/api/v1/config/export", "").Body.String()
	for _, secret := range plantedSecrets {
		if strings.Contains(body, secret) {
			t.Errorf("the export leaked %s", secret)
		}
	}
}

// TestV1ExportIsAnAllowListNotADenyList is the property that makes the export
// safe as the schema grows: every exported field is either a DTO already
// published under its own operation or a reviewed scalar in ExportGlobal.
//
// A field added to config.GlobalConfig tomorrow is absent from the export until
// someone publishes it. The inverse design — marshal everything, strip what
// looks sensitive — fails open and fails silently.
func TestV1ExportIsAnAllowListNotADenyList(t *testing.T) {
	exported := map[string]bool{}
	for _, f := range reflect.VisibleFields(reflect.TypeFor[adminapi.ConfigExportResponse]()) {
		exported[f.Name] = true
	}
	// Every collection in the export is a type the contract already publishes
	// on its own operation, so the export introduces no new resource shape.
	for _, name := range []string{"Listeners", "Routes", "Upstreams", "Streams"} {
		if !exported[name] {
			t.Fatalf("the export no longer composes %s", name)
		}
	}

	// ExportGlobal names its fields one by one. If it ever embeds or mirrors
	// config.GlobalConfig, this count check is the thing that notices.
	global := reflect.TypeFor[adminapi.ExportGlobal]()
	for i := range global.NumField() {
		if global.Field(i).Anonymous {
			t.Fatalf("ExportGlobal embeds %s; embedding a configuration type turns the allow-list "+
				"into a deny-list", global.Field(i).Name)
		}
	}
}

// TestV1ExportIsReadAtOneRevision. Reading the four collections separately can
// straddle a reload and produce a document that never existed; the export is
// captured from a single read, so base_version describes all of it.
func TestV1ExportIsReadAtOneRevision(t *testing.T) {
	s := exportServer(t)
	got := decodeInto[adminapi.ConfigExportResponse](t, getV1(t, s, "/api/v1/config/export", ""))

	if !got.Redacted {
		t.Error("the export does not declare itself redacted")
	}
	if got.BaseVersion == "" {
		t.Fatal("the export carries no base_version")
	}

	// The same version the collections report, because it is the same read.
	routes := decodeInto[adminapi.RoutesResponse](t, getV1(t, s, "/api/v1/routes", ""))
	if routes.BaseVersion != got.BaseVersion {
		t.Errorf("export base_version %q disagrees with the routes collection %q", got.BaseVersion, routes.BaseVersion)
	}
	if len(got.Routes) != len(routes.Routes) {
		t.Errorf("export has %d routes, the collection %d", len(got.Routes), len(routes.Routes))
	}
	if len(got.Listeners) != 1 || len(got.Upstreams) != 1 {
		t.Errorf("listeners=%d upstreams=%d", len(got.Listeners), len(got.Upstreams))
	}
	if got.Global.LogLevel != "debug" || got.Global.LogFormat != "json" {
		t.Errorf("global = %+v", got.Global)
	}
	if !got.Global.AccessLogConfigured {
		t.Error("access_log_configured is false with an access log written")
	}
}

// TestNoV1ResponseBodyCarriesConfigurationBytes is the behavioural half of
// ADR 0019 §24's required property. TestNoV1RouteReturnsRawConfigurationBytes
// checks the catalogue — that no withdrawn path exists and no v1 route asks for
// config:raw or history:raw. That is structural, and it cannot catch a raw body
// returned from a route whose path and permission both look legitimate. This
// exercises every v1 GET and reads what actually comes back.
func TestNoV1ResponseBodyCarriesConfigurationBytes(t *testing.T) {
	// A marker that can only appear if a response embeds configuration text.
	const marker = "PLANTED-ADMIN-TOKEN-aaaaaaaaaaaa"
	s := exportServer(t)

	for _, route := range ExternalRoutes() {
		if !strings.HasPrefix(route.Pattern, apiVersionNamespace) || route.Method != http.MethodGet {
			continue
		}
		// Concrete ids the fixture does not have still exercise the handler's
		// own refusal path, which must also carry no bytes.
		path := strings.NewReplacer(
			"{apply_id}", "rl_000000000000_1",
			"{route_id}", "r-unassigned",
			"{name}", "api",
			"{addr}", "127.0.0.1:8443",
			"{id}", "20260101T000000.000Z",
		).Replace(route.Pattern)

		body := getV1(t, s, path, "").Body.String()
		if strings.Contains(body, marker) {
			t.Errorf("%s returned configuration text containing a secret", path)
		}
		// TOML section headers are the tell that a body carries file content.
		for _, tomlish := range []string{"[[servers]]", "[[upstreams]]", "[admin]", "[global]"} {
			if strings.Contains(body, tomlish) {
				t.Errorf("%s returned raw configuration bytes (found %q)", path, tomlish)
			}
		}
	}
}

// TestV1HistoryDiffReportsCredentialRotationWithoutTheCredential. The diff
// carries free-form before/after values, so a comparator that put a credential
// in either would put it on the wire. The comparators report the change without
// the values; this asserts it rather than trusting it.
func TestV1HistoryDiffReportsCredentialRotationWithoutTheCredential(t *testing.T) {
	s, dir := historyDiffServer(t)
	id := writeHistorySnapshot(t, dir, strings.ReplaceAll(secretBearingConfig,
		"PLANTED-PRINCIPAL-TOKEN-bbbbbbbb", "PLANTED-ROTATED-TOKEN-cccccccccc"))

	rr := getV1(t, s, "/api/v1/config/history/"+id+"/diff", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("diff = %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, secret := range []string{"PLANTED-PRINCIPAL-TOKEN-bbbbbbbb", "PLANTED-ROTATED-TOKEN-cccccccccc"} {
		if strings.Contains(body, secret) {
			t.Errorf("the diff leaked the credential %s", secret)
		}
	}

	got := decodeInto[adminapi.HistoryDiffResponse](t, rr)
	if got.BaseVersion == "" || got.HistoryID != id {
		t.Fatalf("base_version=%q history_id=%q", got.BaseVersion, got.HistoryID)
	}
	// The rotation is still reported: withholding the value must not withhold
	// the fact, or an operator reviewing a rollback would not see it coming.
	var found bool
	for _, e := range got.Modifications {
		if e.Kind == "rbac_principal" && strings.Contains(e.Detail, "credential") {
			found = true
			if e.Before != "" || e.After != "" {
				t.Errorf("the rotation entry carries values: before=%q after=%q", e.Before, e.After)
			}
		}
	}
	if !found {
		t.Errorf("the credential rotation is not reported at all: %+v", got.Modifications)
	}
}

// TestV1HistoryDiffUnknownRevision.
func TestV1HistoryDiffUnknownRevision(t *testing.T) {
	s, _ := historyDiffServer(t)
	rr := getV1(t, s, "/api/v1/config/history/20260101T000000.000Z/diff", "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("unknown revision = %d, want 404", rr.Code)
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeNotFound || env.Error.Details.Kind != "history_revision" {
		t.Fatalf("envelope = %+v", env.Error)
	}
}

// TestV1HistoryDiffBaseVersionMatchesTheRollbackCheck. The preview's
// base_version is what a client passes back to bind the rollback to the
// configuration it reviewed, so it must be the same value the rollback
// validates against, not merely a plausible one.
func TestV1HistoryDiffBaseVersionMatchesTheRollbackCheck(t *testing.T) {
	s, dir := historyDiffServer(t)
	id := writeHistorySnapshot(t, dir, secretBearingConfig)

	got := decodeInto[adminapi.HistoryDiffResponse](t, getV1(t, s, "/api/v1/config/history/"+id+"/diff", ""))
	state, err := s.currentWriteState(false)
	if err != nil {
		t.Fatalf("currentWriteState: %v", err)
	}
	if got.BaseVersion != state.Version {
		t.Fatalf("diff base_version %q is not the persisted version %q", got.BaseVersion, state.Version)
	}
}

func TestV1ExportReportsStorageFailure(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := getV1(t, s, "/api/v1/config/export", "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("export = %d, want 503", rr.Code)
	}
	if env := decodeEnvelope(t, rr); env.Error.Code != adminapi.CodeStorageUnavailable {
		t.Fatalf("code = %q", env.Error.Code)
	}
}

// TestV1ExportSecretRefCountIsPublishedWithoutTheReferences. A change in the
// count is visible without any reference or resolved value being readable.
func TestV1ExportSecretRefCountIsPublishedWithoutTheReferences(t *testing.T) {
	s := exportServer(t)
	rr := getV1(t, s, "/api/v1/config/export", "")
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["secret_ref_count"]; !ok {
		t.Fatal("secret_ref_count is not published")
	}
	if strings.Contains(rr.Body.String(), "${secret:") {
		t.Error("the export publishes a secret reference expression")
	}
}
