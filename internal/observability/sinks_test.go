// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"

	"gopkg.in/natefinch/lumberjack.v2"
)

func sampleRecord() middleware.AccessRecord {
	return middleware.AccessRecord{
		Time:      time.Now(),
		Method:    "GET",
		Host:      "example.com",
		Path:      "/widgets",
		Query:     "a=1",
		Status:    200,
		Bytes:     100,
		Duration:  2 * time.Millisecond,
		Client:    "127.0.0.1",
		Peer:      "127.0.0.1",
		RequestID: "rid-123",
		TraceID:   "tid-456",
		UserAgent: "test-agent",
		Proto:     "HTTP/1.1",
	}
}

func newBase() *slog.Logger { return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)) }

func TestBuildAccessSinksDefaultsToStdout(t *testing.T) {
	var buf bytes.Buffer
	base := slog.New(slog.NewTextHandler(&buf, nil))
	sinks, closers, err := BuildAccessSinks(config.AccessLogConfig{}, base)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(sinks) != 1 {
		t.Fatalf("got %d sinks, want 1", len(sinks))
	}
	if len(closers) != 0 {
		t.Errorf("stdout sink should open no closers, got %d", len(closers))
	}
	sinks[0].Log(sampleRecord())
	if out := buf.String(); !strings.Contains(out, "msg=access") || !strings.Contains(out, "method=GET") {
		t.Errorf("stdout sink output missing fields: %q", out)
	}
}

func TestBuildAccessSinksDisabledOpensNothing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "access.log")
	sinks, closers, err := BuildAccessSinks(config.AccessLogConfig{
		Enabled: config.Bool(false),
		Sinks:   []string{"file"},
		File:    path,
	}, newBase())
	if err != nil {
		t.Fatalf("build disabled sinks: %v", err)
	}
	if len(sinks) != 0 || len(closers) != 0 {
		t.Fatalf("disabled build returned %d sinks / %d closers, want 0/0", len(sinks), len(closers))
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled access log created %q: %v", path, err)
	}
}

func TestBuildAccessSinksRejectsExplicitEmptyEnabledSet(t *testing.T) {
	_, _, err := BuildAccessSinks(config.AccessLogConfig{Enabled: config.Bool(true), Sinks: []string{}}, newBase())
	if err == nil {
		t.Fatal("enabled access log with explicit empty sinks must fail")
	}
}

// TestBuildAccessSinksRejectsEmptyFilePath is a defense-in-depth check: config
// validation already rejects a "file" sink with no path, but BuildAccessSinks
// must not rely solely on that upstream gate — a misconfigured candidate that
// somehow reached here must still fail before any write, not silently probe
// the process's current working directory.
func TestBuildAccessSinksRejectsEmptyFilePath(t *testing.T) {
	_, _, err := BuildAccessSinks(config.AccessLogConfig{Sinks: []string{"file"}, File: ""}, newBase())
	if err == nil {
		t.Fatal("expected an error for a file sink with no configured path")
	}
}

func TestBuildAccessSinksDedupes(t *testing.T) {
	sinks, _, err := BuildAccessSinks(config.AccessLogConfig{Sinks: []string{"stdout", "stdout"}}, newBase())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(sinks) != 1 {
		t.Errorf("duplicate sinks not de-duplicated: got %d, want 1", len(sinks))
	}
}

func TestBuildAccessSinksFileText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	cfg := config.AccessLogConfig{Sinks: []string{"file"}, File: path, Format: "text", RotateMaxMB: 10, RotateKeep: 2}
	sinks, closers, err := BuildAccessSinks(cfg, newBase())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(sinks) != 1 || len(closers) != 1 {
		t.Fatalf("got %d sinks / %d closers, want 1/1", len(sinks), len(closers))
	}
	sinks[0].Log(sampleRecord())
	for _, c := range closers {
		_ = c.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if s := string(data); !strings.Contains(s, "method=GET") || !strings.Contains(s, "request_id=rid-123") {
		t.Errorf("file sink missing fields: %q", s)
	}
}

func TestBuildAccessSinksFileJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.json")
	cfg := config.AccessLogConfig{Sinks: []string{"file"}, File: path, Format: "json"}
	sinks, closers, err := BuildAccessSinks(cfg, newBase())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sinks[0].Log(sampleRecord())
	for _, c := range closers {
		_ = c.Close()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(data), &m); err != nil {
		t.Fatalf("json sink output not valid JSON: %v (%q)", err, data)
	}
	if m["method"] != "GET" || m["status"] != float64(200) {
		t.Errorf("json sink fields wrong: %v", m)
	}
}

func TestBuildAccessSinksFileRotationConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	cfg := config.AccessLogConfig{Sinks: []string{"file"}, File: path, RotateMaxMB: 25, RotateKeep: 4}
	_, closers, err := BuildAccessSinks(cfg, newBase())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	if len(closers) != 1 {
		t.Fatalf("got %d closers, want 1", len(closers))
	}
	lj, ok := closers[0].(*lumberjack.Logger)
	if !ok {
		t.Fatalf("file closer is %T, want *lumberjack.Logger", closers[0])
	}
	if lj.Filename != path || lj.MaxSize != 25 || lj.MaxBackups != 4 || !lj.LocalTime {
		t.Errorf("rotation config not mapped: %+v", lj)
	}
}

