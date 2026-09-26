// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package plugins

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

const identityTestdata = "../../testdata/plugins/"

func testModuleBytes(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(identityTestdata + name + ".wasm")
	if err != nil {
		t.Fatalf("read testdata module: %v", err)
	}
	return b
}

func hexDigest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeModule(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadModulePathAndInlineDigests(t *testing.T) {
	a := testModuleBytes(t, "header-inject")
	dir := t.TempDir()

	fromPath, err := ReadModule(config.PluginConfig{Path: writeModule(t, dir, "a.wasm", a)})
	if err != nil {
		t.Fatalf("path module: %v", err)
	}
	want := ModuleIdentity{Source: SourcePath, Digest: "sha256:" + hexDigest(a), Size: int64(len(a))}
	if fromPath.Identity != want {
		t.Fatalf("path identity = %+v, want %+v", fromPath.Identity, want)
	}
	if string(fromPath.Bytes) != string(a) {
		t.Fatal("snapshot bytes differ from the file")
	}

	fromInline, err := ReadModule(config.PluginConfig{Inline: base64.StdEncoding.EncodeToString(a)})
	if err != nil {
		t.Fatalf("inline module: %v", err)
	}
	if fromInline.Identity.Digest != want.Digest || fromInline.Identity.Source != SourceInline {
		t.Fatalf("inline identity = %+v", fromInline.Identity)
	}

	other, err := ReadModule(config.PluginConfig{Path: writeModule(t, dir, "copy.wasm", a)})
	if err != nil {
		t.Fatal(err)
	}
	if other.Identity != fromPath.Identity {
		t.Fatal("identical bytes at a different path must have the same identity")
	}

	b := testModuleBytes(t, "request-block")
	changed, err := ReadModule(config.PluginConfig{Path: writeModule(t, dir, "a.wasm", b)})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Identity.Digest == fromPath.Identity.Digest {
		t.Fatal("changed bytes at the same path kept the old identity")
	}
	if got := fromPath.Identity.ShortDigest(); got != hexDigest(a)[:12] {
		t.Fatalf("ShortDigest = %q", got)
	}
	if got := ShortDigest("sha256:abc"); got != "abc" {
		t.Fatalf("ShortDigest(short) = %q", got)
	}
}

func TestReadModulePin(t *testing.T) {
	a := testModuleBytes(t, "header-inject")
	path := writeModule(t, t.TempDir(), "a.wasm", a)
	digest := hexDigest(a)

	for _, pin := range []string{digest, strings.ToUpper(digest), "sha256:" + digest, " SHA256:" + digest + " "} {
		m, err := ReadModule(config.PluginConfig{Path: path, SHA256: pin})
		if err != nil {
			t.Fatalf("pin %q: %v", pin, err)
		}
		if !m.Identity.Pinned {
			t.Fatalf("pin %q not reported as pinned", pin)
		}
	}

	wrong := strings.Repeat("0", 64)
	_, err := ReadModule(config.PluginConfig{Path: path, SHA256: wrong})
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("mismatch err = %v, want ErrDigestMismatch", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "sha256:"+wrong) || !strings.Contains(msg, "sha256:"+digest) {
		t.Fatalf("mismatch diagnostics must name pinned and observed digests: %q", msg)
	}
	if strings.Contains(msg, path) || len(msg) > 256 {
		t.Fatalf("mismatch diagnostics leak the path or are unbounded: %q", msg)
	}

	for _, bad := range []string{"abc", strings.Repeat("g", 64), "sha512:" + digest} {
		if _, err := ReadModule(config.PluginConfig{Path: path, SHA256: bad}); err == nil || errors.Is(err, ErrDigestMismatch) {
			t.Fatalf("malformed pin %q: err = %v, want a format error", bad, err)
		}
	}
}

func TestReadModuleRejectsInvalidAndOversized(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]config.PluginConfig{
		"no source":   {},
		"missing":     {Path: filepath.Join(dir, "absent.wasm")},
		"not wasm":    {Path: writeModule(t, dir, "text.wasm", []byte("definitely not wasm"))},
		"bad base64":  {Inline: "***"},
		"short":       {Inline: base64.StdEncoding.EncodeToString([]byte{0, 'a', 's'})},
		"wrong major": {Inline: base64.StdEncoding.EncodeToString([]byte{0, 'a', 's', 'm', 2, 0, 0, 0})},
	}
	for name, pc := range cases {
		if _, err := ReadModule(pc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	prev := moduleByteLimit
	moduleByteLimit = 16
	defer func() { moduleByteLimit = prev }()
	big := append(append([]byte{}, wasmHeader...), make([]byte, 16)...)
	if _, err := ReadModule(config.PluginConfig{Path: writeModule(t, dir, "big.wasm", big)}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized path module: %v", err)
	}
	if _, err := ReadModule(config.PluginConfig{Inline: base64.StdEncoding.EncodeToString(big)}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized inline module: %v", err)
	}
	// Exactly at the limit after decoding, but within the DecodedLen slack.
	edge := append(append([]byte{}, wasmHeader...), make([]byte, 9)...)
	if _, err := ReadModule(config.PluginConfig{Inline: base64.StdEncoding.EncodeToString(edge)}); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("inline module one byte over the limit: %v", err)
	}
}

func TestSameModules(t *testing.T) {
	a := testModuleBytes(t, "header-inject")
	dir := t.TempDir()
	path := writeModule(t, dir, "a.wasm", a)
	cfg := map[string]config.PluginConfig{"p": {Path: path}}
	m, err := ReadModule(cfg["p"])
	if err != nil {
		t.Fatal(err)
	}
	live := map[string]ModuleIdentity{"p": m.Identity}

	if !SameModules(live, cfg) {
		t.Fatal("unchanged bytes must prove the same modules")
	}
	if SameModules(live, map[string]config.PluginConfig{}) || SameModules(live, map[string]config.PluginConfig{"q": {Path: path}}) {
		t.Fatal("a different plugin set is not the same modules")
	}
	writeModule(t, dir, "a.wasm", testModuleBytes(t, "request-block"))
	if SameModules(live, cfg) {
		t.Fatal("same path with changed bytes must not prove the same modules")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if SameModules(live, cfg) {
		t.Fatal("an unreadable module is not proof")
	}
	if !SameModules(nil, nil) {
		t.Fatal("no plugins on either side is the same (empty) module set")
	}
}
