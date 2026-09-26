// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"jul/internal/config"
)

// FuzzResponsePhase drives arbitrary action responses (status, one header,
// body, flush pattern) and guest mutations through a body subscription. The
// oracle: no host panic, a final status in range, framing the host owns
// (Content-Length) always matches the bytes sent, and a transformed body is
// exactly the guest's output.
func FuzzResponsePhase(f *testing.F) {
	f.Add(200, "Content-Type", "text/plain", []byte("hello"), false, "upper")
	f.Add(204, "X-A", "b", []byte{}, true, "echo")
	f.Add(206, "Content-Range", "bytes 0-1/2", []byte("ab"), false, "upper")
	f.Add(200, "Content-Encoding", "gzip", []byte{0x1f, 0x8b}, false, "reject")
	f.Add(599, "Trailer", "X-T", []byte("t"), true, "set-status")
	f.Add(302, "Location", "/x", []byte(strings.Repeat("z", 300)), false, "headers")

	m, err := NewManager(Options{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = m.Close() })
	s, err := m.Build(f.Context(), map[string]config.PluginConfig{"p": v2cfg(func(pc *config.PluginConfig) { pc.MaxResponseBody = config.Size(256) })})
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = s.Close() })

	f.Fuzz(func(t *testing.T, status int, name, value string, body []byte, flush bool, op string) {
		if status < 200 || status > 599 {
			status = 200 + (status%400+400)%400
		}
		origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if name != "" && !strings.ContainsAny(name, "\r\n: \t") && !strings.ContainsAny(value, "\r\n") {
				w.Header().Set(name, value)
			}
			w.WriteHeader(status)
			half := len(body) / 2
			_, _ = w.Write(body[:half])
			if flush {
				w.(http.Flusher).Flush()
			}
			_, _ = w.Write(body[half:])
		})
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("X-Mode", "body")
		r.Header.Set("X-Op", op)
		r.Header.Set("X-Status-To", strconv.Itoa(status))
		rec := httptest.NewRecorder()
		chainFor(s, origin, "p").ServeHTTP(rec, r)
		if rec.Code < 100 || rec.Code > 599 {
			t.Fatalf("status %d", rec.Code)
		}
		if cl := rec.Header().Get("Content-Length"); cl != "" && rec.Header().Get("X-Body-State") == "0" {
			if n, err := strconv.Atoi(cl); err != nil || n != rec.Body.Len() {
				t.Fatalf("Content-Length %q for %d bytes", cl, rec.Body.Len())
			}
		}
		if op == "upper" && rec.Header().Get("X-Replace-RC") == "0" && rec.Body.String() != strings.ToUpper(string(body)) {
			t.Fatalf("transformed body %q, want %q", rec.Body.String(), strings.ToUpper(string(body)))
		}
	})
}
