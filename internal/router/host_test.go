// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package router

import "testing"

func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Example.COM", "example.com"},
		{"example.com:8080", "example.com"},
		{"  EXAMPLE.COM  ", "example.com"},
		{"[::1]:8080", "[::1]"},
		{"[2001:db8::1]", "[2001:db8::1]"},
		{"192.168.1.1:9090", "192.168.1.1"},
		{"localhost", "localhost"},
		{":8080", ""},
		{"", ""},
	}

	for _, c := range cases {
		got := normalizeHost(c.in)
		if got != c.want {
			t.Fatalf("normalizeHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHostScoreExact(t *testing.T) {
	names := []string{"example.com", "api.example.com"}
	score := hostScore(names, "example.com")
	if score != 3 {
		t.Fatalf("score = %d, want 3 (exact)", score)
	}
}

func TestHostScoreWildcard(t *testing.T) {
	names := []string{"*.example.com"}
	score := hostScore(names, "www.example.com")
	if score != 2 {
		t.Fatalf("score = %d, want 2 (wildcard)", score)
	}
}

func TestHostScoreWildcardNoMatchForRoot(t *testing.T) {
	names := []string{"*.example.com"}
	score := hostScore(names, "example.com")
	if score != 0 {
		t.Fatalf("score = %d, want 0 (wildcard should not match root domain)", score)
	}
}

func TestHostScoreNoMatch(t *testing.T) {
	names := []string{"other.com"}
	score := hostScore(names, "example.com")
	if score != 0 {
		t.Fatalf("score = %d, want 0", score)
	}
}

func TestHostScoreBestOfMultiple(t *testing.T) {
	names := []string{"*.example.com", "example.com"}
	score := hostScore(names, "example.com")
	if score != 3 {
		t.Fatalf("score = %d, want 3 (exact beats wildcard)", score)
	}
}

func TestHostScoreCaseInsensitive(t *testing.T) {
	names := []string{"EXAMPLE.COM"}
	score := hostScore(names, "example.com")
	if score != 3 {
		t.Fatalf("score = %d, want 3 (case-insensitive)", score)
	}
}

func TestHostScoreEmptyNames(t *testing.T) {
	score := hostScore([]string{}, "example.com")
	if score != 0 {
		t.Fatalf("score = %d, want 0", score)
	}
}
