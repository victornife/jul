// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"
)

func TestCmdVersionText(t *testing.T) {
	code, out, _ := capture(t, func() int { return cmdVersion(nil) })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	// First line is the stable "Product version" contract shared with -version.
	first := strings.SplitN(out, "\n", 2)[0]
	if want := productName + " " + version; first != want {
		t.Errorf("first line = %q, want %q", first, want)
	}
	for _, label := range []string{"commit:", "built:", "go:", "platform:"} {
		if !strings.Contains(out, label) {
			t.Errorf("output missing %q label:\n%s", label, out)
		}
	}
	if !strings.Contains(out, runtime.GOOS+"/"+runtime.GOARCH) {
		t.Errorf("output missing platform %s/%s:\n%s", runtime.GOOS, runtime.GOARCH, out)
	}
}

func TestCmdVersionJSON(t *testing.T) {
	code, out, _ := capture(t, func() int { return cmdVersion([]string{"-json"}) })
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var m buildMetadata
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if m.Product != productName {
		t.Errorf("product = %q, want %q", m.Product, productName)
	}
	if m.Version != version {
		t.Errorf("version = %q, want %q", m.Version, version)
	}
	if m.OS != runtime.GOOS || m.Arch != runtime.GOARCH {
		t.Errorf("os/arch = %s/%s, want %s/%s", m.OS, m.Arch, runtime.GOOS, runtime.GOARCH)
	}
	if m.GoVersion == "" {
		t.Error("go_version is empty")
	}
	// Commit/build_date degrade to a non-empty sentinel when VCS info is absent.
	if m.Commit == "" || m.BuildDate == "" {
		t.Errorf("commit/build_date must never be empty: commit=%q build_date=%q", m.Commit, m.BuildDate)
	}
}

func TestCmdCompletionAllShells(t *testing.T) {
	markers := map[string]string{
		"bash":       "complete -F _jul jul",
		"zsh":        "#compdef jul",
		"fish":       "complete -c jul",
		"powershell": "Register-ArgumentCompleter",
		"pwsh":       "Register-ArgumentCompleter",
	}
	for shell, marker := range markers {
		t.Run(shell, func(t *testing.T) {
			code, out, _ := capture(t, func() int { return cmdCompletion([]string{shell}) })
			if code != 0 {
				t.Fatalf("exit code = %d, want 0", code)
			}
			if !strings.Contains(out, marker) {
				t.Errorf("%s completion missing marker %q:\n%s", shell, marker, out)
			}
			// Every script advertises the version + completion verbs it completes.
			if !strings.Contains(out, "completion") || !strings.Contains(out, "version") {
				t.Errorf("%s completion does not reference the new verbs", shell)
			}
		})
	}
}

func TestCmdCompletionErrors(t *testing.T) {
	// Unknown shell → usage error.
	if code, _, errOut := capture(t, func() int { return cmdCompletion([]string{"tcsh"}) }); code != 2 {
		t.Errorf("unknown shell: exit = %d, want 2 (stderr: %s)", code, errOut)
	}
	// Missing shell argument → usage error.
	if code, _, _ := capture(t, func() int { return cmdCompletion(nil) }); code != 2 {
		t.Errorf("missing shell: exit = %d, want 2", code)
	}
	// Too many arguments → usage error.
	if code, _, _ := capture(t, func() int { return cmdCompletion([]string{"bash", "extra"}) }); code != 2 {
		t.Errorf("extra arg: exit = %d, want 2", code)
	}
}

func TestDispatchVersionAndCompletion(t *testing.T) {
	// Route-only check: both verbs must be recognized by the dispatcher.
	for _, verb := range []string{"version", "completion"} {
		handled, _ := dispatchSubcommand([]string{verb, "-h"})
		if !handled {
			t.Errorf("dispatchSubcommand(%q) not handled", verb)
		}
	}
}
