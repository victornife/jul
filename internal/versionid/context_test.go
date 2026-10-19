// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package versionid

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"jul/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testContext(t *testing.T, b byte) *Context {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = b + byte(i)
	}
	c, err := New(key)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func TestStandardHMACFramingAndScope(t *testing.T) {
	c := testContext(t, 1)
	key := make([]byte, 32)
	for i := range key {
		key[i] = 1 + byte(i)
	}
	mac := func(domain string, data []byte) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte("jul/versionid/v1\x00"))
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(domain)))
		h.Write(n[:])
		h.Write([]byte(domain))
		binary.BigEndian.PutUint64(n[:], uint64(len(data)))
		h.Write(n[:])
		h.Write(data)
		return h.Sum(nil)
	}
	want := "ev1:" + hex.EncodeToString(mac("context-id", nil)[:16]) + ":" + hex.EncodeToString(mac("effective-config", []byte("synthetic")))
	if got := c.Digest("effective-config", []byte("synthetic")); got != want {
		t.Fatalf("standard primitive/framing disagreement")
	}
	if c.Digest("a", []byte("bc")) == c.Digest("ab", []byte("c")) {
		t.Fatal("ambiguous framing")
	}
	if c.Digest("one", []byte("value")) == c.Digest("two", []byte("value")) {
		t.Fatal("domains merged")
	}
	if testContext(t, 2).Digest("effective-config", []byte("synthetic")) == want {
		t.Fatal("independent keys merged")
	}
	if c.Config(nil) != "" {
		t.Fatal("nil configuration version")
	}
	if _, err := New([]byte("weak")); err == nil {
		t.Fatal("bad key size accepted")
	}
	var nilContext *Context
	if nilContext.ID() != Process().ID() || nilContext.Config(&config.Config{}) != Process().Config(&config.Config{}) {
		t.Fatal("internal fallback context mismatch")
	}
	if strings.Contains(fmt.Sprintf("%v %#v %v %#v", c, c, *c, *c), hex.EncodeToString(key)) {
		t.Fatal("formatting reveals key")
	}
}
func TestVersionFormatAndComparison(t *testing.T) {
	c := testContext(t, 3)
	a := c.Digest("test", []byte("a"))
	b := c.Digest("test", []byte("b"))
	if id, ok := Parse(a); !ok || id != c.ID() {
		t.Fatal("valid format rejected")
	}
	if Comparison(a, b) != "same_context" || Comparison(a, testContext(t, 4).Digest("test", nil)) != "different_context" {
		t.Fatal("comparison scope lost")
	}
	for _, bad := range []string{"", "0123456789abcdef", "ev2:unknown", "ev1::", strings.ToUpper(a), a[:len(a)-1] + "z", a + ":extra"} {
		if _, ok := Parse(bad); ok {
			t.Fatalf("unknown/corrupt format accepted: %q", bad)
		}
		if Comparison(a, bad) != "unverifiable" {
			t.Fatal("legacy format compared")
		}
	}
}
func TestReferencesInEveryStringRemainRotationSensitive(t *testing.T) {
	c := testContext(t, 5)
	path := filepath.Join(t.TempDir(), "reference")
	raw := config.ProxyTarget("http://127.0.0.1:9000", ":8080")
	raw.Servers[0].Name = "before-${env:JUL_VERSION_WEAK}-${file:" + path + "}-${env:JUL_VERSION_WEAK}-after"
	raw.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9000/${env:JUL_VERSION_WEAK}"
	rawBytes, _ := config.Marshal(raw)
	rawDigest := sha256.Sum256(rawBytes)
	seen := map[string]string{}
	for _, value := range []string{"a", "b", "", "a"} {
		t.Setenv("JUL_VERSION_WEAK", value)
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		candidate, err := config.NewCandidate(raw)
		if err != nil {
			t.Fatal(err)
		}
		if err := config.Validate(candidate.Effective); err != nil {
			t.Fatal(err)
		}
		effective := c.Config(candidate.Effective)
		same, _ := config.Marshal(candidate.Raw)
		if sha256.Sum256(same) != rawDigest {
			t.Fatal("reference rotation changed raw CAS")
		}
		if previous, ok := seen[value]; ok && effective != previous {
			t.Fatal("rollback/unchanged value lost equality")
		}
		for prior, tag := range seen {
			if prior != value && effective == tag {
				t.Fatal("accepted resolved change undetected")
			}
		}
		// An attacker can reproduce canonical candidate bytes but cannot compute
		// the external HMAC without the private key. This check accompanies the
		// independently verified HMAC construction, not just two unequal outputs.
		canonical, _ := config.Marshal(candidate.Effective)
		unkeyed := sha256.Sum256(canonical)
		if strings.Contains(effective, hex.EncodeToString(unkeyed[:8])) {
			t.Fatal("unkeyed compatibility alias")
		}
		seen[value] = effective
	}
	// File-only rotation, with env and raw reference unchanged.
	t.Setenv("JUL_VERSION_WEAK", "a")
	os.WriteFile(path, []byte("a"), 0600)
	before, _ := config.NewCandidate(raw)
	os.WriteFile(path, []byte("b"), 0600)
	after, _ := config.NewCandidate(raw)
	if c.Config(before.Effective) == c.Config(after.Effective) {
		t.Fatal("file-only rotation undetected")
	}
	after.Effective.Servers[0].Locations[0].Cache = true
	if c.Config(before.Effective) == c.Config(after.Effective) {
		t.Fatal("non-secret change undetected")
	}
}
