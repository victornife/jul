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

	"jul/internal/config"
	"jul/internal/diagnostics"
	"jul/internal/supportbundle"
)

func TestDispatchDiagnosticsSubcommand(t *testing.T) {
	if handled, code := dispatchDiagnosticsSubcommand(nil); handled || code != 0 {
		t.Fatalf("empty dispatch = %v, %d", handled, code)
	}
	if handled, code := dispatchDiagnosticsSubcommand([]string{"unknown"}); handled || code != 0 {
		t.Fatalf("unknown dispatch = %v, %d", handled, code)
	}
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		if handled, code := dispatchDiagnosticsSubcommand([]string{"doctor", "-unknown"}); !handled || code != 2 {
			t.Fatalf("doctor dispatch = %v, %d", handled, code)
		}
		if handled, code := dispatchDiagnosticsSubcommand([]string{"support-bundle", "-unknown"}); !handled || code != 2 {
			t.Fatalf("bundle dispatch = %v, %d", handled, code)
		}
	})
}

func TestCmdDoctorJSONAndValidation(t *testing.T) {
	configPath := writeCLIConfig(t)
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdDoctor([]string{"-config", configPath, "-json"})
		if code != 0 {
			t.Fatalf("doctor exit = %d, stderr=%s", code, errOut.String())
		}
		var report diagnostics.Report
		if err := json.Unmarshal(out.Bytes(), &report); err != nil {
			t.Fatalf("decode doctor JSON: %v\n%s", err, out.String())
		}
		if report.SchemaVersion != 1 || report.Scope != "local" || len(report.Checks) == 0 {
			t.Fatalf("doctor report = %#v", report)
		}
		if strings.Contains(out.String(), filepath.Dir(configPath)) {
			t.Fatalf("doctor output exposed absolute config directory: %s", out.String())
		}
	})

	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdDoctor([]string{"-timeout", "0s"}); code != 2 {
			t.Fatalf("zero timeout exit = %d", code)
		}
		if code := cmdDoctor([]string{"extra"}); code != 2 {
			t.Fatalf("extra argument exit = %d", code)
		}
		if code := cmdDoctor([]string{"-config", filepath.Join(t.TempDir(), "missing.toml")}); code != 1 {
			t.Fatalf("missing config exit = %d", code)
		}
	})
}

func TestCmdSupportBundleJSONAndPartialFailureExit(t *testing.T) {
	configPath := writeCLIConfig(t)
	directory := t.TempDir()
	output := filepath.Join(directory, "support.tar.gz")
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdSupportBundle([]string{"-config", configPath, "-output", output, "-json"})
		if code != 0 {
			t.Fatalf("support-bundle exit = %d, stderr=%s, stdout=%s", code, errOut.String(), out.String())
		}
		var result supportbundle.FileResult
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("decode bundle JSON: %v\n%s", err, out.String())
		}
		if result.Manifest.FormatVersion != supportbundle.FormatVersion || result.CompressedBytes <= 0 {
			t.Fatalf("bundle result = %#v", result)
		}
		info, err := os.Stat(output)
		if err != nil || info.Size() <= 0 {
			t.Fatalf("bundle file = %#v, %v", info, err)
		}
	})

	missingOutput := filepath.Join(directory, "partial.tar.gz")
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		code := cmdSupportBundle([]string{"-config", filepath.Join(directory, "missing.toml"), "-output", missingOutput})
		if code != 1 {
			t.Fatalf("partial bundle exit = %d, stderr=%s, stdout=%s", code, errOut.String(), out.String())
		}
		if _, err := os.Stat(missingOutput); err != nil {
			t.Fatalf("partial bundle was not produced: %v", err)
		}
		if !strings.Contains(out.String(), "support bundle written") {
			t.Fatalf("partial bundle location missing: %s", out.String())
		}
	})
}

func TestCmdSupportBundleUsageAndExistingOutput(t *testing.T) {
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdSupportBundle([]string{"-timeout", "0s"}); code != 2 {
			t.Fatalf("invalid limit exit = %d", code)
		}
		if code := cmdSupportBundle([]string{"extra"}); code != 2 {
			t.Fatalf("extra argument exit = %d", code)
		}
	})
	configPath := writeCLIConfig(t)
	output := filepath.Join(t.TempDir(), "exists.tar.gz")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdSupportBundle([]string{"-config", configPath, "-output", output}); code != 1 {
			t.Fatalf("existing output exit = %d", code)
		}
		if !strings.Contains(errOut.String(), "already exists") {
			t.Fatalf("existing output error = %s", errOut.String())
		}
	})
}

func TestDiagnosticCapabilitiesBuildProfileAndExtendedUsage(t *testing.T) {
	capabilities := diagnosticCapabilities()
	for _, key := range []string{"waf", "stream_proxy", "wasm_plugins", "acme", "grpc", "http3", "otel", "console", "brotli", "zstd", "importer", "consul", "kubernetes"} {
		if _, ok := capabilities[key]; !ok {
			t.Errorf("capability %q missing", key)
		}
	}
	if diagnosticBuildProfile(map[string]bool{"grpc": true}) != "custom" || diagnosticBuildProfile(map[string]bool{"grpc": false}) != "lean" {
		t.Fatal("build-profile classification mismatch")
	}
	withCLIOutput(t, func(out, errOut *bytes.Buffer) {
		extendedUsage()
		if !strings.Contains(errOut.String(), "jul doctor") || !strings.Contains(errOut.String(), "jul support-bundle") {
			t.Fatalf("extended usage missing diagnostics: %s", errOut.String())
		}
	})
}

func writeCLIConfig(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "www")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := config.Marshal(config.ServeDir(root, "127.0.0.1:8080"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func withCLIOutput(t *testing.T, fn func(*bytes.Buffer, *bytes.Buffer)) {
	t.Helper()
	originalStdout, originalStderr := stdout, stderr
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	stdout, stderr = out, errOut
	defer func() {
		stdout, stderr = originalStdout, originalStderr
	}()
	fn(out, errOut)
}
