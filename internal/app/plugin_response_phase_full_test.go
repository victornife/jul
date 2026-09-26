// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && waf && brotli && zstd

package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"jul/internal/config"
)

// TestV2WAFInspectsThePluginOutput pins ADR 0020 §6: WAF response inspection
// runs after the response hook, so a plugin can neither smuggle content past
// the WAF nor is blocked for content it removed.
func TestV2WAFInspectsThePluginOutput(t *testing.T) {
	body := "the SECRET value"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	defer backend.Close()
	withWAF := func(c *config.Config) {
		c.Servers[0].Locations[0].WAF = &config.WAFConfig{
			Enabled: true, Mode: "block", BlockStatus: 403, ResponseBodyCheck: true,
			InlineRules: "SecResponseBodyMimeType text/plain\n" +
				`SecRule RESPONSE_BODY "@contains SECRET" "id:9430,phase:4,deny,status:451,log"` + "\n",
		}
	}
	redact := config.PluginConfig{
		Path: filepath.Join("..", "..", "testdata", "plugins", "v2-redact.wasm"), ABI: config.PluginABIV2,
		MemoryLimit: config.Size(32 << 20), Config: map[string]string{"secrets": "SECRET"},
	}
	h, _ := buildV2Handler(t, backend.URL, redact, withWAF)
	if rec := get(h); rec.Code != 200 || rec.Body.String() != "the [REDACTED] value" {
		t.Fatalf("redacted response blocked or unchanged: %d %q", rec.Code, rec.Body.String())
	}

	body = "the secret value"
	h, _ = buildV2Handler(t, backend.URL, v2Plugin("upper"), withWAF)
	if rec := get(h, "X-Mode", "body"); rec.Code != 451 {
		t.Fatalf("content introduced by the plugin passed the WAF: %d %q", rec.Code, rec.Body.String())
	}
}

func TestV2ResponsePhaseUnderBrotliAndZstd(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, strings.Repeat("abc ", 300))
	}))
	defer backend.Close()
	want := strings.Repeat("ABC ", 300)
	for _, tc := range []struct {
		enc    string
		decode func(io.Reader) (io.Reader, error)
	}{
		{"br", func(r io.Reader) (io.Reader, error) { return brotli.NewReader(r), nil }},
		{"zstd", func(r io.Reader) (io.Reader, error) { return zstd.NewReader(r) }},
	} {
		t.Run(tc.enc, func(t *testing.T) {
			h, _ := buildV2Handler(t, backend.URL, v2Plugin("upper"), func(c *config.Config) {
				c.Compression = config.CompressionConfig{Enabled: boolPtr(true), Encoders: []string{tc.enc}, Types: []string{"text/plain"}}
			})
			rec := get(h, "X-Mode", "body", "Accept-Encoding", tc.enc)
			if rec.Header().Get("Content-Encoding") != tc.enc {
				t.Fatalf("Content-Encoding = %q", rec.Header().Get("Content-Encoding"))
			}
			r, err := tc.decode(rec.Body)
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(r)
			if string(got) != want {
				t.Fatalf("decoded = %.40q", got)
			}
		})
	}
}
