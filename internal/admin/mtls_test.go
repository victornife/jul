package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// mtlsPatchConfig returns a config with one TLS-enabled server and two
// locations, for exercising the Phase 4j mutual-TLS patch ops and projection.
func mtlsPatchConfig() *config.Config {
	return &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		TLS:         &config.TLSConfig{Enabled: true},
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: "/var/www"},
			{Match: config.MatchConfig{Type: "prefix", Path: "/admin"}, ProxyPass: "http://127.0.0.1:9000"},
		},
	}}}
}

func TestApplyPatchServerSetClientAuthEnable(t *testing.T) {
	c := mtlsPatchConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		ClientAuth:  &clientAuthDef{Mode: "require", CAFile: "/etc/ca.pem", CRLFile: "/etc/crl.pem", VerifySAN: []string{"svc.internal"}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	ca := c.Servers[0].TLS.ClientAuth
	if ca == nil || ca.Mode != "require" || ca.CAFile != "/etc/ca.pem" || ca.CRLFile != "/etc/crl.pem" {
		t.Fatalf("unexpected client_auth: %+v", ca)
	}
	if len(ca.VerifySAN) != 1 || ca.VerifySAN[0] != "svc.internal" {
		t.Errorf("verify_san = %+v", ca.VerifySAN)
	}
	if !strings.Contains(summary, "require") {
		t.Errorf("summary = %q, want require", summary)
	}
}

func TestApplyPatchServerSetClientAuthRequiresCA(t *testing.T) {
	c := mtlsPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		ClientAuth:  &clientAuthDef{Mode: "require"},
	}); err == nil {
		t.Error("expected error: ca_file required for mode require")
	}
}

func TestApplyPatchServerSetClientAuthInvalidMode(t *testing.T) {
	c := mtlsPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		ClientAuth:  &clientAuthDef{Mode: "always"},
	}); err == nil {
		t.Error("expected error: invalid mode")
	}
}

func TestApplyPatchServerSetClientAuthDisable(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem"}
	summary, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		ClientAuth:  &clientAuthDef{Mode: "none"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[0].TLS.ClientAuth != nil {
		t.Errorf("client_auth should be cleared, got %+v", c.Servers[0].TLS.ClientAuth)
	}
	if !strings.Contains(summary, "off") {
		t.Errorf("summary = %q, want off", summary)
	}
}

func TestApplyPatchServerSetClientAuthDisableRejectedWhileRequireCert(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem"}
	c.Servers[0].Locations[1].RequireClientCert = true
	_, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		ClientAuth:  &clientAuthDef{Mode: "none"},
	})
	if err == nil {
		t.Fatal("expected error: cannot disable mTLS while a route requires a client cert")
	}
	if !strings.Contains(err.Error(), "/admin") {
		t.Errorf("error should name the offending route, got %v", err)
	}
}

func TestApplyPatchServerSetClientAuthTLSDisabledRejected(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].TLS.Enabled = false
	if _, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		ClientAuth:  &clientAuthDef{Mode: "require", CAFile: "/etc/ca.pem"},
	}); err == nil {
		t.Error("expected error: mutual TLS requires TLS enabled")
	}
}

func TestApplyPatchServerSetClientAuthServerNotFound(t *testing.T) {
	c := mtlsPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:          "server_set_client_auth",
		Listen:      ":8443",
		ServerNames: []string{"nope"},
		ClientAuth:  &clientAuthDef{Mode: "require", CAFile: "/etc/ca.pem"},
	}); err == nil {
		t.Error("expected error: server not found")
	}
}

func TestApplyPatchLocationToggleRequireClientCertEnableRequiresActive(t *testing.T) {
	c := mtlsPatchConfig() // no client_auth on the server
	enabled := true
	if _, err := applyPatch(c, patchRequest{
		Op:          "location_toggle_require_client_cert",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		MatchType:   "prefix",
		Path:        "/admin",
		Enabled:     &enabled,
	}); err == nil {
		t.Error("expected error: server must have mutual TLS enabled first")
	}
}

