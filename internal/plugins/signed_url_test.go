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

func signedURLExampleGuest(t *testing.T, name string) string {
	t.Helper()
	if dir := os.Getenv("JUL_PLUGIN_FIXTURES"); dir != "" {
		path := filepath.Join(dir, name+".wasm")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fresh %s fixture missing: %v", name, err)
		}
		return path
	}
	path := filepath.Join(t.TempDir(), name+".wasm")
	cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-buildmode=c-shared", "-o", path, "./"+name)
	cmd.Dir = "../../examples/plugins"
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s guest: %v\n%s", name, err, out)
	}
	return path
}

func signedURLGuestLink(resource, kid, key string, exp int64) string {
	h := hmac.New(sha256.New, []byte(key))
	_, _ = fmt.Fprintf(h, "jul-signed-url/v1\nGET\n%s\n%d\n%s", resource, exp, kid)
	return resource + "?" + url.Values{"exp": {fmt.Sprint(exp)}, "kid": {kid}, "sig": {base64.RawURLEncoding.EncodeToString(h.Sum(nil))}}.Encode()
}

func TestSignedURLCompiledGuest(t *testing.T) {
	path := signedURLExampleGuest(t, "signed-url")
	const key = "01234567890123456789012345678901"
	const old = "abcdefghijklmnopqrstuvwxyz012345"
	sign := signedURLGuestLink
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

// Authorization stays in the request phase even when a v2 plugin subscribes
// before it. Denials must never reach the origin or the response callback.
func TestSignedURLCompiledGuestMixedABI(t *testing.T) {
	const key = "01234567890123456789012345678901"
	authPath := signedURLExampleGuest(t, "signed-url")
	metadataPath := signedURLExampleGuest(t, "v2-status-header")
	m := testManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"auth": {
			Path: authPath, ABI: ABIJulV1, Type: "middleware",
			MemoryLimit: config.Size(32 << 20), Timeout: config.Duration(2 * time.Second),
			Config: map[string]string{"key.current": base64.RawURLEncoding.EncodeToString([]byte(key))},
		},
		"metadata": {
			Path: metadataPath, ABI: ABIJulV2, Type: "middleware",
			MemoryLimit: config.Size(32 << 20), Timeout: config.Duration(2 * time.Second),
		},
	})
	now := time.Now().Unix()
	valid := signedURLGuestLink("/media/movie.mp4", "current", key, now+300)
	for _, names := range [][]string{{"auth", "metadata"}, {"metadata", "auth"}} {
		for _, tc := range []struct {
			name, method, uri, rangeHeader, body, statusClass, cacheControl string
			status                                                          int
			allowed                                                         bool
		}{
			{"get", "GET", valid, "", "media", "2xx", "public", 200, true},
			{"head", "HEAD", valid, "", "", "2xx", "public", 200, true},
			{"range", "GET", valid, "bytes=1-3", "edi", "2xx", "public", 206, true},
			{"origin error", "GET", valid, "", "unavailable", "5xx", "no-store", 503, true},
			{"unsigned", "GET", "/media/movie.mp4", "", "Forbidden\n", "", "no-store", 403, false},
			{"expired", "GET", signedURLGuestLink("/media/movie.mp4", "current", key, now-100), "", "Forbidden\n", "", "no-store", 403, false},
			{"tampered", "GET", strings.Replace(valid, "movie", "other", 1), "", "Forbidden\n", "", "no-store", 403, false},
		} {
			t.Run(strings.Join(names, "/")+"/"+tc.name, func(t *testing.T) {
				calls := 0
				origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.Header().Set("Server", "origin")
					w.Header().Set("X-Powered-By", "backend")
					w.Header().Set("Cache-Control", "public")
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.body)))
					if r.Method == http.MethodHead {
						w.Header().Set("Content-Length", "5")
					}
					if r.Header.Get("Range") != "" {
						w.Header().Set("Content-Range", "bytes 1-3/5")
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				})
				r := httptest.NewRequest(tc.method, tc.uri, nil)
				if tc.rangeHeader != "" {
					r.Header.Set("Range", tc.rangeHeader)
				}
				rec := serve(chainFor(s, origin, names...), r)
				wantCalls := 0
				if tc.allowed {
					wantCalls = 1
				}
				if rec.Code != tc.status || calls != wantCalls || rec.Body.String() != tc.body {
					t.Fatalf("status=%d origin calls=%d body=%q", rec.Code, calls, rec.Body.String())
				}
				if rec.Header().Get("X-Upstream-Status-Class") != tc.statusClass || rec.Header().Get("Cache-Control") != tc.cacheControl {
					t.Fatalf("response callback/denial headers: %v", rec.Header())
				}
				if rec.Header().Get("Server") != "" || rec.Header().Get("X-Powered-By") != "" {
					t.Fatalf("backend headers survived v2 callback: %v", rec.Header())
				}
				if tc.rangeHeader != "" && (rec.Header().Get("Content-Range") != "bytes 1-3/5" || rec.Header().Get("Content-Length") != "3") {
					t.Fatalf("partial response changed: %v", rec.Header())
				}
				if tc.method == http.MethodHead && rec.Header().Get("Content-Length") != "5" {
					t.Fatalf("HEAD representation length changed: %v", rec.Header())
				}
			})
		}
	}
}
