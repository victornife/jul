// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream && !windows

package stream

import "syscall"

func setReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}

// protocolSwitchNeedsRetire reports whether a protocol switch (e.g. TCP→UDP on
// the same address) requires the old listener's socket to be closed before the
// new one can bind. On Unix, TCP and UDP occupy independent port spaces so both
// sockets can coexist momentarily; the new socket can be bound before the old
// one is retired, preserving the atomic rollback guarantee.
func protocolSwitchNeedsRetire() bool { return false }
