from pathlib import Path

p = Path("internal/admin/audit_sink_runtime.go")
s = p.read_text()

# Narrow production abstractions around only the filesystem capabilities the
# audit writer needs. They make failure behavior deterministic without turning
# the sink into a generic storage framework.
anchor = '''type createdAuditDir struct {
\tname string
\tinfo fs.FileInfo
}

type auditFileOwner struct {'''
insert = '''type createdAuditDir struct {
\tname string
\tinfo fs.FileInfo
}

type auditFileHandle interface {
\tWrite([]byte) (int, error)
\tClose() error
\tStat() (fs.FileInfo, error)
}

type auditRootHandle interface {
\tLstat(string) (fs.FileInfo, error)
\tOpenFile(string, int, fs.FileMode) (auditFileHandle, error)
\tRename(string, string) error
\tRemove(string) error
\tReadDir(string) ([]fs.DirEntry, error)
\tClose() error
}

type osAuditRootHandle struct{ root *os.Root }

func (r *osAuditRootHandle) Lstat(name string) (fs.FileInfo, error) { return r.root.Lstat(name) }
func (r *osAuditRootHandle) OpenFile(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
\treturn r.root.OpenFile(name, flag, perm)
}
func (r *osAuditRootHandle) Rename(oldName, newName string) error { return r.root.Rename(oldName, newName) }
func (r *osAuditRootHandle) Remove(name string) error              { return r.root.Remove(name) }
func (r *osAuditRootHandle) ReadDir(name string) ([]fs.DirEntry, error) {
\treturn fs.ReadDir(r.root.FS(), name)
}
func (r *osAuditRootHandle) Close() error { return r.root.Close() }

type auditFileOwner struct {'''
if anchor not in s:
    raise SystemExit("interface anchor missing")
s = s.replace(anchor, insert, 1)
s = s.replace('\troot *os.Root\n\tfile *os.File\n', '\troot auditRootHandle\n\tfile auditFileHandle\n', 1)

# A retired generation requests release once. If its bounded retire call times
# out waiting for a selected write, the final writer completion performs the
# release outside auditLog.mu instead of leaking the old owner.
s = s.replace('''\tinflight int
\tretired  bool
\tdrained  chan struct{}
}''', '''\tinflight         int
\tretired          bool
\treleaseRequested bool
\tdrained          chan struct{}
\treleaseOnce      sync.Once
}''', 1)

start = s.index('func (p *preparedAuditSink) retire(ctx context.Context) {')
end = s.index('\nfunc (a *auditLog) Close() error {', start)
s = s[:start] + '''func (p *preparedAuditSink) retire(ctx context.Context) {
\tif p == nil || !p.committed || p.old == nil {
\t\treturn
\t}
\tp.retireOnce.Do(func() {
\t\ta := p.log
\t\ta.mu.Lock()
\t\tp.old.releaseRequested = true
\t\tdrained := p.old.inflight == 0
\t\ta.mu.Unlock()
\t\tif !drained {
\t\t\tselect {
\t\t\tcase <-p.old.drained:
\t\t\tcase <-ctx.Done():
\t\t\t\ta.noteRetirementFailure(auditFailureRetirement, ctx.Err())
\t\t\t\treturn
\t\t\t}
\t\t}
\t\tif err := a.releaseGeneration(p.old, ctx); err != nil {
\t\t\tcategory := auditFailureClose
\t\t\tif errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
\t\t\t\tcategory = auditFailureRetirement
\t\t\t}
\t\t\ta.noteRetirementFailure(category, err)
\t\t}
\t})
}

func (a *auditLog) releaseGeneration(gen *auditSinkGeneration, ctx context.Context) error {
\tif gen == nil {
\t\treturn nil
\t}
\tvar releaseErr error
\tgen.releaseOnce.Do(func() {
\t\treleaseErr = gen.owner.release(ctx, true)
\t})
\treturn releaseErr
}
''' + s[end:]

