// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChainAppliesMiddlewareInOrder(t *testing.T) {
	var order []string
	mw := func(label string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, label+"-in")
				next.ServeHTTP(w, r)
				order = append(order, label+"-out")
			})
		}
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	})

	chained := Chain(handler, mw("a"), mw("b"), mw("c"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	chained.ServeHTTP(rec, req)

	want := []string{"a-in", "b-in", "c-in", "handler", "c-out", "b-out", "a-out"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order[%d] = %q, want %q; full order = %v", i, order[i], want[i], order)
		}
	}
}

func TestChainEmptyMiddleware(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	chained := Chain(handler)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	chained.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestChainSingleMiddleware(t *testing.T) {
	called := false
	mw := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			next.ServeHTTP(w, r)
		})
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	chained := Chain(handler, mw)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	chained.ServeHTTP(rec, req)

	if !called {
		t.Fatal("middleware was not called")
	}
}
