// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
package adminclient

import (
	"context"
	"jul/internal/adminapi"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCacheInspectionClientErrorsAndDecode(t *testing.T) {
	mode := "ok"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/cache/inspect" {
			t.Error(r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		switch mode {
		case "error":
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"error":{"code":"forbidden","message":"inspection denied"}}`))
		case "invalid":
			_, _ = w.Write([]byte("{"))
		default:
			_, _ = w.Write([]byte(`{"api_version":"v1","inspection":{"state":"empty"}}`))
		}
	}))
	defer ts.Close()
	c, err := New(Config{Endpoint: ts.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	req := adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}}
	v, _, err := c.CacheInspect(context.Background(), req)
	if err != nil || v.Inspection.State != "empty" {
		t.Fatal(v, err)
	}
	mode = "error"
	if _, _, err = c.CacheInspect(context.Background(), req); err == nil {
		t.Fatal("permission error hidden")
	}
	mode = "invalid"
	if _, _, err = c.CacheInspect(context.Background(), req); err == nil {
		t.Fatal("decode error hidden")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err = c.CacheInspect(ctx, req); err == nil {
		t.Fatal("cancellation hidden")
	}
}
