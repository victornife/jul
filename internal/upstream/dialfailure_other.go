// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !windows

package upstream

// isPlatformConnRefused is a no-op off Windows: everywhere else,
// errors.Is(err, syscall.ECONNREFUSED) already matches the real errno.
func isPlatformConnRefused(err error) bool {
	return false
}
