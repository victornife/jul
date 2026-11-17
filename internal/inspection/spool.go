// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const spoolIdentity = "jul-coraza-spool-v1\n"

type SpoolSnapshot struct {
	UsedBytes  uint64
	UsedFiles  uint64
	DebtBytes  uint64
	DebtFiles  uint64
	WouldBytes uint64
	WouldFiles uint64
	ByteLimit  uint64
	FileLimit  uint64
}

// SpoolController owns Jul's dedicated, restart-bound Coraza temporary-file
// root and its process-wide logical quotas.
type SpoolController struct {
	mu        sync.Mutex
	governor  *Governor
	root      string
	lockPath  string
	locked    bool
	closed    bool
	used      uint64
	files     uint64
	debt      uint64
	debtFile  uint64
	would     uint64
	wouldFile uint64
	sessions  map[*SpoolTransaction]struct{}
}

// NewSpoolController establishes exclusive ownership of root and removes only
// stale transaction directories marked by a Jul-owned root identity.
func NewSpoolController(governor *Governor, root string) (*SpoolController, error) {
	if governor == nil {
		return nil, errors.New("inspection spool: governor is required")
	}
	limits := governor.Limits()
	if limits.MaxSpoolBytes == 0 || limits.MaxSpoolFiles == 0 {
		return nil, fmt.Errorf("%w: spool quotas must be positive", ErrInvalidLimits)
	}
	if root == "" {
		root = limits.SpoolDir
	}
	if root == "" || filepath.Clean(root) != filepath.Clean(limits.SpoolDir) {
		return nil, fmt.Errorf("%w: spool root must match the published restart-bound path", ErrInvalidLimits)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("inspection spool: resolve root: %w", err)
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, fmt.Errorf("inspection spool: create root: %w", err)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspection spool: inspect root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("inspection spool: root must be a real directory, not a symlink")
	}
	if err := os.Chmod(abs, 0700); err != nil {
		return nil, fmt.Errorf("inspection spool: restrict root permissions: %w", err)
	}

	c := &SpoolController{
		governor: governor,
		root:     abs,
		lockPath: filepath.Join(abs, ".jul-spool.lock"),
		sessions: make(map[*SpoolTransaction]struct{}),
	}
	if err := c.establishRoot(); err != nil {
		return nil, err
	}
	if err := c.removeStaleTransactions(); err != nil {
		_ = c.releaseLock()
		return nil, err
	}
	return c, nil
}

func (c *SpoolController) establishRoot() error {
	marker := filepath.Join(c.root, ".jul-spool-root")
	b, err := os.ReadFile(marker)
	if err == nil {
		if string(b) != spoolIdentity {
			return errors.New("inspection spool: root identity marker is invalid")
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspection spool: read root identity: %w", err)
	} else {
		entries, readErr := os.ReadDir(c.root)
		if readErr != nil {
			return fmt.Errorf("inspection spool: inspect unmarked root: %w", readErr)
		}
		if len(entries) != 0 {
			return errors.New("inspection spool: refusing to adopt a non-empty unmarked root")
		}
		f, createErr := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if createErr != nil {
			return fmt.Errorf("inspection spool: create root identity: %w", createErr)
		}
		_, writeErr := io.WriteString(f, spoolIdentity)
		closeErr := f.Close()
		if err := errors.Join(writeErr, closeErr); err != nil {
			return fmt.Errorf("inspection spool: write root identity: %w", err)
		}
	}

	lock, err := os.OpenFile(c.lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("inspection spool: acquire root lock: %w", err)
	}
	c.locked = true
	_, writeErr := fmt.Fprintf(lock, "%d\n", os.Getpid())
	closeErr := lock.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = c.releaseLock()
		return fmt.Errorf("inspection spool: write root lock: %w", err)
	}
	return nil
}

func (c *SpoolController) removeStaleTransactions() error {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		return fmt.Errorf("inspection spool: list root: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == ".jul-spool-root" || name == ".jul-spool.lock" {
			continue
		}
		if !transactionDirName(name) {
			return fmt.Errorf("inspection spool: refusing unknown root entry %q", name)
		}
		path := filepath.Join(c.root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspection spool: inspect stale transaction: %w", err)
		}
		if info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("inspection spool: recover stale transaction: %w", err)
			}
		} else if err := os.Remove(path); err != nil {
			return fmt.Errorf("inspection spool: remove stale transaction link: %w", err)
		}
	}
	return nil
}

