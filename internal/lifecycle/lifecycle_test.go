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
	return Fingerprint{Values: map[string]any{
		"global.log_format":                  logFormat,
		"global.access_log":                  "",
		"global.error_log":                   "",
		"global.worker_threads":              4,
		"global.redact_min_secret_length":    4,
		"admin.enabled":                      false,
		"admin.listen":                       "",
		"admin.token":                        "",
		"cache.enabled":                      false,
		"cache.memory_max_size":              0,
		"cache.disk_path":                    "",
		"cache.disk_max_size":                0,
		"observability.metrics.host_label":   false,
		"observability.tracing.enabled":      false,
		"observability.tracing.endpoint":     "",
		"observability.tracing.sample_ratio": 0.0,
		"observability.access_log.sinks":     []any{},
		"observability.access_log.file":      "",
		"observability.access_log.format":    "",
		"egress.enabled":                     false,
		"egress.allow":                       []any{},
		"servers.*.tls":                      map[string]any{},
		"servers.*.http3":                    map[string]any{},
		"servers.*.h2c":                      map[string]any{},
		"stream.*.protocol":                  map[string]any{},
	}}
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

func TestComputeFingerprintWorkerThreadsAuto(t *testing.T) {
	cfg := &config.Config{}
	cfg.Global.WorkerThreads = "auto"
	fp := ComputeFingerprint(cfg)
	if fp.Values["global.worker_threads"] == nil {
		t.Fatal("worker_threads fingerprint is nil")
	}
}
