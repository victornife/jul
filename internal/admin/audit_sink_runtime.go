// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	auditDefaultRotateMaxMB = 100
	auditDefaultRotateKeep  = 14
	auditBackupTimeFormat   = "2006-01-02T15-04-05.000"
)

type auditSinkConfig struct {
	path       string
	publicPath string
	maxMB      int
	keep       int
}

func resolveAuditSinkConfig(path string, maxMB, keep int) (auditSinkConfig, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return auditSinkConfig{}, nil
	}
	if maxMB < 0 || keep < 0 {
		return auditSinkConfig{}, fmt.Errorf("negative audit rotation value")
	}
	if maxMB == 0 {
		maxMB = auditDefaultRotateMaxMB
	}
	if keep == 0 {
		keep = auditDefaultRotateKeep
	}
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return auditSinkConfig{}, fmt.Errorf("resolve audit path: %w", err)
	}
	return auditSinkConfig{path: abs, publicPath: path, maxMB: maxMB, keep: keep}, nil
}

func (c auditSinkConfig) enabled() bool { return c.path != "" }
func (c auditSinkConfig) equal(other auditSinkConfig) bool {
	return c.path == other.path && c.maxMB == other.maxMB && c.keep == other.keep
}

type auditFailureCategory string

const (
	auditFailureOpen       auditFailureCategory = "open"
	auditFailurePath       auditFailureCategory = "path_validation"
	auditFailureEncode     auditFailureCategory = "encode"
	auditFailureWrite      auditFailureCategory = "write"
	auditFailureRotate     auditFailureCategory = "rotate"
	auditFailureCleanup    auditFailureCategory = "retention_cleanup"
	auditFailureClose      auditFailureCategory = "close"
	auditFailureRetirement auditFailureCategory = "retirement_timeout"
)

type auditSinkGeneration struct {
	id               uint64
	cfg              auditSinkConfig
	owner            *auditFileOwner
	inflight         int
	retired          bool
	releaseRequested bool
	drained          chan struct{}
	releaseOnce      sync.Once
}

type preparedAuditSink struct {
	log        *auditLog
	candidate  *auditSinkGeneration
	cfg        auditSinkConfig
	old        *auditSinkGeneration
	committed  bool
	once       sync.Once
	retireOnce sync.Once
}

type createdAuditDir struct {
	name string
	info fs.FileInfo
}

type auditFileHandle interface {
	Write([]byte) (int, error)
	Close() error
	Stat() (fs.FileInfo, error)
}

type auditRootHandle interface {
	Lstat(string) (fs.FileInfo, error)
	OpenFile(string, int, fs.FileMode) (auditFileHandle, error)
	Rename(string, string) error
	Remove(string) error
	ReadDir(string) ([]fs.DirEntry, error)
	Close() error
}

type osAuditRootHandle struct{ root *os.Root }

func (r *osAuditRootHandle) Lstat(name string) (fs.FileInfo, error) { return r.root.Lstat(name) }
func (r *osAuditRootHandle) OpenFile(name string, flag int, perm fs.FileMode) (auditFileHandle, error) {
	return r.root.OpenFile(name, flag, perm)
}
func (r *osAuditRootHandle) Rename(oldName, newName string) error {
	return r.root.Rename(oldName, newName)
}
func (r *osAuditRootHandle) Remove(name string) error { return r.root.Remove(name) }
func (r *osAuditRootHandle) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(r.root.FS(), name)
}
func (r *osAuditRootHandle) Close() error { return r.root.Close() }

type auditFileOwner struct {
	// lifeMu never protects disk I/O. Publish may retain/activate a same-path
	// owner even while a slow physical write holds mu.
	lifeMu sync.Mutex
	refs   int
	live   bool
	closed bool

	// mu serializes the physical stream. Tickets are allocated under auditLog.mu
	// and therefore preserve global event order even though the disk write occurs
	// after the event linearization lock is released.
	mu   sync.Mutex
	cond *sync.Cond

	root auditRootHandle
	file auditFileHandle
	base string
	path string
	mode fs.FileMode
	size int64

	nextTicket uint64
	turn       uint64
	firstWrite bool

	createdFile bool
	createdInfo fs.FileInfo
	createdDirs []createdAuditDir
	now         func() time.Time
}

