from pathlib import Path


def patch(path: str, old: str, new: str) -> None:
    p = Path(path)
    s = p.read_text()
    if old not in s:
        raise SystemExit(f"missing anchor in {path}: {old[:140]!r}")
    p.write_text(s.replace(old, new, 1))

# Separate current active health from cumulative/historical last failure.
patch(
    "internal/admin/audit.go",
    '''\tactiveFailure      auditFailureCategory\n\tactiveFailureAt    time.Time\n\tactiveFailureErr   error // operator-log detail only; never serialized\n''',
    '''\tactiveFailure      auditFailureCategory\n\tactiveFailureAt    time.Time\n\tactiveFailureErr   error // operator-log detail only; never serialized\n\tlastFailure        auditFailureCategory\n\tlastFailureAt      time.Time\n''',
)
patch(
    "internal/admin/audit_sink_runtime.go",
    '''\ta.activeFailure = category\n\ta.activeFailureAt = time.Now().UTC()\n}''',
    '''\tnow := time.Now().UTC()\n\ta.activeFailure = category\n\ta.activeFailureAt = now\n\ta.lastFailure = category\n\ta.lastFailureAt = now\n}''',
)
patch(
    "internal/admin/audit_sink_runtime.go",
    '''\t\tLastFailureAt:   a.activeFailureAt,\n''',
    '''\t\tLastFailureAt:   a.lastFailureAt,\n''',
)
patch(
    "internal/admin/audit_sink_runtime.go",
    '''\tif a.activeFailure != "" {\n\t\tst.LastFailureCategory = string(a.activeFailure)\n\t}\n''',
    '''\tif a.lastFailure != "" {\n\t\tst.LastFailureCategory = string(a.lastFailure)\n\t}\n''',
)
patch(
    "internal/admin/audit_sink_runtime.go",
    '''\tif result.err != nil {\n\t\tswitch result.category {''',
    '''\tif result.err != nil {\n\t\tnow := time.Now().UTC()\n\t\ta.lastFailure = result.category\n\t\ta.lastFailureAt = now\n\t\tswitch result.category {''',
)
patch(
    "internal/admin/audit_sink_runtime.go",
    '''\t\tif a.currentSink == gen {\n\t\t\ta.activeFailure = result.category\n\t\t\ta.activeFailureAt = time.Now().UTC()\n\t\t}\n''',
    '''\t\tif a.currentSink == gen {\n\t\t\ta.activeFailure = result.category\n\t\t\ta.activeFailureAt = now\n\t\t}\n''',
)
patch(
    "internal/admin/audit_sink_runtime.go",
    '''\ta.retirementFailures++\n\ta.mu.Unlock()\n''',
    '''\ta.retirementFailures++\n\ta.lastFailure = category\n\ta.lastFailureAt = time.Now().UTC()\n\ta.mu.Unlock()\n''',
)

# Restore the explanatory comment in the stateful e2e fixture. Managed applies
# canonicalize TOML, so acceptance tests must restore semantic state without
# accidentally deleting repository comments.
p = Path("testdata/console-e2e.toml")
s = p.read_text()
needle = "history_keep = 50\nrate_limit_read_per_min = 6000\n"
if needle in s:
    s = s.replace(
        needle,
        "history_keep = 50\n# This fixture is shared by a stateful real-server suite. Keep limiter budgets\n# well above suite traffic so unrelated acceptance tests cannot couple through\n# token-bucket exhaustion; limiter behavior has dedicated unit/security gates.\nrate_limit_read_per_min = 6000\n",
        1,
    )
p.write_text(s)

# Focused observability cross-link: no new Prometheus family is justified; the
# bounded runtime/admin health projection carries active and cumulative state.
p = Path("docs/observability.md")
s = p.read_text()
marker = "##"
pos = s.find(marker)
if pos < 0:
    raise SystemExit("docs/observability.md heading marker missing")
text = '''\n> **Durable audit sink (HR-07C).** Runtime status reports whether the durable sink is configured/active, current active health, generation, cumulative write/rotation/retention-cleanup/retirement failure counters, and the bounded category/time of the last observed failure. Public readiness exposes only the bounded `audit_sink` reason. #160 intentionally adds no Prometheus family: the existing status/health surfaces already provide the required operator signal, avoiding path/error labels and unnecessary cardinality. See [audit sink hot reload](audit-sink-hot-reload.md).\n\n'''
if "Durable audit sink (HR-07C)" not in s:
    s = s[:pos] + text + s[pos:]
p.write_text(s)

