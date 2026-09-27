// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/waf"
)

func TestWAFReportClassifiesRoutes(t *testing.T) {
	c := &config.Config{WAF: config.WAFConfig{Enabled: true}}
	srv := config.ServerConfig{Listen: ":8443", ServerNames: []string{"shop.example"}}
	inherit := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}}
	override := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, WAF: &config.WAFConfig{Enabled: true, Mode: "detect"}}
	off := config.LocationConfig{Match: config.MatchConfig{Type: "exact", Path: "/health"}, WAF: &config.WAFConfig{Enabled: false}}

	r := newWAFReport(c)
	global := waf.PolicyInfo{Mode: "block", BlockStatus: 403, CRSEnabled: true, CRSVersion: "4.25.0", Paranoia: 1, ParanoiaDefault: true,
		RequestBodyLimitBytes: 131072, Rules: waf.RuleCounts{Total: 3, Embedded: 3}}
	r.protected(srv, inherit, global)
	r.protected(srv, inherit, waf.PolicyInfo{Mode: "ignored"})
	r.protected(srv, override, waf.PolicyInfo{Mode: "detect", BlockStatus: 403, ExternalFiles: 1, ExternalDigest: "sha256:ab", RuleFilesConfigured: 1,
		Rules: waf.RuleCounts{Total: 1, External: 1}})
	r.unprotected(srv, off)
	r.unprotected(srv, inherit)
	at := time.Date(2026, 9, 27, 10, 0, 0, 0, time.FixedZone("x", 3600))
	got := r.finish(at)

	if got.CompiledAt != "2026-09-27T09:00:00Z" || !got.GlobalEnabled {
		t.Fatalf("report = %+v", got)
	}
	if got.InheritingRoutes != 2 || got.OverrideRoutes != 1 || got.DisabledOverrideRoutes != 1 || got.UnprotectedRoutes != 2 {
		t.Fatalf("coverage = %+v", got)
	}
	if got.Global == nil || got.Global.Mode != "block" || got.Global.CRSVersion != "4.25.0" || got.Global.Rules.Embedded != 3 {
		t.Fatalf("global = %+v", got.Global)
	}
	if len(got.Overrides) != 2 {
		t.Fatalf("overrides = %+v", got.Overrides)
	}
	o := got.Overrides[0]
	if o.Path != "/api" || !o.Enabled || o.Policy == nil || o.Policy.Mode != "detect" || o.Policy.ExternalDigest != "sha256:ab" {
		t.Fatalf("override = %+v / %+v", o, o.Policy)
	}
	if d := got.Overrides[1]; d.Path != "/health" || d.Enabled || d.Policy != nil {
		t.Fatalf("disabled override = %+v", d)
	}
}

func TestWAFEffectivePublishesServingGeneration(t *testing.T) {
	f := &HandlerFactory{}
	before := f.WAFEffective()
	if before.Compiled != waf.Compiled || before.EmbeddedCRSVersion != waf.EmbeddedCRSVersion() || before.Generation != 0 || before.Global != nil {
		t.Fatalf("before the first generation = %+v", before)
	}
	if waf.Compiled == (before.EmbeddedCRSVersion == "") {
		t.Fatalf("embedded CRS version %q inconsistent with Compiled=%v", before.EmbeddedCRSVersion, waf.Compiled)
	}

	f.publishWAF(3, nil)
	if f.WAFEffective().Generation != 0 {
		t.Fatal("a nil policy must not replace the serving one")
	}
	p := &admin.WAFEffectivePolicy{GlobalEnabled: true, InheritingRoutes: 1, Overrides: []admin.WAFRouteOverride{{Path: "/a"}}}
	f.publishWAF(7, p)
	got := f.WAFEffective()
	if got.Generation != 7 || got.InheritingRoutes != 1 || p.Generation != 0 {
		t.Fatalf("published = %+v (source mutated: %d)", got, p.Generation)
	}
	got.Overrides[0].Path = "/mutated"
	if f.WAFEffective().Overrides[0].Path != "/a" {
		t.Fatal("WAFEffective must return a copy")
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), `"generation":7`) {
		t.Fatalf("json = %s", raw)
	}
}
