// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"encoding/json"
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
