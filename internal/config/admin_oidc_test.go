// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func validOIDCAdmin() AdminConfig {
	return AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: strings.Repeat("r", 32), RBAC: AdminRBACConfig{Enabled: true}, OIDC: AdminOIDCConfig{Enabled: true, Issuer: "https://issuer.example", ClientID: "jul", CallbackURL: "http://127.0.0.1:9090/auth/oidc/callback", Subjects: []AdminOIDCSubject{{Subject: "subject", Principal: "alice", Role: "viewer"}}}}
}
func TestAdminOIDCValidation(t *testing.T) {
	if errs := validateAdminOIDC(validOIDCAdmin()); len(errs) != 0 {
		t.Fatal(errs)
	}
	cases := []struct {
		name   string
		mutate func(*AdminConfig)
	}{
		{"rbac", func(a *AdminConfig) { a.RBAC.Enabled = false }},
		{"issuer-http", func(a *AdminConfig) { a.OIDC.Issuer = "http://issuer.example" }},
		{"issuer-query", func(a *AdminConfig) { a.OIDC.Issuer += "?query=x" }},
		{"callback", func(a *AdminConfig) { a.OIDC.CallbackURL = "https://admin.example/other" }},
		{"callback-remote-http", func(a *AdminConfig) { a.Listen = "0.0.0.0:9090" }},
		{"name", func(a *AdminConfig) { a.OIDC.ProviderName = "bad\nlabel" }},
		{"client", func(a *AdminConfig) { a.OIDC.ClientID = "" }},
		{"secret", func(a *AdminConfig) { a.OIDC.ClientSecret = strings.Repeat("x", 4097) }},
		{"idle", func(a *AdminConfig) { a.OIDC.SessionIdle = Duration(time.Second) }},
		{"absolute", func(a *AdminConfig) { a.OIDC.SessionAbsolute = Duration(48 * time.Hour) }},
		{"counts", func(a *AdminConfig) { a.OIDC.MaxSessions = -1 }},
		{"transactions", func(a *AdminConfig) { a.OIDC.MaxTransactions = 1025 }},
		{"skew", func(a *AdminConfig) { a.OIDC.ClockSkew = Duration(3 * time.Minute) }},
		{"scopes", func(a *AdminConfig) { a.OIDC.Scopes = []string{"profile"} }},
		{"refresh", func(a *AdminConfig) { a.OIDC.Scopes = []string{"openid", "offline_access"} }},
		{"duplicate-scope", func(a *AdminConfig) { a.OIDC.Scopes = []string{"openid", "openid"} }},
		{"mapping", func(a *AdminConfig) { a.OIDC.Subjects = nil }},
		{"duplicate-subject", func(a *AdminConfig) { a.OIDC.Subjects = append(a.OIDC.Subjects, a.OIDC.Subjects[0]) }},
		{"unknown-role", func(a *AdminConfig) { a.OIDC.Subjects[0].Role = "root" }},
		{"reserved-name", func(a *AdminConfig) { a.OIDC.Subjects[0].Principal = "shared" }},
		{"group-claim", func(a *AdminConfig) { a.OIDC.Groups = []AdminOIDCGroup{{Group: "ops", Role: "viewer"}} }},
		{"group-role", func(a *AdminConfig) {
			a.OIDC.GroupClaim = "groups"
			a.OIDC.Groups = []AdminOIDCGroup{{Group: "ops", Role: "root"}}
		}},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			a := validOIDCAdmin()
			tt.mutate(&a)
			if len(validateAdminOIDC(a)) == 0 {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	a := validOIDCAdmin()
	a.OIDC.Enabled = false
	if len(validateAdminOIDC(a)) != 0 {
		t.Fatal("disabled block")
	}
}
func TestOIDCURLs(t *testing.T) {
	for _, raw := range []string{"https://issuer.example/realm", "http://localhost:8080/realm", "http://[::1]:8080/realm"} {
		if _, err := OIDCURL(raw); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, raw := range []string{"", "/relative", "ftp://issuer.example", "http://remote.example", "https://user:pass@example.com", "https://example.com/?secret=x", "https://example.com/#token", "https://example.com/%xx"} {
		if _, err := OIDCURL(raw); err == nil {
			t.Fatal(raw)
		}
	}
}
func TestOIDCParseRoundTripSecretsAndLint(t *testing.T) {
	raw := []byte(`[admin]
enabled=true
listen="127.0.0.1:9090"
token="${env:JUL_TEST_RECOVERY}"
[admin.rbac]
enabled=true
[admin.oidc]
enabled=true
issuer="https://issuer.example"
client_id="jul"
client_secret="${env:JUL_TEST_OIDC_SECRET}"
callback_url="http://127.0.0.1:9090/auth/oidc/callback"
[[admin.oidc.subjects]]
subject="exact-sub"
principal="alice"
role="viewer"
`)
	t.Setenv("JUL_TEST_RECOVERY", strings.Repeat("r", 32))
	t.Setenv("JUL_TEST_OIDC_SECRET", "synthetic-client-secret")
	c, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(encoded)
	if err != nil || again.Admin.OIDC.SessionAbsolute.Std() != 8*time.Hour {
		t.Fatal(err)
	}
	effective, state, _, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Admin.OIDC.ClientSecret != os.Getenv("JUL_TEST_OIDC_SECRET") || !strings.Contains(state.Apply("synthetic-client-secret"), "***") {
		t.Fatal("secret resolution/redaction")
	}
	c.Admin.OIDC.ClientSecret = "literal-sensitive-value"
	found := false
	for _, d := range Lint(c) {
		if strings.Contains(d.Field, "client_secret") {
			found = true
			if strings.Contains(d.Message, "literal-sensitive-value") {
				t.Fatal("lint secret leak")
			}
		}
	}
	if !found {
		t.Fatal("missing literal-secret lint")
	}
}