# Linux real-filesystem matrix: permissions, symlink/special rejection and
# ownership-safe abort when the prepared pathname is externally replaced.
Path("internal/admin/issue160_filesystem_linux_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package admin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIssue160FilesystemCandidatePermissionsAndAbortCleanup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "audit.jsonl")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4))
	if err != nil { t.Fatal(err) }
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("candidate file mode=%v err=%v, want 0640", func() os.FileMode { if info != nil { return info.Mode().Perm() }; return 0 }(), err)
	}
	for _, dir := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b")} {
		info, err := os.Stat(dir); if err != nil { t.Fatal(err) }
		if info.Mode().Perm() != 0o750 { t.Fatalf("%s mode=%o want 0750", dir, info.Mode().Perm()) }
	}
	p.abort()
	if _, err := os.Stat(path); !os.IsNotExist(err) { t.Fatalf("candidate file survived abort: %v", err) }
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) { t.Fatalf("candidate directories survived abort: %v", err) }
}

func TestIssue160FilesystemRejectsSymlinkParentAndSpecialFinal(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o750); err != nil { t.Fatal(err) }
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil { t.Fatal(err) }
	if _, err := prepareAuditFileOwner(filepath.Join(linkDir, "audit.jsonl")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	fifo := filepath.Join(root, "audit.fifo")
	if err := syscall.Mkfifo(fifo, 0o640); err != nil { t.Fatal(err) }
	if _, err := prepareAuditFileOwner(fifo); err == nil { t.Fatal("FIFO final destination accepted") }
	dirFinal := filepath.Join(root, "audit.dir")
	if err := os.Mkdir(dirFinal, 0o750); err != nil { t.Fatal(err) }
	if _, err := prepareAuditFileOwner(dirFinal); err == nil { t.Fatal("directory final destination accepted") }
}

func TestIssue160FilesystemAbortNeverDeletesExternalReplacement(t *testing.T) {
	root := t.TempDir(); path := filepath.Join(root, "audit.jsonl"); saved := filepath.Join(root, "candidate.saved")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4)); if err != nil { t.Fatal(err) }
	if err := os.Rename(path, saved); err != nil { t.Fatal(err) }
	external := []byte("external-owner\n")
	if err := os.WriteFile(path, external, 0o600); err != nil { t.Fatal(err) }
	p.abort()
	got, err := os.ReadFile(path); if err != nil { t.Fatal(err) }
	if string(got) != string(external) { t.Fatalf("external replacement changed: %q", got) }
	if _, err := os.Stat(saved); err != nil { t.Fatalf("renamed candidate was deleted: %v", err) }
}

func TestIssue160FilesystemUnwritableParentFailsWithoutTouchingLiveSink(t *testing.T) {
	if os.Geteuid() == 0 { t.Skip("root bypasses directory permission checks") }
	root := t.TempDir(); aPath := filepath.Join(root, "a.jsonl")
	a := newAuditLogWithSink(8, aPath, 10, 4, nil)
	blocked := filepath.Join(root, "blocked"); if err := os.Mkdir(blocked, 0o500); err != nil { t.Fatal(err) }
	defer os.Chmod(blocked, 0o700)
	if _, err := a.prepareTransition(mustAuditCfg(t, filepath.Join(blocked, "b.jsonl"), 10, 4)); err == nil { t.Fatal("unwritable candidate prepared") }
	a.record(AuditEvent{Operation:"still-a", Result:"success"})
	ids := readAuditIDs(t, aPath); if len(ids) != 1 || ids[0] != 1 { t.Fatalf("live A disturbed: %v", ids) }
	_ = a.Close()
}
''')

# Goleak plus ownership/reference churn. IgnoreCurrent prevents unrelated test
# harness goroutines from becoming false positives while catching #160 leaks.
Path("internal/admin/issue160_leak_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

func TestIssue160NoGoroutineOrOwnerLeakAcrossChurn(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	root := t.TempDir(); a := newAuditLog(64)
	var prior *auditFileOwner
	for i := 0; i < 40; i++ {
		path := filepath.Join(root, fmt.Sprintf("audit-%d.jsonl", i%3))
		p, err := a.prepareTransition(mustAuditCfg(t, path, 1+(i%2), 2+(i%4))); if err != nil { t.Fatal(err) }
		if p != nil { p.commit(); p.retire(context.Background()) }
		a.record(AuditEvent{Operation:"churn", Result:"success"})
		if a.currentSink != nil {
			a.currentSink.owner.lifeMu.Lock(); refs, closed := a.currentSink.owner.refs, a.currentSink.owner.closed; a.currentSink.owner.lifeMu.Unlock()
			if refs != 1 || closed { t.Fatalf("iteration %d active owner refs=%d closed=%v", i, refs, closed) }
			prior = a.currentSink.owner
		}
	}
	if err := a.Close(); err != nil { t.Fatal(err) }
	if prior != nil { prior.lifeMu.Lock(); refs, closed := prior.refs, prior.closed; prior.lifeMu.Unlock(); if refs != 0 || !closed { t.Fatalf("final owner refs=%d closed=%v", refs, closed) } }
}
''')

