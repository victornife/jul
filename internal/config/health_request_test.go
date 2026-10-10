// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestHealthRequestValidation(t *testing.T) {
	if len(validateHealthRequest(&HealthCheckConfig{Type: "http", Host: "https://invalid"}, "health")) == 0 {
		t.Fatal("bad host accepted")
	}
	for _, name := range []string{"Host", "Content-Length", "connection", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization", ":authority", "Bad Name", strings.Repeat("a", 129)} {
		t.Run(name, func(t *testing.T) {
			if len(validateHealthRequest(&HealthCheckConfig{Type: "http", Headers: map[string]string{name: "secret-value"}}, "health")) == 0 {
				t.Fatal("accepted forbidden header")
			}
		})
	}
	for _, value := range []string{"bad\r\nInjected: yes", "bad\x00", strings.Repeat("x", 4097)} {
		errs := validateHealthRequest(&HealthCheckConfig{Type: "http", Headers: map[string]string{"X-Health": value}}, "health")
		if len(errs) == 0 {
			t.Fatal("accepted bad value")
		}
		for _, err := range errs {
			if strings.Contains(err.Error(), value) {
				t.Fatal("value leaked")
			}
		}
	}
	for _, typ := range []string{"tcp", "grpc"} {
		if len(validateHealthRequest(&HealthCheckConfig{Type: typ, Headers: map[string]string{"X": "ok"}}, "health")) == 0 {
			t.Fatal("accepted HTTP headers for other protocol")
		}
	}
	if len(validateHealthRequest(&HealthCheckConfig{Type: "tcp", Host: "health.internal"}, "health")) == 0 {
		t.Fatal("accepted TCP host")
	}
	if len(validateHealthRequest(&HealthCheckConfig{Type: "http", Headers: map[string]string{"x-health": "one", "X-Health": "two"}}, "health")) == 0 {
		t.Fatal("accepted case duplicates")
	}
	headers := map[string]string{}
	for i := 0; i < 33; i++ {
		headers[strings.Repeat("x", i+1)] = "ok"
	}
	if len(validateHealthRequest(&HealthCheckConfig{Type: "http", Headers: headers}, "health")) == 0 {
		t.Fatal("accepted 33 headers")
	}
	headers = map[string]string{"A": strings.Repeat("x", 4096), "B": strings.Repeat("x", 4096), "C": strings.Repeat("x", 4096), "D": strings.Repeat("x", 4096)}
	if len(validateHealthRequest(&HealthCheckConfig{Type: "http", Headers: headers}, "health")) == 0 {
		t.Fatal("accepted aggregate overflow")
	}
	for _, h := range []*HealthCheckConfig{{Type: "http", Host: "health.internal:8080", Headers: map[string]string{"Authorization": "Bearer ${env:HEALTH_TOKEN}", "User-Agent": "probe"}}, {Type: "grpc", Host: "grpc.internal"}} {
		if errs := validateHealthRequest(h, "health"); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
}
func TestHealthAuthorityGrammar(t *testing.T) {
	for _, s := range []string{"api.internal", "api.internal.", "localhost:80", "127.0.0.1", "127.0.0.1:65535", "[::1]", "[::1]:443"} {
		if !validHealthAuthority(s) {
			t.Errorf("rejected %q", s)
		}
	}
	for _, s := range []string{"", "https://api", "a/b", "a?b", "a#b", "u@h", "a\\b", "a b", "a\n", "á.example", "a:0", "a:65536", "a:", "a:-1", "a:abc", "a..b", "[]", "-a", "a-", "a_b", "[invalid]", "::1", strings.Repeat("a", 64) + ".com", strings.Repeat("a", 254)} {
		if validHealthAuthority(s) {
			t.Errorf("accepted %q", s)
		}
	}
}
func TestHealthHeaderSecretResolution(t *testing.T) {
	t.Setenv("JUL_HEALTH_TOKEN", "health-secret-token")
	c := &Config{Upstreams: []UpstreamConfig{{HealthCheck: &HealthCheckConfig{Headers: map[string]string{"Authorization": "Bearer ${env:JUL_HEALTH_TOKEN}"}}}}}
	out, state, _, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if out.Upstreams[0].HealthCheck.Headers["Authorization"] != "Bearer health-secret-token" {
		t.Fatal("not resolved")
	}
	if state.Apply("health-secret-token") != "***" {
		t.Fatal("not redacted")
	}
	b, _ := json.Marshal(c)
	if strings.Contains(string(b), "health-secret-token") {
		t.Fatal("raw mutated")
	}
}

func TestPluginConfigSecretResolutionTraversesMapStructs(t *testing.T) {
	t.Setenv("JUL_SIGNED_CURRENT", "current-encoded-test-key")
	path := filepath.Join(t.TempDir(), "previous-key")
	if err := os.WriteFile(path, []byte("previous-encoded-test-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := &Config{Plugins: map[string]PluginConfig{"signed": {Config: map[string]string{"key.current": "${env:JUL_SIGNED_CURRENT}", "key.previous": "${file:" + path + "}"}}}}
	if IsResolved(c) || CountSecretRefs(c) != 2 {
		t.Fatal("map declarations omitted from secret inspection")
	}
	resolved, state, _, err := Resolve(c)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Plugins["signed"].Config["key.current"] != "current-encoded-test-key" || resolved.Plugins["signed"].Config["key.previous"] != "previous-encoded-test-key" {
		t.Fatal("plugin secrets not resolved")
	}
	if !IsResolved(resolved) || !ContainsStringValue(resolved, "current-encoded-test-key") {
		t.Fatal("resolved map not inspected")
	}
	if state.Apply("current-encoded-test-key previous-encoded-test-key") != "*** ***" {
		t.Fatal("plugin secret values not redacted")
	}
	if CountSecretRefs(c) != 2 {
		t.Fatal("raw map mutated")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = IsResolved(c)
				_ = CountSecretRefs(c)
				_ = ContainsStringValue(c, "JUL_SIGNED_CURRENT")
			}
		}()
	}
	wg.Wait()
	c.Plugins["signed"].Config["key.current"] = "${env:JUL_MISSING_SIGNED_KEY}"
	t.Setenv("JUL_MISSING_SIGNED_KEY", "")
	if err := os.Unsetenv("JUL_MISSING_SIGNED_KEY"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Resolve(c); err == nil {
		t.Fatal("missing plugin secret accepted")
	}
}
