// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package storagefs

import "golang.org/x/sys/unix"

// usageFromStatfs converts Linux statfs. Block counts are in units of the
// fragment size (f_frsize); f_bsize is only the preferred I/O size, and the
// two differ on some filesystems.
func usageFromStatfs(st *unix.Statfs_t) Usage {
	size := int64(st.Frsize) //nolint:unconvert // Frsize is int32 on 32-bit linux
	if size <= 0 {
		size = int64(st.Bsize) //nolint:unconvert // Bsize is int32 on 32-bit linux
	}
	return usageFromBlocks(st.Bavail, st.Blocks, size)
}
