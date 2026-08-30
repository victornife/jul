// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDefaultLimitsAndCollectorFunc(t *testing.T) {
	t.Parallel()
	limits := DefaultLimits()
	if limits.TotalTimeout <= 0 || limits.PerCollectorTimeout <= 0 || limits.MaxArtifacts <= 0 || limits.MaxArtifactBytes <= 0 || limits.MaxUncompressedBytes <= 0 || limits.MaxCompressedBytes <= 0 {
		t.Fatalf("invalid defaults: %#v", limits)
	}
	collector := CollectorFunc{ID: "nil"}
	if collector.Name() != "nil" {
		t.Fatalf("collector name = %q", collector.Name())
	}
	if _, err := collector.Collect(context.Background(), Snapshot{}); !errors.Is(err, ErrCollectorUnimplemented) {
		t.Fatalf("nil collector error = %v", err)
	}
}

func TestGeneratorBuildSanitizesAndRecordsPartialFailures(t *testing.T) {
	t.Parallel()
	secret := "fixture-super-secret"
	collectors := []Collector{
		CollectorFunc{ID: "good", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
			return []Artifact{{
				Path:        "diagnostics/result.json",
				ContentType: "application/json",
				Sensitivity: "test",
				Data:        []byte(`{"token":"` + secret + `","message":"Authorization: Bearer ` + secret + `","safe":7}`),
			}}, nil
		}},
		CollectorFunc{ID: "failure", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
			return nil, errors.New("password=" + secret)
		}},
		CollectorFunc{ID: "panic", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
			panic("api_key=" + secret)
		}},
		CollectorFunc{ID: "empty", Fn: func(context.Context, Snapshot) ([]Artifact, error) { return nil, nil }},
		CollectorFunc{ID: "truncated", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
			return []Artifact{{Path: "logs/tail.txt", ContentType: "text/plain", Data: bytes.Repeat([]byte("x"), 256)}}, nil
		}},
	}
	limits := DefaultLimits()
	limits.MaxArtifactBytes = 128
	generator := NewGenerator(collectors, limits, 1)
	fixed := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	var calls int
	generator.now = func() time.Time {
		calls++
		if calls == 1 {
			return fixed
		}
		return fixed.Add(25 * time.Millisecond)
	}

	bundle, err := generator.Build(context.Background(), Snapshot{Product: "Jul.IA", Version: "test", RedactValues: []string{secret}})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.FormatVersion != FormatVersion || bundle.Manifest.CreatedAt != fixed || bundle.Manifest.DurationMillis != 25 {
		t.Fatalf("unexpected manifest timing/version: %#v", bundle.Manifest)
	}
	if len(bundle.Artifacts) != 2 || len(bundle.Manifest.Collectors) != len(collectors) {
		t.Fatalf("unexpected bundle inventory: artifacts=%d collectors=%#v", len(bundle.Artifacts), bundle.Manifest.Collectors)
	}
	statuses := map[string]CollectorStatus{}
	for _, record := range bundle.Manifest.Collectors {
		statuses[record.Name] = record.Status
	}
	if statuses["good"] != CollectorSuccess || statuses["failure"] != CollectorError || statuses["panic"] != CollectorError || statuses["empty"] != CollectorSkipped || statuses["truncated"] != CollectorTruncated {
		t.Fatalf("unexpected statuses: %#v", statuses)
	}
	if !bundle.Artifacts[1].Truncated {
		t.Fatalf("text artifact was not marked truncated: %#v", bundle.Artifacts[1])
	}
	all, err := json.Marshal(struct {
		Manifest  Manifest
		Artifacts []Artifact
	}{bundle.Manifest, bundle.Artifacts})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(all, []byte(secret)) {
		t.Fatalf("fixture secret survived bundle build: %s", all)
	}
	for _, record := range bundle.Manifest.Artifacts {
		var data []byte
		for _, artifact := range bundle.Artifacts {
			if artifact.Path == record.Path {
				data = artifact.Data
			}
		}
		digest := sha256.Sum256(data)
		if record.SHA256 != hex.EncodeToString(digest[:]) {
			t.Fatalf("checksum mismatch for %s", record.Path)
		}
	}
}