type auditWriteResult struct {
	category auditFailureCategory
	err      error
	durable  bool
}

func newAuditLog(capacity int) *auditLog {
	if capacity <= 0 {
		capacity = auditCap
	}
	return &auditLog{buf: make([]AuditEvent, capacity)}
}

func newAuditLogWithSink(capacity int, path string, maxMB, keep int, log *slog.Logger) *auditLog {
	a := newAuditLog(capacity)
	a.log = log
	cfg, err := resolveAuditSinkConfig(path, maxMB, keep)
	if err != nil {
		a.setStartupSinkFailure(auditSinkConfig{publicPath: path}, auditFailurePath)
		return a
	}
	if !cfg.enabled() {
		return a
	}
	prepared, err := a.prepareTransition(cfg)
	if err != nil {
		cat := auditFailureOpen
		var pe *auditPathError
		if errors.As(err, &pe) {
			cat = auditFailurePath
		}
		a.setStartupSinkFailure(cfg, cat)
		auditLogWarn(log, "audit sink unavailable; durable trail disabled", cfg.publicPath, err)
		return a
	}
	prepared.commit()
	return a
}

func (a *auditLog) setStartupSinkFailure(cfg auditSinkConfig, category auditFailureCategory) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sinkCfg = cfg
	a.sinkConfigured = cfg.publicPath != ""
	now := time.Now().UTC()
	a.activeFailure = category
	a.activeFailureAt = now
	a.lastFailure = category
	a.lastFailureAt = now
}

func (a *auditLog) prepareTransition(cfg auditSinkConfig) (*preparedAuditSink, error) {
	a.mu.Lock()
	if (!a.sinkConfigured && !cfg.enabled()) || (a.sinkConfigured && a.sinkCfg.equal(cfg) && a.currentSink != nil) {
		a.mu.Unlock()
		return nil, nil
	}
	current := a.currentSink
	if !cfg.enabled() {
		a.mu.Unlock()
		return &preparedAuditSink{log: a, cfg: cfg}, nil
	}
	if current != nil && current.cfg.path == cfg.path {
		current.owner.retain()
		candidate := &auditSinkGeneration{cfg: cfg, owner: current.owner, drained: make(chan struct{})}
		a.mu.Unlock()
		return &preparedAuditSink{log: a, candidate: candidate, cfg: cfg}, nil
	}
	a.mu.Unlock()

	owner, err := prepareAuditFileOwner(cfg.path)
	if err != nil {
		return nil, err
	}
	candidate := &auditSinkGeneration{cfg: cfg, owner: owner, drained: make(chan struct{})}
	return &preparedAuditSink{log: a, candidate: candidate, cfg: cfg}, nil
}

func (p *preparedAuditSink) commit() { p.commitWith(nil) }

// commitWith is the exact audit-event publication barrier. publishAdmin is
// bounded in-memory work; it executes while audit events are unable to assign an
// ID/select a sink. The candidate writer was already opened and validated.
func (p *preparedAuditSink) commitWith(publishAdmin func()) {
	if p == nil {
		if publishAdmin != nil {
			publishAdmin()
		}
		return
	}
	p.once.Do(func() {
		a := p.log
		a.mu.Lock()
		defer a.mu.Unlock()
		if publishAdmin != nil {
			publishAdmin()
		}
		p.old = a.currentSink
		a.nextSinkGeneration++
		if p.candidate != nil {
			p.candidate.id = a.nextSinkGeneration
			p.candidate.owner.activate()
		}
		a.currentSink = p.candidate
		a.sinkCfg = p.cfg
		a.sinkConfigured = p.cfg.enabled()
		a.activeFailure = ""
		a.activeFailureAt = time.Time{}
		if p.old != nil {
			p.old.retired = true
			if p.old.inflight == 0 {
				close(p.old.drained)
			}
		}
		p.committed = true
	})
}

func (p *preparedAuditSink) abort() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.candidate != nil {
			_ = p.candidate.owner.release(context.Background(), false)
		}
	})
}