# Shutdown uses the same deferred-release rule. The public Close keeps its
# existing five-second bound; tests and lifecycle callers can use the helper.
start = s.index('func (a *auditLog) Close() error {')
end = s.index('\nfunc (a *auditLog) statusReport()', start)
s = s[:start] + '''func (a *auditLog) Close() error {
\tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
\tdefer cancel()
\treturn a.closeWithContext(ctx)
}

func (a *auditLog) closeWithContext(ctx context.Context) error {
\ta.mu.Lock()
\told := a.currentSink
\ta.currentSink = nil
\tif old != nil {
\t\told.releaseRequested = true
\t\tif !old.retired {
\t\t\told.retired = true
\t\t\tif old.inflight == 0 {
\t\t\t\tclose(old.drained)
\t\t\t}
\t\t}
\t}
\ta.mu.Unlock()
\tif old == nil {
\t\treturn nil
\t}
\tselect {
\tcase <-old.drained:
\tcase <-ctx.Done():
\t\treturn ctx.Err()
\t}
\treturn a.releaseGeneration(old, ctx)
}
''' + s[end:]

# The last selected write owns deferred release after a bounded retire/shutdown
# timeout. Disk close remains outside the global event/ID lock.
start = s.index('func (a *auditLog) completeWrite(gen *auditSinkGeneration, result auditWriteResult) {')
end = s.index('\nfunc (a *auditLog) noteRetirementFailure', start)
s = s[:start] + '''func (a *auditLog) completeWrite(gen *auditSinkGeneration, result auditWriteResult) {
\ta.mu.Lock()
\tgen.inflight--
\tif gen.retired && gen.inflight == 0 {
\t\tclose(gen.drained)
\t}
\tshouldRelease := gen.retired && gen.releaseRequested && gen.inflight == 0
\tif result.err != nil {
\t\tswitch result.category {
\t\tcase auditFailureRotate:
\t\t\ta.rotateFailures++
\t\tcase auditFailureCleanup:
\t\t\ta.cleanupFailures++
\t\tdefault:
\t\t\ta.writeFailures++
\t\t}
\t\tif a.currentSink == gen {
\t\t\ta.activeFailure = result.category
\t\t\ta.activeFailureAt = time.Now().UTC()
\t\t}
\t} else if a.currentSink == gen {
\t\ta.activeFailure = ""
\t\ta.activeFailureAt = time.Time{}
\t}
\ta.mu.Unlock()

\tif result.err != nil {
\t\tauditLogWarn(a.log, "audit sink persistence failed", gen.cfg.publicPath, result.err)
\t}
\tif shouldRelease {
\t\tctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
\t\tdefer cancel()
\t\tif err := a.releaseGeneration(gen, ctx); err != nil {
\t\t\tcategory := auditFailureClose
\t\t\tif errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
\t\t\t\tcategory = auditFailureRetirement
\t\t\t}
\t\t\ta.noteRetirementFailure(category, err)
\t\t}
\t}
}
''' + s[end:]

s = s.replace('entries, err := fs.ReadDir(o.root.FS(), ".")', 'entries, err := o.root.ReadDir(".")', 1)

