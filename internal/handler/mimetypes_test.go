// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

// withSystemTypes replaces the system MIME lookup for one test.
func withSystemTypes(t *testing.T, table map[string]string) {
	t.Helper()
	prev := systemTypeByExtension
	systemTypeByExtension = func(ext string) string { return table[ext] }
	t.Cleanup(func() { systemTypeByExtension = prev })
}

var wantStreamingTypes = map[string]string{
	".m3u8": "application/vnd.apple.mpegurl",
	".mpd":  "application/dash+xml",
	".ts":   "video/mp2t",
	".m4s":  "video/iso.segment",
	".mkv":  "video/matroska",
	".aac":  "audio/aac",
}

// With no system MIME database (the distroless image), the six streaming
// extensions still get their media types (#510).
func TestStreamingTypesWithoutSystemDatabase(t *testing.T) {
	withSystemTypes(t, nil)
	for ext, want := range wantStreamingTypes {
		if got := contentTypeByExtension(ext); got != want {
			t.Errorf("%s = %q, want %q", ext, got, want)
		}
	}
	if got := contentTypeByExtension(".M3U8"); got != wantStreamingTypes[".m3u8"] {
		t.Errorf(".M3U8 = %q, want case-insensitive match", got)
	}
}

// The documented precedence: the streaming table wins for its own extensions
// (Debian maps .ts to Qt Linguist sources), and the system database is used
// for everything else.
func TestStreamingTypesPrecedence(t *testing.T) {
	withSystemTypes(t, map[string]string{
		".ts":  "text/vnd.trolltech.linguist",
		".mkv": "video/x-matroska",
		".foo": "application/x-foo",
	})
	if got := contentTypeByExtension(".ts"); got != "video/mp2t" {
		t.Errorf(".ts = %q, want video/mp2t over the system mapping", got)
	}
	if got := contentTypeByExtension(".mkv"); got != "video/matroska" {
		t.Errorf(".mkv = %q, want video/matroska", got)
	}
	if got := contentTypeByExtension(".foo"); got != "application/x-foo" {
		t.Errorf(".foo = %q, want the system mapping", got)
	}
	if got := contentTypeByExtension(".unknown"); got != "" {
		t.Errorf(".unknown = %q, want empty (caller sniffs)", got)
	}
}

// Both static paths, direct and precompressed sidecar, send the streaming
// type. A playlist would otherwise be sniffed as text/plain.
func TestStaticServesStreamingTypes(t *testing.T) {
	withSystemTypes(t, nil)
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "live.m3u8"), "#EXTM3U\n#EXT-X-VERSION:3\n")
	mustWrite(t, filepath.Join(dir, "live.m3u8.gz"), "GZIP-BYTES")
	mustWrite(t, filepath.Join(dir, "seg0.ts"), "\x47\x40\x00\x10")
	h, err := NewStaticWithOptions(config.ServerConfig{}, config.LocationConfig{Root: dir}, StaticOptions{
		Precompressed: true,
		Encoders:      []string{"gzip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c, ok := h.(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = c.Close() })
	}

	for _, tc := range []struct {
		target, enc, want string
	}{
		{"http://h/live.m3u8", "", "application/vnd.apple.mpegurl"},
		{"http://h/live.m3u8", "gzip", "application/vnd.apple.mpegurl"},
		{"http://h/seg0.ts", "", "video/mp2t"},
	} {
		headers := map[string]string{}
		if tc.enc != "" {
			headers["Accept-Encoding"] = tc.enc
		}
		rec := get(h, tc.target, headers)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s (%s) status = %d", tc.target, tc.enc, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got != tc.want {
			t.Errorf("%s (%q) Content-Type = %q, want %q", tc.target, tc.enc, got, tc.want)
		}
		if got := rec.Header().Get("Content-Encoding"); got != tc.enc {
			t.Errorf("%s Content-Encoding = %q, want %q", tc.target, got, tc.enc)
		}
	}
}
