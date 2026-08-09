// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

// This guards the full runtime-preflight chain. In lean builds waf.Check used
// to return directly, which silently skipped later auth and compression dry
// runs after a successful WAF check.
func TestRuntimePreflightContinuesPastWAFCheck(t *testing.T) {
	cfg := issue80BaseConfig()
	cfg.Servers[0].Locations[0].Auth = &config.AuthConfig{
		Basic: &config.BasicAuthConfig{
			File:  filepath.Join(t.TempDir(), "missing.htpasswd"),
			Realm: "Issue 80",
		},
	}

	err := validateRuntimeComponents(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected missing htpasswd file to fail runtime preflight")
	}
	if !strings.Contains(err.Error(), "auth:") {
		t.Fatalf("runtime preflight error = %q, want auth context", err)
	}
}
