// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/diagnostics"
	"jul/internal/supportbundle"
)

func TestDiagnosticBuildProfileRecognizesFull(t *testing.T) {
	t.Parallel()
	full := map[string]bool{}
	for _, key := range []string{"waf", "stream_proxy", "wasm_plugins", "acme", "grpc", "http3", "otel", "console", "brotli", "zstd", "importer", "consul", "kubernetes"} {
		full[key] = true
	}
	if got := diagnosticBuildProfile(full); got != "full" {
		t.Fatalf("full build profile = %q", got)
	}
}

func TestDiagnosticsCLIIncludesBuildIdentity(t *testing.T) {
	configPath := writeCLIConfig(t)
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdDoctor([]string{"-config", configPath, "-json"}); code != 0 {
			t.Fatalf("doctor exit = %d, stderr=%s", code, errOut.String())
		}
		var report diagnostics.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		for _, result := range report.Checks {
			if result.Code == "SYSTEM_RUNTIME" {
				if result.Evidence["commit"] == "" || result.Evidence["build_profile"] == "" {
					t.Fatalf("doctor build identity = %#v", result.Evidence)
				}
				return
			}
		}
		t.Fatal("SYSTEM_RUNTIME result missing")
	})

	output := filepath.Join(t.TempDir(), "support.tar.gz")
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdSupportBundle([]string{"-config", configPath, "-output", output, "-json"}); code != 0 {
			t.Fatalf("bundle exit = %d, stderr=%s", code, errOut.String())
		}
		var result supportbundle.FileResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Manifest.Commit == "" || result.Manifest.BuildProfile == "" {
			t.Fatalf("bundle build identity = %#v", result.Manifest)
		}
	})
}

func TestSupportBundleFatalErrorsHideAbsolutePaths(t *testing.T) {
	configPath := writeCLIConfig(t)
	directory := t.TempDir()
	parent := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdSupportBundle([]string{"-config", configPath, "-output", filepath.Join(parent, "bundle.tar.gz")})
		if code != 1 {
			t.Fatalf("bundle exit = %d, stdout=%s, stderr=%s", code, out.String(), errOut.String())
		}
		if strings.Contains(errOut.String(), directory) || !strings.Contains(errOut.String(), "[PATH REDACTED]") {
			t.Fatalf("fatal error path was not redacted: %s", errOut.String())
		}
	})
}
