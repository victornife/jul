// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// healthHandler is a trivial handler used as the "upstream" for benchmark
// requests. The WAF middleware runs before it.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}

// BenchmarkWAF_NoRules measures the middleware overhead when the WAF is enabled
// but no rules are configured (empty engine). This gives a floor for the
// Coraza-request lifecycle cost (phase evaluation, transaction creation).
func BenchmarkWAF_NoRules(b *testing.B) {
	fw, err := New(context.Background(), config.WAFConfig{Enabled: true, InlineRules: `SecRuleEngine On`}, Options{})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	mw := fw.Middleware()
	h := mw(http.HandlerFunc(healthHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.Clone(req.Context()))
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", rec.Code)
		}
	}
}

// BenchmarkWAF_CRSBlock_Pass measures the overhead when the embedded CRS is
// active in block mode for a benign request (no rule matches). This is the
// steady-state cost of running the full rule set.
func BenchmarkWAF_CRSBlock_Pass(b *testing.B) {
	fw, err := New(context.Background(), config.WAFConfig{
		Enabled:    true,
		Mode:       "block",
		CRSEnabled: true,
		Paranoia:   1,
	}, Options{})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	mw := fw.Middleware()
	h := mw(http.HandlerFunc(healthHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.Clone(req.Context()))
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", rec.Code)
		}
	}
}

// BenchmarkWAF_CRSBlock_Block measures the cost of a CRS-triggered block
// (path-traversal probe). This is a blocked request, so it never reaches the
// upstream handler.
func BenchmarkWAF_CRSBlock_Block(b *testing.B) {
	fw, err := New(context.Background(), config.WAFConfig{
		Enabled:    true,
		Mode:       "block",
		CRSEnabled: true,
		Paranoia:   1,
	}, Options{})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	mw := fw.Middleware()
	h := mw(http.HandlerFunc(healthHandler))

	req := httptest.NewRequest(http.MethodGet, "/?file=../../../etc/passwd", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.Clone(req.Context()))
		if rec.Code != http.StatusForbidden {
			b.Fatalf("unexpected status: %d", rec.Code)
		}
	}
}

// BenchmarkWAF_CRSDetect_Pass measures the overhead when the CRS is in detect
// mode for a benign request. Detect mode evaluates rules but never interrupts,
// so it exercises the full rule set without the short-circuit path.
func BenchmarkWAF_CRSDetect_Pass(b *testing.B) {
	fw, err := New(context.Background(), config.WAFConfig{
		Enabled:    true,
		Mode:       "detect",
		CRSEnabled: true,
		Paranoia:   1,
	}, Options{})
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	mw := fw.Middleware()
	h := mw(http.HandlerFunc(healthHandler))

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.Clone(req.Context()))
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status: %d", rec.Code)
		}
	}
}
