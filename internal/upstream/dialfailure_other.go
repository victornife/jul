// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !windows

package upstream

import (
	"errors"
	"syscall"
)

// isPlatformConnRefused is a no-op off Windows: everywhere else,
// errors.Is(err, syscall.ECONNREFUSED) already matches the real errno.
func isPlatformConnRefused(err error) bool {
	return false
}

// localResourceExhausted reports a dial that failed because this process or
// the host ran out of file descriptors.
func localResourceExhausted(err error) bool {
	return errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}
