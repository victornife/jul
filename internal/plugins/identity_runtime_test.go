// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

// TestBuildRecordsCompiledModuleIdentity proves every plugin in a Set carries
// the identity of the bytes it compiled, for path and inline sources.
func TestBuildRecordsCompiledModuleIdentity(t *testing.T) {
	m := testManager(t)
	a := testModuleBytes(t, "header-inject")
	s := buildSet(t, m, map[string]config.PluginConfig{
		"file":   pcfg("header-inject"),
		"inline": pcfg("header-inject", func(pc *config.PluginConfig) { pc.Path = ""; pc.Inline = base64.StdEncoding.EncodeToString(a) }),
	})
	ids := s.Identities()
	want := "sha256:" + hexDigest(a)
	if ids["file"].Digest != want || ids["file"].Source != SourcePath {
		t.Fatalf("file identity = %+v", ids["file"])
	}
	if ids["inline"].Digest != want || ids["inline"].Source != SourceInline {
		t.Fatalf("inline identity = %+v", ids["inline"])
	}
	if (*Set)(nil).Identities() != nil {
		t.Fatal("nil set must report no identities")
	}
}

// TestCompileUsesTheHashedSnapshot is the deterministic TOCTOU regression:
// the module file is replaced with different bytes after Jul read and hashed
// it but before compilation. The compiled plugin must be the hashed bytes (its
// behaviour and identity), and a pin on those bytes must still hold.
func TestCompileUsesTheHashedSnapshot(t *testing.T) {
	a := testModuleBytes(t, "header-inject")
	b := testModuleBytes(t, "request-block")
	path := filepath.Join(t.TempDir(), "mod.wasm")
	if err := os.WriteFile(path, a, 0o600); err != nil {
		t.Fatal(err)
	}
	afterModuleRead = func(string) {
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Errorf("swap module: %v", err)
		}
	}
	defer func() { afterModuleRead = nil }()

	m := testManager(t)
	pin := hexDigest(a)
	s := buildSet(t, m, map[string]config.PluginConfig{"p": pcfg("header-inject", func(pc *config.PluginConfig) {
		pc.Path = path
		pc.SHA256 = pin
	})})
	afterModuleRead = nil

	if got := s.Identities()["p"].Digest; got != "sha256:"+pin {
		t.Fatalf("identity %s is not the hashed snapshot", got)
	}
	next, _ := okNext()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Block", "1")
	s.Middleware("p")(next).ServeHTTP(rec, req)
	if rec.Header().Get("X-Plugin") != "header-inject" {
		t.Fatal("compiled module is not the hashed bytes")
	}

	// The next candidate reads the replaced bytes: same path, new identity,
	// and the old pin now fails the build before anything is published.
	_, err := m.Build(context.Background(), map[string]config.PluginConfig{"p": pcfg("", func(pc *config.PluginConfig) {
		pc.Path = path
		pc.SHA256 = pin
	})})
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("stale pin on changed bytes: err = %v, want ErrDigestMismatch", err)
	}
	unpinned := buildSet(t, m, map[string]config.PluginConfig{"p": pcfg("", func(pc *config.PluginConfig) { pc.Path = path })})
	if got := unpinned.Identities()["p"].Digest; got != "sha256:"+hexDigest(b) {
		t.Fatalf("same path with changed bytes kept identity %s", got)
	}
}
