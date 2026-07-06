// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestIDPreservesClientHeader(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "client-id-123")
	rec := httptest.NewRecorder()

	RequestID()(handler).ServeHTTP(rec, req)

	if got := rec.Header().Get(HeaderRequestID); got != "client-id-123" {
		t.Fatalf("response request-id = %q, want client-id-123", got)
	}
}

func TestRequestIDGeneratesNewWhenMissing(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	RequestID()(handler).ServeHTTP(rec, req)

	got := rec.Header().Get(HeaderRequestID)
	if got == "" {
		t.Fatal("expected generated request-id in response, got empty")
	}
	if len(got) != 16 {
		t.Fatalf("generated request-id length = %d, want 16 (hex of 8 bytes)", len(got))
	}
}

func TestRequestIDInContext(t *testing.T) {
	var captured string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = RequestIDFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(HeaderRequestID, "ctx-test")
	rec := httptest.NewRecorder()

	RequestID()(handler).ServeHTTP(rec, req)

	if captured != "ctx-test" {
		t.Fatalf("request-id from context = %q, want ctx-test", captured)
	}
}

func TestRequestIDFromEmptyContext(t *testing.T) {
	if got := RequestIDFrom(context.Background()); got != "" {
		t.Fatalf("expected empty string from empty context, got %q", got)
	}
}

func TestTraceIDRoundTrip(t *testing.T) {
	ctx := WithTraceID(context.Background(), "trace-42")
	if got := TraceIDFrom(ctx); got != "trace-42" {
		t.Fatalf("trace-id = %q, want trace-42", got)
	}
}

func TestTraceIDFromEmptyContext(t *testing.T) {
	if got := TraceIDFrom(context.Background()); got != "" {
		t.Fatalf("expected empty trace-id, got %q", got)
	}
}

func TestClaimsRoundTrip(t *testing.T) {
	claims := map[string]any{"sub": "user-1", "role": "admin"}
	ctx := WithClaims(context.Background(), claims)
	got := ClaimsFrom(ctx)
	if got == nil {
		t.Fatal("expected claims, got nil")
	}
	if got["sub"] != "user-1" {
		t.Fatalf("claim sub = %v, want user-1", got["sub"])
	}
}

func TestClaimsFromEmptyContext(t *testing.T) {
	if got := ClaimsFrom(context.Background()); got != nil {
		t.Fatalf("expected nil claims, got %v", got)
	}
}

func TestNewIDLength(t *testing.T) {
	id := newID()
	if len(id) != 16 {
		t.Fatalf("id length = %d, want 16", len(id))
	}
}

func TestNewIDUniqueness(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 100; i++ {
		id := newID()
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestNewIDIsHex(t *testing.T) {
	for i := 0; i < 50; i++ {
		id := newID()
		if !isHex(id) {
			t.Fatalf("id %q is not valid hex", id)
		}
	}
}

func isHex(s string) bool {
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
