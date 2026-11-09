// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

// These tests characterize the pinned Coraza engine's buffering behaviour for
// the #695 resource assessment (docs/reviews/2026-11-09-695-waf-wasm-resource-assessment.md).
// They pin observed behaviour; they are not a statement that it is desired.

func assessmentFirewall(t *testing.T, cfg config.WAFConfig) *Firewall {
	t.Helper()
	cfg.Enabled = true
	if cfg.Mode == "" {
		cfg.Mode = "block"
	}
	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("waf.New: %v", err)
	}
	t.Cleanup(func() { _ = fw.Close() })
	return fw
}

func bodySpillFiles(t *testing.T, dir string) int {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, "body*"))
	if err != nil {
		t.Fatal(err)
	}
	return len(m)
}

// TestAssessmentRequestBodySpill records where an inspected request body is
// held while the inner handler runs: Coraza's in-memory limit decides between
// heap and a temporary file under os.TempDir().
func TestAssessmentRequestBodySpill(t *testing.T) {
	cases := []struct {
		name      string
		crs       bool
		limit     string
		bodyBytes int
		wantFiles int
	}{
		{"no-crs default limit 64KiB body", false, "", 64 << 10, 0},
		{"no-crs 1MiB limit 512KiB body", false, "1mb", 512 << 10, 0},
		{"crs default limit 64KiB body", true, "", 64 << 10, 0},
		{"crs 1MiB limit 96KiB body", true, "1mb", 96 << 10, 0},
		{"crs 1MiB limit 512KiB body", true, "1mb", 512 << 10, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			cfg := config.WAFConfig{CRSEnabled: tc.crs}
			if tc.limit != "" {
				if err := cfg.RequestBodyLimit.UnmarshalText([]byte(tc.limit)); err != nil {
					t.Fatal(err)
				}
			}
			fw := assessmentFirewall(t, cfg)
			during := -1
			var got int
			h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				during = bodySpillFiles(t, tmp)
				var buf bytes.Buffer
				n, _ := buf.ReadFrom(r.Body)
				got = int(n)
				w.WriteHeader(http.StatusNoContent)
			}))
			req := httptest.NewRequest(http.MethodPost, "http://app.example.test/upload", bytes.NewReader(jsonBody(tc.bodyBytes)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusNoContent || got != tc.bodyBytes {
				t.Fatalf("status=%d forwarded=%d want 204/%d", rr.Code, got, tc.bodyBytes)
			}
			if during != tc.wantFiles {
				t.Fatalf("temp body files during handler = %d, want %d", during, tc.wantFiles)
			}
			if after := bodySpillFiles(t, tmp); after != 0 {
				t.Fatalf("temp body files after completion = %d, want 0", after)
			}
		})
	}
}

// TestAssessmentCRSResponseBodyAccess records whether a phase-4 response rule
// executes, with and without response_body_check, with and without CRS.
func TestAssessmentCRSResponseBodyAccess(t *testing.T) {
	const marker = "ASSESSMENT-LEAK-MARKER"
	rule := `SecRule RESPONSE_BODY "@contains ` + marker + `" "id:1695001,phase:4,deny,status:403,log"`
	cases := []struct {
		name          string
		crs           bool
		responseCheck bool
		contentType   string
		wantStatus    int
	}{
		{"no-crs check=false", false, false, "text/plain", http.StatusOK},
		// response_body_check alone does not select any MIME type.
		{"no-crs check=true no mime directive", false, true, "text/plain", http.StatusOK},
		// @coraza.conf-recommended enables response body access and text MIME
		// types, so CRS inspects text responses even when the Jul field is false.
		{"crs check=false text/plain", true, false, "text/plain", http.StatusForbidden},
		{"crs check=true text/plain", true, true, "text/plain", http.StatusForbidden},
		{"crs check=false application/json", true, false, "application/json", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fw := assessmentFirewall(t, config.WAFConfig{CRSEnabled: tc.crs, ResponseBodyCheck: tc.responseCheck, InlineRules: rule})
			h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(strings.Repeat("x", 1024) + marker))
			}))
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://app.example.test/page", nil))
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
		})
	}
}

// TestAssessmentArgumentLimitPartialInspection records that the pinned engine
// silently drops JSON arguments beyond SecArgumentsLimit (default 1000), so a
// CRS-detectable payload after the limit passes in block mode unless a rule
// acts on ARGUMENTS_LIMIT_REACHED.
func TestAssessmentArgumentLimitPartialInspection(t *testing.T) {
	const guard = `SecRule ARGUMENTS_LIMIT_REACHED "@eq 1" "id:1695190,phase:2,deny,status:413,log"`
	cases := []struct {
		name       string
		items      int
		attackLast bool
		guard      bool
		wantStatus int
	}{
		{"301 args clean", 100, false, false, http.StatusOK},
		{"301 args attack last", 100, true, false, http.StatusForbidden},
		{"1201 args attack last passes uninspected", 400, true, false, http.StatusOK},
		{"guard 301 args clean", 100, false, true, http.StatusOK},
		{"guard 1003 args clean rejected", 334, false, true, http.StatusRequestEntityTooLarge},
		{"guard 1201 args attack last", 400, true, true, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.WAFConfig{CRSEnabled: true}
			if tc.guard {
				cfg.InlineRules = guard
			}
			fw := assessmentFirewall(t, cfg)
			h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodPost, "http://app.example.test/api", bytes.NewReader(itemsBody(tc.items, tc.attackLast)))
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
		})
	}
}

// itemsBody returns a JSON array of n objects (3n+1 arguments), optionally with
// an XSS payload in the last object.
func itemsBody(n int, attackLast bool) []byte {
	var b strings.Builder
	b.WriteString(`{"marker":"p","items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		note := "synthetic assessment payload"
		if attackLast && i == n-1 {
			note = "<script>alert(document.cookie)</script>"
		}
		fmt.Fprintf(&b, `{"id":%d,"name":"item-%d","note":%q}`, i, i, note)
	}
	b.WriteString("]}")
	return []byte(b.String())
}

// jsonBody returns a benign JSON document of exactly n bytes.
func jsonBody(n int) []byte {
	const head, tail = `{"data":"`, `"}`
	return []byte(head + strings.Repeat("a", n-len(head)-len(tail)) + tail)
}
