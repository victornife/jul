// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditLogDurableSinkWritesJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "audit.jsonl")

	a := newAuditLogWithSink(8, path, 0, 0, nil)
	a.record(AuditEvent{Operation: "config.apply", Result: "success", Detail: "applied"})
	a.record(AuditEvent{Operation: "config.rollback", Result: "failure", Detail: "bad id"})
	if err := a.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open sink: %v", err)
	}
	defer f.Close()

	var events []AuditEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev AuditEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			t.Fatalf("unmarshal line: %v", err)
		}
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("persisted %d events, want 2", len(events))
	}
	if events[0].Operation != "config.apply" || events[1].Operation != "config.rollback" {
		t.Errorf("unexpected order/content: %+v", events)
	}
	// IDs are assigned monotonically and persisted.
	if events[0].ID != 1 || events[1].ID != 2 {
		t.Errorf("ids = %d,%d, want 1,2", events[0].ID, events[1].ID)
	}
}

func TestAuditLogDurableSinkPersistsRedacted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	a := newAuditLogWithSink(8, path, 0, 0, nil)
	// A detail carrying a credential marker must be redacted before it is both
	// buffered and persisted.
	a.record(AuditEvent{Operation: "auth.fail", Result: "failure", Detail: "Authorization: Bearer sekret"})
	_ = a.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sink: %v", err)
	}
	if string(data) == "" {
		t.Fatal("sink is empty")
	}
	var ev AuditEvent
	if err := json.Unmarshal(data[:len(data)-1], &ev); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ev.Detail != "[redacted]" {
		t.Errorf("detail = %q, want [redacted]", ev.Detail)
	}
}

func TestAuditLogNoSinkWhenPathEmpty(t *testing.T) {
	a := newAuditLogWithSink(8, "", 0, 0, nil)
	if a.sink != nil {
		t.Error("expected no durable sink for empty path")
	}
	if a.statusReport() != nil {
		t.Error("expected nil status when no durable sink is configured")
	}
	// Still records in memory.
	a.record(AuditEvent{Operation: "x", Result: "success"})
	if got := a.snapshot("", "", 0); len(got) != 1 {
		t.Errorf("in-memory records = %d, want 1", len(got))
	}
	if err := a.Close(); err != nil {
		t.Errorf("close with no sink: %v", err)
	}
}

// TestAuditLogSinkOpenFailureSurfacesDegradedStatus proves a misconfigured
// durable path is fail-loud (P3-08): the sink degrades to memory-only AND the
// failure is reported as a degraded status instead of being silently dropped,
// while request handling (in-memory recording) keeps working.
func TestAuditLogSinkOpenFailureSurfacesDegradedStatus(t *testing.T) {
	dir := t.TempDir()
	// Make the would-be parent directory an existing file so the sink cannot be
	// created — a portable way to force an open failure.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	path := filepath.Join(blocker, "audit.jsonl")

	a := newAuditLogWithSink(8, path, 0, 0, nil)
	if a.sink != nil {
		t.Fatal("sink should be nil when the path cannot be opened")
	}
	// The API is never taken down: in-memory recording still works.
	a.record(AuditEvent{Operation: "config.apply", Result: "success"})
	if got := a.snapshot("", "", 0); len(got) != 1 {
		t.Errorf("in-memory records = %d, want 1", len(got))
	}

	st := a.statusReport()
	if st == nil {
		t.Fatal("status should be reported when a durable path is configured")
	}
	if !st.Configured {
		t.Error("configured = false, want true")
	}
	if st.Healthy {
		t.Error("healthy = true, want false for an unopenable sink")
	}
	if st.Error == "" {
		t.Error("error should explain why the sink is degraded")
	}
	if st.Path != path {
		t.Errorf("path = %q, want %q", st.Path, path)
	}
}

// TestAuditLogHealthyStatusWhenWritable proves a writable durable sink reports a
// healthy status with no write failures, so the field is a true signal.
func TestAuditLogHealthyStatusWhenWritable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 0, 0, nil)
	t.Cleanup(func() { _ = a.Close() })

	a.record(AuditEvent{Operation: "config.apply", Result: "success"})
	st := a.statusReport()
	if st == nil {
		t.Fatal("status should be reported")
	}
	if !st.Configured || !st.Healthy {
		t.Errorf("status = %+v, want configured+healthy", st)
	}
	if st.WriteFailures != 0 {
		t.Errorf("write_failures = %d, want 0", st.WriteFailures)
	}
	if st.Error != "" {
		t.Errorf("error = %q, want empty", st.Error)
	}
}
