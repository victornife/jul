// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type auditFaultFile struct {
	base    auditFileHandle
	writeFn func([]byte) (int, error)
	closeFn func() error
}

func (f *auditFaultFile) Write(p []byte) (int, error) {
	if f.writeFn != nil {
		return f.writeFn(p)
	}
	return f.base.Write(p)
}
func (f *auditFaultFile) Close() error {
	if f.closeFn != nil {
		return f.closeFn()
	}
	return f.base.Close()
}
func (f *auditFaultFile) Stat() (fs.FileInfo, error) { return f.base.Stat() }

type auditFaultRoot struct {
	base     auditRootHandle
	lstatFn  func(string) (fs.FileInfo, error)
	openFn   func(string, int, fs.FileMode) (auditFileHandle, error)
	renameFn func(string, string) error
	removeFn func(string) error
	readFn   func(string) ([]fs.DirEntry, error)
	closeFn  func() error
}

func (r *auditFaultRoot) Lstat(name string) (fs.FileInfo, error) {
	if r.lstatFn != nil {
		return r.lstatFn(name)
	}
	return r.base.Lstat(name)
}
func (r *auditFaultRoot) OpenFile(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
	if r.openFn != nil {
		return r.openFn(name, flag, perm)
	}
	return r.base.OpenFile(name, flag, perm)
}
func (r *auditFaultRoot) Rename(oldName, newName string) error {
	if r.renameFn != nil {
		return r.renameFn(oldName, newName)
	}
	return r.base.Rename(oldName, newName)
}
func (r *auditFaultRoot) Remove(name string) error {
	if r.removeFn != nil {
		return r.removeFn(name)
	}
	return r.base.Remove(name)
}
func (r *auditFaultRoot) ReadDir(name string) ([]fs.DirEntry, error) {
	if r.readFn != nil {
		return r.readFn(name)
	}
	return r.base.ReadDir(name)
}
func (r *auditFaultRoot) Close() error {
	if r.closeFn != nil {
		return r.closeFn()
	}
	return r.base.Close()
}

func TestAuditSinkRoundTripAtoBtoA(t *testing.T) {
	d := t.TempDir()
	ap := filepath.Join(d, "a.jsonl")
	bp := filepath.Join(d, "b.jsonl")
	a := newAuditLog(16)
	pa, err := a.prepareTransition(mustAuditCfg(t, ap, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	pa.commit()
	a.record(AuditEvent{Operation: "a1", Result: "success"})
	pb, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	pb.commit()
	pb.retire(context.Background())
	a.record(AuditEvent{Operation: "b", Result: "success"})
	pa2, err := a.prepareTransition(mustAuditCfg(t, ap, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	pa2.commit()
	pa2.retire(context.Background())
	a.record(AuditEvent{Operation: "a2", Result: "success"})
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if got := readAuditIDs(t, ap); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("A ids=%v", got)
	}
	if got := readAuditIDs(t, bp); len(got) != 1 || got[0] != 2 {
		t.Fatalf("B ids=%v", got)
	}
}

func TestAuditSinkAbortAndCommitAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	p.abort()
	p.abort()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("abort left candidate: %v", err)
	}
	p2, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	p2.commit()
	gen := a.currentSink.id
	p2.commit()
	if a.currentSink.id != gen {
		t.Fatal("second commit churned generation")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAuditSinkShortWriteIsCountedAndRingSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	base := a.currentSink.owner.file
	a.currentSink.owner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) { return len(p) - 1, nil }}
	a.record(AuditEvent{Operation: "short", Result: "success"})
	st := a.statusReport()
	if st == nil || st.WriteFailures != 1 || st.LastFailureCategory != string(auditFailureWrite) {
		t.Fatalf("status=%+v", st)
	}
	if got := a.snapshot("", "", 0); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("ring=%+v", got)
	}
	_ = a.Close()
}

