// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/waf"
)

func TestResolveWAFScopeConfigPrecedenceAndAmbiguity(t *testing.T) {
	idApp2 := "rt-app2-api"
	idNamedSlash := "/api" // durable route_id on server 2 that matches server 1's path
	cfg := &config.Config{
		WAF: config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"app1.example.test"},
				Locations: []config.LocationConfig{
					{
						Match: config.MatchConfig{Type: "prefix", Path: "/api"},
						WAF:   &config.WAFConfig{Enabled: true, Mode: "detect", CRSEnabled: true},
					},
				},
			},
			{
				Listen:      ":8443",
				ServerNames: []string{"app2.example.test"},
				Locations: []config.LocationConfig{
					{
						RouteID: &idApp2,
						Match:   config.MatchConfig{Type: "prefix", Path: "/api"},
						WAF:     &config.WAFConfig{Enabled: true, Mode: "block", BlockStatus: 429, CRSEnabled: true},
					},
				},
			},
		},
	}

	// 1. Ambiguous path "/api" across two virtual hosts must be rejected.
	if _, _, err := resolveWAFScopeConfig(cfg, "/api"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous path error for /api, got %v", err)
	}

	// 2. Qualified <server_name><path> resolves unambiguously to app2.
	wcfgQual, labelQual, err := resolveWAFScopeConfig(cfg, "app2.example.test/api")
	if err != nil || wcfgQual.BlockStatus != 429 || labelQual != "rt-app2-api" {
		t.Fatalf("qualified scope = (%+v, %q, %v)", wcfgQual, labelQual, err)
	}

	// 3. Durable route_id has explicit precedence over path matching on an earlier server.
	cfgPrecedence := *cfg
	cfgPrecedence.Servers = append([]config.ServerConfig(nil), cfg.Servers...)
	cfgPrecedence.Servers[1].Locations = []config.LocationConfig{
		{
			RouteID: &idNamedSlash,
			Match:   config.MatchConfig{Type: "prefix", Path: "/v2"},
			WAF:     &config.WAFConfig{Enabled: true, Mode: "block", BlockStatus: 418, CRSEnabled: true},
		},
	}
	wcfgID, _, err := resolveWAFScopeConfig(&cfgPrecedence, "/api")
	if err != nil || wcfgID.BlockStatus != 418 {
		t.Fatalf("expected durable route_id precedence (418), got (%+v, %v)", wcfgID, err)
	}
}

