// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build windows

package storagefs

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// fsKey is the volume mount path holding an object. It is internal
// deduplication state only and is never exported.
type fsKey struct{ vol string }

// realPath follows symlinks and junctions so a link that crosses volumes is
// measured where writes actually land. On failure the lexical path is used.
func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func identity(p string, _ os.FileInfo) (fsKey, error) {
	p16, err := windows.UTF16PtrFromString(realPath(p))
	if err != nil {
		return fsKey{}, err
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	if err := windows.GetVolumePathName(p16, &buf[0], uint32(len(buf))); err != nil {
		return fsKey{}, &os.PathError{Op: "GetVolumePathName", Path: p, Err: err}
	}
	return fsKey{vol: windows.UTF16ToString(buf)}, nil
}

// statFS reads quota-aware capacity: GetDiskFreeSpaceEx reports the bytes
// available to the calling user, and a total that honours per-user quotas.
var statFS = func(path string) (Usage, error) {
	dir := realPath(path)
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		dir = filepath.Dir(dir)
	}
	p16, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return Usage{}, err
	}
	var freeToCaller, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p16, &freeToCaller, &total, &totalFree); err != nil {
		return Usage{}, &os.PathError{Op: "GetDiskFreeSpaceEx", Path: dir, Err: err}
	}
	return Usage{Available: freeToCaller, HaveAvailable: true, Total: total, HaveTotal: total > 0}, nil
}
