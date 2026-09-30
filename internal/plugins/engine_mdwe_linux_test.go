// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && linux

package plugins

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const mdweChildEnv = "JUL_TEST_ENGINE_MDWE_CHILD"

// TestEngineModeUnderMemoryDenyWriteExecute applies the kernel W^X policy that
// systemd's MemoryDenyWriteExecute=yes installs (PR_SET_MDWE) to a child copy
// of this test binary and checks the engine falls back to the interpreter.
func TestEngineModeUnderMemoryDenyWriteExecute(t *testing.T) {
	if os.Getenv(mdweChildEnv) == "1" {
		if err := unix.Prctl(unix.PR_SET_MDWE, unix.PR_MDWE_REFUSE_EXEC_GAIN, 0, 0, 0); err != nil {
			fmt.Println("ENGINE=unsupported")
			return
		}
		mode, _ := EngineMode()
		fmt.Println("ENGINE=" + mode)
		return
	}
	if !compilerPlatformSupported() {
		t.Skip("wazero compiler unsupported on this platform")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestEngineModeUnderMemoryDenyWriteExecute$", "-test.count=1")
	cmd.Env = append(os.Environ(), mdweChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("run child: %v", err)
	}
	switch {
	case strings.Contains(string(out), "ENGINE=unsupported"):
		t.Skip("kernel lacks PR_SET_MDWE (Linux < 6.3)")
	case !strings.Contains(string(out), "ENGINE="+EngineInterpreter):
		t.Fatalf("under MDWE the engine must be %q; child output:\n%s", EngineInterpreter, out)
	}
}