func TestCLISubcommandWAF(t *testing.T) {
	var outBuf, errBuf bytes.Buffer
	origOut, origErr := stdout, stderr
	stdout, stderr = &outBuf, &errBuf
	defer func() {
		stdout, stderr = origOut, origErr
	}()

	reset := func() {
		outBuf.Reset()
		errBuf.Reset()
	}

	// 1. Missing or unknown subcommand returns 2.
	if handled, code := dispatchDiagnosticsSubcommand([]string{"waf"}); !handled || code != 2 {
		t.Fatalf("jul waf (no args) = (%v, %d), want (true, 2)", handled, code)
	}
	reset()
	if handled, code := dispatchDiagnosticsSubcommand([]string{"waf", "unknown"}); !handled || code != 2 {
		t.Fatalf("jul waf unknown = (%v, %d), want (true, 2)", handled, code)
	}
	reset()

	tmpDir := t.TempDir()
	ruleFile := filepath.Join(tmpDir, "custom.conf")
	initialRule := `SecRule REQUEST_HEADERS:X-Block-Probe "@streq 1" "id:107001,phase:1,deny,status:403,log"` + "\n"
	if err := os.WriteFile(ruleFile, []byte(initialRule), 0o600); err != nil {
		t.Fatalf("write ruleFile: %v", err)
	}
	cfgFile := filepath.Join(tmpDir, "jul.toml")
	cfgContent := `[waf]
enabled = true
mode = "block"
crs_enabled = true
directives_files = ["` + filepath.ToSlash(ruleFile) + `"]

[[servers]]
listen = "127.0.0.1:8080"
  [[servers.locations]]
  route_id = "main-route"
  match = { type = "prefix", path = "/" }
  root = "` + filepath.ToSlash(tmpDir) + `"
`
	if err := os.WriteFile(cfgFile, []byte(cfgContent), 0o600); err != nil {
		t.Fatalf("write cfgFile: %v", err)
	}

	if !waf.Compiled {
		// In a lean build, jul waf evaluate/manifest/verify-assets exit 1 with actionable -tags waf guidance.
		for _, sub := range [][]string{
			{"waf", "evaluate", "-config", cfgFile},
			{"waf", "manifest", "-config", cfgFile},
			{"waf", "verify-assets", "-config", cfgFile, "-manifest", "m.json"},
		} {
			reset()
			_, code := dispatchDiagnosticsSubcommand(sub)
			if code != 1 || !strings.Contains(errBuf.String(), "-tags waf") {
				t.Fatalf("lean %v = code %d, stderr=%q", sub, code, errBuf.String())
			}
		}
		return
	}

	// 2. Full build: jul waf evaluate (text and JSON, plus baseline comparison and route scope).
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "evaluate", "-config", cfgFile, "-baseline", cfgFile, "-scope", "main-route"}); code != 0 {
		t.Fatalf("jul waf evaluate text failed (code=%d): out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "10/10 passed") {
		t.Fatalf("unexpected evaluate output: %s", outBuf.String())
	}

	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "evaluate", "-config", cfgFile, "-json"}); code != 0 {
		t.Fatalf("jul waf evaluate -json failed (code=%d): err=%s", code, errBuf.String())
	}
	if !strings.Contains(outBuf.String(), `"pack_id": "jul-waf-default-v1"`) {
		t.Fatalf("unexpected evaluate JSON: %s", outBuf.String())
	}

	// 3. jul waf manifest and jul waf verify-assets.
	manifestFile := filepath.Join(tmpDir, "waf-manifest.json")
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "manifest", "-config", cfgFile, "-out", manifestFile}); code != 0 {
		t.Fatalf("jul waf manifest failed (code=%d): err=%s", code, errBuf.String())
	}

	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "verify-assets", "-config", cfgFile, "-manifest", manifestFile}); code != 0 {
		t.Fatalf("jul waf verify-assets failed (code=%d): out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	if !strings.Contains(outBuf.String(), "status=verified verified=true") {
		t.Fatalf("unexpected verify-assets output: %s", outBuf.String())
	}

	// Modify ruleFile in-place -> verify-assets returns exit code 1 with status=asset_changed.
	if err := os.WriteFile(ruleFile, []byte(initialRule+"# edited\n"), 0o600); err != nil {
		t.Fatalf("edit ruleFile: %v", err)
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "verify-assets", "-config", cfgFile, "-manifest", manifestFile, "-json"}); code != 1 {
		t.Fatalf("jul waf verify-assets after edit = %d, want 1", code)
	}
	if !strings.Contains(outBuf.String(), `"status": "asset_changed"`) {
		t.Fatalf("unexpected verify-assets JSON after edit: %s", outBuf.String())
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "verify-assets", "-config", cfgFile, "-manifest", manifestFile}); code != 1 {
		t.Fatalf("jul waf verify-assets text after edit = %d, want 1", code)
	}

	// Custom fixtures file and error branches.
	fixturesFile := filepath.Join("..", "..", "examples", "waf-policy", "fixtures.json")
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "evaluate", "-config", cfgFile, "-fixtures", fixturesFile}); code != 0 {
		t.Fatalf("jul waf evaluate -fixtures failed (code=%d): err=%s", code, errBuf.String())
	}
	for _, errArgs := range [][]string{
		{"waf", "evaluate", "extra-arg"},
		{"waf", "evaluate", "-config", filepath.Join(tmpDir, "missing.toml"), "-json"},
		{"waf", "evaluate", "-config", cfgFile, "-scope", "unknown-route"},
		{"waf", "evaluate", "-config", cfgFile, "-fixtures", filepath.Join(tmpDir, "missing.json")},
		{"waf", "manifest", "extra-arg"},
		{"waf", "manifest", "-config", filepath.Join(tmpDir, "missing.toml")},
		{"waf", "manifest", "-config", cfgFile, "-scope", "unknown-route"},
		{"waf", "verify-assets"},
		{"waf", "verify-assets", "-config", cfgFile, "-manifest", filepath.Join(tmpDir, "missing.json")},
	} {
		reset()
		if _, code := dispatchDiagnosticsSubcommand(errArgs); code == 0 {
			t.Fatalf("expected non-zero exit code for %v", errArgs)
		}
	}

	// 4. Item 3 Subcases A & B: ${file:...} and ${env:...} provider resolution in
	// evaluate -> manifest -> verify-assets -> posture, plus durable version-key
	// context binding and redaction of resolved secret values on CLI error paths.
	rulePathSecretFile := filepath.Join(tmpDir, "rule-path-ref.txt")
	if err := os.WriteFile(ruleFile, []byte(initialRule), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulePathSecretFile, []byte(filepath.ToSlash(ruleFile)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JUL_TEST_WAF_LISTEN", "127.0.0.1:8089")
	t.Setenv("JUL_TEST_WAF_CANARY_SECRET", "canary-cli-provider-secret-999")

	providerCfgFile := filepath.Join(tmpDir, "jul-provider.toml")
	providerCfgContent := `[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:JUL_TEST_WAF_CANARY_SECRET}"

[waf]
enabled = true
mode = "block"
crs_enabled = true
directives_files = ["${file:` + filepath.ToSlash(rulePathSecretFile) + `}"]

[[servers]]
listen = "${env:JUL_TEST_WAF_LISTEN}"
  [[servers.locations]]
  route_id = "provider-route"
  match = { type = "prefix", path = "/" }
  root = "` + filepath.ToSlash(tmpDir) + `"
`
	if err := os.WriteFile(providerCfgFile, []byte(providerCfgContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Provision a durable version key file and verify CLI evaluate + manifest use that exact context.
	resolvedKeyRoot := tmpDir
	if canonical, err := filepath.EvalSymlinks(tmpDir); err == nil {
		resolvedKeyRoot = canonical
	}
	keyDir := filepath.Join(resolvedKeyRoot, "private-key")
	keyFile := filepath.Join(keyDir, "effective-version.key")
	reset()
	if code := cmdVersionKey([]string{"init", "--file", keyFile}); code != 0 {
		t.Fatalf("version-key init failed (code=%d): %s", code, errBuf.String())
	}

	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{
		"waf", "evaluate",
		"-config", providerCfgFile,
		"-baseline", providerCfgFile,
		"-version-key-file", keyFile,
		"-json",
	}); code != 0 {
		t.Fatalf("provider jul waf evaluate failed (code=%d): err=%s", code, errBuf.String())
	}
	evalJSON := outBuf.String()
	if strings.Contains(evalJSON, "canary-cli-provider-secret-999") {
		t.Fatal("CLI evaluate output leaked resolved secret canary")
	}

	providerManifestFile := filepath.Join(tmpDir, "provider-manifest.json")
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{
		"waf", "manifest",
		"-config", providerCfgFile,
		"-version-key-file", keyFile,
		"-out", providerManifestFile,
		"-json",
	}); code != 0 {
		t.Fatalf("provider jul waf manifest failed (code=%d): err=%s", code, errBuf.String())
	}
	manifestObj, err := waf.ParsePolicyManifest(outBuf.Bytes())
	if err != nil {
		t.Fatalf("ParsePolicyManifest: %v", err)
	}
	evalRepObj, err := waf.ParseEvaluationReport([]byte(evalJSON))
	if err != nil {
		t.Fatalf("ParseEvaluationReport: %v", err)
	}
	if evalRepObj.Summary.PolicyDigest != manifestObj.PolicyDigest || evalRepObj.Summary.ExternalDigest != manifestObj.ExternalDigest {
		t.Fatalf("durable version-key context did not produce matching digests across evaluate (%q) and manifest (%q)",
			evalRepObj.Summary.PolicyDigest, manifestObj.PolicyDigest)
	}

	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{
		"waf", "verify-assets",
		"-config", providerCfgFile,
		"-manifest", providerManifestFile,
		"-version-key-file", keyFile,
	}); code != 0 {
		t.Fatalf("provider jul waf verify-assets failed (code=%d): err=%s", code, errBuf.String())
	}

	// Verify jul posture on providerCfgFile resolves ${env:...} and ${file:...}
	// without emitting a false certificates_literal_secret_in_config warning.
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"posture", "-config", providerCfgFile, "-json"}); code != 0 {
		t.Fatalf("provider jul posture -json failed (code=%d): err=%s", code, errBuf.String())
	}
	if strings.Contains(outBuf.String(), "certificates_literal_secret_in_config") || strings.Contains(outBuf.String(), "canary-cli-provider-secret-999") {
		t.Fatalf("provider jul posture emitted false literal secret or leaked canary: %s", outBuf.String())
	}

	// Verify an error on a provider-resolved config redacts the secret canary.
	reset()
	_, errCode := dispatchDiagnosticsSubcommand([]string{
		"waf", "evaluate",
		"-config", providerCfgFile,
		"-scope", "canary-cli-provider-secret-999",
	})
	if errCode == 0 || strings.Contains(errBuf.String(), "canary-cli-provider-secret-999") {
		t.Fatalf("expected redacted non-zero error, got code=%d stderr=%q", errCode, errBuf.String())
	}

	// 5. Verify jul posture CLI (both text and -json) and error branches.
	if err := os.WriteFile(ruleFile, []byte(initialRule+"# edited\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"posture", "-config", cfgFile}); code != 0 || !strings.Contains(outBuf.String(), "Gateway Security Posture") {
		t.Fatalf("jul posture text code = %d, out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"posture", "-config", cfgFile, "-json"}); code != 0 || !strings.Contains(outBuf.String(), `"overall_status"`) {
		t.Fatalf("jul posture -json code = %d, out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	for _, badPosture := range [][]string{
		{"posture", "extra-arg"},
		{"posture", "-config", filepath.Join(tmpDir, "missing.toml")},
	} {
		reset()
		if _, code := dispatchDiagnosticsSubcommand(badPosture); code == 0 {
			t.Fatalf("expected non-zero exit code for %v", badPosture)
		}
	}

	// 6. Verify jul waf events CLI against an httptest server.
	statusMode := 200
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if statusMode == 302 {
			http.Redirect(w, r, tsURLForRedirect, http.StatusFound)
			return
		}
		if statusMode != 200 {
			w.WriteHeader(statusMode)
			_, _ = w.Write([]byte(`{"error":"forbidden"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_version":"v1","collection_state":"active","retention":{"max_events":256,"max_event_bytes":4096,"ttl_seconds":900},"stats":{"retained_events":1,"recorded_total":1},"events":[{"event_id":"wev_0123456789abcdef0123456789abcdef","time":"2026-11-02T00:00:00Z","outcome":"interrupted_request","mode":"block","client_status":403,"origin_reached":false,"method":"POST","host":"example.test","path":"/login","route":{"route_id":"rt-1"},"total_matched_rules":1}]}`))
	}))
	defer ts.Close()
	tsURLForRedirect = ts.URL

	tokFile := filepath.Join(tmpDir, "events-token.txt")
	if err := os.WriteFile(tokFile, []byte("tok-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "events", "-url", ts.URL, "-token-file", tokFile}); code != 0 || !strings.Contains(outBuf.String(), "wev_0123456789abcdef0123456789abcdef") {
		t.Fatalf("jul waf events -token-file code = %d, out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "events", "-url", ts.URL, "-token", "tok-1", "-event-id", "wev_0123456789abcdef0123456789abcdef", "-outcome", "interrupted_request", "-rule-id", "942100", "-route-id", "rt-1", "-limit", "10"}); code != 0 || !strings.Contains(outBuf.String(), "wev_0123456789abcdef0123456789abcdef") {
		t.Fatalf("jul waf events text code = %d, out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "events", "-url", ts.URL, "-json"}); code != 0 || !strings.Contains(outBuf.String(), `"collection_state": "active"`) {
		t.Fatalf("jul waf events -json code = %d, out=%s err=%s", code, outBuf.String(), errBuf.String())
	}
	statusMode = 302
	reset()
	if _, code := dispatchDiagnosticsSubcommand([]string{"waf", "events", "-url", ts.URL}); code == 0 {
		t.Fatal("expected redirect response not to be followed and to return non-zero")
	}
	statusMode = 403
	for _, badEvents := range [][]string{
		{"waf", "events", "extra-arg"},
		{"waf", "events", "-url", "http://remote.example:9090"},
		{"waf", "events", "-url", ts.URL, "-token-file", filepath.Join(tmpDir, "missing-token.txt")},
		{"waf", "events", "-url", ts.URL},
		{"waf", "events", "-url", "http://127.0.0.1:1"},
	} {
		reset()
		if _, code := dispatchDiagnosticsSubcommand(badEvents); code == 0 {
			t.Fatalf("expected non-zero exit code for %v", badEvents)
		}
	}
}

var tsURLForRedirect string