# Split real parent acquisition from destination validation so tests can inject
# precise identity/open/rotation failures through the same production logic.
start = s.index('func prepareAuditFileOwner(path string) (*auditFileOwner, error) {')
end = s.index('\nfunc prepareAuditParent(', start)
new_prepare = r'''func prepareAuditFileOwner(path string) (*auditFileOwner, error) {
	parent := filepath.Dir(path)
	base := filepath.Base(path)
	root, createdDirs, err := prepareAuditParent(parent)
	if err != nil {
		return nil, &auditPathError{err: err}
	}
	return prepareAuditFileOwnerAtRoot(root, base, path, createdDirs)
}

func prepareAuditFileOwnerAtRoot(root auditRootHandle, base, path string, createdDirs []createdAuditDir) (*auditFileOwner, error) {
	cleanupRoot := true
	defer func() {
		if cleanupRoot {
			_ = root.Close()
		}
	}()

	o := &auditFileOwner{root: root, base: base, path: path, refs: 1, createdDirs: createdDirs, now: time.Now, firstWrite: true}
	o.cond = sync.NewCond(&o.mu)
	info, err := root.Lstat(base)
	created := false
	if errors.Is(err, fs.ErrNotExist) {
		f, openErr := root.OpenFile(base, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o640)
		if openErr != nil {
			o.cleanupCandidate()
			return nil, fmt.Errorf("create audit file: %w", openErr)
		}
		o.file = f
		created = true
		info, err = f.Stat()
		if err != nil {
			_ = f.Close()
			o.file = nil
			o.cleanupCandidate()
			return nil, fmt.Errorf("stat created audit file: %w", err)
		}
		o.createdFile = true
		o.createdInfo = info
	} else if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			o.cleanupCandidate()
			return nil, &auditPathError{err: fmt.Errorf("destination is not a regular file")}
		}
		f, openErr := root.OpenFile(base, os.O_WRONLY|os.O_APPEND, 0)
		if openErr != nil {
			o.cleanupCandidate()
			return nil, fmt.Errorf("open audit file: %w", openErr)
		}
		o.file = f
		opened, statErr := f.Stat()
		post, postErr := root.Lstat(base)
		if statErr != nil || postErr != nil || !os.SameFile(info, opened) || !os.SameFile(opened, post) {
			_ = f.Close()
			o.file = nil
			o.cleanupCandidate()
			return nil, &auditPathError{err: errors.New("audit destination changed while opening")}
		}
		info = opened
	} else {
		o.cleanupCandidate()
		return nil, fmt.Errorf("lstat audit file: %w", err)
	}
	post, postErr := root.Lstat(base)
	if postErr != nil || !os.SameFile(info, post) {
		_ = o.file.Close()
		o.file = nil
		o.cleanupCandidate()
		return nil, &auditPathError{err: errors.New("audit destination identity changed during preparation")}
	}
	o.mode = info.Mode()
	o.size = info.Size()
	o.createdFile = created
	o.createdInfo = info
	cleanupRoot = false
	return o, nil
}
'''
s = s[:start] + new_prepare + s[end:]

s = s.replace('func prepareAuditParent(parent string) (*os.Root, []createdAuditDir, error) {', 'func prepareAuditParent(parent string) (auditRootHandle, []createdAuditDir, error) {', 1)
s = s.replace('\t\treturn root, nil, nil\n', '\t\treturn &osAuditRootHandle{root: root}, nil, nil\n', 1)
s = s.replace('\treturn parentRoot, created, nil\n', '\treturn &osAuditRootHandle{root: parentRoot}, created, nil\n', 1)

# Bounded close owns eventual root cleanup even if the caller times out. That
# prevents a retirement timeout from orphaning the directory FD once Close
# eventually returns.
start = s.index('func (o *auditFileOwner) release(ctx context.Context, committed bool) error {')
end = s.index('\nfunc (o *auditFileOwner) cleanupCandidate()', start)
s = s[:start] + r'''func (o *auditFileOwner) release(ctx context.Context, committed bool) error {
	o.lifeMu.Lock()
	if o.refs > 0 {
		o.refs--
	}
	if o.refs != 0 || o.closed {
		o.lifeMu.Unlock()
		return nil
	}
	o.closed = true
	shouldCleanup := !o.live && !committed
	o.lifeMu.Unlock()

	// Release is never invoked from Publish. The cleanup operation itself may
	// outlive a bounded retirement caller if the OS Close blocks, but it keeps
	// ownership of the root and completes exactly once when Close returns.
	o.mu.Lock()
	file := o.file
	o.file = nil
	o.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		var releaseErr error
		if file != nil {
			releaseErr = file.Close()
		}
		if shouldCleanup {
			o.cleanupCandidate()
		}
		if o.root != nil {
			if err := o.root.Close(); releaseErr == nil {
				releaseErr = err
			}
		}
		done <- releaseErr
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
''' + s[end:]

