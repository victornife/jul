// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
//go:build wasmplugins

package plugins

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

func signedURLGuest(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("JUL_PLUGIN_FIXTURES"); dir != "" {
		path := filepath.Join(dir, "signed-url.wasm")
		if _, err := os.Stat(path); err != nil {
			t.Fatal("fresh signed-url fixture missing:", err)
		}
		return path
	}
	path := filepath.Join(t.TempDir(), "signed-url.wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "./signed-url")
	cmd.Dir = "../../examples/plugins"
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build signed-url guest: %v\n%s", err, out)
	}
	return path
}

func TestSignedURLCompiledGuest(t *testing.T) {
	path := signedURLGuest(t)
	const key = "01234567890123456789012345678901"
	const old = "abcdefghijklmnopqrstuvwxyz012345"
	sign := func(resource, kid, k string, exp int64) string {
		h := hmac.New(sha256.New, []byte(k))
		_, _ = fmt.Fprintf(h, "jul-signed-url/v1\nGET\n%s\n%d\n%s", resource, exp, kid)
		return resource + "?" + url.Values{"exp": {fmt.Sprint(exp)}, "kid": {kid}, "sig": {base64.RawURLEncoding.EncodeToString(h.Sum(nil))}}.Encode()
	}
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"signed": {Path: path, Type: "middleware", MemoryLimit: config.Size(32 << 20), Timeout: config.Duration(2 * time.Second), Config: map[string]string{"key.current": base64.RawURLEncoding.EncodeToString([]byte(key)), "key.previous": base64.RawURLEncoding.EncodeToString([]byte(old)), "clock_skew_seconds": "10"}}})
	now := time.Now().Unix()
	for _, tc := range []struct {
		name, method, uri string
		want              int
	}{
		{"valid", "GET", sign("/media/movie.mp4", "current", key, now+300), 200},
		{"head", "HEAD", sign("/media/movie.mp4", "current", key, now+300), 200},
		{"expired proves real clock", "GET", sign("/media/movie.mp4", "current", key, now-100), 403},
		{"rotation", "GET", sign("/media/movie.mp4", "previous", old, now+300), 200},
		{"skew", "GET", sign("/media/movie.mp4", "current", key, now-2), 200},
		{"tampered path", "GET", strings.Replace(sign("/media/movie.mp4", "current", key, now+300), "movie", "other", 1), 403},
		{"bad signature", "GET", sign("/media/movie.mp4", "current", old, now+300), 403},
		{"unknown key", "GET", sign("/media/movie.mp4", "unknown", key, now+300), 403},
		{"unsigned", "GET", "/media/movie.mp4", 403}, {"post", "POST", sign("/media/movie.mp4", "current", key, now+300), 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
			rec := httptest.NewRecorder()
			s.Middleware("signed")(next).ServeHTTP(rec, httptest.NewRequest(tc.method, tc.uri, nil))
			if rec.Code != tc.want || called != (tc.want == 200) {
				t.Fatalf("status %d next %v", rec.Code, called)
			}
			if tc.want == 403 && (rec.Header().Get("Cache-Control") != "no-store" || rec.Body.String() != "Forbidden\n") {
				t.Fatal("denial leaked or is cacheable")
			}
		})
	}
	bad := buildSet(t, m, map[string]config.PluginConfig{"bad": {Path: path, Type: "middleware", MemoryLimit: config.Size(32 << 20), Timeout: config.Duration(time.Second), Config: map[string]string{"key.current": "bad"}}})
	next, called := okNext()
	rec := httptest.NewRecorder()
	bad.Middleware("bad")(next).ServeHTTP(rec, httptest.NewRequest("GET", "/media/movie.mp4", nil))
	if *called || rec.Code != 403 {
		t.Fatal("bad config failed open")
	}
}
