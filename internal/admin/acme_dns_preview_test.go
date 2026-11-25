// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestDNS01PreviewAndHistoryAreSecretSafeAndDoNotIssue(t *testing.T) {
	const oldKey = "YWFhYWFhYWFhYWFhYWFhYWFhYWFhYWFh"
	const newKey = "YmJiYmJiYmJiYmJiYmJiYmJiYmJiYmJi"
	t.Setenv("JUL_DNS_PREVIEW_OLD", oldKey)
	t.Setenv("JUL_DNS_PREVIEW_NEW", newKey)

	var orders, updates atomic.Int32
	ca := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		orders.Add(1)
		http.Error(w, "unexpected CA request", http.StatusInternalServerError)
	}))
	defer ca.Close()
	dns, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := dns.Accept()
			if err != nil {
				return
			}
			updates.Add(1)
			_ = conn.Close()
		}
	}()
	defer func() { _ = dns.Close(); <-done }()

	raw := fmt.Sprintf(`
[[servers]]
listen = "127.0.0.1:8443"
server_names = ["example.test", "*.example.test"]
  [servers.tls]
  enabled = true
    [servers.tls.acme]
    enabled = true
    email = "ops@example.test"
    ca = %q
    challenge = "dns-01"
    dns_provider = "rfc2136"
      [servers.tls.acme.rfc2136]
      server = %q
      observers = [%q]
      zone = "example.test"
      tsig_key_name = "jul.example.test"
      tsig_secret = "${env:JUL_DNS_PREVIEW_OLD}"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = "http://127.0.0.1:9000"
`, ca.URL+"/directory", dns.Addr().String(), dns.Addr().String())
	before, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	afterRaw := strings.Replace(raw, "JUL_DNS_PREVIEW_OLD", "JUL_DNS_PREVIEW_NEW", 1)
	assessment, err := previewRawCandidate(context.Background(), before, nil,
		lifecycle.Live{BoundHTTPAddrs: []string{"127.0.0.1:8443"}}, "base", []byte(afterRaw))
	if err != nil {
		t.Fatal(err)
	}
	if !assessment.Valid || assessment.Lifecycle.CanApplyHot || !assessment.Lifecycle.CanStageRestart {
		t.Fatalf("credential rotation must be staged: valid=%v lifecycle=%+v errors=%+v",
			assessment.Valid, assessment.Lifecycle, assessment.ValidationErrors)
	}
	if !diffHas(assessment.Diff, "Change DNS-01 provider settings or credential reference") {
		t.Fatalf("credential rotation missing from diff: %+v", assessment.Diff)
	}
	preview, err := json.Marshal(struct {
		Diff      ConfigDiff
		Lint      []config.Diagnostic
		Lifecycle lifecycle.Result
		Errors    []validationError
	}{assessment.Diff, assessment.Lint, assessment.Lifecycle, assessment.ValidationErrors})
	if err != nil {
		t.Fatal(err)
	}
	assertDNSPreviewSecretsAbsent(t, string(preview), oldKey, newKey)

	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:    func() (*config.Config, error) { return before, nil },
		ReadConfigRaw: func() ([]byte, error) { return []byte(raw), nil },
	})
	batch, err := json.Marshal(patchApplyRequest{Ops: []patchRequest{{
		Op: "route_set_target", Listen: "127.0.0.1:8443",
		ServerNames: []string{"example.test", "*.example.test"},
		MatchType:   "prefix", Path: "/", Target: "http://127.0.0.1:9001",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(batch)))
	if rr.Code != http.StatusOK {
		t.Fatalf("typed preview: %d %s", rr.Code, rr.Body.String())
	}
	assertDNSPreviewSecretsAbsent(t, rr.Body.String(), oldKey, newKey)

	after, err := config.Parse([]byte(afterRaw))
	if err != nil {
		t.Fatal(err)
	}
	s.deps.LoadConfig = func() (*config.Config, error) { return after, nil }
	s.deps.ReadConfigRaw = func() ([]byte, error) { return []byte(afterRaw), nil }
	dir := t.TempDir()
	s.hist = newHistory(dir, 50)
	id := writeHistorySnapshot(t, dir, raw)
	history := getV1(t, s, "/api/v1/config/history/"+id+"/diff", "")
	if history.Code != http.StatusOK || !strings.Contains(history.Body.String(), "DNS-01 provider") {
		t.Fatalf("history must report provider rotation: %d %s", history.Code, history.Body.String())
	}
	assertDNSPreviewSecretsAbsent(t, history.Body.String(), oldKey, newKey)
	export := getV1(t, s, "/api/v1/config/export", "")
	if export.Code != http.StatusOK {
		t.Fatalf("config export: %d %s", export.Code, export.Body.String())
	}
	assertDNSPreviewSecretsAbsent(t, export.Body.String(), oldKey, newKey)
	if orders.Load() != 0 || updates.Load() != 0 {
		t.Fatalf("preview initiated CA requests (%d) or DNS connections (%d)", orders.Load(), updates.Load())
	}
}

func assertDNSPreviewSecretsAbsent(t *testing.T, body string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Fatal("DNS-01 credential escaped into an operator response")
		}
	}
}
