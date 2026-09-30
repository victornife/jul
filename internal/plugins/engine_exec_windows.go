// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && windows

package plugins

import "golang.org/x/sys/windows"

// executableMemorySupported repeats wazero's probe: commit one RW page and ask
// for PAGE_EXECUTE_READ, which a dynamic-code policy refuses.
func executableMemorySupported() bool {
	const size = 4096
	addr, err := windows.VirtualAlloc(0, size, windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if err != nil {
		return false
	}
	defer func() { _ = windows.VirtualFree(addr, 0, windows.MEM_RELEASE) }()
	var old uint32
	return windows.VirtualProtect(addr, size, windows.PAGE_EXECUTE_READ, &old) == nil
}
