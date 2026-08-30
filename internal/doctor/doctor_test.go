// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package doctor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/diagnostics"
)

func TestSafeConfigMetadata(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Global: config.GlobalConfig{ConfigAuthority: "managed"},
		Admin: config.AdminConfig{
			Enabled: true,
			Listen:  "127.0.0.1:2019",
			Token:   "must-not-appear",
			RBAC: config.AdminRBACConfig{
				Enabled:    true,
				Principals: []config.AdminPrincipal{{Name: "operator", Token: "also-secret"}},
			},
		},
		Servers: []config.ServerConfig{{
			Listen:    "127.0.0.1:8080",
			Locations: []config.LocationConfig{{}, {}},
		}},
		Upstreams: []config.UpstreamConfig{{Servers: []config.UpstreamServer{{Address: "127.0.0.1:1"}, {Address: "127.0.0.1:2"}}}},
		Streams:   []config.StreamServer{{Listen: "127.0.0.1:9000", Protocol: "tcp"}},
		Plugins:   map[string]config.PluginConfig{"example": {}},
	}
	caps := map[string]bool{"grpc": true}
	got := SafeConfigMetadata(cfg, caps)
	caps["grpc"] = false
	if got.Authority != "managed" || !got.AdminEnabled || !got.AdminAuthenticated || !got.AdminRBACEnabled {
		t.Fatalf("unexpected security metadata: %#v", got)
	}
	if got.Servers != 1 || got.Listeners != 3 || got.Routes != 2 || got.Upstreams != 1 || got.Backends != 2 || got.Streams != 1 || got.Plugins != 1 {
		t.Fatalf("unexpected counts: %#v", got)
	}
	if !got.Capabilities["grpc"] {
		t.Fatal("capabilities were not cloned")
	}
	if got := SafeConfigMetadata(nil, nil); got.Authority != "" || got.Capabilities != nil {
		t.Fatalf("nil metadata = %#v", got)
	}
	if got := SafeConfigMetadata(&config.Config{}, nil); got.Authority != "file_owned" {
		t.Fatalf("default authority = %q", got.Authority)
	}
}

func TestRunValidConfigIsNetworkFreeByDefault(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, config.ServeDir(t.TempDir(), "127.0.0.1:8080"))
	report := Run(context.Background(), Options{
		ConfigPath:   path,
		Product:      "Jul.IA",
		Version:      "test",
		Capabilities: map[string]bool{"console": true},
	})
	if report.SchemaVersion != 1 || report.Scope != "local" || report.Source != filepath.Base(path) {
		t.Fatalf("unexpected report envelope: %#v", report)
	}
	if report.Summary.Errors != 0 {
		t.Fatalf("valid config reported errors: %#v", report)
	}
	if resultByCode(t, report, "RUNTIME_PREFLIGHT").Status != diagnostics.StatusSkipped || resultByCode(t, report, "LISTENER_BIND").Status != diagnostics.StatusSkipped {
		t.Fatalf("network checks were not skipped: %#v", report.Checks)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(filepath.Dir(path))) {
		t.Fatalf("report exposed an absolute temporary path: %s", encoded)
	}
}

