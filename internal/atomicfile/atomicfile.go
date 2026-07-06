// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package atomicfile writes a file's contents atomically and crash-safely.
//
// Data is written to a temporary file in the destination directory, flushed to
// stable storage, and renamed over the target. A crash or power loss at any
// point therefore leaves either the previous complete file or the new complete
// file in place — never a truncated or partially written one. This matters for
// configuration, history snapshots, and other state a running server rewrites:
// a half-written config that survived a crash could fail to parse or, worse,
// parse into something unintended.
package atomicfile

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Write atomically replaces the file at path with data.
//
// When path already exists its permission bits are preserved, so an operator
// who deliberately widened or tightened a file's mode keeps that choice across
// rewrites. When path is new the file is created with perm — callers pass a
// restrictive default (0o600) for state that may carry secrets so a freshly
// created file is never world-readable regardless of umask.
//
// The write lands in a same-directory temporary file that is fsync'd and then
// renamed over path; the directory entry is best-effort fsync'd afterwards so
// the rename itself is durable where the platform supports it. Concurrent
// readers always observe either the old or the new complete content.
func Write(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)

	mode := perm
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	// Remove the temp file on any early return. After a successful rename it no
	// longer exists at this name, so the deferred Remove is a harmless no-op.
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("set temp file mode: %w", err)
	}
	// Retry rename on Windows where anti-virus or indexing briefly locks
	// the temp file after close, causing "Access is denied."
	var renameErr error
	for i := 0; i < 5; i++ {
		if renameErr = os.Rename(tmpName, path); renameErr == nil {
			break
		}
		if i < 4 {
			time.Sleep(time.Duration(i+1) * 20 * time.Millisecond)
		}
	}
	if renameErr != nil {
		return fmt.Errorf("rename temp file into place: %w", renameErr)
	}

	// Best-effort durability for the rename. Syncing a directory is not
	// supported on every platform (notably Windows), so failures here are
	// ignored: the rename has already made the new content visible, and this
	// only governs whether that rename survives a power loss.
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
