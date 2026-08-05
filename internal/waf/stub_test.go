// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !waf

package waf

import (
	"context"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestLeanBuildRejectsWAFConstruction(t *testing.T) {
	if Compiled {
		t.Fatal("lean WAF test compiled with Compiled=true")
	}

	firewall, err := New(context.Background(), config.WAFConfig{Enabled: true}, Options{})
	if err == nil {
		t.Fatal("WAF construction succeeded without the waf build tag")
	}
	if firewall != nil {
		t.Fatal("failed WAF construction returned a non-nil firewall")
	}
	if !strings.Contains(err.Error(), "-tags waf") {
		t.Fatalf("New error = %q, want actionable waf tag guidance", err)
	}
}

func TestLeanFirewallSurfaceIsNoOp(t *testing.T) {
	firewall := &Firewall{}
	if firewall.Middleware() != nil {
		t.Fatal("lean firewall unexpectedly returned middleware")
	}
	if err := firewall.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
