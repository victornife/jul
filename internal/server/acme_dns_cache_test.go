// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"golang.org/x/crypto/acme/autocert"
)

func TestDNSDirectoryCacheProtectsAndBoundsMaterial(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	cache := dnsDirectoryCache(dir)
	ctx := context.Background()
	if err := cache.Put(ctx, "dns01-test", []byte("first")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "dns01-test"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("private cache file mode = %o", info.Mode().Perm())
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := cache.Put(cancelled, "dns01-test", []byte("replacement")); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled cache write: %v", err)
	}
	got, err := cache.Get(ctx, "dns01-test")
	if err != nil || string(got) != "first" {
		t.Fatalf("cancelled write changed the cached material: %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dns01-test"), make([]byte, maxCachedCertBytes+100), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = cache.Get(ctx, "dns01-test")
	if err != nil || len(got) != maxCachedCertBytes+1 {
		t.Fatalf("oversized read was not bounded: %d bytes, %v", len(got), err)
	}
	if _, err := cache.Get(ctx, "../outside"); err == nil {
		t.Fatal("cache accepted a path outside its directory")
	}
	if err := cache.Delete(ctx, "dns01-test"); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.Get(ctx, "dns01-test"); !errors.Is(err, autocert.ErrCacheMiss) {
		t.Fatalf("deleted entry is still present: %v", err)
	}
}
