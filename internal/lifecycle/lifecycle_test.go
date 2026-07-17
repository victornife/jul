// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"testing"

	"jul/internal/config"
)

func TestFieldClassExact(t *testing.T) {
	if got := FieldClass("global.log_format"); got != RestartRequiredClass {
		t.Fatalf("log_format class = %v, want restart_required", got)
	}
	if got := FieldClass("global.log_level"); got != HotReloadClass {
		t.Fatalf("log_level class = %v, want hot_reload", got)
	}
	if got := FieldClass("servers.*.listen"); got != NewListenerOnlyClass {
		t.Fatalf("listen class = %v, want new_listener_only", got)
	}
}

func TestFieldClassUnknownIsHotReload(t *testing.T) {
	if got := FieldClass("unknown.path"); got != HotReloadClass {
		t.Fatalf("unknown class = %v, want hot_reload", got)
	}
}

func TestLookupWildcard(t *testing.T) {
	e := Lookup("servers.0.locations.3.proxy_pass")
	if e == nil {
		t.Fatal("expected wildcard match")
	}
	if e.Class != HotReloadClass {
		t.Fatalf("proxy_pass class = %v, want hot_reload", e.Class)
	}
}

func TestRestartRequiredDetectsChange(t *testing.T) {
	old := makeFingerprint("text")
	next := makeFingerprint("json")
	reason, need := RestartRequired(old, next)
	if !need {
		t.Fatal("expected restart required")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason")
	}
}

func makeFingerprint(logFormat string) Fingerprint {
	cfg := &config.Config{}
	cfg.Global.LogFormat = logFormat
	return ComputeFingerprint(cfg)
}

func TestRestartRequiredNoChange(t *testing.T) {
	base := makeFingerprint("text")
	reason, need := RestartRequired(base, base)
	if need {
		t.Fatalf("unexpected restart required: %s", reason)
	}
}

func TestRestartRequiredIgnoresListenerAddition(t *testing.T) {
	base := makeFingerprint("text")
	base.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
	}
	candidate := makeFingerprint("text")
	candidate.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
		"127.0.0.1:8081": map[string]any{"enabled": true},
	}
	if reason, need := RestartRequired(base, candidate); need {
		t.Fatalf("listener addition should not require restart: %s", reason)
	}
}

func TestRestartRequiredDetectsExistingListenerTLSChange(t *testing.T) {
	base := makeFingerprint("text")
	base.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": false},
	}
	candidate := makeFingerprint("text")
	candidate.Values["servers.*.tls"] = map[string]any{
		"127.0.0.1:8080": map[string]any{"enabled": true},
	}
	if _, need := RestartRequired(base, candidate); !need {
		t.Fatal("changing TLS on an existing listener should require restart")
	}
}

func TestComputeFingerprintIncludesLogFormat(t *testing.T) {
	cfg := &config.Config{}
	cfg.Global.LogFormat = "json"
	fp := ComputeFingerprint(cfg)
	if got := fp.Values["global.log_format"]; got != "json" {
		t.Fatalf("log_format fingerprint = %v, want json", got)
	}
}

func TestComputeFingerprintTLSAggregatesVirtualHostsPerAddress(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: ":8443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-a"}},
			{Listen: ":8443", ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-b"}},
		},
	}
	fp := ComputeFingerprint(cfg)
	tls, ok := fp.Values["servers.*.tls"].(map[string]any)
	if !ok {
		t.Fatalf("tls fingerprint type = %T, want map[string]any", fp.Values["servers.*.tls"])
	}
	vhosts, ok := tls[":8443"].(map[string]any)
	if !ok {
		t.Fatalf("tls vhosts type = %T, want map[string]any", tls[":8443"])
	}
	if len(vhosts) != 2 {
		t.Fatalf("expected 2 vhosts, got %d", len(vhosts))
	}
	if _, ok := vhosts["a.example.com"]; !ok {
		t.Fatal("missing vhost a.example.com")
	}
	if _, ok := vhosts["b.example.com"]; !ok {
		t.Fatal("missing vhost b.example.com")
	}
}

func TestComputeFingerprintTLSIgnoresVirtualHostOrder(t *testing.T) {
	cfgA := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: ":8443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-a"}},
			{Listen: ":8443", ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-b"}},
		},
	}
	cfgB := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: ":8443", ServerNames: []string{"b.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-b"}},
			{Listen: ":8443", ServerNames: []string{"a.example.com"}, TLS: &config.TLSConfig{Enabled: true, Cert: "cert-a"}},
		},
	}
	fpA := ComputeFingerprint(cfgA)
	fpB := ComputeFingerprint(cfgB)
	if reason, need := RestartRequired(fpA, fpB); need {
		t.Fatalf("same vhosts in different order should not require restart: %s", reason)
	}
}

func TestComputeFingerprintWorkerThreadsNotStartupConsumed(t *testing.T) {
	cfg := &config.Config{}
	cfg.Global.WorkerThreads = "auto"
	fp := ComputeFingerprint(cfg)
	if _, ok := fp.Values["global.worker_threads"]; ok {
		t.Fatal("worker_threads is hot-reloadable and must not appear in startup fingerprint")
	}
}
