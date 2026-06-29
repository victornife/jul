package config

import (
	"bytes"
	"testing"
	"time"
)

// --- Duration MarshalText ---

func TestDurationMarshalText(t *testing.T) {
	d := Duration(30 * time.Second)
	b, err := d.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("30s")) {
		t.Fatalf("marshal = %q, want 30s", b)
	}
}

func TestDurationMarshalTextZero(t *testing.T) {
	d := Duration(0)
	b, err := d.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("0s")) {
		t.Fatalf("marshal = %q, want 0s", b)
	}
}

func TestDurationStd(t *testing.T) {
	d := Duration(5 * time.Minute)
	if d.Std() != 5*time.Minute {
		t.Fatalf("std = %v, want 5m", d.Std())
	}
}

func TestDurationRoundTrip(t *testing.T) {
	original := Duration(2*time.Hour + 30*time.Minute)
	b, _ := original.MarshalText()

	var parsed Duration
	if err := parsed.UnmarshalText(b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if parsed != original {
		t.Fatalf("round-trip failed: %v != %v", parsed, original)
	}
}

// --- Size MarshalText ---

func TestSizeMarshalTextBytes(t *testing.T) {
	s := Size(512)
	b, err := s.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("512")) {
		t.Fatalf("marshal = %q, want 512", b)
	}
}

func TestSizeMarshalTextKilo(t *testing.T) {
	s := Size(64 * 1024)
	b, err := s.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("64k")) {
		t.Fatalf("marshal = %q, want 64k", b)
	}
}

func TestSizeMarshalTextMega(t *testing.T) {
	s := Size(128 * 1024 * 1024)
	b, err := s.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("128m")) {
		t.Fatalf("marshal = %q, want 128m", b)
	}
}

func TestSizeMarshalTextGiga(t *testing.T) {
	s := Size(2 * 1024 * 1024 * 1024)
	b, err := s.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("2g")) {
		t.Fatalf("marshal = %q, want 2g", b)
	}
}

func TestSizeMarshalTextZero(t *testing.T) {
	s := Size(0)
	b, err := s.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(b, []byte("0")) {
		t.Fatalf("marshal = %q, want 0", b)
	}
}

func TestSizeBytes(t *testing.T) {
	s := Size(1024)
	if s.Bytes() != 1024 {
		t.Fatalf("bytes = %d, want 1024", s.Bytes())
	}
}

func TestSizeRoundTrip(t *testing.T) {
	cases := []string{"512", "64k", "128m", "2g", "0"}
	for _, c := range cases {
		var s Size
		if err := s.UnmarshalText([]byte(c)); err != nil {
			t.Fatalf("unmarshal %q: %v", c, err)
		}
		b, _ := s.MarshalText()
		if string(b) != c {
			t.Fatalf("round-trip %q -> %q", c, b)
		}
	}
}

// --- UpstreamServer MarshalText ---

func TestUpstreamServerMarshalTextDefaultWeight(t *testing.T) {
	u := UpstreamServer{Address: "127.0.0.1:3000", Weight: 1}
	b, err := u.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(b) != "127.0.0.1:3000" {
		t.Fatalf("marshal = %q, want 127.0.0.1:3000", b)
	}
}

func TestUpstreamServerMarshalTextWithWeight(t *testing.T) {
	u := UpstreamServer{Address: "127.0.0.1:3000", Weight: 5}
	b, err := u.MarshalText()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(b) != "127.0.0.1:3000 weight=5" {
		t.Fatalf("marshal = %q, want 127.0.0.1:3000 weight=5", b)
	}
}

func TestUpstreamServerRoundTrip(t *testing.T) {
	cases := []string{
		"127.0.0.1:3000",
		"127.0.0.1:3000 weight=5",
		"[::1]:8080",
	}
	for _, c := range cases {
		var u UpstreamServer
		if err := u.UnmarshalText([]byte(c)); err != nil {
			t.Fatalf("unmarshal %q: %v", c, err)
		}
		b, _ := u.MarshalText()
		var reparsed UpstreamServer
		if err := reparsed.UnmarshalText(b); err != nil {
			t.Fatalf("re-unmarshal %q: %v", string(b), err)
		}
		if u.Address != reparsed.Address || u.Weight != reparsed.Weight {
			t.Fatalf("round-trip mismatch for %q", c)
		}
	}
}
