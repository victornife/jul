// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"testing"
	"time"

	"jul/internal/middleware"
)

func TestLogTailSnapshotNewestFirstAndLimit(t *testing.T) {
	lt := NewLogTail(8)
	for i := 0; i < 5; i++ {
		lt.record(LogEntry{Path: "/p", Status: 200 + i})
	}

	all := lt.Snapshot(0)
	if len(all) != 5 {
		t.Fatalf("snapshot len = %d, want 5", len(all))
	}
	if all[0].Status != 204 {
		t.Errorf("newest status = %d, want 204 (newest-first)", all[0].Status)
	}
	if all[4].Status != 200 {
		t.Errorf("oldest status = %d, want 200", all[4].Status)
	}

	limited := lt.Snapshot(2)
	if len(limited) != 2 {
		t.Fatalf("limited len = %d, want 2", len(limited))
	}
	if limited[0].Status != 204 || limited[1].Status != 203 {
		t.Errorf("limited = %d,%d, want 204,203 (newest two)", limited[0].Status, limited[1].Status)
	}
}

func TestLogTailRingOverwrites(t *testing.T) {
	lt := NewLogTail(3)
	for i := 0; i < 6; i++ {
		lt.record(LogEntry{Status: i})
	}
	got := lt.Snapshot(0)
	if len(got) != 3 {
		t.Fatalf("snapshot len = %d, want 3 (bounded)", len(got))
	}
	// Newest-first: the last three recorded were 5,4,3.
	want := []int{5, 4, 3}
	for i, w := range want {
		if got[i].Status != w {
			t.Errorf("entry %d status = %d, want %d", i, got[i].Status, w)
		}
	}
}

func TestLogTailLogRedactsAndNormalizes(t *testing.T) {
	lt := NewLogTail(4)
	lt.Log(middleware.AccessRecord{
		Time:      time.Now(),
		Method:    "GET",
		Host:      "api.example.com:8443",
		Path:      "/users/12345/orders?token=secret",
		Status:    200,
		Duration:  1500 * time.Microsecond,
		Client:    "203.0.113.7",
		Peer:      "203.0.113.7",
		UserAgent: "curl/8.4.0",
	})
	e := lt.Snapshot(0)[0]
	if e.Host != "api.example.com" {
		t.Errorf("host = %q, want port stripped", e.Host)
	}
	if e.Path != "/users/:id/orders" {
		t.Errorf("path = %q, want id redacted and query dropped", e.Path)
	}
	if e.UserAgent != "curl" {
		t.Errorf("user_agent = %q, want coarse family curl", e.UserAgent)
	}
	if e.DurationMs != 1.5 {
		t.Errorf("duration_ms = %v, want 1.5", e.DurationMs)
	}
	if e.ClientIP != "203.0.113.7" {
		t.Errorf("client_ip = %q, want preserved", e.ClientIP)
	}
	if e.PeerIP != "" {
		t.Errorf("peer_ip = %q, want omitted when it equals the client", e.PeerIP)
	}
}

func TestLogTailSubscribeDeliversAndUnsubscribe(t *testing.T) {
	lt := NewLogTail(4)
	ch, cancel := lt.Subscribe()

	lt.record(LogEntry{Path: "/live", Status: 201})
	select {
	case e := <-ch:
		if e.Path != "/live" {
			t.Errorf("delivered path = %q, want /live", e.Path)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for live entry")
	}

	cancel()
	if _, ok := <-ch; ok {
		t.Error("channel should be closed after cancel")
	}
	// A second cancel must not panic.
	cancel()
	// Recording after unsubscribe must not block or panic.
	lt.record(LogEntry{Path: "/after"})
}

func TestLogTailSubscribeDropsForSlowReader(t *testing.T) {
	lt := NewLogTail(1024)
	ch, cancel := lt.Subscribe()
	defer cancel()
	// Flood well past the channel buffer without reading; record must never block.
	for i := 0; i < 5000; i++ {
		lt.record(LogEntry{Status: i})
	}
	// The buffer holds at most its capacity; the rest were dropped, not blocked.
	if len(ch) > cap(ch) {
		t.Fatalf("channel len %d exceeds cap %d", len(ch), cap(ch))
	}
}
