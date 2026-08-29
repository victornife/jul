// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build windows

package upstream

import (
	"net"
	"syscall"
	"testing"
)

// TestClassifyDialErrorWindowsLocalizedErrno pins the actual bug: dialing a
// refused port on Windows returns syscall.Errno(10061) (WSAECONNREFUSED),
// whose Error() text is produced by the OS in its configured display
// language. On a non-English Windows box that text never contains "refused",
// so classification must not depend on it — it must recognize the errno
// value itself.
func TestClassifyDialErrorWindowsLocalizedErrno(t *testing.T) {
	localized := &net.OpError{Op: "dial", Err: syscall.Errno(10061)}
	if got := ClassifyDialError(localized); got != "refused" {
		t.Errorf("ClassifyDialError(WSAECONNREFUSED) = %q, want %q", got, "refused")
	}
}