func TestAuditSinkInjectedWriteErrorIsCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	base := a.currentSink.owner.file
	a.currentSink.owner.file = &auditFaultFile{base: base, writeFn: func([]byte) (int, error) { return 0, errors.New("injected write") }}
	a.record(AuditEvent{Operation: "write-error", Result: "success"})
	st := a.statusReport()
	if st == nil || st.WriteFailures != 1 || st.Healthy {
		t.Fatalf("status=%+v", st)
	}
	_ = a.Close()
}

func TestAuditSinkCloseFailureIsAdvisoryToNewGeneration(t *testing.T) {
	d := t.TempDir()
	ap := filepath.Join(d, "a.jsonl")
	bp := filepath.Join(d, "b.jsonl")
	a := newAuditLogWithSink(8, ap, 10, 4, nil)
	old := a.currentSink.owner
	base := old.file
	old.file = &auditFaultFile{base: base, closeFn: func() error { _ = base.Close(); return errors.New("injected close") }}
	p, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	p.commit()
	p.retire(context.Background())
	st := a.statusReport()
	if st == nil || !st.Healthy || st.RetireFailures != 1 {
		t.Fatalf("new generation poisoned by close failure: %+v", st)
	}
	_ = a.Close()
}

func TestAuditSinkBlockedWriteDoesNotBlockPublishAndEventuallyReleasesOldOwner(t *testing.T) {
	d := t.TempDir()
	ap := filepath.Join(d, "a.jsonl")
	bp := filepath.Join(d, "b.jsonl")
	a := newAuditLogWithSink(8, ap, 10, 4, nil)
	oldGen := a.currentSink
	oldOwner := oldGen.owner
	base := oldOwner.file
	started := make(chan struct{})
	release := make(chan struct{})
	recordDone := make(chan struct{})
	var once sync.Once
	oldOwner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) { once.Do(func() { close(started) }); <-release; return base.Write(p) }}
	go func() { a.record(AuditEvent{Operation: "blocked", Result: "success"}); close(recordDone) }()
	<-started
	p, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	publishDone := make(chan struct{})
	go func() { p.commit(); close(publishDone) }()
	<-publishDone
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.retire(ctx)
	if a.retirementFailures != 1 {
		t.Fatalf("retire failures=%d", a.retirementFailures)
	}
	close(release)
	<-recordDone
	oldOwner.lifeMu.Lock()
	closed, refs := oldOwner.closed, oldOwner.refs
	oldOwner.lifeMu.Unlock()
	if !closed || refs != 0 {
		t.Fatalf("old owner not released after drain: closed=%v refs=%d", closed, refs)
	}
	_ = a.Close()
}

func TestAuditSinkBlockedCloseIsBoundedAndCompletesExactlyOnce(t *testing.T) {
	d := t.TempDir()
	ap := filepath.Join(d, "a.jsonl")
	bp := filepath.Join(d, "b.jsonl")
	a := newAuditLogWithSink(8, ap, 10, 4, nil)
	old := a.currentSink.owner
	base := old.file
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	var once sync.Once
	calls := 0
	old.file = &auditFaultFile{base: base, closeFn: func() error {
		calls++
		once.Do(func() { close(started) })
		<-release
		err := base.Close()
		close(done)
		return err
	}}
	p, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	p.commit()
	ctx, cancel := context.WithCancel(context.Background())
	retired := make(chan struct{})
	go func() { p.retire(ctx); close(retired) }()
	<-started
	cancel()
	<-retired
	if a.retirementFailures != 1 {
		t.Fatalf("retire failures=%d", a.retirementFailures)
	}
	close(release)
	<-done
	if calls != 1 {
		t.Fatalf("close calls=%d want 1", calls)
	}
	_ = a.Close()
}

