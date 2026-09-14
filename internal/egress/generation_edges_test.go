// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestGenerationManagerDefensiveEdges(t *testing.T) {
	if _, err := NewManager(config.EgressConfig{Enabled: true}); err == nil {
		t.Fatal("enabled policy without allow-list must fail")
	}

	var nilManager *Manager
	if nilManager.Current() != nil {
		t.Fatal("nil Manager.Current should be nil")
	}
	if _, _, err := nilManager.Prepare(config.EgressConfig{}); err == nil {
		t.Fatal("nil Manager.Prepare should fail")
	}
	if nilManager.Publish(nil) != nil {
		t.Fatal("nil Manager.Publish should return nil")
	}

	m := &Manager{}
	if m.Publish(nil) != nil {
		t.Fatal("Publish(nil) should return nil")
	}
	rt := &dynamicRoundTripper{manager: m, subsystem: SubsystemACME}
	if _, err := rt.RoundTrip(nil); err == nil {
		t.Fatal("RoundTrip(nil) should fail")
	}
	req := httptest.NewRequest("GET", "http://example.test/", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip without a published generation should fail")
	}

	(&dynamicRoundTripper{}).CloseIdleConnections()
	(&guardedRoundTripper{}).CloseIdleConnections()
}

func TestGenerationNilAndCloseEdges(t *testing.T) {
	var nilGeneration *Generation
	if nilGeneration.ID() != 0 {
		t.Fatal("nil generation ID should be zero")
	}
	if nilGeneration.For(SubsystemAuth) != nil {
		t.Fatal("nil generation guard should be nil")
	}
	nilGeneration.CloseIdleConnections()
	if err := nilGeneration.Close(); err != nil {
		t.Fatalf("nil generation Close: %v", err)
	}

	m, err := NewManager(config.EgressConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.Current().Close(); err != nil {
		t.Fatalf("generation Close: %v", err)
	}
	(&dynamicRoundTripper{manager: m, subsystem: SubsystemOCSP}).CloseIdleConnections()
}

func TestPolicySignatureSuffixRule(t *testing.T) {
	p := &Policy{hosts: []hostRule{{value: "example.com", suffix: true}}}
	got := policySignature(p)
	if got != "enabled\x00suffix:example.com" {
		t.Fatalf("signature = %q", got)
	}
}