func (p *preparedAuditSink) retire(ctx context.Context) {
	if p == nil || !p.committed || p.old == nil {
		return
	}
	p.retireOnce.Do(func() {
		a := p.log
		a.mu.Lock()
		p.old.releaseRequested = true
		drained := p.old.inflight == 0
		a.mu.Unlock()
		if !drained {
			select {
			case <-p.old.drained:
			case <-ctx.Done():
				a.noteRetirementFailure(auditFailureRetirement, ctx.Err())
				return
			}
		}
		if err := a.releaseGeneration(p.old, ctx); err != nil {
			category := auditFailureClose
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				category = auditFailureRetirement
			}
			a.noteRetirementFailure(category, err)
		}
	})
}

func (a *auditLog) releaseGeneration(gen *auditSinkGeneration, ctx context.Context) error {
	if gen == nil {
		return nil
	}
	var releaseErr error
	gen.releaseOnce.Do(func() {
		releaseErr = gen.owner.release(ctx, true)
	})
	return releaseErr
}

func (a *auditLog) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return a.closeWithContext(ctx)
}

func (a *auditLog) closeWithContext(ctx context.Context) error {
	a.mu.Lock()
	old := a.currentSink
	a.currentSink = nil
	if old != nil {
		old.releaseRequested = true
		if !old.retired {
			old.retired = true
			if old.inflight == 0 {
				close(old.drained)
			}
		}
	}
	a.mu.Unlock()
	if old == nil {
		return nil
	}
	select {
	case <-old.drained:
	case <-ctx.Done():
		return ctx.Err()
	}
	return a.releaseGeneration(old, ctx)
}

func (a *auditLog) statusReport() *AuditSinkStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.sinkConfigured {
		return nil
	}
	st := &AuditSinkStatus{
		Configured:      true,
		Active:          a.currentSink != nil,
		Healthy:         a.currentSink != nil && a.activeFailure == "",
		WriteFailures:   a.writeFailures,
		RotateFailures:  a.rotateFailures,
		CleanupFailures: a.cleanupFailures,
		RetireFailures:  a.retirementFailures,
		LastFailureAt:   a.lastFailureAt,
	}
	if a.currentSink != nil {
		st.Generation = a.currentSink.id
	}
	if a.lastFailure != "" {
		st.LastFailureCategory = string(a.lastFailure)
	}
	return st
}

func auditLogWarn(log *slog.Logger, msg, path string, err error) {
	if log != nil {
		log.Warn(msg, "path", path, "err", err)
	}
}

func (a *auditLog) record(ev AuditEvent) {
	a.mu.Lock()
	a.nextID++
	ev.ID = a.nextID
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	ev.Actor = redactActor(ev.Actor)
	ev.Detail = redactAuditText(ev.Detail)
	a.buf[a.next] = ev
	a.next = (a.next + 1) % len(a.buf)
	if a.next == 0 {
		a.full = true
	}
	gen := a.currentSink
	var ticket uint64
	if gen != nil {
		gen.inflight++
		ticket = gen.owner.allocateTicket()
	}
	a.mu.Unlock()

	if gen == nil {
		return
	}
	line, err := json.Marshal(ev)
	if err != nil {
		a.completeWrite(gen, auditWriteResult{category: auditFailureEncode, err: err})
		return
	}
	line = append(line, '\n')
	result := gen.owner.write(ticket, gen.cfg, line)
	a.completeWrite(gen, result)
}

func (a *auditLog) completeWrite(gen *auditSinkGeneration, result auditWriteResult) {
	a.mu.Lock()
	gen.inflight--
	if gen.retired && gen.inflight == 0 {
		close(gen.drained)
	}
	shouldRelease := gen.retired && gen.releaseRequested && gen.inflight == 0
	if result.err != nil {
		now := time.Now().UTC()
		a.lastFailure = result.category
		a.lastFailureAt = now
		switch result.category {
		case auditFailureRotate:
			a.rotateFailures++
		case auditFailureCleanup:
			a.cleanupFailures++
		default:
			a.writeFailures++
		}
		if a.currentSink == gen {
			a.activeFailure = result.category
			a.activeFailureAt = now
		}
	} else if a.currentSink == gen {
		a.activeFailure = ""
		a.activeFailureAt = time.Time{}
	}
	a.mu.Unlock()

	if result.err != nil {
		auditLogWarn(a.log, "audit sink persistence failed", gen.cfg.publicPath, result.err)
	}
	if shouldRelease {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.releaseGeneration(gen, ctx); err != nil {
			category := auditFailureClose
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				category = auditFailureRetirement
			}
			a.noteRetirementFailure(category, err)
		}
	}
}