func TestAuditSinkShutdownTimeoutDefersReleaseToLastWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	owner := a.currentSink.owner
	base := owner.file
	started := make(chan struct{})
	release := make(chan struct{})
	recordDone := make(chan struct{})
	var once sync.Once
	owner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) { once.Do(func() { close(started) }); <-release; return base.Write(p) }}
	go func() { a.record(AuditEvent{Operation: "shutdown", Result: "success"}); close(recordDone) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := a.closeWithContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close err=%v", err)
	}
	close(release)
	<-recordDone
	owner.lifeMu.Lock()
	closed, refs := owner.closed, owner.refs
	owner.lifeMu.Unlock()
	if !closed || refs != 0 {
		t.Fatalf("shutdown owner leaked: closed=%v refs=%d", closed, refs)
	}
}

func TestAuditSinkIdentityRaceIsRejected(t *testing.T) {
	d := t.TempDir()
	baseName := "audit.jsonl"
	path := filepath.Join(d, baseName)
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	real := &osAuditRootHandle{root: root}
	swapped := false
	fault := &auditFaultRoot{base: real}
	fault.openFn = func(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
		if !swapped && name == baseName {
			swapped = true
			if err := real.Rename(baseName, "old.jsonl"); err != nil {
				return nil, err
			}
			f, err := real.OpenFile(baseName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o640)
			if err != nil {
				return nil, err
			}
			_ = f.Close()
		}
		return real.OpenFile(name, flag, perm)
	}
	if _, err := prepareAuditFileOwnerAtRoot(fault, baseName, path, nil); err == nil {
		t.Fatal("identity swap accepted")
	}
}

func TestAuditSinkRotationFailureAndRecoveryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	seed := make([]byte, 1024*1024-8)
	if err := os.WriteFile(path, seed, 0o640); err != nil {
		t.Fatal(err)
	}
	a := newAuditLogWithSink(8, path, 1, 2, nil)
	owner := a.currentSink.owner
	root := owner.root
	owner.root = &auditFaultRoot{base: root, renameFn: func(string, string) error { return errors.New("injected rotate") }}
	a.record(AuditEvent{Operation: "rotate-error", Result: "success"})
	st := a.statusReport()
	if st == nil || st.RotateFailures != 1 || st.LastFailureCategory != string(auditFailureRotate) {
		t.Fatalf("status=%+v", st)
	}
	owner.root = root
	_ = a.Close()
}

func TestAuditSinkCleanupFailureDoesNotLoseWrittenEvent(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "audit.jsonl")
	seed := make([]byte, 1024*1024-8)
	if err := os.WriteFile(path, seed, 0o640); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"audit-2026-01-01T00-00-00.000.jsonl", "audit-2026-01-02T00-00-00.000.jsonl"} {
		if err := os.WriteFile(filepath.Join(d, name), []byte("backup"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	a := newAuditLogWithSink(8, path, 1, 1, nil)
	owner := a.currentSink.owner
	root := owner.root
	owner.root = &auditFaultRoot{base: root, removeFn: func(string) error { return errors.New("injected cleanup") }}
	a.record(AuditEvent{Operation: "cleanup-error", Result: "success"})
	st := a.statusReport()
	if st == nil || st.CleanupFailures != 1 || st.LastFailureCategory != string(auditFailureCleanup) {
		t.Fatalf("status=%+v", st)
	}
	owner.root = root
	if ids := readAuditIDs(t, path); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("durable ids=%v", ids)
	}
	_ = a.Close()
}

func TestAuditSinkRotationBoundariesAndBackupCollision(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "audit.jsonl")
	payload := []byte("12345678")
	max := int64(1024 * 1024)
	if err := os.WriteFile(path, make([]byte, max-int64(len(payload))), 0o640); err != nil {
		t.Fatal(err)
	}
	owner, err := prepareAuditFileOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := mustAuditCfg(t, path, 1, 2)
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	owner.now = func() time.Time { return fixed }
	collision := "audit-" + fixed.Format(auditBackupTimeFormat) + ".jsonl"
	if err := os.WriteFile(filepath.Join(d, collision), []byte("collision"), 0o640); err != nil {
		t.Fatal(err)
	}
	if got := owner.write(0, cfg, payload); got.err != nil {
		t.Fatal(got.err)
	}
	if _, err := os.Stat(filepath.Join(d, "audit-"+fixed.Add(time.Millisecond).Format(auditBackupTimeFormat)+".jsonl")); err != nil {
		t.Fatalf("collision fallback backup missing: %v", err)
	}
	_ = owner.release(context.Background(), true)

	path2 := filepath.Join(d, "second.jsonl")
	if err := os.WriteFile(path2, make([]byte, max-int64(len(payload))), 0o640); err != nil {
		t.Fatal(err)
	}
	owner2, err := prepareAuditFileOwner(path2)
	if err != nil {
		t.Fatal(err)
	}
	owner2.firstWrite = false
	if got := owner2.write(0, mustAuditCfg(t, path2, 1, 2), payload); got.err != nil {
		t.Fatal(got.err)
	}
	if owner2.size != max {
		t.Fatalf("exact boundary size=%d", owner2.size)
	}
	if got := owner2.write(1, mustAuditCfg(t, path2, 1, 2), []byte("x")); got.err != nil {
		t.Fatal(got.err)
	}
	if owner2.size != 1 {
		t.Fatalf("greater-than boundary did not rotate; size=%d", owner2.size)
	}
	_ = owner2.release(context.Background(), true)
}