func TestRunReportsStrictDecodeFailureAndSkipsDependents(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(path, []byte("[global]\nunknown_field = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := Run(context.Background(), Options{ConfigPath: path})
	if resultByCode(t, report, "CONFIG_PARSE").Status != diagnostics.StatusError {
		t.Fatalf("parse result = %#v", resultByCode(t, report, "CONFIG_PARSE"))
	}
	if resultByCode(t, report, "CONFIG_VALIDATE").Status != diagnostics.StatusSkipped {
		t.Fatalf("validate result = %#v", resultByCode(t, report, "CONFIG_VALIDATE"))
	}
	if report.Summary.Errors == 0 || ExitCode(report, false) != 1 {
		t.Fatalf("unexpected summary/exit: %#v", report.Summary)
	}
}

func TestConfigFileCheckModesAndTypes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode assertions are not portable to Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "server.toml")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &session{options: Options{ConfigPath: path}}
	if result := s.configFileCheck(context.Background()); result.Status != diagnostics.StatusWarning {
		t.Fatalf("open mode result = %#v", result)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if result := s.configFileCheck(context.Background()); result.Status != diagnostics.StatusPass {
		t.Fatalf("private mode result = %#v", result)
	}
	link := filepath.Join(dir, "link.toml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	s.options.ConfigPath = link
	if result := s.configFileCheck(context.Background()); result.Status != diagnostics.StatusWarning {
		t.Fatalf("symlink result = %#v", result)
	}
	s.options.ConfigPath = dir
	if result := s.configFileCheck(context.Background()); result.Status != diagnostics.StatusError {
		t.Fatalf("directory result = %#v", result)
	}
	s.options.ConfigPath = filepath.Join(dir, "missing")
	if result := s.configFileCheck(context.Background()); result.Status != diagnostics.StatusError {
		t.Fatalf("missing result = %#v", result)
	}
}

func TestConfiguredPathInspectionAndDeduplication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("private-mode assertion is POSIX-specific")
	}
	dir := t.TempDir()
	regular := filepath.Join(dir, "input")
	if err := os.WriteFile(regular, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	private := filepath.Join(dir, "key")
	if err := os.WriteFile(private, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		item   configuredPath
		state  string
		status diagnostics.Status
	}{
		{configuredPath{Kind: "input", Path: regular, Input: true}, "ok", diagnostics.StatusPass},
		{configuredPath{Kind: "key", Path: private, Input: true, Private: true}, "private_mode_too_open", diagnostics.StatusWarning},
		{configuredPath{Kind: "missing", Path: filepath.Join(dir, "missing"), Input: true}, "missing", diagnostics.StatusError},
		{configuredPath{Kind: "output", Path: filepath.Join(dir, "future.log")}, "not_created_parent_exists", diagnostics.StatusPass},
		{configuredPath{Kind: "dir", Path: regular, WantDir: true}, "wrong_type", diagnostics.StatusError},
	}
	for _, tc := range cases {
		state, status := inspectConfiguredPath(tc.item)
		if state != tc.state || status != tc.status {
			t.Errorf("inspect %#v = %q/%q, want %q/%q", tc.item, state, status, tc.state, tc.status)
		}
	}
	input := []configuredPath{{Kind: "same", Path: regular, Input: true}, {Kind: "same", Path: regular, Input: true}, {Kind: "empty"}}
	if got := dedupeConfiguredPaths(input); len(got) != 1 {
		t.Fatalf("deduped paths = %#v", got)
	}
}

func TestCollectConfiguredPathsDoesNotExposeValuesInResult(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	secretPath := filepath.Join(dir, "private-name.key")
	cfg := &config.Config{
		Admin: config.AdminConfig{HistoryDir: filepath.Join(dir, "history")},
		Servers: []config.ServerConfig{{TLS: &config.TLSConfig{Cert: filepath.Join(dir, "cert.pem"), Key: secretPath}}},
		Plugins: map[string]config.PluginConfig{"p": {Path: filepath.Join(dir, "module.wasm")}},
	}
	s := &session{cfg: cfg}
	result := s.configuredPathsCheck(context.Background())
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secretPath)) || bytes.Contains(encoded, []byte(dir)) {
		t.Fatalf("path value leaked: %s", encoded)
	}
	if len(collectConfiguredPaths(cfg)) < 3 {
		t.Fatalf("expected configured paths, got %#v", collectConfiguredPaths(cfg))
	}
}