func TestApplyPatchLocationToggleRequireClientCertEnable(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem"}
	enabled := true
	summary, err := applyPatch(c, patchRequest{
		Op:          "location_toggle_require_client_cert",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		MatchType:   "prefix",
		Path:        "/admin",
		Enabled:     &enabled,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !c.Servers[0].Locations[1].RequireClientCert {
		t.Error("require_client_cert not set on /admin")
	}
	if !strings.Contains(summary, "certificate enabled") {
		t.Errorf("summary = %q, want certificate enabled", summary)
	}
}

func TestApplyPatchLocationToggleRequireClientCertDisable(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].Locations[1].RequireClientCert = true
	disabled := false
	if _, err := applyPatch(c, patchRequest{
		Op:          "location_toggle_require_client_cert",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		MatchType:   "prefix",
		Path:        "/admin",
		Enabled:     &disabled,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Servers[0].Locations[1].RequireClientCert {
		t.Error("require_client_cert should be cleared")
	}
}

func TestApplyPatchLocationToggleRequireClientCertNotFound(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem"}
	enabled := true
	if _, err := applyPatch(c, patchRequest{
		Op:          "location_toggle_require_client_cert",
		Listen:      ":443",
		ServerNames: []string{"app.example"},
		MatchType:   "prefix",
		Path:        "/missing",
		Enabled:     &enabled,
	}); err == nil {
		t.Error("expected error: location not found")
	}
}

func TestProjectMTLS(t *testing.T) {
	c := mtlsPatchConfig()
	c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem", CRLFile: "/etc/crl.pem", VerifySAN: []string{"svc.internal"}}
	c.Servers[0].Locations[1].RequireClientCert = true
	// A plain (non-TLS) server must be excluded from the projection.
	c.Servers = append(c.Servers, config.ServerConfig{Listen: ":80"})

	proj := projectMTLS(c)
	if len(proj.Servers) != 1 {
		t.Fatalf("got %d mTLS servers, want 1", len(proj.Servers))
	}
	sp := proj.Servers[0]
	if sp.Listen != ":443" || sp.Mode != "require" || sp.CAFile != "/etc/ca.pem" || sp.CRLFile != "/etc/crl.pem" {
		t.Errorf("server projection = %+v", sp)
	}
	if len(sp.VerifySAN) != 1 || sp.VerifySAN[0] != "svc.internal" {
		t.Errorf("verify_san = %+v", sp.VerifySAN)
	}
	if len(sp.Locations) != 2 {
		t.Fatalf("got %d locations, want 2", len(sp.Locations))
	}
	if sp.Locations[0].RequireClientCert {
		t.Errorf("location / should not require a client cert")
	}
	if !sp.Locations[1].RequireClientCert || sp.Locations[1].Match != "/admin" {
		t.Errorf("location /admin projection = %+v", sp.Locations[1])
	}
}

func TestProjectMTLSNormalizesInactiveModeToNone(t *testing.T) {
	c := mtlsPatchConfig() // TLS server with no client_auth
	proj := projectMTLS(c)
	if len(proj.Servers) != 1 || proj.Servers[0].Mode != "none" {
		t.Errorf("expected a single server with mode none, got %+v", proj.Servers)
	}
}

func TestHandleMTLSEndpoint(t *testing.T) {
	cfg := mtlsPatchConfig()
	srv := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return cfg, nil },
	})
	req := httptest.NewRequest("GET", "/api/mtls", nil)
	rec := httptest.NewRecorder()
	srv.handleMTLS(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "app.example") || !strings.Contains(body, `"mode":"none"`) {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestDiffMTLSBindTimeWarning(t *testing.T) {
	// Enabling mutual TLS must surface the bind-time restart caveat.
	before := mtlsPatchConfig()
	after := mtlsPatchConfig()
	after.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem"}
	if d := diffConfigs(before, after); !warnHas(d, "listener binds") {
		t.Errorf("expected bind-time warning on enable, got %+v", d.Warnings)
	}

	// Changing a field on an already-active block must also warn.
	mk := func(ca string) *config.Config {
		c := mtlsPatchConfig()
		c.Servers[0].TLS.ClientAuth = &config.ClientAuthConfig{Mode: "require", CAFile: ca}
		return c
	}
	if d := diffConfigs(mk("/etc/ca1.pem"), mk("/etc/ca2.pem")); !warnHas(d, "listener binds") {
		t.Errorf("expected bind-time warning on ca_file change, got %+v", d.Warnings)
	}

	// An identical config must not emit the bind-time warning.
	if d := diffConfigs(mk("/etc/ca1.pem"), mk("/etc/ca1.pem")); warnHas(d, "listener binds") {
		t.Errorf("unexpected bind-time warning for identical config, got %+v", d.Warnings)
	}
}
