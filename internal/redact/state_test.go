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
