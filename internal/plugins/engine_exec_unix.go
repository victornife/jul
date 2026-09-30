// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && unix

package plugins

import "golang.org/x/sys/unix"

// executableMemorySupported repeats wazero's probe: map one private anonymous
// RW page and ask the kernel to make it RX. A W^X policy such as systemd's
// MemoryDenyWriteExecute refuses the mprotect.
func executableMemorySupported() bool {
	b, err := unix.Mmap(-1, 0, unix.Getpagesize(), unix.PROT_READ|unix.PROT_WRITE, unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		return false
	}
	defer func() { _ = unix.Munmap(b) }()
	return unix.Mprotect(b, unix.PROT_READ|unix.PROT_EXEC) == nil
}
