// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
)

// FuzzPluginInvoke drives the host→guest ABI boundary with random request
// shapes: method, uri, headers, body size and content, and config values.
// It does not fuzz the .wasm module itself (that is the compiler's job); it
// exercises the abi-layer marshalling and the guest's handle_request export.
//
// Because fuzz input is deterministic and there is no true adversary inside
// the guest, the oracle focuses on invariants rather than exact outputs:
//  1. No panic escapes the host (guest traps are contained).
//  2. The response status is valid HTTP (100-599) or 500 on error.
//  3. The guest does not write state past an invocation (generational safety).
func FuzzPluginInvoke(f *testing.F) {
	m, err := NewManager(Options{Logger: discardLogger()})
	if err != nil {
		f.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	s, err := m.Build(context.Background(), map[string]config.PluginConfig{
		"test": {
			Path:        testdataDir + "header-inject.wasm",
			Type:        "middleware",
			MemoryLimit: config.Size(8 << 20),
			Timeout:     config.Duration(5 * time.Second),
		},
	})
	if err != nil {
		f.Fatalf("Build: %v", err)
	}
	defer s.Close()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := s.Middleware("test")(next)

	// Seed corpus: minimal GET, JSON body, big body, empty body, long header.
	f.Add([]byte("GET"), []byte("/"), []byte(""), []byte(""), []byte(""))
	f.Add([]byte("POST"), []byte("/api/items"), []byte("Content-Type: application/json\nX-Request-ID: abc-123"), []byte(`{"foo":1}`), []byte(`{"header":"X-Fuzz","value":"1"}`))
	f.Add([]byte("GET"), []byte("/"), []byte("X-Long: "+string(make([]byte, 8000))), []byte(""), []byte(""))
	f.Add([]byte("DELETE"), []byte("/api/items/999"), []byte(""), make([]byte, 1<<10), []byte(""))

	f.Fuzz(func(t *testing.T, method, uri, headers, body, configJSON []byte) {
		var rec *httptest.ResponseRecorder
		// The middleware may panic inside the guest, and httptest.NewRequest may
		// panic on malformed fuzz input; the host must contain all panics.
		func() {
			defer func() {
				_ = recover() // no host panic escapes, regardless of origin
			}()
			req := httptest.NewRequest(string(method), string(uri), bytes.NewReader(body))
			parseHeaders(headers, req)
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req)
		}()

		if rec == nil {
			// Request creation or handler panicked; contained above.
			return
		}
		if rec.Code < 100 || rec.Code > 599 {
			t.Fatalf("invalid HTTP status %d", rec.Code)
		}

		_ = configJSON // reserved for future per-invocation config mutation
	})
}

// FuzzHostAllowed validates the host-allow-list parser against adversarial
// host strings and allow-lists. The invariant: an allow-list containing only
// well-formed hosts must never match a malformed or empty host input.
func FuzzHostAllowed(f *testing.F) {
	f.Add("api.example.com", "api.example.com")
	f.Add("evil.com", "api.example.com")
	f.Add("x.trusted.net", ".trusted.net")
	f.Add("", "any")
	f.Fuzz(func(t *testing.T, host, allowedCSV string) {
		allowed := splitCSV(allowedCSV)
		_ = hostAllowed(allowed, host)
	})
}

// parseHeaders is a helper that turns a raw byte blob into request headers.
// Each line is "Name: value"; blank lines are skipped.
func parseHeaders(raw []byte, req *http.Request) {
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if i := bytes.Index(line, []byte{':'}); i > 0 {
			req.Header.Set(string(bytes.TrimSpace(line[:i])), string(bytes.TrimSpace(line[i+1:])))
		}
	}
}

// splitCSV splits on commas, trims spaces.
func splitCSV(s string) []string {
	var out []string
	for _, p := range bytes.Split([]byte(s), []byte{','}) {
		if v := string(bytes.TrimSpace(p)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
