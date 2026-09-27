// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build darwin || freebsd

package storagefs

import "golang.org/x/sys/unix"

// usageFromStatfs converts BSD-family statfs, whose block counts are in units
// of f_bsize. FreeBSD's f_bavail is signed and goes negative when an
// unprivileged writer is already inside the root reserve; that is "no space
// available", clamped to zero rather than wrapped.
func usageFromStatfs(st *unix.Statfs_t) Usage {
	avail := int64(st.Bavail) //nolint:unconvert // uint64 on darwin, int64 on freebsd
	if avail < 0 {
		avail = 0
	}
	return usageFromBlocks(uint64(avail), st.Blocks, int64(st.Bsize)) //nolint:unconvert // Bsize is uint32 on darwin
}
