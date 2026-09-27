// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/waf"
)

// wafFactoryConfig serves four routes: two inheriting the global policy, one
// detect-mode override loading an external rule file, and one override that
// turns the WAF off.
func wafFactoryConfig(rules string) *config.Config {
	cfg := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:0")
	cfg.WAF = config.WAFConfig{Enabled: true, CRSEnabled: true, RequestBodyLimit: 128 << 10}
	base := cfg.Servers[0].Locations[0]
	api := base
	api.Match.Path = "/api"
	api.WAF = &config.WAFConfig{Enabled: true, Mode: "detect", DirectivesFiles: []string{rules}, RequestBodyLimit: 64 << 10}
	health := base
	health.Match.Path = "/health"
	health.WAF = &config.WAFConfig{Enabled: false}
	static := base
	static.Match.Path = "/static"
	cfg.Servers[0].Locations = append(cfg.Servers[0].Locations, api, health, static)
	return cfg
}

func prepareAndCommit(t *testing.T, f *HandlerFactory, cfg *config.Config) uint64 {
	t.Helper()
	_, genID, commit, _, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retire := commit()
	if retire != nil {
		retire()
	}
	return genID
}

func TestFactoryWAFEffectiveTracksServingGeneration(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	dir := t.TempDir()
	rules := filepath.Join(dir, "api.conf")
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(rules, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`SecRule REQUEST_URI "@contains /api/admin" "id:40001,phase:1,deny,status:403"` + "\n")

	gen1 := prepareAndCommit(t, f, wafFactoryConfig(rules))
	p := f.WAFEffective()
	if !p.Compiled || p.Generation != gen1 || p.CompiledAt == "" || !p.GlobalEnabled {
		t.Fatalf("serving policy = %+v", p)
	}
	if p.InheritingRoutes != 2 || p.OverrideRoutes != 1 || p.DisabledOverrideRoutes != 1 || p.UnprotectedRoutes != 1 {
		t.Fatalf("coverage = %+v", p)
	}
	if p.Global == nil || !p.Global.CRSEnabled || p.Global.CRSVersion != p.EmbeddedCRSVersion || p.Global.Rules.Embedded == 0 || p.Global.ExternalFiles != 0 {
		t.Fatalf("global = %+v", p.Global)
	}
	var api = p.Overrides[0].Policy
	if api == nil || api.Mode != "detect" || api.CRSEnabled || api.RequestBodyLimitBytes != 64<<10 || api.ExternalFiles != 1 || api.Rules.External != 1 {
		t.Fatalf("api override = %+v", api)
	}
	digest1 := api.ExternalDigest
	if strings.Contains(fmt.Sprintf("%+v %+v", *p, *api), dir) {
		t.Fatal("effective policy leaks the rule file path")
	}

	// A candidate whose rule file no longer compiles leaves the serving
	// projection on the previous generation (Prepare fails before Publish).
	write("SecRule ARGS \"@notAnOperator x\" \"id:40002,phase:1,deny\"\n")
	if _, _, _, _, err := f.Prepare(context.Background(), wafFactoryConfig(rules)); err == nil {
		t.Fatal("broken rule file compiled")
	}
	if after := f.WAFEffective(); after.Generation != gen1 || after.Overrides[0].Policy.ExternalDigest != digest1 {
		t.Fatalf("failed candidate changed the serving projection: %+v", after)
	}

	// An aborted candidate is not serving either.
	write(`SecRule REQUEST_URI "@contains /api/other" "id:40003,phase:1,deny,status:403"` + "\n")
	_, _, _, abort, err := f.Prepare(context.Background(), wafFactoryConfig(rules))
	if err != nil {
		t.Fatal(err)
	}
	abort()
	if after := f.WAFEffective(); after.Generation != gen1 || after.Overrides[0].Policy.ExternalDigest != digest1 {
		t.Fatalf("aborted candidate changed the serving projection: %+v", after)
	}

	// Same path, changed bytes, committed: a new generation and a new digest.
	gen2 := prepareAndCommit(t, f, wafFactoryConfig(rules))
	after := f.WAFEffective()
	if after.Generation != gen2 || gen2 == gen1 || after.Overrides[0].Policy.ExternalDigest == digest1 {
		t.Fatalf("same-path content change not observable: gen %d->%d digest %s->%s", gen1, gen2, digest1, after.Overrides[0].Policy.ExternalDigest)
	}

	// A global policy change is reflected; an inline-only global loads no file.
	cfg := wafFactoryConfig(rules)
	cfg.WAF = config.WAFConfig{Enabled: true, Mode: "detect", InlineRules: `SecRule ARGS:q "@streq x" "id:40004,phase:1,deny"`, RequestBodyLimit: 128 << 10}
	prepareAndCommit(t, f, cfg)
	if g := f.WAFEffective().Global; g == nil || g.Mode != "detect" || g.CRSEnabled || g.Rules.Inline != 1 || g.ExternalFiles != 0 {
		t.Fatalf("global after change = %+v", g)
	}
}

// Every inheriting route shares one compiled global engine, so a generation
// enforces a single global rule set even if a rule file changes mid-build.
func TestFactorySharesTheGlobalFirewall(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()
	cfg := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:0")
	cfg.WAF = config.WAFConfig{Enabled: true, InlineRules: `SecRule ARGS:q "@streq x" "id:40010,phase:1,deny"`, RequestBodyLimit: 128 << 10}
	for _, p := range []string{"/a", "/b", "/c"} {
		loc := cfg.Servers[0].Locations[0]
		loc.Match.Path = p
		cfg.Servers[0].Locations = append(cfg.Servers[0].Locations, loc)
	}
	compiles := 0
	orig := newFirewall
	newFirewall = func(ctx context.Context, c config.WAFConfig, o waf.Options) (*waf.Firewall, error) {
		compiles++
		return orig(ctx, c, o)
	}
	t.Cleanup(func() { newFirewall = orig })
	prepareAndCommit(t, f, cfg)
	if compiles != 1 {
		t.Fatalf("global policy compiled %d times for 4 inheriting routes, want 1", compiles)
	}
	if p := f.WAFEffective(); p.InheritingRoutes != 4 || p.Global == nil || p.Global.Rules.Inline != 1 {
		t.Fatalf("shared global = %+v", p)
	}
}