# Issue-owned microbenchmarks. Rotation is measured separately from steady append.
Path("internal/admin/issue160_benchmark_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func BenchmarkIssue160AuditRecordDisabled(b *testing.B) {
	a := newAuditLog(b.N + 1); ev := AuditEvent{Operation:"bench", Result:"success"}; b.ResetTimer()
	for i:=0;i<b.N;i++ { a.record(ev) }
}

func BenchmarkIssue160AuditRecordEnabled(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.jsonl"); a := newAuditLogWithSink(b.N+1,path,100,14,nil); defer a.Close()
	ev := AuditEvent{Operation:"bench",Result:"success"}; b.ResetTimer()
	for i:=0;i<b.N;i++ { a.record(ev) }
}

func BenchmarkIssue160AuditRecordConcurrent(b *testing.B) {
	path := filepath.Join(b.TempDir(), "audit.jsonl"); a := newAuditLogWithSink(b.N+1,path,100,14,nil); defer a.Close()
	ev := AuditEvent{Operation:"bench",Result:"success"}; b.ResetTimer()
	b.RunParallel(func(pb *testing.PB){ for pb.Next(){ a.record(ev) } })
}

func BenchmarkIssue160AuditTransitionSamePath(b *testing.B) {
	path := filepath.Join(b.TempDir(),"audit.jsonl"); a:=newAuditLogWithSink(8,path,100,14,nil); defer a.Close(); b.ResetTimer()
	for i:=0;i<b.N;i++ { p,err:=a.prepareTransition(mustAuditCfg(b,path,100,14+(i%2))); if err!=nil{b.Fatal(err)}; if p!=nil{p.commit();p.retire(context.Background())} }
}

func BenchmarkIssue160AuditTransitionPathSwitch(b *testing.B) {
	root:=b.TempDir(); a:=newAuditLog(8); defer a.Close(); b.ResetTimer()
	for i:=0;i<b.N;i++ { path:=filepath.Join(root,fmt.Sprintf("audit-%d.jsonl",i%2)); p,err:=a.prepareTransition(mustAuditCfg(b,path,100,14)); if err!=nil{b.Fatal(err)}; if p!=nil{p.commit();p.retire(context.Background())} }
}

func BenchmarkIssue160AuditRotation(b *testing.B) {
	root:=b.TempDir(); payload:=[]byte("{\"id\":1}\n")
	for i:=0;i<b.N;i++ { path:=filepath.Join(root,fmt.Sprintf("audit-%d.jsonl",i)); if err:=os.WriteFile(path,make([]byte,1024*1024-int64(len(payload))),0o640);err!=nil{b.Fatal(err)}; o,err:=prepareAuditFileOwner(path);if err!=nil{b.Fatal(err)}; cfg:=mustAuditCfg(b,path,1,2); b.StartTimer(); result:=o.write(0,cfg,payload); b.StopTimer(); if result.err!=nil{b.Fatal(result.err)}; _=o.release(context.Background(),true) }
}
''')

# Historical failure contract tests.
p = Path("internal/admin/issue160_writer_matrix_test.go")
s = p.read_text()
s += r'''

func TestAuditSinkRecoveredHealthPreservesLastFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	owner := a.currentSink.owner; base := owner.file
	failed := true
	owner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int,error) { if failed { failed=false; return 0,errors.New("one-shot") }; return base.Write(p) }}
	a.record(AuditEvent{Operation:"fail",Result:"success"})
	a.record(AuditEvent{Operation:"recover",Result:"success"})
	st:=a.statusReport(); if st==nil || !st.Healthy || st.WriteFailures!=1 || st.LastFailureCategory!=string(auditFailureWrite) || st.LastFailureAt.IsZero(){t.Fatalf("recovered status=%+v",st)}
	_ = a.Close()
}

func TestAuditSinkRetiredCloseFailureIsHistoricalButNotActive(t *testing.T) {
	d:=t.TempDir(); a:=newAuditLogWithSink(8,filepath.Join(d,"a.jsonl"),10,4,nil); old:=a.currentSink.owner; base:=old.file
	old.file=&auditFaultFile{base:base,closeFn:func()error{_ = base.Close();return errors.New("close-history")}}
	p,err:=a.prepareTransition(mustAuditCfg(t,filepath.Join(d,"b.jsonl"),10,4));if err!=nil{t.Fatal(err)};p.commit();p.retire(context.Background())
	st:=a.statusReport();if st==nil || !st.Healthy || st.RetireFailures!=1 || st.LastFailureCategory!=string(auditFailureClose) || st.LastFailureAt.IsZero(){t.Fatalf("status=%+v",st)}
	_ = a.Close()
}
'''
p.write_text(s)

print("HR-07C final hardening staged")
