// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

type coverageErrorWriter struct{ err error }

func (writer coverageErrorWriter) Write([]byte) (int, error) { return 0, writer.err }

type coverageShortWriter struct{}

func (coverageShortWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	return len(data) - 1, nil
}

func TestSupportBundleCoverageLimitWriterAndArchiveErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("fixture writer failure")

	cases := []struct {
		name   string
		writer *limitWriter
		data   []byte
		want   error
	}{
		{
			name:   "limit already consumed",
			writer: &limitWriter{writer: io.Discard, limit: 0},
			data:   []byte("x"),
			want:   ErrArchiveTooLarge,
		},
		{
			name:   "oversized write propagates writer error",
			writer: &limitWriter{writer: coverageErrorWriter{err: sentinel}, limit: 1},
			data:   []byte("xx"),
			want:   sentinel,
		},
		{
			name:   "oversized write detects short write",
			writer: &limitWriter{writer: coverageShortWriter{}, limit: 2},
			data:   []byte("xxx"),
			want:   io.ErrShortWrite,
		},
		{
			name:   "oversized write reaches exact limit",
			writer: &limitWriter{writer: io.Discard, limit: 2},
			data:   []byte("xxx"),
			want:   ErrArchiveTooLarge,
		},
		{
			name:   "ordinary write propagates writer error",
			writer: &limitWriter{writer: coverageErrorWriter{err: sentinel}, limit: 8},
			data:   []byte("x"),
			want:   sentinel,
		},
		{
			name:   "ordinary write detects short write",
			writer: &limitWriter{writer: coverageShortWriter{}, limit: 8},
			data:   []byte("xx"),
			want:   io.ErrShortWrite,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.writer.Write(test.data)
			if !errors.Is(err, test.want) {
				t.Fatalf("Write error = %v, want %v", err, test.want)
			}
		})
	}

	if got := normalizeArchiveError(context.Canceled); !errors.Is(got, context.Canceled) {
		t.Fatalf("cancellation normalization = %v", got)
	}
	if got := normalizeArchiveError(sentinel); !errors.Is(got, sentinel) || !strings.Contains(got.Error(), "write support-bundle archive") {
		t.Fatalf("generic normalization = %v", got)
	}

	tarWriter := tar.NewWriter(coverageErrorWriter{err: sentinel})
	if err := writeTarEntry(tarWriter, "failure.txt", "text/plain", []byte("data"), time.Time{}); !errors.Is(err, sentinel) {
		t.Fatalf("tar header error = %v", err)
	}
}