func TestTLSCertificateChecks(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	validCert, validKey := writeCertificate(t, dir, "valid", time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	soonCert, soonKey := writeCertificate(t, dir, "soon", time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	expiredCert, expiredKey := writeCertificate(t, dir, "expired", time.Now().Add(-48*time.Hour), time.Now().Add(-24*time.Hour))

	result := (&session{cfg: &config.Config{Servers: []config.ServerConfig{{TLS: &config.TLSConfig{Cert: validCert, Key: validKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusPass {
		t.Fatalf("valid pair result = %#v", result)
	}
	result = (&session{cfg: &config.Config{Servers: []config.ServerConfig{{TLS: &config.TLSConfig{Cert: soonCert, Key: soonKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusWarning {
		t.Fatalf("soon pair result = %#v", result)
	}
	result = (&session{cfg: &config.Config{Servers: []config.ServerConfig{{TLS: &config.TLSConfig{Cert: expiredCert, Key: expiredKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusError {
		t.Fatalf("expired pair result = %#v", result)
	}
	result = (&session{cfg: &config.Config{Servers: []config.ServerConfig{{TLS: &config.TLSConfig{Cert: validCert, Key: expiredKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusError {
		t.Fatalf("mismatched pair result = %#v", result)
	}
	result = (&session{cfg: &config.Config{}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusSkipped {
		t.Fatalf("empty pair result = %#v", result)
	}
}

func TestAdminSecurityMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  config.AdminConfig
		want diagnostics.Status
	}{
		{"disabled", config.AdminConfig{}, diagnostics.StatusPass},
		{"loopback-auth", config.AdminConfig{Enabled: true, Listen: "127.0.0.1:2019", Token: "secret"}, diagnostics.StatusPass},
		{"loopback-open", config.AdminConfig{Enabled: true, Listen: "127.0.0.1:2019"}, diagnostics.StatusWarning},
		{"remote-auth", config.AdminConfig{Enabled: true, Listen: "0.0.0.0:2019", Token: "secret"}, diagnostics.StatusWarning},
		{"remote-open", config.AdminConfig{Enabled: true, Listen: "0.0.0.0:2019"}, diagnostics.StatusError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := (&session{cfg: &config.Config{Admin: tc.cfg}}).adminSecurityCheck(context.Background())
			if result.Status != tc.want {
				t.Fatalf("status = %q, result=%#v", result.Status, result)
			}
			if strings.Contains(result.Message, "secret") || strings.Contains(result.Remediation, "secret") {
				t.Fatalf("credential leaked: %#v", result)
			}
		})
	}
	if !isLoopbackListen("[::1]:2019") || !isLoopbackListen("localhost:2019") || isLoopbackListen(":2019") || isLoopbackListen("bad") {
		t.Fatal("loopback classification mismatch")
	}
}

func TestConfiguredListenersAndBindProbe(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Servers: []config.ServerConfig{
			{Listen: "127.0.0.1:0", HTTP3: &config.HTTP3Config{Enabled: true}},
			{Listen: "127.0.0.1:0"},
		},
		Streams: []config.StreamServer{{Listen: "127.0.0.1:0", Protocol: "udp"}},
		Admin:   config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"},
	}
	listeners := configuredListeners(cfg)
	if len(listeners) != 2 {
		t.Fatalf("listeners = %#v", listeners)
	}
	for _, listener := range listeners {
		if err := probeListener(context.Background(), listener.Network, listener.Address); err != nil {
			t.Fatalf("probe %s failed: %v", listener.Network, err)
		}
	}
	s := &session{cfg: cfg, options: Options{CheckNetwork: true}}
	if result := s.listenerBindCheck(context.Background()); result.Status != diagnostics.StatusPass {
		t.Fatalf("listener result = %#v", result)
	}

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	s.cfg = &config.Config{Servers: []config.ServerConfig{{Listen: occupied.Addr().String()}}}
	if result := s.listenerBindCheck(context.Background()); result.Status != diagnostics.StatusError {
		t.Fatalf("occupied result = %#v", result)
	}
}

func TestRuntimeChecksAndPrerequisites(t *testing.T) {
	t.Parallel()
	s := &session{options: Options{Product: "Jul.IA", Version: "test", Capabilities: map[string]bool{"grpc": true}}}
	if result := s.topologyCheck(context.Background()); result.Status != diagnostics.StatusSkipped {
		t.Fatalf("topology prerequisite = %#v", result)
	}
	if result := s.runtimePreflightCheck(context.Background()); result.Status != diagnostics.StatusSkipped {
		t.Fatalf("preflight default = %#v", result)
	}
	if result := s.listenerBindCheck(context.Background()); result.Status != diagnostics.StatusSkipped {
		t.Fatalf("listener default = %#v", result)
	}
	if result := s.systemRuntimeCheck(context.Background()); result.Status != diagnostics.StatusPass {
		t.Fatalf("system result = %#v", result)
	}
}

func TestRenderAndExitCode(t *testing.T) {
	t.Parallel()
	report := diagnostics.Report{
		SchemaVersion: 1,
		Summary: diagnostics.Summary{Status: diagnostics.StatusWarning, Passed: 1, Warnings: 1},
		Checks: []diagnostics.Result{
			{Code: "OK", Status: diagnostics.StatusPass, Message: "fine"},
			{Code: "WARN", Status: diagnostics.StatusWarning, Message: "attention", Remediation: "fix it"},
		},
	}
	var text bytes.Buffer
	if err := RenderText(&text, report); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text.String(), "jul doctor: warning") || !strings.Contains(text.String(), "remediation: fix it") {
		t.Fatalf("unexpected text: %s", text.String())
	}
	var encoded bytes.Buffer
	if err := WriteJSON(&encoded, report); err != nil {
		t.Fatal(err)
	}
	var decoded diagnostics.Report
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil || decoded.SchemaVersion != 1 {
		t.Fatalf("json round trip failed: %v %#v", err, decoded)
	}
	if ExitCode(report, false) != 0 || ExitCode(report, true) != 2 {
		t.Fatalf("warning exit codes differ")
	}
	report.Summary.Errors = 1
	if ExitCode(report, false) != 1 {
		t.Fatalf("error exit code differs")
	}
}

func TestFlattenErrorsAndHelpers(t *testing.T) {
	t.Parallel()
	joined := errors.Join(errors.New("one"), errors.Join(errors.New("two"), errors.New("three")))
	if got := flattenErrors(joined); len(got) != 3 {
		t.Fatalf("flattened = %#v", got)
	}
	if got := flattenErrors(nil); got != nil {
		t.Fatalf("nil flattened = %#v", got)
	}
	if result := errorResult("failed", errors.New("token=secret"), "retry"); result.Status != diagnostics.StatusError {
		t.Fatalf("error result = %#v", result)
	}
	if result := prerequisiteSkipped("missing", "fix"); result.Status != diagnostics.StatusSkipped {
		t.Fatalf("skipped result = %#v", result)
	}
}

func writeConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	data, err := config.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func resultByCode(t *testing.T, report diagnostics.Report, code string) diagnostics.Result {
	t.Helper()
	for _, result := range report.Checks {
		if result.Code == code {
			return result
		}
	}
	t.Fatalf("result %s not found in %#v", code, report.Checks)
	return diagnostics.Result{}
}

func writeCertificate(t *testing.T, dir, name string, notBefore, notAfter time.Time) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "doctor.test"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"doctor.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, name+".crt")
	keyPath := filepath.Join(dir, name+".key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
