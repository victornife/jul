// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build windows

package upstream

import (
	"errors"
	"syscall"
)

// wsaeconnrefused is Winsock's real WSAECONNREFUSED code. syscall.ECONNREFUSED
// on Windows is an unrelated, invented POSIX-numbered value package os uses
// internally; it never equals the errno the OS actually returns for a refused
// TCP connect, so errors.Is(err, syscall.ECONNREFUSED) never matches here.
// Comparing the real numeric code instead of err.Error()'s OS-localized text
// keeps classification correct regardless of the machine's display language.
const wsaeconnrefused = syscall.Errno(10061)

func isPlatformConnRefused(err error) bool {
	var errno syscall.Errno
	return errors.As(err, &errno) && errno == wsaeconnrefused
}