func TestBuildAccessSinksUnknownSink(t *testing.T) {
	sinks, closers, err := BuildAccessSinks(config.AccessLogConfig{Sinks: []string{"bogus"}}, newBase())
	if err == nil {
		t.Fatal("expected error for unknown sink")
	}
	if sinks != nil || closers != nil {
		t.Error("failed build must return no sinks or closers")
	}
}

func TestBuildAccessSinksRollsBackOnError(t *testing.T) {
	// A file sink opened before a later failing sink must be closed and not
	// leaked back to the caller.
	dir := t.TempDir()
	cfg := config.AccessLogConfig{Sinks: []string{"file", "bogus"}, File: filepath.Join(dir, "access.log")}
	sinks, closers, err := BuildAccessSinks(cfg, newBase())
	if err == nil {
		t.Fatal("expected error for unknown sink after file sink")
	}
	if sinks != nil || closers != nil {
		t.Error("failed build must return no sinks or closers")
	}
}

// TestBuildAccessSinksFailedFileSinkLeavesNoRealFile pins #98's fix: probing
// writability must never touch the real target path, so a candidate build
// that ultimately fails (e.g. Abort after a later sink error) leaves no
// artifact at the configured file path — only a temp sentinel that is removed
// immediately.
func TestBuildAccessSinksFailedFileSinkLeavesNoRealFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	cfg := config.AccessLogConfig{Sinks: []string{"file", "bogus"}, File: path}
	if _, _, err := BuildAccessSinks(cfg, newBase()); err == nil {
		t.Fatal("expected error for unknown sink after file sink")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a failed candidate build created the real log file: stat err = %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a failed candidate build left %d artifact(s) in %s: %v", len(entries), dir, entries)
	}
}

// TestProbeWritableDirRejectsEmptyPath pins a Copilot review finding on #376:
// an empty path must not silently probe the process's current working
// directory (which is normally writable, masking a misconfiguration that
// config validation already rejects elsewhere).
func TestProbeWritableDirRejectsEmptyPath(t *testing.T) {
	if err := probeWritableDir(""); err == nil {
		t.Fatal("expected an error for an empty path")
	}
	if err := probeWritableDir("   "); err == nil {
		t.Fatal("expected an error for a whitespace-only path")
	}
}

// TestProbeWritableDirLeavesNoSentinelBehind pins the other half of the same
// review finding: the temporary sentinel must actually be gone afterward, not
// merely attempted-and-ignored.
func TestProbeWritableDirLeavesNoSentinelBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	if err := probeWritableDir(path); err != nil {
		t.Fatalf("probeWritableDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("probeWritableDir left %d artifact(s) behind: %v", len(entries), entries)
	}
}

// TestProbeWritableDirMkdirAllFailure covers the directory-creation failure
// branch: a path component that already exists as a regular file can never
// become a directory, on any platform or privilege level.
func TestProbeWritableDirMkdirAllFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := probeWritableDir(filepath.Join(blocker, "nested", "access.log"))
	if err == nil {
		t.Fatal("expected an error when a path component is a regular file")
	}
	if !strings.Contains(err.Error(), "cannot create directory") {
		t.Fatalf("error does not identify itself as a directory-creation failure: %v", err)
	}
}

// TestProbeWritableDirCreateTempFailure covers the "directory exists but is
// not writable" branch, mirroring the root/skip-guarded pattern already used
// elsewhere in this repo for permission-based tests
// (internal/apicontract/apicontractgen/main_test.go).
func TestProbeWritableDirCreateTempFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: file mode does not deny writes")
	}
	dir := t.TempDir()
	sub := filepath.Join(dir, "readonly")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sub, 0o555); err != nil {
		t.Skipf("cannot make directory read-only on this platform: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

	err := probeWritableDir(filepath.Join(sub, "access.log"))
	if err == nil {
		t.Skip("the platform ignored the directory mode")
	}
	if !strings.Contains(err.Error(), "not writable") {
		t.Fatalf("error does not identify itself as a writability failure: %v", err)
	}
}

// TestProbeWritableDirCloseFailure and TestProbeWritableDirRemoveFailure cover
// the close/remove failure branches via probeCloseFile/probeRemoveFile — a
// real close or remove failure on a just-created temp file is impractical to
// trigger portably, so these inject the failure deterministically instead.
func TestProbeWritableDirCloseFailure(t *testing.T) {
	orig := probeCloseFile
	probeCloseFile = func(*os.File) error { return errors.New("injected close failure") }
	defer func() { probeCloseFile = orig }()

	err := probeWritableDir(filepath.Join(t.TempDir(), "access.log"))
	if err == nil || !strings.Contains(err.Error(), "closing writability probe") {
		t.Fatalf("expected a close-failure error, got %v", err)
	}
}

func TestProbeWritableDirRemoveFailure(t *testing.T) {
	orig := probeRemoveFile
	probeRemoveFile = func(string) error { return errors.New("injected remove failure") }
	defer func() { probeRemoveFile = orig }()

	err := probeWritableDir(filepath.Join(t.TempDir(), "access.log"))
	if err == nil || !strings.Contains(err.Error(), "removing writability probe") {
		t.Fatalf("expected a remove-failure error, got %v", err)
	}
}
