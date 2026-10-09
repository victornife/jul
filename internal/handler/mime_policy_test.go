// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"jul/internal/config"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStaticMIMEPolicy(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"file.ts", "file.foo", "file.unknown"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("hello content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	table := map[string]string{".ts": "application/x-custom"}
	empty := map[string]string{}
	for _, tc := range []struct {
		policy     *config.MIMEConfig
		file, want string
	}{
		{nil, "file.ts", "video/mp2t"},
		{&config.MIMEConfig{Types: &table, DefaultType: "text/plain"}, "file.ts", "application/x-custom"},
		{&config.MIMEConfig{Types: &table, DefaultType: "text/plain"}, "file.foo", "text/plain"},
		{&config.MIMEConfig{Types: &table}, "file.foo", "application/octet-stream"},
		{&config.MIMEConfig{Types: &empty, DefaultType: "application/x-empty"}, "file.ts", "application/x-empty"},
		{&config.MIMEConfig{DefaultType: "application/x-unknown"}, "file.unknown", "application/x-unknown"},
		{&config.MIMEConfig{DefaultType: "application/x-unknown"}, "file.ts", "video/mp2t"},
	} {
		handler, err := NewStatic(config.ServerConfig{}, config.LocationConfig{Root: root, MIME: tc.policy})
		if err != nil {
			t.Fatal(err)
		}
		defer handler.(interface{ Close() error }).Close()
		for _, method := range []string{"GET", "HEAD"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(method, "/"+tc.file, nil))
			if got := rec.Header().Get("Content-Type"); got != tc.want {
				t.Fatalf("%s %s: %q want %q", method, tc.file, got, tc.want)
			}
			if method == "HEAD" && rec.Body.Len() != 0 {
				t.Fatal("HEAD body")
			}
		}
	}
}

func TestStaticMIMEPrecompressedAndHeaderPrecedence(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "file.foo")
	if err := os.WriteFile(source, []byte("original bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	sidecar := source + ".gz"
	if err := os.WriteFile(sidecar, []byte("encoded bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(sidecar, future, future); err != nil {
		t.Fatal(err)
	}
	table := map[string]string{".foo": "application/x-custom"}
	handler, err := NewStaticWithOptions(config.ServerConfig{}, config.LocationConfig{Root: root, MIME: &config.MIMEConfig{Types: &table}},
		StaticOptions{Precompressed: true, Encoders: []string{"gzip"}})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.(interface{ Close() error }).Close()
	for _, override := range []bool{false, true} {
		req := httptest.NewRequest("GET", "/file.foo", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		want := "application/x-custom"
		if override {
			rec.Header().Set("Content-Type", "application/x-explicit")
			want = "application/x-explicit"
		}
		handler.ServeHTTP(rec, req)
		if rec.Header().Get("Content-Type") != want || rec.Header().Get("Content-Encoding") != "gzip" {
			t.Fatal(rec.Header())
		}
	}
}

func TestProxyFlushPolicy(t *testing.T) {
	if proxyFlushInterval(config.LocationConfig{}) != 0 {
		t.Fatal("default")
	}
	if proxyFlushInterval(config.LocationConfig{ProxyBuffering: config.Bool(false)}) != -1 {
		t.Fatal("off")
	}
	if proxyFlushInterval(config.LocationConfig{ProxyBuffering: config.Bool(true)}) != 0 {
		t.Fatal("true must be rejected by validation")
	}
}

func TestMIMEContentTypeFallbacks(t *testing.T) {
	h := &staticHandler{}
	if h.contentType(".unknown-native", true) != "" || h.contentType(".TS", false) != "video/mp2t" {
		t.Fatal("native fallback")
	}
	h.mimePolicy = &config.MIMEConfig{}
	if h.contentType(".unknown-native", false) != "" {
		t.Fatal("sniff fallback")
	}
}
