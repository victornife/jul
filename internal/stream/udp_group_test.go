// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// A backend that reaches the relay as a group address (a hostname or a
// discovered backend that validation could not see) is refused at dial time:
// no session is created and the failure is counted (#511).
func TestUDPRefusesGroupBackendAtDial(t *testing.T) {
	var failures atomic.Int64
	var m udpMetrics
	hooks := m.hooks()
	hooks.OnDialFailure = func(proto, _ string) {
		if proto == "udp" {
			failures.Add(1)
		}
	}
	s := newTestServer(t, hooks)
	addr := freeUDPAddr(t)
	// Reload does not run config validation, standing in for a backend whose
	// address only becomes known at runtime.
	if err := s.Reload([]config.StreamServer{{
		Listen:    addr,
		Protocol:  "udp",
		ProxyPass: "239.255.255.250:1900",
	}}, nil); err != nil {
		t.Fatalf("reload: %v", err)
	}

	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte("M-SEARCH * HTTP/1.1\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if !eventually(func() bool { return failures.Load() >= 1 }) {
		t.Fatal("the group backend was not refused as a dial failure")
	}
	time.Sleep(50 * time.Millisecond)
	if n := m.conns.Load(); n != 0 {
		t.Fatalf("UDP sessions = %d, want 0 for a refused group backend", n)
	}
}
