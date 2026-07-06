// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import "testing"

func TestTrafficTrackerRecordsHostsAndOrigins(t *testing.T) {
	tr := newTrafficTracker()
	tr.record("api.example.com:443", "https://app.example.com", "https://app.example.com/page?q=1", "GET")
	tr.record("api.example.com", "https://app.example.com", "", "OPTIONS")
	tr.record("api.example.com", "https://api.example.com", "", "POST")

	snap := tr.snapshot()
	if got := snap.Hosts["api.example.com"]; got != 3 {
		t.Errorf("host count = %v, want 3", got)
	}
	if got := snap.Origins["https://app.example.com"]; got != 2 {
		t.Errorf("origin count = %v, want 2", got)
	}
	// Referer should keep only the host, never the path/query.
	if got := snap.Referers["app.example.com"]; got != 1 {
		t.Errorf("referer host count = %v, want 1", got)
	}
	if snap.PreflightCount != 1 {
		t.Errorf("preflight = %v, want 1", snap.PreflightCount)
	}
	// app.example.com origin vs api.example.com host => cross-origin (2 records);
	// api.example.com origin vs api.example.com host => same-origin (1 record).
	if snap.CrossOrigin != 2 {
		t.Errorf("cross-origin = %v, want 2", snap.CrossOrigin)
	}
	if snap.SameOrigin != 1 {
		t.Errorf("same-origin = %v, want 1", snap.SameOrigin)
	}
}

func TestTrafficTrackerOverflowFoldsToOther(t *testing.T) {
	c := newTopNCounter(2)
	c.add("a")
	c.add("b")
	c.add("c") // overflow
	c.add("c") // overflow
	if c.values["a"] != 1 || c.values["b"] != 1 {
		t.Error("first two keys should be retained")
	}
	if c.values["(other)"] != 2 {
		t.Errorf("overflow count = %v, want 2", c.values["(other)"])
	}
}

func TestNormalizeHelpersStripSensitiveParts(t *testing.T) {
	if got := normalizeHost(""); got != "(none)" {
		t.Errorf("empty host = %q, want (none)", got)
	}
	if got := normalizeHost("API.Example.com:8443"); got != "api.example.com" {
		t.Errorf("normalizeHost = %q, want api.example.com", got)
	}
	if got := normalizeOrigin("https://App.Example.com:443"); got != "https://app.example.com" {
		t.Errorf("normalizeOrigin = %q, want https://app.example.com", got)
	}
	// Referer path and query must be discarded.
	if got := normalizeRefererHost("https://app.example.com/secret?token=abc"); got != "app.example.com" {
		t.Errorf("normalizeRefererHost = %q, want app.example.com", got)
	}
}
