// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package doctor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/diagnostics"
)

func TestAdminHasUsableCredential(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		admin config.AdminConfig
		want  bool
	}{
		{"legacy token", config.AdminConfig{Token: "secret"}, true},
		{"active principal", config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{{Token: "secret", ExpiresAt: now.Add(time.Hour)}}}}, true},
		{"disabled principal", config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{{Token: "secret", Disabled: true}}}}, false},
		{"expired principal", config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{{Token: "secret", ExpiresAt: now.Add(-time.Second)}}}}, false},
		{"blank token", config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{{Token: "   "}}}}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := adminHasUsableCredential(test.admin, now); got != test.want {
				t.Fatalf("adminHasUsableCredential = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRemoteAdminWithOnlyDisabledOrExpiredPrincipalsFails(t *testing.T) {
	t.Parallel()
	for _, principal := range []config.AdminPrincipal{
		{Token: "secret", Disabled: true},
		{Token: "secret", ExpiresAt: time.Now().Add(-time.Hour)},
	} {
		cfg := &config.Config{Admin: config.AdminConfig{
			Enabled: true,
			Listen:  "0.0.0.0:2019",
			RBAC:    config.AdminRBACConfig{Enabled: true, Principals: []config.AdminPrincipal{principal}},
		}}
		if result := (&session{cfg: cfg}).adminSecurityCheck(context.Background()); result.Status != diagnostics.StatusError {
			t.Fatalf("admin result = %#v", result)
		}
	}
}

func TestConfiguredPathsIncludeStaticRoots(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing-root")
	cfg := &config.Config{Servers: []config.ServerConfig{{Locations: []config.LocationConfig{{Root: missing}}}}}
	if result := (&session{cfg: cfg}).configuredPathsCheck(context.Background()); result.Status != diagnostics.StatusError {
		t.Fatalf("missing static root result = %#v", result)
	}
	if err := os.Mkdir(missing, 0o700); err != nil {
		t.Fatal(err)
	}
	if result := (&session{cfg: cfg}).configuredPathsCheck(context.Background()); result.Status != diagnostics.StatusPass {
		t.Fatalf("existing static root result = %#v", result)
	}
}

func TestTLSCertificateValidityAndConfiguredNameCoverage(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	futureCert, futureKey := writeCertificate(t, directory, "future", time.Now().Add(time.Hour), time.Now().Add(90*24*time.Hour))
	result := (&session{cfg: &config.Config{Servers: []config.ServerConfig{{ServerNames: []string{"doctor.test"}, TLS: &config.TLSConfig{Cert: futureCert, Key: futureKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusError || result.Evidence["not_yet_valid"] != 1 {
		t.Fatalf("future certificate result = %#v", result)
	}

	validCert, validKey := writeCertificate(t, directory, "covered", time.Now().Add(-time.Hour), time.Now().Add(90*24*time.Hour))
	result = (&session{cfg: &config.Config{Servers: []config.ServerConfig{{ServerNames: []string{"doctor.test"}, TLS: &config.TLSConfig{Cert: validCert, Key: validKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusPass || result.Evidence["hostnames_checked"] != 1 {
		t.Fatalf("covered certificate result = %#v", result)
	}
	result = (&session{cfg: &config.Config{Servers: []config.ServerConfig{{ServerNames: []string{"other.test"}, TLS: &config.TLSConfig{Cert: validCert, Key: validKey}}}}}).tlsCertificatesCheck(context.Background())
	if result.Status != diagnostics.StatusError || result.Evidence["hostname_mismatches"] != 1 {
		t.Fatalf("hostname mismatch result = %#v", result)
	}
}

func TestCertificatePairsMergeConfiguredNamesDeterministically(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Servers: []config.ServerConfig{
		{ServerNames: []string{"b.example.test", "a.example.test"}, TLS: &config.TLSConfig{Cert: "shared.crt", Key: "shared.key"}},
		{ServerNames: []string{"a.example.test", "*.example.test"}, TLS: &config.TLSConfig{Cert: "shared.crt", Key: "shared.key"}},
	}}
	pairs := collectCertificatePairs(cfg)
	if len(pairs) != 1 {
		t.Fatalf("certificate pairs = %#v", pairs)
	}
	if got, want := strings.Join(pairs[0].ServerNames, ","), "*.example.test,a.example.test,b.example.test"; got != want {
		t.Fatalf("merged server names = %q, want %q", got, want)
	}
}

func TestPrivateModeGuidanceIsPlatformAware(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "private.key")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	state, status := inspectConfiguredPath(configuredPath{Kind: "key", Path: path, Input: true, Private: true})
	if runtime.GOOS == "windows" {
		if state != "ok" || status != diagnostics.StatusPass {
			t.Fatalf("Windows mode guidance = %q/%q", state, status)
		}
		return
	}
	if state != "private_mode_too_open" || status != diagnostics.StatusWarning {
		t.Fatalf("POSIX mode guidance = %q/%q", state, status)
	}
}

func TestSystemRuntimeIncludesBuildIdentity(t *testing.T) {
	t.Parallel()
	result := (&session{options: Options{Product: "Jul.IA", Version: "test", Commit: "abcdef", BuildProfile: "full"}}).systemRuntimeCheck(context.Background())
	if result.Evidence["commit"] != "abcdef" || result.Evidence["build_profile"] != "full" {
		t.Fatalf("runtime identity = %#v", result.Evidence)
	}
}
