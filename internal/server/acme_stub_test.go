// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !acme

package server

import (
	"testing"

	"jul/internal/config"
)

func TestNewACMEManagerStubRejectsEnabledWithoutTag(t *testing.T) {
	mgr, err := NewACMEManager(acmeServerCfg().Servers, nil)
	if err == nil {
		t.Fatal("expected error: acme enabled but binary lacks the acme build tag")
	}
	if mgr != nil {
		t.Error("manager must be nil on error")
	}
	if ACMECompiled {
		t.Error("ACMECompiled must be false without the acme tag")
	}
}

func TestNewACMEManagerStubNilWhenNoACME(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{Listen: ":80"}}}
	mgr, err := NewACMEManager(cfg.Servers, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		t.Error("expected nil manager when no block enables acme")
	}
}
