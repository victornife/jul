// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminoidc

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/versionid"
)

func TestInjectedContextScopesDerivedGroupPrincipalWithoutChangingOwnership(t *testing.T) {
	cfg := config.AdminOIDCConfig{
		Enabled: true, Issuer: "https://idp.invalid", ClientID: "jul-console",
		CallbackURL: "http://127.0.0.1:9298/auth/oidc/callback", GroupClaim: "groups",
		Groups: []config.AdminOIDCGroup{{Group: "operators", Role: "viewer"}},
	}
	claims := map[string]any{"sub": "synthetic-subject", "groups": []any{"operators"}}
	private := subjectFingerprint(cfg.Issuer, "synthetic-subject")
	principals := make([]string, 0, 3)
	for _, seed := range []byte{11, 11, 12} {
		key, err := versionid.New(bytes.Repeat([]byte{seed}, 32))
		if err != nil {
			t.Fatal(err)
		}
		r, err := New(cfg, policy(t), &http.Client{}, key)
		if err != nil {
			t.Fatal(err)
		}
		id, err := r.mapIdentity(claims)
		if err != nil || id.Role != "viewer" || id.TokenID != "oidc:"+private {
			t.Fatal("injected context changed group admission or private ownership")
		}
		if !strings.HasPrefix(id.Principal, "oidc-") || strings.Contains(id.Principal, private) {
			t.Fatal("derived public principal exposes private fingerprint")
		}
		principals = append(principals, id.Principal)
	}
	if principals[0] != principals[1] || principals[0] == principals[2] {
		t.Fatal("derived principal lost its injected context scope")
	}
}