func (a *auditLog) noteRetirementFailure(category auditFailureCategory, err error) {
	a.mu.Lock()
	a.retirementFailures++
	a.lastFailure = category
	a.lastFailureAt = time.Now().UTC()
	a.mu.Unlock()
	if a.log != nil {
		a.log.Warn("retired audit sink could not close cleanly", "category", category, "err", err)
	}
}

func (o *auditFileOwner) retain() {
	o.lifeMu.Lock()
	o.refs++
	o.lifeMu.Unlock()
}

func (o *auditFileOwner) activate() {
	o.lifeMu.Lock()
	o.live = true
	o.lifeMu.Unlock()
}

// allocateTicket is called only while auditLog.mu is held. That global event
// linearization lock serializes tickets even when two sink generations share
// the same physical owner during a same-path rotation-policy handoff.
func (o *auditFileOwner) allocateTicket() uint64 {
	t := o.nextTicket
	o.nextTicket++
	return t
}

func (o *auditFileOwner) write(ticket uint64, cfg auditSinkConfig, p []byte) auditWriteResult {
	o.mu.Lock()
	for ticket != o.turn {
		o.cond.Wait()
	}
	result := o.writeLocked(cfg, p)
	o.turn++
	o.cond.Broadcast()
	o.mu.Unlock()
	return result
}

func (o *auditFileOwner) writeLocked(cfg auditSinkConfig, p []byte) auditWriteResult {
	max := int64(cfg.maxMB) * 1024 * 1024
	if int64(len(p)) > max {
		return auditWriteResult{category: auditFailureWrite, err: fmt.Errorf("audit event length %d exceeds maximum file size %d", len(p), max)}
	}
	rotate := o.size+int64(len(p)) > max
	// Preserve lumberjack v2.2.1's lazy first-open boundary after Jul's old
	// startup probe: exact equality rotates for the first write, while an already
	// open stream rotates only on greater-than.
	if o.firstWrite && o.size+int64(len(p)) >= max {
		rotate = true
	}
	var cleanupErr error
	if rotate {
		var err error
		cleanupErr, err = o.rotateLocked(cfg.keep)
		if err != nil {
			return auditWriteResult{category: auditFailureRotate, err: err}
		}
	}
	o.firstWrite = false
	if o.file == nil {
		return auditWriteResult{category: auditFailureWrite, err: errors.New("audit file unavailable")}
	}
	n, err := o.file.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	o.size += int64(n)
	if err != nil {
		return auditWriteResult{category: auditFailureWrite, err: err}
	}
	if cleanupErr != nil {
		// Persistence succeeded; retention compliance degraded independently.
		return auditWriteResult{category: auditFailureCleanup, err: cleanupErr, durable: true}
	}
	return auditWriteResult{durable: true}
}

func (o *auditFileOwner) rotateLocked(keep int) (cleanupErr error, rotateErr error) {
	if o.file == nil {
		return nil, errors.New("audit file is not open")
	}
	oldSize := o.size
	if err := o.file.Close(); err != nil {
		return nil, fmt.Errorf("close before rotation: %w", err)
	}
	o.file = nil
	backup, err := o.nextBackupNameLocked()
	if err != nil {
		_ = o.reopenExistingLocked(oldSize)
		return nil, err
	}
	if err := o.root.Rename(o.base, backup); err != nil {
		_ = o.reopenExistingLocked(oldSize)
		return nil, fmt.Errorf("rename audit file for rotation: %w", err)
	}
	f, err := o.root.OpenFile(o.base, os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, o.mode.Perm())
	if err != nil {
		// Best-effort rollback restores the historical active name. We never
		// truncate or discard the backup if rollback itself is unsafe/fails.
		if rollbackErr := o.root.Rename(backup, o.base); rollbackErr == nil {
			_ = o.reopenExistingLocked(oldSize)
		}
		return nil, fmt.Errorf("open audit file after rotation: %w", err)
	}
	o.file = f
	o.size = 0
	if err := o.pruneLocked(keep); err != nil {
		return &auditCleanupError{err: err}, nil
	}
	return nil, nil
}

