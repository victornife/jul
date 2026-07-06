// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/redact"
)

func TestExpandSecretsEnvAndFile(t *testing.T) {
	t.Setenv("JUL_TEST_TOKEN", "s3cr3t-token-value")
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "k8s-token")
	if err := os.WriteFile(secretFile, []byte("file-secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := &Config{
		Admin: AdminConfig{Enabled: true, Token: "${env:JUL_TEST_TOKEN}"},
		Servers: []ServerConfig{{
			Listen: "127.0.0.1:8080",
			Locations: []LocationConfig{{
				Headers: map[string]string{"X-Api-Key": "Bearer ${env:JUL_TEST_TOKEN}"},
			}},
		}},
		Upstreams: []UpstreamConfig{{
			Name: "k8s",
			Discovery: &DiscoveryConfig{
				Type:       "kubernetes",
				Kubernetes: &KubernetesDiscovery{Token: "${file:" + secretFile + "}"},
			},
		}},
	}

	if err := ExpandSecrets(c); err != nil {
		t.Fatalf("ExpandSecrets: %v", err)
	}
	if c.Admin.Token != "s3cr3t-token-value" {
		t.Errorf("admin token = %q, want resolved env value", c.Admin.Token)
	}
	if got := c.Servers[0].Locations[0].Headers["X-Api-Key"]; got != "Bearer s3cr3t-token-value" {
		t.Errorf("header = %q, want embedded env value", got)
	}
	if got := c.Upstreams[0].Discovery.Kubernetes.Token; got != "file-secret-value" {
		t.Errorf("k8s token = %q, want trimmed file value", got)
	}
	// Resolved values must be registered for redaction.
	if masked := redact.Apply("token=s3cr3t-token-value end"); !strings.Contains(masked, redact.Mask) {
		t.Errorf("resolved env secret not redacted: %q", masked)
	}
	if masked := redact.Apply("k8s=file-secret-value end"); !strings.Contains(masked, redact.Mask) {
		t.Errorf("resolved file secret not redacted: %q", masked)
	}
}

func TestExpandSecretsErrors(t *testing.T) {
	cases := map[string]*Config{
		"missing env":    {Admin: AdminConfig{Token: "${env:JUL_DOES_NOT_EXIST_XYZ}"}},
		"missing file":   {Admin: AdminConfig{Token: "${file:/no/such/secret/file}"}},
		"unknown scheme": {Admin: AdminConfig{Token: "${vault:secret/data}"}},
		"empty env":      {Admin: AdminConfig{Token: "${env:}"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ExpandSecrets(c); err == nil {
				t.Fatalf("expected error for %s", name)
			}
		})
	}
}

func TestExpandSecretsAppliesRedactFloor(t *testing.T) {
	// The redact registry is a process-global; restore the default floor and use
	// distinctive short values that cannot collide with other tests' Apply input.
	defer redact.SetMinLen(redact.DefaultMinLen)
	t.Setenv("JUL_TEST_SHORT", "Xq7") // 3 chars, below the default floor of 4

	// Default floor (4): a 3-char resolved secret is not registered for masking.
	c := &Config{Admin: AdminConfig{Token: "${env:JUL_TEST_SHORT}"}}
	if err := ExpandSecrets(c); err != nil {
		t.Fatalf("ExpandSecrets: %v", err)
	}
	if masked := redact.Apply("v=Xq7 w"); strings.Contains(masked, redact.Mask) {
		t.Errorf("short secret masked at the default floor: %q", masked)
	}

	// Lowering redact_min_secret_length registers and masks the short secret.
	c2 := &Config{
		Global: GlobalConfig{RedactMinSecretLength: 3},
		Admin:  AdminConfig{Token: "${env:JUL_TEST_SHORT}"},
	}
	if err := ExpandSecrets(c2); err != nil {
		t.Fatalf("ExpandSecrets: %v", err)
	}
	if masked := redact.Apply("v=Xq7 w"); !strings.Contains(masked, redact.Mask) {
		t.Errorf("short secret not masked after lowering the floor: %q", masked)
	}
}

func TestExpandSecretsNoRefIsNoop(t *testing.T) {
	c := &Config{Admin: AdminConfig{Token: "literal-token"}}
	if err := ExpandSecrets(c); err != nil {
		t.Fatalf("ExpandSecrets: %v", err)
	}
	if c.Admin.Token != "literal-token" {
		t.Errorf("token changed: %q", c.Admin.Token)
	}
}

func TestCountSecretRefs(t *testing.T) {
	c := &Config{
		Admin: AdminConfig{Token: "${env:A}"},
		Servers: []ServerConfig{{
			Locations: []LocationConfig{{
				Headers: map[string]string{"H": "${file:/x} and ${env:B}"},
			}},
		}},
	}
	if got := CountSecretRefs(c); got != 3 {
		t.Errorf("CountSecretRefs = %d, want 3", got)
	}
}

func TestContainsSecretRef(t *testing.T) {
	if !containsSecretRef("${env:X}") {
		t.Error("env ref not detected")
	}
	if containsSecretRef("literal") {
		t.Error("literal flagged as ref")
	}
	if containsSecretRef("${vault:x}") {
		t.Error("unknown scheme should not count as a supported ref")
	}
}
