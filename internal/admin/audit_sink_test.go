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

	a := newAuditLogWithSink(8, path, nil)
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
	a := newAuditLogWithSink(8, path, nil)
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
	a := newAuditLogWithSink(8, "", nil)
	if a.sink != nil {
		t.Error("expected no durable sink for empty path")
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