func (o *auditFileOwner) reopenExistingLocked(size int64) error {
	f, err := o.root.OpenFile(o.base, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	o.file = f
	o.size = size
	return nil
}

func (o *auditFileOwner) nextBackupNameLocked() (string, error) {
	filename := o.base
	ext := filepath.Ext(filename)
	prefix := filename[:len(filename)-len(ext)]
	t := o.now()
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("%s-%s%s", prefix, t.Add(time.Duration(i)*time.Millisecond).Format(auditBackupTimeFormat), ext)
		_, err := o.root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate unique audit backup name")
}

type auditBackup struct {
	name string
	time time.Time
}

func (o *auditFileOwner) pruneLocked(keep int) error {
	if keep <= 0 {
		return nil
	}
	entries, err := o.root.ReadDir(".")
	if err != nil {
		return err
	}
	ext := filepath.Ext(o.base)
	prefix := strings.TrimSuffix(o.base, ext) + "-"
	var backups []auditBackup
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ext) {
			continue
		}
		stamp := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ext)
		t, err := time.ParseInLocation(auditBackupTimeFormat, stamp, time.Local)
		if err == nil {
			backups = append(backups, auditBackup{name: name, time: t})
		}
	}
	sort.Slice(backups, func(i, j int) bool { return backups[i].time.After(backups[j].time) })
	if len(backups) <= keep {
		return nil
	}
	var first error
	for _, backup := range backups[keep:] {
		if err := o.root.Remove(backup.name); err != nil && first == nil {
			first = err
		}
	}
	return first
}

type auditCleanupError struct{ err error }

func (e *auditCleanupError) Error() string { return "audit retention cleanup: " + e.err.Error() }
func (e *auditCleanupError) Unwrap() error { return e.err }

type auditPathError struct{ err error }

func (e *auditPathError) Error() string { return "audit path validation: " + e.err.Error() }
func (e *auditPathError) Unwrap() error { return e.err }

func prepareAuditFileOwner(path string) (*auditFileOwner, error) {
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

func prepareAuditParent(parent string) (auditRootHandle, []createdAuditDir, error) {
	parent = filepath.Clean(parent)
	missing := []string{}
	ancestor := parent
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, nil, fmt.Errorf("parent %q is not a regular directory", ancestor)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, nil, err
		}
		missing = append(missing, filepath.Base(ancestor))
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return nil, nil, fmt.Errorf("no existing ancestor for %q", parent)
		}
		ancestor = next
	}
	root, err := os.OpenRoot(ancestor)
	if err != nil {
		return nil, nil, err
	}
	rel, err := filepath.Rel(ancestor, parent)
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	if rel == "." {
		return &osAuditRootHandle{root: root}, nil, nil
	}
	if err := root.MkdirAll(rel, 0o750); err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	created := make([]createdAuditDir, 0, len(missing))
	for i := 0; i < len(parts); i++ {
		name := filepath.Join(parts[:i+1]...)
		info, statErr := root.Lstat(name)
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			_ = root.Close()
			return nil, nil, fmt.Errorf("prepared audit parent is unsafe")
		}
		if i >= len(parts)-len(missing) {
			created = append(created, createdAuditDir{name: filepath.Join(ancestor, name), info: info})
		}
	}
	parentRoot, err := root.OpenRoot(rel)
	_ = root.Close()
	if err != nil {
		return nil, nil, err
	}
	return &osAuditRootHandle{root: parentRoot}, created, nil
}

func (o *auditFileOwner) release(ctx context.Context, committed bool) error {
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

func (o *auditFileOwner) cleanupCandidate() {
	if o.createdFile && o.root != nil && o.createdInfo != nil {
		if info, err := o.root.Lstat(o.base); err == nil && os.SameFile(info, o.createdInfo) && info.Size() == 0 {
			_ = o.root.Remove(o.base)
		}
	}
	for i := len(o.createdDirs) - 1; i >= 0; i-- {
		d := o.createdDirs[i]
		if info, err := os.Lstat(d.name); err == nil && os.SameFile(info, d.info) && info.IsDir() {
			_ = os.Remove(d.name) // removal succeeds only if the owned directory is still empty
		}
	}
}
