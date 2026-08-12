// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream && windows

package stream

// setReuseAddr is intentionally a no-op on Windows.
//
// On Unix SO_REUSEADDR allows rapid rebinding when the previous process is in
// TIME_WAIT. On Windows the same flag has a completely different (and
// dangerous) meaning: it permits a second socket to forcibly bind to a port
// already in use by another process, producing indeterminate behaviour and
// creating a port-hijacking vector.  Windows already provides “Enhanced Socket
// Security” by default (since WS2K3), so listeners created without special
// flags are safe for production.
//
// See: https://learn.microsoft.com/en-us/windows/win32/winsock/using-so-reuseaddr-and-so-exclusiveaddruse
func setReuseAddr(fd uintptr) error {
	return nil
}

// protocolSwitchNeedsRetire reports whether a protocol switch (e.g. TCP→UDP on
// the same address) requires the old listener's socket to be closed before the
// new one can bind. On Windows, TCP and UDP share the same port namespace under
// Enhanced Socket Security (SO_EXCLUSIVEADDRUSE), so a TCP socket holding port
// P prevents a UDP socket from binding to P. The old listener must therefore be
// retired before the new socket can be bound.
func protocolSwitchNeedsRetire() bool { return true }