p.write_text(s)

# Deterministic writer/failure/concurrency matrix.
Path("internal/admin/issue160_writer_matrix_test.go").write_text(r'''// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
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
	if f.writeFn != nil { return f.writeFn(p) }
	return f.base.Write(p)
}
func (f *auditFaultFile) Close() error {
	if f.closeFn != nil { return f.closeFn() }
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
	if r.lstatFn != nil { return r.lstatFn(name) }
	return r.base.Lstat(name)
}
func (r *auditFaultRoot) OpenFile(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
	if r.openFn != nil { return r.openFn(name, flag, perm) }
	return r.base.OpenFile(name, flag, perm)
}
func (r *auditFaultRoot) Rename(oldName, newName string) error {
	if r.renameFn != nil { return r.renameFn(oldName, newName) }
	return r.base.Rename(oldName, newName)
}
func (r *auditFaultRoot) Remove(name string) error {
	if r.removeFn != nil { return r.removeFn(name) }
	return r.base.Remove(name)
}
func (r *auditFaultRoot) ReadDir(name string) ([]fs.DirEntry, error) {
	if r.readFn != nil { return r.readFn(name) }
	return r.base.ReadDir(name)
}
func (r *auditFaultRoot) Close() error {
	if r.closeFn != nil { return r.closeFn() }
	return r.base.Close()
}

func TestAuditSinkRoundTripAtoBtoA(t *testing.T) {
	d := t.TempDir(); ap := filepath.Join(d, "a.jsonl"); bp := filepath.Join(d, "b.jsonl")
	a := newAuditLog(16)
	pa, err := a.prepareTransition(mustAuditCfg(t, ap, 10, 4)); if err != nil { t.Fatal(err) }; pa.commit()
	a.record(AuditEvent{Operation: "a1", Result: "success"})
	pb, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4)); if err != nil { t.Fatal(err) }; pb.commit(); pb.retire(context.Background())
	a.record(AuditEvent{Operation: "b", Result: "success"})
	pa2, err := a.prepareTransition(mustAuditCfg(t, ap, 10, 4)); if err != nil { t.Fatal(err) }; pa2.commit(); pa2.retire(context.Background())
	a.record(AuditEvent{Operation: "a2", Result: "success"})
	if err := a.Close(); err != nil { t.Fatal(err) }
	if got := readAuditIDs(t, ap); len(got) != 2 || got[0] != 1 || got[1] != 3 { t.Fatalf("A ids=%v", got) }
	if got := readAuditIDs(t, bp); len(got) != 1 || got[0] != 2 { t.Fatalf("B ids=%v", got) }
}

func TestAuditSinkAbortAndCommitAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4)); if err != nil { t.Fatal(err) }
	p.abort(); p.abort()
	if _, err := os.Stat(path); !os.IsNotExist(err) { t.Fatalf("abort left candidate: %v", err) }
	p2, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4)); if err != nil { t.Fatal(err) }
	p2.commit(); gen := a.currentSink.id; p2.commit()
	if a.currentSink.id != gen { t.Fatal("second commit churned generation") }
	if err := a.Close(); err != nil { t.Fatal(err) }
}

func TestAuditSinkShortWriteIsCountedAndRingSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	base := a.currentSink.owner.file
	a.currentSink.owner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) { return len(p)-1, nil }}
	a.record(AuditEvent{Operation: "short", Result: "success"})
	st := a.statusReport(); if st == nil || st.WriteFailures != 1 || st.LastFailureCategory != string(auditFailureWrite) { t.Fatalf("status=%+v", st) }
	if got := a.snapshot("", "", 0); len(got) != 1 || got[0].ID != 1 { t.Fatalf("ring=%+v", got) }
	_ = a.Close()
}

func TestAuditSinkInjectedWriteErrorIsCounted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	base := a.currentSink.owner.file
	a.currentSink.owner.file = &auditFaultFile{base: base, writeFn: func([]byte) (int, error) { return 0, errors.New("injected write") }}
	a.record(AuditEvent{Operation: "write-error", Result: "success"})
	st := a.statusReport(); if st == nil || st.WriteFailures != 1 || st.Healthy { t.Fatalf("status=%+v", st) }
	_ = a.Close()
}

func TestAuditSinkCloseFailureIsAdvisoryToNewGeneration(t *testing.T) {
	d := t.TempDir(); ap := filepath.Join(d, "a.jsonl"); bp := filepath.Join(d, "b.jsonl")
	a := newAuditLogWithSink(8, ap, 10, 4, nil)
	old := a.currentSink.owner; base := old.file
	old.file = &auditFaultFile{base: base, closeFn: func() error { _ = base.Close(); return errors.New("injected close") }}
	p, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4)); if err != nil { t.Fatal(err) }; p.commit(); p.retire(context.Background())
	st := a.statusReport(); if st == nil || !st.Healthy || st.RetireFailures != 1 { t.Fatalf("new generation poisoned by close failure: %+v", st) }
	_ = a.Close()
}

func TestAuditSinkBlockedWriteDoesNotBlockPublishAndEventuallyReleasesOldOwner(t *testing.T) {
	d := t.TempDir(); ap := filepath.Join(d, "a.jsonl"); bp := filepath.Join(d, "b.jsonl")
	a := newAuditLogWithSink(8, ap, 10, 4, nil)
	oldGen := a.currentSink; oldOwner := oldGen.owner; base := oldOwner.file
	started := make(chan struct{}); release := make(chan struct{}); recordDone := make(chan struct{})
	var once sync.Once
	oldOwner.file = &auditFaultFile{base: base, writeFn: func(p []byte) (int, error) { once.Do(func(){close(started)}); <-release; return base.Write(p) }}
	go func(){ a.record(AuditEvent{Operation:"blocked", Result:"success"}); close(recordDone) }()
	<-started
	p, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4)); if err != nil { t.Fatal(err) }
	publishDone := make(chan struct{}); go func(){ p.commit(); close(publishDone) }()
	<-publishDone
	ctx, cancel := context.WithCancel(context.Background()); cancel(); p.retire(ctx)
	if a.retirementFailures != 1 { t.Fatalf("retire failures=%d", a.retirementFailures) }
	close(release); <-recordDone
	oldOwner.lifeMu.Lock(); closed, refs := oldOwner.closed, oldOwner.refs; oldOwner.lifeMu.Unlock()
	if !closed || refs != 0 { t.Fatalf("old owner not released after drain: closed=%v refs=%d", closed, refs) }
	_ = a.Close()
}

func TestAuditSinkBlockedCloseIsBoundedAndCompletesExactlyOnce(t *testing.T) {
	d := t.TempDir(); ap := filepath.Join(d, "a.jsonl"); bp := filepath.Join(d, "b.jsonl")
	a := newAuditLogWithSink(8, ap, 10, 4, nil)
	old := a.currentSink.owner; base := old.file
	started := make(chan struct{}); release := make(chan struct{}); done := make(chan struct{}); var once sync.Once; calls := 0
	old.file = &auditFaultFile{base: base, closeFn: func() error { calls++; once.Do(func(){close(started)}); <-release; err:=base.Close(); close(done); return err }}
	p, err := a.prepareTransition(mustAuditCfg(t, bp, 10, 4)); if err != nil { t.Fatal(err) }; p.commit()
	ctx, cancel := context.WithCancel(context.Background())
	retired := make(chan struct{}); go func(){ p.retire(ctx); close(retired) }()
	<-started; cancel(); <-retired
	if a.retirementFailures != 1 { t.Fatalf("retire failures=%d", a.retirementFailures) }
	close(release); <-done
	if calls != 1 { t.Fatalf("close calls=%d want 1", calls) }
	_ = a.Close()
}

func TestAuditSinkShutdownTimeoutDefersReleaseToLastWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	owner := a.currentSink.owner; base := owner.file
	started := make(chan struct{}); release := make(chan struct{}); recordDone := make(chan struct{}); var once sync.Once
	owner.file = &auditFaultFile{base: base, writeFn: func(p []byte)(int,error){ once.Do(func(){close(started)}); <-release; return base.Write(p) }}
	go func(){ a.record(AuditEvent{Operation:"shutdown", Result:"success"}); close(recordDone) }(); <-started
	ctx, cancel := context.WithCancel(context.Background()); cancel()
	if err := a.closeWithContext(ctx); !errors.Is(err, context.Canceled) { t.Fatalf("close err=%v", err) }
	close(release); <-recordDone
	owner.lifeMu.Lock(); closed, refs := owner.closed, owner.refs; owner.lifeMu.Unlock()
	if !closed || refs != 0 { t.Fatalf("shutdown owner leaked: closed=%v refs=%d", closed, refs) }
}

func TestAuditSinkIdentityRaceIsRejected(t *testing.T) {
	d := t.TempDir(); baseName := "audit.jsonl"; path := filepath.Join(d, baseName)
	if err := os.WriteFile(path, []byte("old"), 0o640); err != nil { t.Fatal(err) }
	root, err := os.OpenRoot(d); if err != nil { t.Fatal(err) }
	real := &osAuditRootHandle{root: root}; swapped := false
	fault := &auditFaultRoot{base: real}
	fault.openFn = func(name string, flag int, perm fs.FileMode) (auditFileHandle,error) {
		if !swapped && name == baseName { swapped = true; if err:=real.Rename(baseName,"old.jsonl"); err!=nil{return nil,err}; f,err:=real.OpenFile(baseName,os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND,0o640); if err!=nil{return nil,err}; _=f.Close() }
		return real.OpenFile(name,flag,perm)
	}
	if _, err := prepareAuditFileOwnerAtRoot(fault, baseName, path, nil); err == nil { t.Fatal("identity swap accepted") }
}

func TestAuditSinkRotationFailureAndRecoveryPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	seed := make([]byte, 1024*1024-8); if err:=os.WriteFile(path,seed,0o640); err!=nil{t.Fatal(err)}
	a := newAuditLogWithSink(8,path,1,2,nil); owner:=a.currentSink.owner; root:=owner.root
	owner.root=&auditFaultRoot{base:root, renameFn:func(string,string)error{return errors.New("injected rotate")}}
	a.record(AuditEvent{Operation:"rotate-error",Result:"success"})
	st:=a.statusReport(); if st==nil || st.RotateFailures!=1 || st.LastFailureCategory!=string(auditFailureRotate){t.Fatalf("status=%+v",st)}
	owner.root=root
	_ = a.Close()
}

func TestAuditSinkCleanupFailureDoesNotLoseWrittenEvent(t *testing.T) {
	d:=t.TempDir(); path:=filepath.Join(d,"audit.jsonl")
	seed:=make([]byte,1024*1024-8); if err:=os.WriteFile(path,seed,0o640); err!=nil{t.Fatal(err)}
	for _, name := range []string{"audit-2026-01-01T00-00-00.000.jsonl","audit-2026-01-02T00-00-00.000.jsonl"} { if err:=os.WriteFile(filepath.Join(d,name),[]byte("backup"),0o640); err!=nil{t.Fatal(err)} }
	a:=newAuditLogWithSink(8,path,1,1,nil); owner:=a.currentSink.owner; root:=owner.root
	owner.root=&auditFaultRoot{base:root, removeFn:func(string)error{return errors.New("injected cleanup")}}
	a.record(AuditEvent{Operation:"cleanup-error",Result:"success"})
	st:=a.statusReport(); if st==nil || st.CleanupFailures!=1 || st.LastFailureCategory!=string(auditFailureCleanup){t.Fatalf("status=%+v",st)}
	owner.root=root
	if ids:=readAuditIDs(t,path); len(ids)!=1 || ids[0]!=1 { t.Fatalf("durable ids=%v",ids) }
	_ = a.Close()
}

func TestAuditSinkRotationBoundariesAndBackupCollision(t *testing.T) {
	d:=t.TempDir(); path:=filepath.Join(d,"audit.jsonl"); payload:=[]byte("12345678")
	max:=int64(1024*1024)
	if err:=os.WriteFile(path,make([]byte,max-int64(len(payload))),0o640); err!=nil{t.Fatal(err)}
	owner,err:=prepareAuditFileOwner(path); if err!=nil{t.Fatal(err)}; cfg:=mustAuditCfg(t,path,1,2)
	fixed:=time.Date(2026,1,2,3,4,5,0,time.Local); owner.now=func()time.Time{return fixed}
	collision:="audit-"+fixed.Format(auditBackupTimeFormat)+".jsonl"; if err:=os.WriteFile(filepath.Join(d,collision),[]byte("collision"),0o640);err!=nil{t.Fatal(err)}
	if got:=owner.write(0,cfg,payload); got.err!=nil{t.Fatal(got.err)}
	if _,err:=os.Stat(filepath.Join(d,"audit-"+fixed.Add(time.Millisecond).Format(auditBackupTimeFormat)+".jsonl"));err!=nil{t.Fatalf("collision fallback backup missing: %v",err)}
	_ = owner.release(context.Background(),true)

	path2:=filepath.Join(d,"second.jsonl"); if err:=os.WriteFile(path2,make([]byte,max-int64(len(payload))),0o640);err!=nil{t.Fatal(err)}
	owner2,err:=prepareAuditFileOwner(path2);if err!=nil{t.Fatal(err)}; owner2.firstWrite=false
	if got:=owner2.write(0,mustAuditCfg(t,path2,1,2),payload);got.err!=nil{t.Fatal(got.err)}
	if owner2.size!=max{t.Fatalf("exact boundary size=%d",owner2.size)}
	if got:=owner2.write(1,mustAuditCfg(t,path2,1,2),[]byte("x"));got.err!=nil{t.Fatal(got.err)}
	if owner2.size!=1{t.Fatalf("greater-than boundary did not rotate; size=%d",owner2.size)}
	_ = owner2.release(context.Background(),true)
}

func TestAuditSinkRootOpenFailureIsTyped(t *testing.T) {
	d:=t.TempDir(); root,err:=os.OpenRoot(d);if err!=nil{t.Fatal(err)}; real:=&osAuditRootHandle{root:root}
	fault:=&auditFaultRoot{base:real, openFn:func(string,int,fs.FileMode)(auditFileHandle,error){return nil,errors.New("injected open")}}
	_,err=prepareAuditFileOwnerAtRoot(fault,"audit.jsonl",filepath.Join(d,"audit.jsonl"),nil)
	if err==nil{t.Fatal("open failure accepted")}
}

func TestAuditSinkOversizedEventFailsWithoutWriting(t *testing.T) {
	path:=filepath.Join(t.TempDir(),"audit.jsonl"); owner,err:=prepareAuditFileOwner(path);if err!=nil{t.Fatal(err)}
	got:=owner.write(0,mustAuditCfg(t,path,1,2),make([]byte,1024*1024+1))
	if got.err==nil || !errors.Is(got.err,io.ErrShortWrite) && got.category!=auditFailureWrite { if got.category!=auditFailureWrite {t.Fatalf("result=%+v",got)} }
	_ = owner.release(context.Background(),true)
}
''')

print("HR-07C writer hardening staged")
