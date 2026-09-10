// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

type auditStatFaultFile struct {
	auditFileHandle
	err error
}

func (f *auditStatFaultFile) Stat() (fs.FileInfo, error) { return nil, f.err }

func TestIssue160CoverageAdminRuntimeLifecycleWrappers(t *testing.T) {
	var nilServer *Server
	if err := nilServer.prepareAuditRuntime(config.AdminConfig{}, nil); err != nil {
		t.Fatalf("nil server prepare: %v", err)
	}

	s := &Server{audit: newAuditLog(8)}
	badResolved := PrepareAuth(config.AdminConfig{}, nil)
	if err := s.prepareAuditRuntime(config.AdminConfig{
		AuditLogFile:        "audit.jsonl",
		AuditLogRotateMaxMB: -1,
	}, badResolved); err == nil {
		t.Fatal("negative rotation was accepted by admin runtime prepare")
	}

	d := t.TempDir()
	blocker := filepath.Join(d, "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badPath := PrepareAuth(config.AdminConfig{}, nil)
	if err := s.prepareAuditRuntime(config.AdminConfig{
		AuditLogFile:        filepath.Join(blocker, "audit.jsonl"),
		AuditLogRotateMaxMB: 1,
		AuditLogRotateKeep:  2,
	}, badPath); err == nil {
		t.Fatal("unusable audit parent was accepted by admin runtime prepare")
	}

	s.CommitPreparedAdminRuntime(nil)
	s.AbortPreparedAdminRuntime(nil)
	s.RetirePreparedAdminRuntime(context.Background(), nil)

	cfgA := config.AdminConfig{
		AuditLogFile:        filepath.Join(d, "a.jsonl"),
		AuditLogRotateMaxMB: 1,
		AuditLogRotateKeep:  2,
	}
	preparedA := PrepareAuth(cfgA, nil)
	if err := s.prepareAuditRuntime(cfgA, preparedA); err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(preparedA)

	cfgB := cfgA
	cfgB.AuditLogFile = filepath.Join(d, "b.jsonl")
	preparedB := PrepareAuth(cfgB, nil)
	if err := s.prepareAuditRuntime(cfgB, preparedB); err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(preparedB)
	s.RetirePreparedAdminRuntime(context.Background(), preparedB)

	cfgC := cfgB
	cfgC.AuditLogFile = filepath.Join(d, "c.jsonl")
	preparedC := PrepareAuth(cfgC, nil)
	if err := s.prepareAuditRuntime(cfgC, preparedC); err != nil {
		t.Fatal(err)
	}
	s.AbortPreparedAdminRuntime(preparedC)

	if err := s.audit.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIssue160CoverageAuditHelpersAndPatchValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	startup := newAuditLogWithSink(0, "audit.jsonl", -1, 0, logger)
	if st := startup.statusReport(); st == nil || st.Healthy || st.LastFailureCategory != string(auditFailurePath) {
		t.Fatalf("invalid startup status=%+v", st)
	}
	auditLogWarn(logger, "test warning", "redacted-test-path", errors.New("injected"))

	ring := newAuditLog(1)
	ring.record(AuditEvent{Operation: "wrap", Result: "success"})
	if !ring.full {
		t.Fatal("single-slot ring did not mark itself full")
	}
	if err := ring.releaseGeneration(nil, context.Background()); err != nil {
		t.Fatalf("nil generation release: %v", err)
	}
	ring.forgetClosedAuditOwner("", nil)

	var nilPrepared *preparedAuditSink
	called := false
	nilPrepared.commitWith(func() { called = true })
	if !called {
		t.Fatal("nil prepared sink did not execute publication callback")
	}
	nilPrepared.abort()

	warn := newAuditLog(8)
	warn.log = logger
	warn.warnMu.Lock()
	warn.lastSinkWarning = time.Now().Add(-auditSinkWarnInterval)
	warn.suppressedSinkWarnings = 2
	warn.warnMu.Unlock()
	warn.warnPersistence(
		&auditSinkGeneration{cfg: auditSinkConfig{publicPath: "audit.jsonl"}},
		auditWriteResult{category: auditFailureWrite, err: errors.New("injected write")},
	)
	warn.noteRetirementFailure(auditFailureClose, errors.New("injected close"))

	cleanupCause := errors.New("cleanup")
	cleanupErr := &auditCleanupError{err: cleanupCause}
	if !strings.Contains(cleanupErr.Error(), "cleanup") || !errors.Is(cleanupErr, cleanupCause) {
		t.Fatalf("cleanup error wrapping failed: %v", cleanupErr)
	}
	pathCause := errors.New("path")
	pathErr := &auditPathError{err: pathCause}
	if !strings.Contains(pathErr.Error(), "path") || !errors.Is(pathErr, pathCause) {
		t.Fatalf("path error wrapping failed: %v", pathErr)
	}

	health := &Server{deps: Deps{AdminHealth: func() error { return errors.New("backend health failed") }}}
	if err := health.AdminHealthStatus(); err == nil {
		t.Fatal("generic admin health failure was not surfaced")
	}

	cfg := &config.Config{}
	if _, err := applyPatch(cfg, patchRequest{Op: "admin_audit_sink_set"}); err == nil {
		t.Fatal("missing audit sink payload accepted")
	}
	negKeep := -1
	if _, err := applyPatch(cfg, patchRequest{
		Op:             "admin_audit_sink_set",
		AdminAuditSink: &adminAuditSinkPatch{RotateKeep: &negKeep},
	}); err == nil {
		t.Fatal("negative rotate_keep accepted")
	}
	maxMB := 9
	detail, err := applyPatch(cfg, patchRequest{
		Op:             "admin_audit_sink_set",
		AdminAuditSink: &adminAuditSinkPatch{RotateMaxMB: &maxMB},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.AuditLogRotateMaxMB != maxMB || !strings.Contains(detail, "audit_log_rotate_max_mb") {
		t.Fatalf("rotate_max_mb patch not applied: cfg=%+v detail=%q", cfg.Admin, detail)
	}
}

func TestIssue160CoverageOwnerRegistryAndLowLevelFailures(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "audit.jsonl")
	cfg := mustAuditCfg(t, path, 1, 2)

	// A closed weak owner must be discarded and replaced, not retained.
	a := newAuditLog(8)
	a.sinkOwners[cfg.path] = &auditFileOwner{closed: true}
	prepared, err := a.prepareTransition(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prepared.abort()

	// Publish must also tolerate a lazily absent owner registry.
	b := newAuditLog(8)
	b.sinkOwners = nil
	prepared, err = b.prepareTransition(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prepared.commit()
	if b.sinkOwners == nil {
		t.Fatal("publish did not initialize owner registry")
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	if got := (&auditFileOwner{}).writeLocked(cfg, []byte("x")); got.err == nil || got.category != auditFailureWrite {
		t.Fatalf("nil file write result=%+v", got)
	}
	if _, err := (&auditFileOwner{}).rotateLocked(1); err == nil {
		t.Fatal("rotation without an open file succeeded")
	}

	closePath := filepath.Join(d, "close.jsonl")
	closeOwner, err := prepareAuditFileOwner(closePath)
	if err != nil {
		t.Fatal(err)
	}
	closeBase := closeOwner.file
	closeOwner.file = &auditFaultFile{base: closeBase, closeFn: func() error { return errors.New("close-before-rotate") }}
	if _, err := closeOwner.rotateLocked(1); err == nil {
		t.Fatal("close-before-rotation failure was ignored")
	}
	closeOwner.file = closeBase
	if err := closeOwner.release(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	openPath := filepath.Join(d, "open-after-rotate.jsonl")
	openOwner, err := prepareAuditFileOwner(openPath)
	if err != nil {
		t.Fatal(err)
	}
	realRoot := openOwner.root
	faultRoot := &auditFaultRoot{base: realRoot}
	faultRoot.openFn = func(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
		if flag&os.O_CREATE != 0 {
			return nil, errors.New("open-after-rotate")
		}
		return realRoot.OpenFile(name, flag, perm)
	}
	openOwner.root = faultRoot
	if _, err := openOwner.rotateLocked(1); err == nil {
		t.Fatal("open-after-rotation failure was ignored")
	}
	if openOwner.file == nil {
		t.Fatal("rotation rollback did not reopen the historical active file")
	}

	openOwner.root = &auditFaultRoot{base: realRoot, lstatFn: func(string) (fs.FileInfo, error) {
		return nil, errors.New("backup-lstat")
	}}
	if _, err := openOwner.nextBackupNameLocked(); err == nil {
		t.Fatal("backup lstat failure was ignored")
	}
	openOwner.root = realRoot
	if err := openOwner.pruneLocked(0); err != nil {
		t.Fatal(err)
	}
	openOwner.root = &auditFaultRoot{base: realRoot, readFn: func(string) ([]fs.DirEntry, error) {
		return nil, errors.New("readdir")
	}}
	if err := openOwner.pruneLocked(1); err == nil {
		t.Fatal("retention readdir failure was ignored")
	}
	openOwner.root = realRoot
	if err := os.Mkdir(filepath.Join(d, "ignored-directory"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := openOwner.pruneLocked(1); err != nil {
		t.Fatal(err)
	}
	if err := openOwner.release(context.Background(), true); err != nil {
		t.Fatal(err)
	}

	if _, err := validateAuditDirChain("."); err != nil {
		t.Fatalf("relative directory chain validation failed: %v", err)
	}
}

func TestIssue160CoveragePreparedFileFailureBranches(t *testing.T) {
	// Existing destination whose append open fails.
	d1 := t.TempDir()
	path1 := filepath.Join(d1, "audit.jsonl")
	if err := os.WriteFile(path1, []byte("existing"), 0o640); err != nil {
		t.Fatal(err)
	}
	root1, err := os.OpenRoot(d1)
	if err != nil {
		t.Fatal(err)
	}
	real1 := &osAuditRootHandle{root: root1}
	fault1 := &auditFaultRoot{base: real1, openFn: func(string, int, fs.FileMode) (auditFileHandle, error) {
		return nil, errors.New("existing-open")
	}}
	if _, err := prepareAuditFileOwnerAtRoot(fault1, "audit.jsonl", path1, nil); err == nil {
		t.Fatal("existing destination open failure accepted")
	}

	// Generic Lstat failure is distinct from an absent file.
	d2 := t.TempDir()
	root2, err := os.OpenRoot(d2)
	if err != nil {
		t.Fatal(err)
	}
	real2 := &osAuditRootHandle{root: root2}
	fault2 := &auditFaultRoot{base: real2, lstatFn: func(string) (fs.FileInfo, error) {
		return nil, errors.New("lstat")
	}}
	if _, err := prepareAuditFileOwnerAtRoot(fault2, "audit.jsonl", filepath.Join(d2, "audit.jsonl"), nil); err == nil {
		t.Fatal("generic lstat failure accepted")
	}

	// A file successfully created but failing Stat must abort preparation.
	d3 := t.TempDir()
	root3, err := os.OpenRoot(d3)
	if err != nil {
		t.Fatal(err)
	}
	real3 := &osAuditRootHandle{root: root3}
	fault3 := &auditFaultRoot{base: real3}
	fault3.openFn = func(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
		f, err := real3.OpenFile(name, flag, perm)
		if err != nil {
			return nil, err
		}
		return &auditStatFaultFile{auditFileHandle: f, err: errors.New("created-stat")}, nil
	}
	if _, err := prepareAuditFileOwnerAtRoot(fault3, "audit.jsonl", filepath.Join(d3, "audit.jsonl"), nil); err == nil {
		t.Fatal("created-file stat failure accepted")
	}

	// A destination that changes identity after the verified open must fail the
	// final identity check as well, not only the open-time check.
	d4 := t.TempDir()
	path4 := filepath.Join(d4, "audit.jsonl")
	if err := os.WriteFile(path4, []byte("existing"), 0o640); err != nil {
		t.Fatal(err)
	}
	root4, err := os.OpenRoot(d4)
	if err != nil {
		t.Fatal(err)
	}
	real4 := &osAuditRootHandle{root: root4}
	calls := 0
	fault4 := &auditFaultRoot{base: real4}
	fault4.lstatFn = func(name string) (fs.FileInfo, error) {
		calls++
		if calls >= 3 {
			return nil, fs.ErrNotExist
		}
		return real4.Lstat(name)
	}
	if _, err := prepareAuditFileOwnerAtRoot(fault4, "audit.jsonl", path4, nil); err == nil {
		t.Fatal("post-open identity disappearance accepted")
	}
}
