// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/diagnostics"
)

func TestDefaultCollectorsAreClosedAndOrdered(t *testing.T) {
	t.Parallel()
	collectors := DefaultCollectors()
	got := make([]string, len(collectors))
	for i, collector := range collectors {
		got[i] = collector.Name()
	}
	want := []string{"notice", "build", "configuration_metadata", "doctor", "access_log"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("collector registry = %v, want %v", got, want)
	}
}

func TestDefaultBundleContainsDoctorMetadataAndNoFixtureSecrets(t *testing.T) {
	directory := t.TempDir()
	configPath := writeSupportConfig(t, config.ServeDir(filepath.Join(directory, "www"), "127.0.0.1:8080"))
	secret := "bundle-fixture-secret"
	generator := NewGenerator(DefaultCollectors(), DefaultLimits(), 1)
	output := filepath.Join(directory, "support.tar.gz")
	result, err := generator.WriteFile(context.Background(), output, Snapshot{
		Product:      "Jul.IA",
		Version:      "test",
		Commit:       "abcdef",
		BuildProfile: "full",
		ConfigPath:   configPath,
		Capabilities: map[string]bool{"console": true},
		RedactValues: []string{secret},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	entries := extractArchive(t, archive)
	for _, expected := range []string{"manifest.json", "NOTICE.txt", "build/runtime.json", "configuration/metadata.json", "diagnostics/doctor.json", "diagnostics/doctor.txt"} {
		if _, ok := entries[expected]; !ok {
			t.Fatalf("archive missing %s: %v", expected, mapKeys(entries))
		}
	}
	if _, ok := entries["logs/access.log.tail"]; ok {
		t.Fatal("logs were collected without explicit opt-in")
	}
	if bytes.Contains(archive, []byte(secret)) {
		t.Fatal("fixture secret survived final archive bytes")
	}
	var report diagnostics.Report
	if err := json.Unmarshal(entries["diagnostics/doctor.json"], &report); err != nil {
		t.Fatal(err)
	}
	if report.SchemaVersion != 1 || report.Scope != "local" {
		t.Fatalf("doctor report = %#v", report)
	}
	if result.Manifest.FormatVersion != FormatVersion || result.Manifest.RedactionProfile != RedactionProfile {
		t.Fatalf("file result manifest = %#v", result.Manifest)
	}
	var metadata map[string]any
	if err := json.Unmarshal(entries["configuration/metadata.json"], &metadata); err != nil {
		t.Fatal(err)
	}
	if _, exists := metadata["token"]; exists {
		t.Fatalf("metadata contains token key: %#v", metadata)
	}
}

func TestAccessLogCollectorRequiresOptInConfiguredFileAndRedacts(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "access.log")
	secret := "log-fixture-secret"
	content := strings.Repeat("old line\n", 100) + "Authorization: Bearer " + secret + " password=" + secret + "\nlast safe line\n"
	if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ServeDir(filepath.Join(directory, "www"), "127.0.0.1:8080")
	cfg.Observability.AccessLog.Enabled = config.Bool(true)
	cfg.Observability.AccessLog.Sinks = []string{"file"}
	cfg.Observability.AccessLog.File = logPath
	configPath := writeSupportConfig(t, cfg)

	artifacts, err := collectAccessLog(context.Background(), Snapshot{ConfigPath: configPath})
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("non-opt-in collection = %#v, %v", artifacts, err)
	}
	artifacts, err = collectAccessLog(context.Background(), Snapshot{ConfigPath: configPath, IncludeLogs: true, LogTailBytes: 128})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].Path != "logs/access.log.tail" || !artifacts[0].Truncated {
		t.Fatalf("log artifact = %#v", artifacts)
	}

	generator := NewGenerator([]Collector{CollectorFunc{ID: "access_log", Fn: collectAccessLog}}, DefaultLimits(), 1)
	bundle, err := generator.Build(context.Background(), Snapshot{ConfigPath: configPath, IncludeLogs: true, LogTailBytes: 128, RedactValues: []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Artifacts) != 1 || bytes.Contains(bundle.Artifacts[0].Data, []byte(secret)) {
		t.Fatalf("redacted log artifact = %#v", bundle.Artifacts)
	}
	if !bytes.Contains(bundle.Artifacts[0].Data, []byte("last safe line")) {
		t.Fatalf("tail does not contain final line: %s", bundle.Artifacts[0].Data)
	}
}

func TestAccessLogCollectorRejectsSymlinkAndNonRegularFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not consistently permitted on Windows CI")
	}
	directory := t.TempDir()
	realLog := filepath.Join(directory, "real.log")
	if err := os.WriteFile(realLog, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.log")
	if err := os.Symlink(realLog, link); err != nil {
		t.Fatal(err)
	}
	cfg := config.ServeDir(filepath.Join(directory, "www"), "127.0.0.1:8080")
	cfg.Observability.AccessLog.Enabled = config.Bool(true)
	cfg.Observability.AccessLog.Sinks = []string{"file"}
	cfg.Observability.AccessLog.File = link
	configPath := writeSupportConfig(t, cfg)
	if _, err := collectAccessLog(context.Background(), Snapshot{ConfigPath: configPath, IncludeLogs: true}); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink error = %v", err)
	}

	cfg.Observability.AccessLog.File = directory
	configPath = writeSupportConfig(t, cfg)
	if _, err := collectAccessLog(context.Background(), Snapshot{ConfigPath: configPath, IncludeLogs: true}); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory error = %v", err)
	}
}

func TestTailRegularFileBoundsPartialLineAndCancellation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "tail.log")
	content := []byte("first long line\nsecond\nthird\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	data, truncated, err := tailRegularFile(context.Background(), path, int64(len(content)), 15)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || string(data) != "second\nthird\n" {
		t.Fatalf("tail = %q truncated=%v", data, truncated)
	}
	data, truncated, err = tailRegularFile(context.Background(), path, int64(len(content)), int64(len(content)+10))
	if err != nil || truncated || !bytes.Equal(data, content) {
		t.Fatalf("full tail = %q truncated=%v err=%v", data, truncated, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := tailRegularFile(ctx, path, int64(len(content)), int64(len(content))); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled tail error = %v", err)
	}
}

func TestCollectorHelpersAndBuildPayload(t *testing.T) {
	t.Parallel()
	if configPath(Snapshot{}) != "server.toml" || configPath(Snapshot{ConfigPath: "custom.toml"}) != "custom.toml" {
		t.Fatal("config path default mismatch")
	}
	if !containsString([]string{"stdout", "FILE"}, "file") || containsString(nil, "file") {
		t.Fatal("containsString mismatch")
	}
	input := map[string]bool{"grpc": true}
	clone := cloneCapabilities(input)
	input["grpc"] = false
	if !clone["grpc"] || cloneCapabilities(nil) != nil {
		t.Fatal("capability clone mismatch")
	}
	artifacts, err := collectBuild(context.Background(), Snapshot{Product: "Jul.IA", Capabilities: map[string]bool{"grpc": true}})
	if err != nil || len(artifacts) != 1 {
		t.Fatalf("build collector = %#v, %v", artifacts, err)
	}
	var payload map[string]any
	if err := json.Unmarshal(artifacts[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"go_version", "goos", "goarch", "goroutines", "alloc_bytes"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("build payload missing %s: %#v", key, payload)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectBuild(ctx, Snapshot{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build collector error = %v", err)
	}
}

func writeSupportConfig(t *testing.T, cfg *config.Config) string {
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

func mapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
