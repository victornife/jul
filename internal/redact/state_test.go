// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package redact

import (
	"bytes"
	"testing"
)

func TestStateApply(t *testing.T) {
	s := NewState([]string{"secret", "token"}, DefaultMinLen)

	got := s.Apply("request with secret and token")
	want := "request with *** and ***"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

func TestStateIgnoresShortValues(t *testing.T) {
	s := NewState([]string{"ab", "longsecret"}, DefaultMinLen)

	got := s.Apply("ab and longsecret")
	want := "ab and ***"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

func TestStateWithValue(t *testing.T) {
	s := NewState([]string{"first"}, DefaultMinLen).WithValue("second")
	got := s.Apply("first second")
	want := "*** ***"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

func TestStateWithMinLen(t *testing.T) {
	s := NewState([]string{"abc"}, 3)
	got := s.Apply("abc")
	want := "***"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}

	s2 := s.WithMinLen(4)
	// Existing values remain registered even if they are shorter than the new
	// floor; WithMinLen only filters newly added values.
	got2 := s2.Apply("abc")
	if got2 != "***" {
		t.Fatalf("Apply after raising minLen = %q, want %q", got2, "***")
	}
	// New values shorter than the raised floor are ignored.
	s3 := s2.WithValue("xyz")
	if s3.Apply("xyz") != "xyz" {
		t.Fatal("new value shorter than raised minLen was registered")
	}

	// Original state is unmodified.
	if s.Apply("abc") != "***" {
		t.Fatal("original State was mutated by WithMinLen")
	}
}

func TestStateWriter(t *testing.T) {
	var buf bytes.Buffer
	s := NewState([]string{"hunter2"}, DefaultMinLen)
	n, err := s.Writer(&buf).Write([]byte("password is hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	if n != len("password is hunter2") {
		t.Fatalf("Writer reported %d bytes, want %d", n, len("password is hunter2"))
	}
	if got, want := buf.String(), "password is ***"; got != want {
		t.Fatalf("Writer output = %q, want %q", got, want)
	}
}

func TestInstallIsAtomic(t *testing.T) {
	empty := EmptyState()
	Install(empty)

	s1 := NewState([]string{"alpha"}, DefaultMinLen)
	s2 := NewState([]string{"beta"}, DefaultMinLen)

	Install(s1)
	if Global().Count() != 1 || Global().Apply("alpha") != "***" {
		t.Fatal("first install did not take effect")
	}

	Install(s2)
	if Global().Apply("alpha") != "alpha" {
		t.Fatal("old secret still masked after replacing state")
	}
	if Global().Apply("beta") != "***" {
		t.Fatal("new secret not masked after replacing state")
	}
}

func TestLegacyAPIUsesLiveState(t *testing.T) {
	Install(EmptyState())
	SetMinLen(3)
	Add("abc")
	if Global().Apply("abc") != "***" {
		t.Fatal("Add did not update live state")
	}
	if Global().MinLen() != 3 {
		t.Fatalf("live minLen = %d, want 3", Global().MinLen())
	}
}

// TestDynamicWriterObservesLaterInstalls verifies that a Writer created before
// any secrets are installed still masks values added by later redact.Install
// calls (R6-01).
func TestStateUnion(t *testing.T) {
	s1 := NewState([]string{"alpha"}, DefaultMinLen)
	s2 := NewState([]string{"beta"}, DefaultMinLen)
	u := s1.Union(s2)
	if u.Apply("alpha beta gamma") != "*** *** gamma" {
		t.Fatalf("union did not mask both secrets: %q", u.Apply("alpha beta gamma"))
	}
	// Originals unmodified.
	if s1.Apply("beta") != "beta" {
		t.Fatal("first state mutated by Union")
	}
	if s2.Apply("alpha") != "alpha" {
		t.Fatal("second state mutated by Union")
	}
}

func TestDynamicWriterObservesLaterInstalls(t *testing.T) {
	Install(EmptyState())

	var buf bytes.Buffer
	w := Writer(&buf)

	// Writer created before secret exists.
	if _, err := w.Write([]byte("password is hunter2")); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "password is hunter2"; got != want {
		t.Fatalf("before install: %q, want %q", got, want)
	}

	// Install a secret; the same writer must now mask it.
	Install(NewState([]string{"hunter2"}, DefaultMinLen))
	buf.Reset()
	if _, err := w.Write([]byte("password is hunter2")); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "password is ***"; got != want {
		t.Fatalf("after install: %q, want %q", got, want)
	}

	// Rotate to a different secret; the same writer must observe the new state.
	Install(NewState([]string{"battery-horse-staple"}, DefaultMinLen))
	buf.Reset()
	if _, err := w.Write([]byte("password is hunter2")); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "password is hunter2"; got != want {
		t.Fatalf("after rotation (old secret): %q, want %q", got, want)
	}
	buf.Reset()
	if _, err := w.Write([]byte("token is battery-horse-staple")); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "token is ***"; got != want {
		t.Fatalf("after rotation (new secret): %q, want %q", got, want)
	}
}

// TestStateApplyOverlappingLongestFirst verifies that when one registered value
// is a substring of another, the longer value is masked first so its suffix is
// not leaked (R7-08).
func TestStateApplyOverlappingLongestFirst(t *testing.T) {
	s := NewState([]string{"token1234", "token123456"}, DefaultMinLen)
	got := s.Apply("value is token123456")
	want := "value is ***"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}

// TestStateApplyPrefixValue verifies that a shorter secret which is a prefix of
// a non-secret string does not corrupt the longer non-secret string.
func TestStateApplyPrefixValue(t *testing.T) {
	s := NewState([]string{"token"}, DefaultMinLen)
	got := s.Apply("value is tokenExtra")
	want := "value is ***Extra"
	if got != want {
		t.Fatalf("Apply = %q, want %q", got, want)
	}
}