func TestGeneratorRejectsUnsafeDuplicateAndResourceExcess(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		artifacts []Artifact
		limits    Limits
		want      error
	}{
		{
			name:      "unsafe path",
			artifacts: []Artifact{{Path: "../secret", ContentType: "text/plain", Data: []byte("x")}},
			want:      ErrUnsafeArtifactPath,
		},
		{
			name: "duplicate path",
			artifacts: []Artifact{
				{Path: "same.txt", ContentType: "text/plain", Data: []byte("one")},
				{Path: "same.txt", ContentType: "text/plain", Data: []byte("two")},
			},
			want: ErrDuplicateArtifact,
		},
		{
			name:      "too many",
			artifacts: []Artifact{{Path: "one.txt", ContentType: "text/plain", Data: []byte("one")}, {Path: "two.txt", ContentType: "text/plain", Data: []byte("two")}},
			limits:    Limits{MaxArtifacts: 1},
			want:      ErrTooManyArtifacts,
		},
		{
			name:      "large json",
			artifacts: []Artifact{{Path: "large.json", ContentType: "application/json", Data: []byte(`{"value":"` + strings.Repeat("x", 200) + `"}`)}},
			limits:    Limits{MaxArtifactBytes: 64},
			want:      ErrArtifactTooLarge,
		},
		{
			name:      "large bundle",
			artifacts: []Artifact{{Path: "large.txt", ContentType: "text/plain", Data: bytes.Repeat([]byte("x"), 100)}},
			limits:    Limits{MaxArtifactBytes: 200, MaxUncompressedBytes: 50},
			want:      ErrBundleTooLarge,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			collector := CollectorFunc{ID: "test", Fn: func(context.Context, Snapshot) ([]Artifact, error) { return tc.artifacts, nil }}
			bundle, err := NewGenerator([]Collector{collector}, tc.limits, 1).Build(context.Background(), Snapshot{})
			if err != nil {
				t.Fatalf("Build returned fatal error: %v", err)
			}
			if len(bundle.Artifacts) != 0 || len(bundle.Manifest.Collectors) != 1 || bundle.Manifest.Collectors[0].Status != CollectorError {
				t.Fatalf("unexpected failed collector bundle: %#v", bundle)
			}
			if !strings.Contains(bundle.Manifest.Collectors[0].Error, tc.want.Error()) {
				t.Fatalf("collector error %q does not contain %q", bundle.Manifest.Collectors[0].Error, tc.want)
			}
		})
	}
}

func TestSafeArtifactPath(t *testing.T) {
	t.Parallel()
	valid := []string{"NOTICE.txt", "diagnostics/doctor.json", "a-b/c_d.txt"}
	for _, value := range valid {
		if got, err := safeArtifactPath(value); err != nil || got != value {
			t.Errorf("safeArtifactPath(%q) = %q, %v", value, got, err)
		}
	}
	invalid := []string{"", "/absolute", "../escape", "a/../b", "a\\b", ".", "a//b"}
	for _, value := range invalid {
		if _, err := safeArtifactPath(value); !errors.Is(err, ErrUnsafeArtifactPath) {
			t.Errorf("safeArtifactPath(%q) error = %v", value, err)
		}
	}
}

