// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && !unix && !windows

package plugins

// executableMemorySupported is false where wazero has no compiler backend.
func executableMemorySupported() bool { return false }
