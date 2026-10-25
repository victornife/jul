// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/adminapi"
)

func TestCachePurgeClientMethodsAndExitCodes(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/cache/purge/preview", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminapi.CachePurgePreviewResponse{
			APIVersion:             "v1",
			CacheEnabled:           true,
			PreviewToken:           "cpt1.1.100.abcdef",
			MatchedResources:       2,
			MatchedRepresentations: 4,
		})
	})
	mux.HandleFunc("POST /api/v1/cache/purge", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Idempotency-Key") != "idem-client-1" {
			t.Errorf("Idempotency-Key = %q, want idem-client-1", r.Header.Get("Idempotency-Key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminapi.CachePurgeResponse{
			APIVersion:            "v1",
			OperationID:           "cp_1_test",
			State:                 "succeeded",
			Terminal:              true,
			Outcome:               "succeeded",
			InvalidationEffective: "effective",
		})
	})
	mux.HandleFunc("GET /api/v1/cache/purges/cp_1_test", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(adminapi.CachePurgeResponse{
			APIVersion:            "v1",
			OperationID:           "cp_1_test",
			State:                 "succeeded",
			Terminal:              true,
			Outcome:               "succeeded",
			InvalidationEffective: "effective",
		})
	})

	ts := httptest.NewServer(mux)
	defer ts.Close()

	c, err := New(Config{
		Endpoint: ts.URL,
		Token:    "test-token",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	prev, _, err := c.CachePurgePreview(context.Background(), adminapi.CachePurgePreviewRequest{
		Selector: adminapi.CacheSelector{Kind: "tag", Host: "portal.example.com", Tags: []string{"article:42"}},
	})
	if err != nil || prev.MatchedResources != 2 {
		t.Fatalf("CachePurgePreview: prev=%+v err=%v", prev, err)
	}

	execRes, _, err := c.CachePurge(context.Background(), adminapi.CachePurgeRequest{
		Selector:     adminapi.CacheSelector{Kind: "tag", Host: "portal.example.com", Tags: []string{"article:42"}},
		PreviewToken: prev.PreviewToken,
	}, "idem-client-1")
	if err != nil || execRes.OperationID != "cp_1_test" {
		t.Fatalf("CachePurge: res=%+v err=%v", execRes, err)
	}

	opRes, _, err := c.CachePurgeOperation(context.Background(), "cp_1_test")
	if err != nil || opRes.OperationID != "cp_1_test" {
		t.Fatalf("CachePurgeOperation: res=%+v err=%v", opRes, err)
	}

	// Verify CachePurgeExit mapping for all terminal and non-terminal states.
	if got := CachePurgeExit(adminapi.CachePurgeResponse{Terminal: true, State: "succeeded", InvalidationEffective: "effective"}); got != 0 {
		t.Errorf("succeeded exit = %d, want 0", got)
	}
	if got := CachePurgeExit(adminapi.CachePurgeResponse{Terminal: true, State: "partial", InvalidationEffective: "uncertain"}); got != 5 {
		t.Errorf("uncertain exit = %d, want 5", got)
	}
	if got := CachePurgeExit(adminapi.CachePurgeResponse{Terminal: true, State: "partial", InvalidationEffective: "incomplete"}); got != 4 {
		t.Errorf("partial exit = %d, want 4", got)
	}
	if got := CachePurgeExit(adminapi.CachePurgeResponse{Terminal: true, State: "failed", InvalidationEffective: "incomplete"}); got != 9 {
		t.Errorf("failed exit = %d, want 9", got)
	}
}
