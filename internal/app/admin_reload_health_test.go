// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"strings"
	"testing"

	"jul/internal/server"
)

func TestNoChangePreservesActiveAdminDegradation(t *testing.T) {
	var state adminReloadHealth
	state.observe(server.ReloadResult{
		Outcome:   server.ReloadAppliedDegraded,
		Published: true,
		Admin:     server.ReloadSubsystemResult{Status: server.ReloadSubsystemFailed, Error: "policy commit"},
	})
	if err := state.health(); err == nil || !strings.Contains(err.Error(), "policy commit") {
		t.Fatalf("published degradation health = %v", err)
	}

	state.observe(server.ReloadResult{
		Outcome:   server.ReloadNoChange,
		Published: false,
		Admin:     server.ReloadSubsystemResult{Status: server.ReloadSubsystemSkipped},
	})
	if err := state.health(); err == nil || !strings.Contains(err.Error(), "policy commit") {
		t.Fatalf("no_change cleared prior degradation: %v", err)
	}

	state.observe(server.ReloadResult{
		Outcome:   server.ReloadAppliedLive,
		Published: true,
		Admin:     server.ReloadSubsystemResult{Status: server.ReloadSubsystemOK},
	})
	if err := state.health(); err != nil {
		t.Fatalf("published repair did not clear degradation: %v", err)
	}
}

func TestAdminReloadHealthUsesSafeFallbackDetail(t *testing.T) {
	var state adminReloadHealth
	state.observe(server.ReloadResult{
		Published: true,
		Admin:     server.ReloadSubsystemResult{Status: server.ReloadSubsystemTimedOut},
	})
	if err := state.health(); err == nil || err.Error() != "admin subsystem reload failed" {
		t.Fatalf("fallback health = %v", err)
	}
}
