// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoverConvertsPanicTo500(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&discardWriter{}, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Recover(log)(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestRecoverRePanicOnAbortHandler(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&discardWriter{}, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	})

	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("expected http.ErrAbortHandler to be re-panicked, got %v", rec)
		}
	}()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Recover(log)(handler).ServeHTTP(rec, req)
	t.Fatal("expected panic to propagate")
}

func TestRecoverPassesThroughWithoutPanic(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&discardWriter{}, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Recover(log)(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestRecoverSetsContentType(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&discardWriter{}, nil))
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	Recover(log)(handler).ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("content-type = %q, want text/plain; charset=utf-8", ct)
	}
}

type discardWriter struct{}

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
