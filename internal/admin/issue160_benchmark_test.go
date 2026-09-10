// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func benchAuditCfg(path string, maxMB, keep int) auditSinkConfig {
	return auditSinkConfig{path: path, publicPath: path, maxMB: maxMB, keep: keep}
}

func BenchmarkIssue160AuditRecordDisabled(b *testing.B) {
	a := newAuditLog(b.N + 1)
	ev := AuditEvent{Operation: "bench", Result: "success"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.record(ev)
	}
}

func BenchmarkIssue160AuditRecordEnabled(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(b.N+1, path, 100, 14, nil)
	defer a.Close()
	ev := AuditEvent{Operation: "bench", Result: "success"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.record(ev)
	}
}

func BenchmarkIssue160AuditRecordConcurrent(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(b.N+1, path, 100, 14, nil)
	defer a.Close()
	ev := AuditEvent{Operation: "bench", Result: "success"}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			a.record(ev)
		}
	})
}

func BenchmarkIssue160AuditTransitionSamePath(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 100, 14, nil)
	defer a.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := a.prepareTransition(benchAuditCfg(path, 100, 14+(i%2)))
		if err != nil {
			b.Fatal(err)
		}
		if p != nil {
			p.commit()
			p.retire(context.Background())
		}
	}
}

func BenchmarkIssue160AuditTransitionPathSwitch(b *testing.B) {
	root := b.TempDir()
	a := newAuditLog(8)
	defer a.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(root, fmt.Sprintf("audit-%d.jsonl", i%2))
		p, err := a.prepareTransition(benchAuditCfg(path, 100, 14))
		if err != nil {
			b.Fatal(err)
		}
		if p != nil {
			p.commit()
			p.retire(context.Background())
		}
	}
}

func BenchmarkIssue160AuditRotation(b *testing.B) {
	root := b.TempDir()
	payload := []byte("{\"id\":1}\n")
	for i := 0; i < b.N; i++ {
		path := filepath.Join(root, fmt.Sprintf("audit-%d.jsonl", i))
		if err := os.WriteFile(path, make([]byte, 1024*1024-int64(len(payload))), 0o640); err != nil {
			b.Fatal(err)
		}
		o, err := prepareAuditFileOwner(path)
		if err != nil {
			b.Fatal(err)
		}
		cfg := benchAuditCfg(path, 1, 2)
		b.StartTimer()
		result := o.write(0, cfg, payload)
		b.StopTimer()
		if result.err != nil {
			b.Fatal(result.err)
		}
		_ = o.release(context.Background(), true)
	}
}
