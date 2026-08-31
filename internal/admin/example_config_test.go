// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"os"
	"testing"

	"jul/internal/config"
)

// TestAdminAPIExampleIsValidAndSatisfiesTheContract keeps the documented
// example honest. An example that stops parsing is worse than no example, and
// an example that quietly stops demonstrating the thing it exists to
// demonstrate — a loopback admin listener and least-privilege principals — is
// worse still, because a reader copies it.
func TestAdminAPIExampleIsValidAndSatisfiesTheContract(t *testing.T) {
	for k, v := range map[string]string{
		"JUL_SCRAPE_TOKEN": "scrape-token-32-chars-padded----",
		"JUL_DEPLOY_TOKEN": "deploy-token-32-chars-padded----",
		"JUL_ADMIN_TOKEN":  "admin-token-32-chars-padded-----",
	} {
		t.Setenv(k, v)
	}

	raw, err := os.ReadFile("../../testdata/admin-api.toml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatalf("testdata/admin-api.toml does not parse: %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("testdata/admin-api.toml is not valid: %v", err)
	}

	if !addrIsLoopback(cfg.Admin.Listen) {
		t.Fatalf("the example binds the admin listener to %q, which the transport gate refuses in cleartext; "+
			"the example exists to show the shape that works", cfg.Admin.Listen)
	}
	if !cfg.Admin.RBAC.Enabled {
		t.Fatal("the example must enable RBAC: the legacy single-token identity is a wildcard principal holding read and write, " +
			"so a scrape token issued that way carries full control-plane access")
	}

	// The scrape identity must hold metrics:read and nothing more. This is the
	// property the example is for.
	var scrapeRole string
	for _, p := range cfg.Admin.RBAC.Principals {
		if p.Name == "prometheus" {
			scrapeRole = p.Role
		}
	}
	if scrapeRole == "" {
		t.Fatal("the example no longer defines a dedicated scrape principal")
	}
	for _, r := range cfg.Admin.RBAC.Roles {
		if r.Name != scrapeRole {
			continue
		}
		if len(r.Permissions) != 1 || r.Permissions[0] != "metrics:read" {
			t.Fatalf("the scrape role holds %v; it exists to hold metrics:read and nothing else", r.Permissions)
		}
		return
	}
	t.Fatalf("the scrape principal references role %q, which the example does not define", scrapeRole)
}
