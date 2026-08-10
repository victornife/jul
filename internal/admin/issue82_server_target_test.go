// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// issue82SharedListenerConfig is the canonical regression fixture for server
// operations that must distinguish named and catch-all virtual hosts sharing one listener.
func issue82SharedListenerConfig() *config.Config {
	return &config.Config{Servers: []config.ServerConfig{
		{Listen: ":8443", ServerNames: []string{"a.example"}},
		{Listen: ":8443", ServerNames: []string{"b.example"}},
		{Listen: ":8443", ServerNames: []string{}},
	}}
}

func TestIssue82ServerSetLimitsTargetsExactSharedListenerVhost(t *testing.T) {
	c := issue82SharedListenerConfig()
	_, err := applyPatch(c, patchRequest{
		Op:          "server_set_limits",
		Listen:      ":8443",
		ServerNames: []string{"b.example"},
		Limits:      &serverLimits{IdleTimeout: "19s"},
	})
	if err != nil {
		t.Fatalf("server_set_limits: %v", err)
	}
	if got := c.Servers[1].IdleTimeout.Std(); got != 19*time.Second {
		t.Fatalf("vhost B idle_timeout = %v, want 19s", got)
	}
	if got := c.Servers[0].IdleTimeout.Std(); got != 0 {
		t.Fatalf("vhost A was mutated: idle_timeout = %v", got)
	}
	if got := c.Servers[2].IdleTimeout.Std(); got != 0 {
		t.Fatalf("catch-all was mutated: idle_timeout = %v", got)
	}
}

func TestIssue82CatchAllServerTargetPreservesPresence(t *testing.T) {
	c := issue82SharedListenerConfig()
	names := []string{}
	_, err := applyPatch(c, patchRequest{
		Op:          "server_set_limits",
		Listen:      ":8443",
		ServerNames: names,
		Limits:      &serverLimits{ReadTimeout: "23s"},
	})
	if err != nil {
		t.Fatalf("target catch-all: %v", err)
	}
	if got := c.Servers[2].ReadTimeout.Std(); got != 23*time.Second {
		t.Fatalf("catch-all read_timeout = %v, want 23s", got)
	}
	if got := c.Servers[0].ReadTimeout.Std(); got != 0 {
		t.Fatalf("named vhost A was mutated: %v", got)
	}
	if got := c.Servers[1].ReadTimeout.Std(); got != 0 {
		t.Fatalf("named vhost B was mutated: %v", got)
	}
}

func TestIssue82ServerTogglesTargetExactSharedListenerVhost(t *testing.T) {
	c := issue82SharedListenerConfig()
	_, err := applyPatch(c, patchRequest{
		Op:          "server_toggle_h2c",
		Listen:      ":8443",
		ServerNames: []string{"b.example"},
		Enabled:     boolPtr(true),
	})
	if err != nil {
		t.Fatalf("server_toggle_h2c: %v", err)
	}
	if !c.Servers[1].H2C {
		t.Fatal("h2c was not enabled on vhost B")
	}
	if c.Servers[0].H2C || c.Servers[2].H2C {
		t.Fatal("h2c operation mutated a different shared-listener vhost")
	}

	for i := range c.Servers {
		c.Servers[i].TLS = &config.TLSConfig{Enabled: true}
	}
	_, err = applyPatch(c, patchRequest{
		Op:          "server_toggle_http3",
		Listen:      ":8443",
		ServerNames: []string{"b.example"},
		Enabled:     boolPtr(true),
	})
	if err != nil {
		t.Fatalf("server_toggle_http3: %v", err)
	}
	if c.Servers[1].HTTP3 == nil || !c.Servers[1].HTTP3.Enabled {
		t.Fatal("HTTP/3 was not enabled on vhost B")
	}
	if c.Servers[0].HTTP3 != nil || c.Servers[2].HTTP3 != nil {
		t.Fatal("HTTP/3 operation mutated a different shared-listener vhost")
	}
}

func TestIssue82LegacyListenOnlyRejectsAmbiguousAndAllowsUnique(t *testing.T) {
	c := issue82SharedListenerConfig()
	_, err := applyPatch(c, patchRequest{
		Op:     "server_set_limits",
		Listen: ":8443",
		Limits: &serverLimits{IdleTimeout: "7s"},
	})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("legacy shared listener = %v, want bounded ambiguous error", err)
	}
	for i := range c.Servers {
		if c.Servers[i].IdleTimeout != 0 {
			t.Fatalf("ambiguous legacy operation mutated server %d", i)
		}
	}

	unique := &config.Config{Servers: []config.ServerConfig{{Listen: ":8080"}}}
	_, err = applyPatch(unique, patchRequest{
		Op:     "server_set_limits",
		Listen: ":8080",
		Limits: &serverLimits{IdleTimeout: "7s"},
	})
	if err != nil {
		t.Fatalf("unique legacy listener should remain compatible: %v", err)
	}
	if got := unique.Servers[0].IdleTimeout.Std(); got != 7*time.Second {
		t.Fatalf("unique legacy listener idle_timeout = %v, want 7s", got)
	}
}

func TestIssue82ServerTargetIsIndependentOfServerOrder(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		c := issue82SharedListenerConfig()
		if reverse {
			c.Servers[0], c.Servers[1] = c.Servers[1], c.Servers[0]
		}
		_, err := applyPatch(c, patchRequest{
			Op:          "server_toggle_h2c",
			Listen:      ":8443",
			ServerNames: []string{"b.example"},
			Enabled:     boolPtr(true),
		})
		if err != nil {
			t.Fatalf("reverse=%v: %v", reverse, err)
		}
		for i := range c.Servers {
			want := len(c.Servers[i].ServerNames) == 1 && c.Servers[i].ServerNames[0] == "b.example"
			if c.Servers[i].H2C != want {
				t.Fatalf("reverse=%v names=%v h2c=%v want=%v", reverse, c.Servers[i].ServerNames, c.Servers[i].H2C, want)
			}
		}
	}
}
