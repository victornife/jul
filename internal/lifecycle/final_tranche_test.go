// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"testing"

	"jul/internal/config"
)

func TestFinalTrancheLifecycleDecisions(t *testing.T) {
	for _, path := range []string{
		"rate_limit.max_conns",
		"admin.history_keep",
		"servers.*.tls.acme.ocsp_stapling",
	} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("missing lifecycle entry %s", path)
		}
		if e.Class != HotReloadClass || e.StartupConsumed {
			t.Fatalf("%s = class %s startup=%t, want hot_reload/startup=false", path, e.Class, e.StartupConsumed)
		}
	}
	for _, path := range []string{
		"admin.history_dir",
		"servers.*.http3.enabled",
		"servers.*.tls.min_version",
		"servers.*.tls.client_auth.mode",
	} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("missing lifecycle entry %s", path)
		}
		if e.Class != RestartRequiredClass {
			t.Fatalf("%s = %s, want restart_required", path, e.Class)
		}
	}
}

func TestHistoryRetentionMixedWithDirectoryRemainsWholeCandidateRestart(t *testing.T) {
	before := &config.Config{Admin: config.AdminConfig{HistoryDir: "/history/a", HistoryKeep: 50}}
	after := &config.Config{Admin: config.AdminConfig{HistoryDir: "/history/b", HistoryKeep: 10}}
	res, err := Classify(before, after, Live{})
	if err != nil {
		t.Fatal(err)
	}
	if res.CanApplyHot || !res.CanStageRestart {
		t.Fatalf("mixed history candidate must stage as a whole: %+v", res)
	}
	if !contains(res.RestartRequired, "admin.history_dir") {
		t.Fatalf("unexpected mixed history classification: restart=%v", res.RestartRequired)
	}
}
