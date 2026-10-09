// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"jul/internal/config"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMIMEGenerationsAndInvalidCandidate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file.ts"), []byte("content"), 0600); err != nil {
		t.Fatal(err)
	}
	table := map[string]string{".ts": "application/x-first"}
	cfg := &config.Config{MIME: &config.MIMEConfig{Types: &table, DefaultType: "text/plain"}, Servers: []config.ServerConfig{
		{Listen: "127.0.0.1:0", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Root: root}}},
	}}
	f, cleanup := minimalFactory(t)
	defer cleanup()
	first, _, err := f.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	table[".ts"] = "application/x-second"
	second, retireFirst, err := f.Build(context.Background(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	if retireFirst != nil {
		defer retireFirst()
	}
	get := func(generation map[string]http.Handler) string {
		rec := httptest.NewRecorder()
		generation["127.0.0.1:0"].ServeHTTP(rec, httptest.NewRequest("GET", "/file.ts", nil))
		return rec.Header().Get("Content-Type")
	}
	if get(first) != "application/x-first" || get(second) != "application/x-second" {
		t.Fatal("MIME generations share mutable state")
	}
	table[".ts"] = "text/plain\r\nX: bad"
	if err = config.Validate(cfg); err == nil {
		t.Fatal("invalid candidate accepted")
	}
	if get(second) != "application/x-second" {
		t.Fatal("invalid candidate changed published generation")
	}
}
