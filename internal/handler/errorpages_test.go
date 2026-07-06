// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewErrorPagesValid(t *testing.T) {
	ep, err := NewErrorPages(map[string]string{
		"404": "/errors/404.html",
		"500": "https://errors.example.com/500",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ep.pages) != 2 {
		t.Fatalf("expected 2 pages, got %d", len(ep.pages))
	}
}

func TestNewErrorPagesInvalidStatus(t *testing.T) {
	_, err := NewErrorPages(map[string]string{"abc": "/x.html"})
	if err == nil {
		t.Fatal("expected error for invalid status code")
	}
}

func TestRenderCustomFile(t *testing.T) {
	tmpDir := t.TempDir()
	pagePath := filepath.Join(tmpDir, "404.html")
	_ = os.WriteFile(pagePath, []byte("<html>not found</html>"), 0o644)

	ep, _ := NewErrorPages(map[string]string{"404": pagePath})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ep.Render(rec, req, 404)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := rec.Body.String(); body != "<html>not found</html>" {
		t.Fatalf("body = %q, want custom page", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q, want text/html; charset=utf-8", ct)
	}
}

func TestRenderRedirect(t *testing.T) {
	ep, _ := NewErrorPages(map[string]string{"500": "https://errors.example.com/500"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/path", nil)
	ep.Render(rec, req, 500)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://errors.example.com/500" {
		t.Fatalf("location = %q, want https URL", loc)
	}
}

func TestRenderMissingFileFallsBack(t *testing.T) {
	ep, _ := NewErrorPages(map[string]string{"503": "/nonexistent/page.html"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ep.Render(rec, req, 503)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Body.String() == "" {
		t.Fatal("expected default error page body")
	}
}

func TestRenderNilReceiver(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	var ep *ErrorPages
	ep.Render(rec, req, 404)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestRenderNoMapping(t *testing.T) {
	ep, _ := NewErrorPages(map[string]string{"404": "/404.html"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ep.Render(rec, req, 500)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestIsURL(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"http://example.com", true},
		{"https://example.com", true},
		{"/local/path", false},
		{"ftp://example.com", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := isURL(tt.s); got != tt.want {
			t.Fatalf("isURL(%q) = %v, want %v", tt.s, got, tt.want)
		}
	}
}
