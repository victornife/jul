// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/server"
)

var (
	digestA = "sha256:" + strings.Repeat("a", 64)
	digestB = "sha256:" + strings.Repeat("b", 64)
)

func TestPluginProjectionCarriesServingDigestAndPin(t *testing.T) {
	c := &config.Config{Plugins: map[string]config.PluginConfig{
		"live":    {Path: "/opt/live.wasm", SHA256: strings.Repeat("a", 64)},
		"pending": {Path: "/opt/pending.wasm"},
	}}
	out := projectPlugins(c, true)
	attachPluginModules(&out, map[string]PluginModule{"live": {Digest: digestA}, "gone": {Digest: digestB}})
	byName := map[string]PluginProjection{}
	for _, p := range out.Plugins {
		byName[p.Name] = p
	}
	live := byName["live"]
	if live.Digest != digestA || live.DigestShort != strings.Repeat("a", 12) || !live.Pinned {
		t.Fatalf("live projection = %+v", live)
	}
	pending := byName["pending"]
	if pending.Digest != "" || pending.DigestShort != "" || pending.Pinned {
		t.Fatalf("a plugin that is not serving must not claim a digest: %+v", pending)
	}
	if got := shortDigest("sha256:abc"); got != "abc" {
		t.Fatalf("shortDigest = %q", got)
	}
}

func TestPluginDiffShowsPinChange(t *testing.T) {
	pin := strings.Repeat("c", 64)
	d := &ConfigDiff{}
	diffPluginFields("p", config.PluginConfig{Path: "p.wasm"}, config.PluginConfig{Path: "p.wasm", SHA256: strings.ToUpper(pin)}, d)
	found := false
	for _, e := range d.Modifications {
		if strings.Contains(e.Detail, "sha256 pin") {
			found = true
			if e.Before != "(none)" || e.After != "sha256:"+pin {
				t.Fatalf("pin diff entry = %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("pin change missing from diff: %+v", d.Modifications)
	}

	same := &ConfigDiff{}
	diffPluginFields("p", config.PluginConfig{SHA256: pin}, config.PluginConfig{SHA256: "sha256:" + pin}, same)
	for _, e := range same.Modifications {
		if strings.Contains(e.Detail, "sha256 pin") {
			t.Fatal("equivalent pin spellings reported as a change")
		}
	}
	if got := pluginPin(config.PluginConfig{SHA256: "zz"}); got != "zz" {
		t.Fatalf("malformed pin shown as %q, want as written", got)
	}
}

func TestManagedApplyRecordsPluginModuleChanges(t *testing.T) {
	s := newHistoryServer(t.TempDir())
	s.audit = newAuditLog(16)
	changes := []server.PluginModuleChange{
		{Name: "p", Before: digestA, After: digestB},
		{Name: "added", After: digestA},
		{Name: "removed", Before: digestB},
	}
	res := ConfigApplyResult{ApplyID: "rl_1", OK: true, Mode: "hot", Reload: &server.ReloadResult{Outcome: server.ReloadAppliedLive, PluginModules: changes}}
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply, Actor: "alice"}, res, []byte(managedHistoryPrevRaw))
	if err != nil || id == "" {
		t.Fatalf("RecordManagedHistory = %q, %v", id, err)
	}
	meta, err := s.hist.getMeta(id)
	if err != nil || len(meta.PluginModules) != 3 || meta.PluginModules[0] != changes[0] {
		t.Fatalf("history metadata plugin modules = %+v (%v)", meta, err)
	}

	events := s.audit.snapshot("plugin.module_identity_changed", "", 0)
	if len(events) != 3 {
		t.Fatalf("audit events = %+v", events)
	}
	details := map[string]string{}
	for _, ev := range events {
		details[ev.ResourceID] = ev.Detail
	}
	if details["p"] != "module content "+digestA+" -> "+digestB {
		t.Fatalf("same-path change detail = %q", details["p"])
	}
	if !strings.HasPrefix(details["added"], "module content (none) -> ") || !strings.HasSuffix(details["removed"], "-> (none)") {
		t.Fatalf("add/remove details = %+v", details)
	}

	// A failed apply publishes nothing and records no module change.
	failed := &Server{audit: newAuditLog(4)}
	_, _ = failed.RecordManagedHistory(ApplyRequestContext{}, ConfigApplyResult{Reload: &server.ReloadResult{PluginModules: changes}}, nil)
	if n := len(failed.audit.snapshot("plugin.", "", 0)); n != 0 {
		t.Fatalf("failed apply audited %d module changes", n)
	}
	var nilServer *Server
	if _, err := nilServer.RecordManagedHistory(ApplyRequestContext{}, res, nil); err != nil {
		t.Fatal(err)
	}
	(&Server{}).auditPluginModuleChanges("x", changes)
}

func TestBuildPluginPreservesAndSetsPin(t *testing.T) {
	pin := strings.Repeat("d", 64)
	existing := config.PluginConfig{Path: "p.wasm", SHA256: pin}

	kept, _, err := buildPlugin(pluginDef{Path: "p.wasm"}, existing)
	if err != nil || kept.SHA256 != pin {
		t.Fatalf("editor without a pin field dropped the pin: %+v %v", kept, err)
	}
	empty := ""
	cleared, _, err := buildPlugin(pluginDef{Path: "p.wasm", SHA256: &empty}, existing)
	if err != nil || cleared.SHA256 != "" {
		t.Fatalf("explicit empty pin did not clear: %+v %v", cleared, err)
	}
	upper := "SHA256:" + strings.ToUpper(pin)
	set, _, err := buildPlugin(pluginDef{Path: "p.wasm", SHA256: &upper}, config.PluginConfig{})
	if err != nil || set.SHA256 != pin {
		t.Fatalf("explicit pin not canonicalized: %+v %v", set, err)
	}
	bad := "nope"
	if _, _, err := buildPlugin(pluginDef{Path: "p.wasm", SHA256: &bad}, existing); err == nil {
		t.Fatal("malformed pin accepted by the editor")
	}
}

func TestHandlePluginsServesRuntimeDigest(t *testing.T) {
	cfg := pluginPatchConfig()
	srv := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:      func() (*config.Config, error) { return cfg, nil },
		PluginsCompiled: true,
		PluginModules:   func() map[string]PluginModule { return map[string]PluginModule{"inject": {Digest: digestA}} },
	})
	rec := httptest.NewRecorder()
	srv.handlePlugins(rec, httptest.NewRequest("GET", "/api/plugins", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, `"digest":"`+digestA+`"`) || !strings.Contains(body, `"digest_short":"aaaaaaaaaaaa"`) {
		t.Fatalf("status %d body %s", rec.Code, body)
	}
	if strings.Count(body, `"digest":`) != 1 {
		t.Fatalf("digest attached to a plugin that is not serving: %s", body)
	}
}
