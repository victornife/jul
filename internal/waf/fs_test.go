// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"testing"
	"testing/fstest"
)

func TestNormalizeFS(t *testing.T) {
	inner := fstest.MapFS{
		"foo/bar/baz.txt": &fstest.MapFile{Data: []byte("hello")},
		"foo/qux.txt":     &fstest.MapFile{Data: []byte("world")},
	}
	n := &normalizeFS{inner: inner}

	// 1. ReadFile with backslashes must resolve.
	data, err := n.ReadFile(`foo\bar\baz.txt`)
	if err != nil {
		t.Fatalf("ReadFile backslash path: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("ReadFile got %q, want hello", string(data))
	}

	// 2. Glob with backslashes must resolve and return forward slashes.
	matches, err := n.Glob(`foo\*.txt`)
	if err != nil {
		t.Fatalf("Glob backslash pattern: %v", err)
	}
	if len(matches) != 1 || matches[0] != "foo/qux.txt" {
		t.Errorf("Glob matches = %v, want [foo/qux.txt]", matches)
	}

	// 3. Open + ReadDir with backslashes.
	entries, err := n.ReadDir(`foo\bar`)
	if err != nil {
		t.Fatalf("ReadDir backslash path: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "baz.txt" {
		t.Errorf("ReadDir entries = %v, want [baz.txt]", entries)
	}

	// 4. Forward-slash paths must still work.
	_, err = n.ReadFile("foo/bar/baz.txt")
	if err != nil {
		t.Fatalf("ReadFile forward slash path: %v", err)
	}
}

func TestNormalizeFS_StatStub(t *testing.T) {
	inner := fstest.MapFS{
		"x/y.txt": &fstest.MapFile{Data: []byte("hi")},
	}
	n := &normalizeFS{inner: inner}

	fi, err := n.Stat(`x\y.txt`)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if fi.Name() != "y.txt" {
		t.Errorf("Stat.Name = %q, want y.txt", fi.Name())
	}
}

func TestNormSlashes(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`foo\bar\baz`, "foo/bar/baz"},
		{`foo/bar/baz`, "foo/bar/baz"},
		{`foo\bar/baz`, "foo/bar/baz"},
		{"", ""},
		{"no-slashes", "no-slashes"},
	}
	for _, tt := range tests {
		got := normSlashes(tt.in)
		if got != tt.want {
			t.Errorf("normSlashes(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
