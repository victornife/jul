// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"runtime"
	"sync"

	"github.com/tetratelabs/wazero"
	"golang.org/x/sys/cpu"
)

// Engine names reported by EngineMode.
const (
	EngineCompiler    = "compiler"
	EngineInterpreter = "interpreter"
)

var (
	engineOnce   sync.Once
	engineMode   string
	engineReason string
	// executableMemory is the probe seam; tests replace it before first use.
	executableMemory = executableMemorySupported
)

// EngineMode reports which wazero engine this process compiles plugins with,
// and why. It mirrors wazero v1.12's own automatic choice
// (platform.CompilerSupports: platform support plus an RW->RX mprotect probe)
// so the runtime can select the engine explicitly and the reported mode is the
// mode in use. The answer is fixed for the process lifetime.
func EngineMode() (mode, reason string) {
	engineOnce.Do(func() {
		switch {
		case !compilerPlatformSupported():
			engineMode = EngineInterpreter
			engineReason = "the wazero compiler does not support " + runtime.GOOS + "/" + runtime.GOARCH
		case !executableMemory():
			engineMode = EngineInterpreter
			engineReason = "executable memory is denied to this process (for example systemd MemoryDenyWriteExecute=yes)"
		default:
			engineMode = EngineCompiler
			engineReason = "native code generation is available"
		}
	})
	return engineMode, engineReason
}

// compilerPlatformSupported matches wazero v1.12 compilerPlatformSupports for
// the core features Jul enables (no threads proposal).
func compilerPlatformSupported() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "netbsd", "windows":
		if runtime.GOARCH == "arm64" {
			return true
		}
		fallthrough
	case "dragonfly", "solaris", "illumos":
		return runtime.GOARCH == "amd64" && cpu.X86.HasSSE41
	default:
		return false
	}
}

func newRuntimeConfig() wazero.RuntimeConfig {
	if mode, _ := EngineMode(); mode == EngineCompiler {
		return wazero.NewRuntimeConfigCompiler()
	}
	return wazero.NewRuntimeConfigInterpreter()
}