func TestGeneratorPerCollectorTimeoutTotalCancellationAndConcurrency(t *testing.T) {
	t.Parallel()
	limits := DefaultLimits()
	limits.PerCollectorTimeout = 5 * time.Millisecond
	timeoutCollector := CollectorFunc{ID: "timeout", Fn: func(ctx context.Context, _ Snapshot) ([]Artifact, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	bundle, err := NewGenerator([]Collector{timeoutCollector}, limits, 1).Build(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Collectors[0].Status != CollectorError || !strings.Contains(bundle.Manifest.Collectors[0].Error, "deadline") {
		t.Fatalf("timeout record = %#v", bundle.Manifest.Collectors[0])
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewGenerator(nil, limits, 1).Build(canceled, Snapshot{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled build error = %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	blocking := CollectorFunc{ID: "blocking", Fn: func(ctx context.Context, _ Snapshot) ([]Artifact, error) {
		close(started)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	generator := NewGenerator([]Collector{blocking}, DefaultLimits(), 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, _ = generator.Build(context.Background(), Snapshot{})
	}()
	<-started
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer waitCancel()
	if _, err := generator.Build(waitCtx, Snapshot{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrency gate error = %v", err)
	}
	close(release)
	wg.Wait()
}

func TestWriteArchiveRoundTripDeterminismAndLimits(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	limits := DefaultLimits()
	bundle := Bundle{
		Manifest: Manifest{FormatVersion: FormatVersion, CreatedAt: created, RedactionProfile: RedactionProfile},
		Artifacts: []Artifact{
			{Path: "a.txt", ContentType: "text/plain", Data: []byte("alpha")},
			{Path: "b.json", ContentType: "application/json", Data: []byte("{\"ok\":true}\n")},
		},
		limits: limits,
	}
	var first, second bytes.Buffer
	if _, err := WriteArchive(context.Background(), &first, bundle); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteArchive(context.Background(), &second, bundle); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("archive output is not deterministic")
	}
	entries := extractArchive(t, first.Bytes())
	if string(entries["a.txt"]) != "alpha" || string(entries["b.json"]) != "{\"ok\":true}\n" {
		t.Fatalf("unexpected archive entries: %#v", entries)
	}
	var manifest Manifest
	if err := json.Unmarshal(entries["manifest.json"], &manifest); err != nil || manifest.FormatVersion != FormatVersion {
		t.Fatalf("manifest decode failed: %v %#v", err, manifest)
	}

	tooSmall := bundle
	tooSmall.limits = limits
	tooSmall.limits.MaxCompressedBytes = 10
	if _, err := WriteArchive(context.Background(), io.Discard, tooSmall); !errors.Is(err, ErrArchiveTooLarge) {
		t.Fatalf("compressed limit error = %v", err)
	}
	tooSmall = bundle
	tooSmall.limits = limits
	tooSmall.limits.MaxUncompressedBytes = 1
	if _, err := WriteArchive(context.Background(), io.Discard, tooSmall); !errors.Is(err, ErrBundleTooLarge) {
		t.Fatalf("uncompressed limit error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := WriteArchive(ctx, io.Discard, bundle); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled archive error = %v", err)
	}
}

func TestWriteFilePermissionsNoOverwriteAndSymlinkRejection(t *testing.T) {
	directory := t.TempDir()
	collector := CollectorFunc{ID: "one", Fn: func(context.Context, Snapshot) ([]Artifact, error) {
		return []Artifact{{Path: "one.txt", ContentType: "text/plain", Data: []byte("one")}}, nil
	}}
	generator := NewGenerator([]Collector{collector}, DefaultLimits(), 1)
	output := filepath.Join(directory, "bundle.tar.gz")
	result, err := generator.WriteFile(context.Background(), output, Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != output || result.CompressedBytes <= 0 {
		t.Fatalf("unexpected file result: %#v", result)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("output mode = %o", info.Mode().Perm())
	}
	if _, err := generator.WriteFile(context.Background(), output, Snapshot{}); !errors.Is(err, ErrOutputExists) {
		t.Fatalf("overwrite error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(directory, ".jul-support-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files remain: %v %v", matches, err)
	}

	if runtime.GOOS != "windows" {
		realDirectory := filepath.Join(directory, "real")
		if err := os.Mkdir(realDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedDirectory := filepath.Join(directory, "linked")
		if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
			t.Fatal(err)
		}
		if _, err := generator.WriteFile(context.Background(), filepath.Join(linkedDirectory, "unsafe.tar.gz"), Snapshot{}); !errors.Is(err, ErrUnsafeOutputPath) {
			t.Fatalf("symlink output error = %v", err)
		}
	}
}

func TestRedactExactValuesLongestFirstAndTextSanitization(t *testing.T) {
	t.Parallel()
	data := redactExactValues([]byte("long-secret short"), []string{"secret", "long-secret", ""})
	if string(data) != "[REDACTED] short" {
		t.Fatalf("exact redaction = %q", data)
	}
	text := sanitizeText([]byte("Authorization: Bearer abc\npassword=hunter2"))
	if bytes.Contains(text, []byte("abc")) || bytes.Contains(text, []byte("hunter2")) {
		t.Fatalf("text sanitizer leaked: %s", text)
	}
}

func extractArchive(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	gzipReader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	entries := map[string][]byte{}
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		content, err := io.ReadAll(tarReader)
		if err != nil {
			t.Fatal(err)
		}
		entries[header.Name] = content
	}
	return entries
}
