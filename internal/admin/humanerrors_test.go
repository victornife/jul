// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "testing"

func TestHumanizeErrExtractsPathPerLine(t *testing.T) {
	raw := "servers[0]: 'listen' is required\n" +
		"upstreams[1].servers[0]: address is required\n" +
		"at least one [[servers]] block is required"

	got := humanizeErr(raw)
	if len(got) != 3 {
		t.Fatalf("got %d issues, want 3: %+v", len(got), got)
	}

	if got[0].Path != "servers[0]" {
		t.Errorf("issue 0 path = %q, want servers[0]", got[0].Path)
	}
	if got[0].Summary != "'listen' is required" {
		t.Errorf("issue 0 summary = %q, want the path-stripped message", got[0].Summary)
	}

	if got[1].Path != "upstreams[1].servers[0]" {
		t.Errorf("issue 1 path = %q, want upstreams[1].servers[0]", got[1].Path)
	}
	if got[1].Summary != "address is required" {
		t.Errorf("issue 1 summary = %q", got[1].Summary)
	}

	// A message with no structural path keeps an empty Path and the full text.
	if got[2].Path != "" {
		t.Errorf("issue 2 path = %q, want empty", got[2].Path)
	}
	if got[2].Summary != "at least one [[servers]] block is required" {
		t.Errorf("issue 2 summary = %q", got[2].Summary)
	}
}

func TestHumanizeErrMappedCodeCarriesPath(t *testing.T) {
	got := humanizeErr(`servers[0].locations[2]: no upstream named "api"`)
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(got), got)
	}
	if got[0].Code != "unknown_upstream" {
		t.Errorf("code = %q, want unknown_upstream", got[0].Code)
	}
	if got[0].Path != "servers[0].locations[2]" {
		t.Errorf("path = %q, want servers[0].locations[2]", got[0].Path)
	}
}

func TestHumanizeErrSubsystemPrefixIsNotAPath(t *testing.T) {
	// A bare "waf:" prefix is a subsystem category, not a structural config
	// path, so it must not be lifted into Path.
	got := humanizeErr("waf: rule set failed to compile")
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(got), got)
	}
	if got[0].Code != "waf_build" {
		t.Errorf("code = %q, want waf_build", got[0].Code)
	}
	if got[0].Path != "" {
		t.Errorf("path = %q, want empty for a subsystem prefix", got[0].Path)
	}
}

func TestPathPrefixLen(t *testing.T) {
	cases := map[string]int{
		"servers[0]: x":              10, // "servers[0]"
		"servers[0].locations[2]: x": 23,
		"upstreams[1].servers[0]: x": 23,
		"at least one [[servers]]":   2, // "at" is a bare token (no colon follows)
		"waf: x":                     3, // "waf"
		"[[unknown]] reserved":       0, // starts with '[', not an identifier
		"servers[: malformed":        0, // malformed index
	}
	for in, want := range cases {
		if got := pathPrefixLen(in); got != want {
			t.Errorf("pathPrefixLen(%q) = %d, want %d", in, got, want)
		}
	}
}