func TestSupportBundleCoverageArchiveAndOutputBoundaries(t *testing.T) {
	t.Parallel()
	limits := DefaultLimits()
	zeroTimeBundle := Bundle{
		Manifest: Manifest{FormatVersion: FormatVersion, RedactionProfile: RedactionProfile},
		limits:   limits,
	}
	var output bytes.Buffer
	if _, err := WriteArchive(context.Background(), &output, zeroTimeBundle); err != nil {
		t.Fatalf("zero-time archive: %v", err)
	}

	unsafe := Bundle{
		Manifest:  Manifest{FormatVersion: FormatVersion, RedactionProfile: RedactionProfile},
		Artifacts: []Artifact{{Path: "../escape", ContentType: "text/plain", Data: []byte("x")}},
		limits:    limits,
	}
	if _, err := WriteArchive(context.Background(), io.Discard, unsafe); !errors.Is(err, ErrUnsafeArtifactPath) {
		t.Fatalf("unsafe archive path error = %v", err)
	}

	sentinel := errors.New("archive sink failed")
	if _, err := WriteArchive(context.Background(), coverageErrorWriter{err: sentinel}, zeroTimeBundle); !errors.Is(err, sentinel) {
		t.Fatalf("archive sink error = %v", err)
	}

	if _, err := validateOutputPath("   "); !errors.Is(err, ErrUnsafeOutputPath) {
		t.Fatalf("empty output error = %v", err)
	}
	directory := t.TempDir()
	if _, err := validateOutputPath(filepath.Join(directory, "missing", "bundle.tar.gz")); err == nil || !strings.Contains(err.Error(), "output directory") {
		t.Fatalf("missing output directory error = %v", err)
	}
	notDirectory := filepath.Join(directory, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateOutputPath(filepath.Join(notDirectory, "bundle.tar.gz")); !errors.Is(err, ErrUnsafeOutputPath) {
		t.Fatalf("non-directory parent error = %v", err)
	}
}

func TestSupportBundleCoverageGeneratorBoundaries(t *testing.T) {
	t.Parallel()
	generator := NewGenerator(nil, Limits{}, 0)
	defaults := DefaultLimits()
	if generator.limits != defaults || cap(generator.semaphore) != 1 {
		t.Fatalf("normalized generator = %#v", generator)
	}

	fixed := time.Date(2026, 8, 30, 20, 0, 0, 0, time.UTC)
	calls := 0
	generator.now = func() time.Time {
		calls++
		if calls == 1 {
			return fixed
		}
		return fixed.Add(-time.Second)
	}
	bundle, err := generator.Build(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.DurationMillis != 0 {
		t.Fatalf("negative duration was not clamped: %#v", bundle.Manifest)
	}

	artifact := Artifact{Path: "same.txt", ContentType: "text/plain", Data: []byte("x")}
	duplicateAcrossCollectors := NewGenerator([]Collector{
		CollectorFunc{ID: "first", Fn: func(context.Context, Snapshot) ([]Artifact, error) { return []Artifact{artifact}, nil }},
		CollectorFunc{ID: "second", Fn: func(context.Context, Snapshot) ([]Artifact, error) { return []Artifact{artifact}, nil }},
	}, DefaultLimits(), 1)
	bundle, err = duplicateAcrossCollectors.Build(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Artifacts) != 1 || bundle.Manifest.Collectors[1].Status != CollectorError || !strings.Contains(bundle.Manifest.Collectors[1].Error, ErrDuplicateArtifact.Error()) {
		t.Fatalf("cross-collector duplicate result = %#v", bundle)
	}

	invalidJSON := NewGenerator([]Collector{CollectorFunc{
		ID: "invalid-json",
		Fn: func(context.Context, Snapshot) ([]Artifact, error) {
			return []Artifact{{Path: "broken.json", ContentType: "application/json", Data: []byte("{")}}, nil
		},
	}}, DefaultLimits(), 1)
	bundle, err = invalidJSON.Build(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Collectors[0].Status != CollectorError || !strings.Contains(bundle.Manifest.Collectors[0].Error, "sanitize JSON artifact") {
		t.Fatalf("invalid JSON result = %#v", bundle.Manifest.Collectors[0])
	}

	limits := DefaultLimits()
	limits.PerCollectorTimeout = time.Millisecond
	lateSuccess := NewGenerator([]Collector{CollectorFunc{
		ID: "late-success",
		Fn: func(ctx context.Context, _ Snapshot) ([]Artifact, error) {
			// Wait for the deadline itself (not a fixed sleep) so this is not a
			// race against OS timer/scheduler resolution on slower CI runners:
			// ctx.Err() is guaranteed set before Done() closes. The collector
			// still returns (nil, nil), simulating one that ignores its own
			// context, to exercise the generator's post-hoc ctx.Err() check.
			<-ctx.Done()
			return nil, nil
		},
	}}, limits, 1)
	bundle, err = lateSuccess.Build(context.Background(), Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Manifest.Collectors[0].Status != CollectorError || !strings.Contains(bundle.Manifest.Collectors[0].Error, "deadline") {
		t.Fatalf("late collector result = %#v", bundle.Manifest.Collectors[0])
	}

	limits = DefaultLimits()
	limits.TotalTimeout = 5 * time.Millisecond
	limits.PerCollectorTimeout = time.Second
	totalTimeout := NewGenerator([]Collector{
		CollectorFunc{ID: "wait", Fn: func(ctx context.Context, _ Snapshot) ([]Artifact, error) {
			<-ctx.Done()
			return nil, nil
		}},
		CollectorFunc{ID: "never", Fn: func(context.Context, Snapshot) ([]Artifact, error) { return nil, nil }},
	}, limits, 1)
	if _, err := totalTimeout.Build(context.Background(), Snapshot{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("total timeout error = %v", err)
	}
}

func TestSupportBundleCoverageCollectorEarlyExits(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := collectConfigurationMetadata(canceled, Snapshot{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled metadata collector error = %v", err)
	}
	if _, err := collectConfigurationMetadata(context.Background(), Snapshot{ConfigPath: filepath.Join(t.TempDir(), "missing.toml")}); err == nil {
		t.Fatal("missing configuration did not fail metadata collection")
	}

	directory := t.TempDir()
	cfg := config.ServeDir(filepath.Join(directory, "www"), "127.0.0.1:8080")
	configPath := writeSupportConfig(t, cfg)
	artifacts, err := collectAccessLog(context.Background(), Snapshot{ConfigPath: configPath, IncludeLogs: true})
	if err != nil || len(artifacts) != 0 {
		t.Fatalf("disabled file access log = %#v, %v", artifacts, err)
	}

	cfg.Observability.AccessLog.Enabled = config.Bool(true)
	cfg.Observability.AccessLog.Sinks = []string{"file"}
	cfg.Observability.AccessLog.File = filepath.Join(directory, "missing.log")
	configPath = writeSupportConfig(t, cfg)
	if _, err := collectAccessLog(context.Background(), Snapshot{ConfigPath: configPath, IncludeLogs: true}); err == nil {
		t.Fatal("missing configured access log did not fail")
	}

	logPath := filepath.Join(directory, "single-line.log")
	if err := os.WriteFile(logPath, []byte("single-line-without-newline"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, truncated, err := tailRegularFile(context.Background(), logPath, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(data) != 0 {
		t.Fatalf("partial line tail = %q, truncated=%v", data, truncated)
	}
}