func TestAuditSinkRootOpenFailureIsTyped(t *testing.T) {
	d := t.TempDir()
	root, err := os.OpenRoot(d)
	if err != nil {
		t.Fatal(err)
	}
	real := &osAuditRootHandle{root: root}
	fault := &auditFaultRoot{base: real, openFn: func(string, int, fs.FileMode) (auditFileHandle, error) { return nil, errors.New("injected open") }}
	_, err = prepareAuditFileOwnerAtRoot(fault, "audit.jsonl", filepath.Join(d, "audit.jsonl"), nil)
	if err == nil {
		t.Fatal("open failure accepted")
	}
}

func TestAuditSinkOversizedEventFailsWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	owner, err := prepareAuditFileOwner(path)
	if err != nil {
		t.Fatal(err)
	}
	got := owner.write(0, mustAuditCfg(t, path, 1, 2), make([]byte, 1024*1024+1))
	if got.err == nil || !errors.Is(got.err, io.ErrShortWrite) && got.category != auditFailureWrite {
		if got.category != auditFailureWrite {
			t.Fatalf("result=%+v", got)
		}
	}
	_ = owner.release(context.Background(), true)
}

func TestAuditSinkRecoveredHealthPreservesLastFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	owner := a.currentSink.owner
	base := owner.file
	failed := true
	owner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) {
		if failed {
			failed = false
			return 0, errors.New("one-shot")
		}
		return base.Write(p)
	}}
	a.record(AuditEvent{Operation: "fail", Result: "success"})
	a.record(AuditEvent{Operation: "recover", Result: "success"})
	st := a.statusReport()
	if st == nil || !st.Healthy || st.WriteFailures != 1 || st.LastFailureCategory != string(auditFailureWrite) || st.LastFailureAt.IsZero() {
		t.Fatalf("recovered status=%+v", st)
	}
	_ = a.Close()
}

func TestAuditSinkRetiredCloseFailureIsHistoricalButNotActive(t *testing.T) {
	d := t.TempDir()
	a := newAuditLogWithSink(8, filepath.Join(d, "a.jsonl"), 10, 4, nil)
	old := a.currentSink.owner
	base := old.file
	old.file = &auditFaultFile{base: base, closeFn: func() error { _ = base.Close(); return errors.New("close-history") }}
	p, err := a.prepareTransition(mustAuditCfg(t, filepath.Join(d, "b.jsonl"), 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	p.commit()
	p.retire(context.Background())
	st := a.statusReport()
	if st == nil || !st.Healthy || st.RetireFailures != 1 || st.LastFailureCategory != string(auditFailureClose) || st.LastFailureAt.IsZero() {
		t.Fatalf("status=%+v", st)
	}
	_ = a.Close()
}