func transactionDirName(name string) bool {
	if len(name) < len("tx-")+6 || len(name) > len("tx-")+32 || !strings.HasPrefix(name, "tx-") {
		return false
	}
	for _, r := range name[3:] {
		if !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (c *SpoolController) Root() string {
	if c == nil {
		return ""
	}
	return c.root
}

func (c *SpoolController) MatchesRoot(root string) bool {
	if c == nil {
		return false
	}
	if root == "" {
		root = "./jul-data/inspection-spool"
	}
	abs, err := filepath.Abs(root)
	return err == nil && abs == c.root
}

// NewTransaction creates an isolated temporary-file namespace for one WAF
// transaction. Coraza receives this object as its per-transaction provider.
func (c *SpoolController) NewTransaction() (*SpoolTransaction, error) {
	if c == nil {
		return nil, errors.New("inspection spool: controller is unavailable")
	}
	c.mu.Lock()
	if c.closed || !c.locked {
		c.mu.Unlock()
		return nil, errors.New("inspection spool: controller is closed")
	}
	c.mu.Unlock()
	dir, err := os.MkdirTemp(c.root, "tx-")
	if err != nil {
		return nil, fmt.Errorf("inspection spool: create transaction directory: %w", err)
	}
	tx := &SpoolTransaction{controller: c, dir: dir, files: make(map[string]*SpoolFile)}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = os.Remove(dir)
		return nil, errors.New("inspection spool: controller is closed")
	}
	c.sessions[tx] = struct{}{}
	c.mu.Unlock()
	return tx, nil
}

func (c *SpoolController) Snapshot() SpoolSnapshot {
	if c == nil {
		return SpoolSnapshot{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	limits := c.governor.Limits()
	return SpoolSnapshot{
		UsedBytes: c.used, UsedFiles: c.files,
		DebtBytes: c.debt, DebtFiles: c.debtFile,
		WouldBytes: c.would, WouldFiles: c.wouldFile,
		ByteLimit: limits.MaxSpoolBytes, FileLimit: limits.MaxSpoolFiles,
	}
}

func (c *SpoolController) reserveFile() error {
	c.mu.Lock()
	if c.closed || c.files == ^uint64(0) {
		c.mu.Unlock()
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	limits := c.governor.Limits()
	rejected := c.files >= limits.MaxSpoolFiles
	if rejected {
		if limits.Mode == Enforce {
			c.mu.Unlock()
			_ = c.governor.RecordRejection(SpoolFilesSaturated, limits.Mode)
			return &AdmissionError{Outcome: SpoolFilesSaturated}
		}
		c.wouldFile = saturatedAdd(c.wouldFile, 1)
	}
	c.files++
	c.mu.Unlock()
	if rejected {
		_ = c.governor.RecordRejection(SpoolFilesSaturated, limits.Mode)
	}
	return nil
}

func (c *SpoolController) refundFile() {
	c.mu.Lock()
	if c.files > 0 {
		c.files--
	}
	c.mu.Unlock()
}

func (c *SpoolController) reserveBytes(n uint64) error {
	c.mu.Lock()
	if c.closed || ^uint64(0)-c.used < n {
		c.mu.Unlock()
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	limits := c.governor.Limits()
	rejected := c.used > limits.MaxSpoolBytes || n > limits.MaxSpoolBytes-c.used
	if rejected {
		if limits.Mode == Enforce {
			c.mu.Unlock()
			_ = c.governor.RecordRejection(SpoolBytesSaturated, limits.Mode)
			return &AdmissionError{Outcome: SpoolBytesSaturated}
		}
		c.would = saturatedAdd(c.would, n)
	}
	c.used += n
	c.mu.Unlock()
	if rejected {
		_ = c.governor.RecordRejection(SpoolBytesSaturated, limits.Mode)
	}
	return nil
}

func (c *SpoolController) refundBytes(n uint64) {
	c.mu.Lock()
	if n >= c.used {
		c.used = 0
	} else {
		c.used -= n
	}
	c.mu.Unlock()
}

func (c *SpoolController) refundDebt(n uint64) {
	c.mu.Lock()
	if n >= c.debt {
		c.debt = 0
	} else {
		c.debt -= n
	}
	if c.debtFile > 0 {
		c.debtFile--
	}
	c.mu.Unlock()
}

type SpoolTransaction struct {
	mu         sync.Mutex
	controller *SpoolController
	dir        string
	files      map[string]*SpoolFile
	sticky     error
	cleanupErr error
	closed     bool
}

func (tx *SpoolTransaction) CreateTemp(_, _ string) (*SpoolFile, error) {
	if tx == nil || tx.controller == nil {
		return nil, errors.New("inspection spool: transaction is unavailable")
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	if tx.closed {
		return nil, errors.New("inspection spool: transaction is closed")
	}
	if err := tx.controller.reserveFile(); err != nil {
		tx.sticky = errors.Join(tx.sticky, err)
		return nil, err
	}
	file, err := os.CreateTemp(tx.dir, "coraza-")
	if err != nil {
		tx.controller.refundFile()
		tx.sticky = errors.Join(tx.sticky, err)
		return nil, fmt.Errorf("inspection spool: create temporary file: %w", err)
	}
	sf := &SpoolFile{owner: tx, file: file, name: file.Name()}
	tx.files[sf.name] = sf
	return sf, nil
}

func (tx *SpoolTransaction) Remove(name string) error {
	if tx == nil {
		return errors.New("inspection spool: transaction is unavailable")
	}
	tx.mu.Lock()
	sf := tx.files[name]
	tx.mu.Unlock()
	if sf == nil {
		tx.mu.Lock()
		err := fmt.Errorf("inspection spool: refusing removal of unowned path")
		tx.sticky = errors.Join(tx.sticky, err)
		tx.cleanupErr = errors.Join(tx.cleanupErr, err)
		tx.mu.Unlock()
		return err
	}
	sf.mu.Lock()
	if !sf.closed {
		err := errors.New("inspection spool: refusing removal before confirmed close")
		sf.markDebtLocked()
		sf.mu.Unlock()
		tx.mu.Lock()
		tx.sticky = errors.Join(tx.sticky, err)
		tx.cleanupErr = errors.Join(tx.cleanupErr, err)
		tx.mu.Unlock()
		return err
	}
	err := os.Remove(sf.name)
	if err != nil && !os.IsNotExist(err) {
		sf.markDebtLocked()
		sf.mu.Unlock()
		tx.mu.Lock()
		tx.sticky = errors.Join(tx.sticky, fmt.Errorf("inspection spool: remove temporary file: %w", err))
		tx.cleanupErr = errors.Join(tx.cleanupErr, err)
		tx.mu.Unlock()
		return err
	}
	if sf.debt {
		tx.controller.refundDebt(sf.bytes)
	}
	tx.controller.refundBytes(sf.bytes)
	tx.controller.refundFile()
	sf.removed = true
	sf.mu.Unlock()
	tx.mu.Lock()
	delete(tx.files, name)
	tx.mu.Unlock()
	return nil
}

func (tx *SpoolTransaction) Err() error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.sticky
}

func (tx *SpoolTransaction) CleanupErr() error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	defer tx.mu.Unlock()
	return tx.cleanupErr
}

// Close retries ownership cleanup after Coraza has closed the transaction.
// Unconfirmed close/removal failures remain charged as cleanup debt until
// process restart recovery can remove the marked transaction directory.
func (tx *SpoolTransaction) Close() error {
	if tx == nil {
		return nil
	}
	tx.mu.Lock()
	if tx.closed {
		err := tx.sticky
		tx.mu.Unlock()
		return err
	}
	files := make([]*SpoolFile, 0, len(tx.files))
	for _, f := range tx.files {
		files = append(files, f)
	}
	tx.mu.Unlock()

	var cleanupErr error
	for _, f := range files {
		if err := f.Close(); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
			continue
		}
		if err := tx.Remove(f.name); err != nil {
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}

	tx.mu.Lock()
	tx.closed = true
	if len(tx.files) == 0 {
		if err := os.Remove(tx.dir); err != nil && !os.IsNotExist(err) {
			tx.sticky = errors.Join(tx.sticky, fmt.Errorf("inspection spool: remove transaction directory: %w", err))
			tx.cleanupErr = errors.Join(tx.cleanupErr, err)
			cleanupErr = errors.Join(cleanupErr, err)
		}
	} else {
		files = files[:0]
		for _, f := range tx.files {
			files = append(files, f)
		}
	}
	tx.sticky = errors.Join(tx.sticky, cleanupErr)
	err := tx.cleanupErr
	tx.mu.Unlock()
	for _, f := range files {
		f.mu.Lock()
		f.markDebtLocked()
		f.mu.Unlock()
	}

	tx.controller.mu.Lock()
	delete(tx.controller.sessions, tx)
	tx.controller.mu.Unlock()
	return err
}

type SpoolFile struct {
	mu      sync.Mutex
	owner   *SpoolTransaction
	file    *os.File
	name    string
	bytes   uint64
	closed  bool
	removed bool
	debt    bool
}

func (f *SpoolFile) Name() string { return f.name }

func (f *SpoolFile) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.removed {
		return 0, os.ErrClosed
	}
	reserved := uint64(len(p))
	if err := f.owner.controller.reserveBytes(reserved); err != nil {
		f.owner.mu.Lock()
		f.owner.sticky = errors.Join(f.owner.sticky, err)
		f.owner.mu.Unlock()
		return 0, err
	}
	n, err := f.file.Write(p)
	if n < 0 || n > len(p) {
		f.owner.controller.refundBytes(reserved)
		inv := &AdmissionError{Outcome: InvalidAccounting}
		f.owner.mu.Lock()
		f.owner.sticky = errors.Join(f.owner.sticky, inv)
		f.owner.mu.Unlock()
		return 0, inv
	}
	written := uint64(n)
	if written < reserved {
		f.owner.controller.refundBytes(reserved - written)
	}
	f.bytes += written
	if err != nil {
		f.owner.mu.Lock()
		f.owner.sticky = errors.Join(f.owner.sticky, fmt.Errorf("inspection spool: write temporary file: %w", err))
		f.owner.mu.Unlock()
		return n, err
	}
	if n != len(p) {
		f.owner.mu.Lock()
		f.owner.sticky = errors.Join(f.owner.sticky, io.ErrShortWrite)
		f.owner.mu.Unlock()
		return n, io.ErrShortWrite
	}
	return n, nil
}

func (f *SpoolFile) ReadAt(p []byte, off int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.file.ReadAt(p, off)
	expected := uint64(0)
	if off >= 0 && uint64(off) < f.bytes {
		expected = min(uint64(len(p)), f.bytes-uint64(off))
	}
	if uint64(max(n, 0)) < expected || (err != nil && !errors.Is(err, io.EOF)) {
		f.owner.mu.Lock()
		f.owner.sticky = errors.Join(f.owner.sticky, fmt.Errorf("inspection spool: read temporary file: %w", errOrUnexpectedEOF(err)))
		f.owner.mu.Unlock()
	}
	return n, err
}

func errOrUnexpectedEOF(err error) error {
	if err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func (f *SpoolFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.removed {
		return nil
	}
	if err := f.file.Close(); err != nil {
		f.owner.mu.Lock()
		f.owner.sticky = errors.Join(f.owner.sticky, fmt.Errorf("inspection spool: close temporary file: %w", err))
		f.owner.cleanupErr = errors.Join(f.owner.cleanupErr, err)
		f.owner.mu.Unlock()
		f.markDebtLocked()
		return err
	}
	f.closed = true
	return nil
}

func (f *SpoolFile) markDebtLocked() {
	if f.debt || f.removed {
		return
	}
	f.debt = true
	c := f.owner.controller
	c.mu.Lock()
	c.debt = saturatedAdd(c.debt, f.bytes)
	c.debtFile++
	c.mu.Unlock()
}

func saturatedAdd(a, b uint64) uint64 {
	if ^uint64(0)-a < b {
		return ^uint64(0)
	}
	return a + b
}

// Close tears down active transactions and releases exclusive root ownership.
// Failed cleanup remains on disk for the next startup's identity-checked sweep.
func (c *SpoolController) Close() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	sessions := make([]*SpoolTransaction, 0, len(c.sessions))
	for tx := range c.sessions {
		sessions = append(sessions, tx)
	}
	c.mu.Unlock()
	var err error
	for _, tx := range sessions {
		err = errors.Join(err, tx.Close())
	}
	if lockErr := c.releaseLock(); lockErr != nil {
		err = errors.Join(err, lockErr)
	}
	return err
}

func (c *SpoolController) releaseLock() error {
	c.mu.Lock()
	if !c.locked {
		c.mu.Unlock()
		return nil
	}
	c.locked = false
	c.mu.Unlock()
	if err := os.Remove(c.lockPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspection spool: release root lock: %w", err)
	}
	return nil
}
