// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
)

// forceEngineProbe re-runs engine selection with a fake executable-memory probe.
func forceEngineProbe(t *testing.T, probe func() bool) {
	t.Helper()
	oldProbe := executableMemory
	executableMemory = probe
	engineOnce = sync.Once{}
	t.Cleanup(func() {
		executableMemory = oldProbe
		engineOnce = sync.Once{}
	})
}

func TestEngineModeFollowsExecutableMemoryProbe(t *testing.T) {
	forceEngineProbe(t, func() bool { return false })
	mode, reason := EngineMode()
	if mode != EngineInterpreter {
		t.Fatalf("denied executable memory: mode %q, want %q", mode, EngineInterpreter)
	}
	if compilerPlatformSupported() && !strings.Contains(reason, "MemoryDenyWriteExecute") {
		t.Fatalf("reason %q should name the executable-memory policy", reason)
	}

	forceEngineProbe(t, func() bool { return true })
	mode, _ = EngineMode()
	want := EngineInterpreter
	if compilerPlatformSupported() {
		want = EngineCompiler
	}
	if mode != want {
		t.Fatalf("allowed executable memory: mode %q, want %q", mode, want)
	}
}

func TestEngineModeMatchesThisHost(t *testing.T) {
	forceEngineProbe(t, executableMemorySupported)
	mode, reason := EngineMode()
	if reason == "" {
		t.Fatal("EngineMode must explain its choice")
	}
	want := EngineInterpreter
	if compilerPlatformSupported() && executableMemorySupported() {
		want = EngineCompiler
	}
	if mode != want {
		t.Fatalf("mode %q, want %q", mode, want)
	}
}

func TestInterpreterEngineServesPlugins(t *testing.T) {
	forceEngineProbe(t, func() bool { return false })
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"hi": pcfg("header-inject")})
	next, called := okNext()
	rec := httptest.NewRecorder()
	s.Middleware("hi")(next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !*called || rec.Code != http.StatusOK {
		t.Fatalf("interpreter-engine plugin did not continue: called=%v status=%d", *called, rec.Code)
	}
}
