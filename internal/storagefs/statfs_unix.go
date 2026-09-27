// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux || darwin || freebsd

package storagefs

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// fsKey is the device number of the filesystem holding an object. It is
// internal deduplication state only and is never exported.
type fsKey struct{ dev uint64 }

func identity(_ string, fi os.FileInfo) (fsKey, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return fsKey{}, errUnsupported
	}
	return fsKey{dev: uint64(st.Dev)}, nil //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
}

// statFS reads the capacity of the filesystem holding path; path may be a
// file or a directory (statfs follows symlinks).
var statFS = func(path string) (Usage, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return Usage{}, &os.PathError{Op: "statfs", Path: path, Err: err}
	}
	return usageFromStatfs(&st), nil
}
