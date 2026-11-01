// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

func TestRetiredPurgeRouteCannotMutateCache(t *testing.T) {
	for _, spec := range Catalog {
		if spec.Pattern == "/cache/purge" {
			t.Fatal("retired purge route remains registered")
		}
	}
	if _, exists := InternalRouteReasons()["/cache/purge"]; exists {
		t.Fatal("retired route remains in classification inventory")
	}
	for _, console := range []bool{false, true} {
		for _, token := range []string{"", "retirement-token"} {
			p := &fakePurger{}
			s := newTestServer(t, config.AdminConfig{Console: &console, Token: token}, Deps{Cache: p})
			h := s.routes()
			for _, path := range []string{"/cache/purge", "/cache/purge?key=GET%0Aexample.com%0A/a", "/cache/purge/"} {
				for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
					r := httptest.NewRequest(method, path, strings.NewReader(`{"all":true}`))
					if token != "" {
						r.Header.Set("Authorization", "Bearer "+token)
					}
					rr := httptest.NewRecorder()
					h.ServeHTTP(rr, r)
					if rr.Code != http.StatusNotFound {
						t.Fatalf("console=%t %s %s returned %d, want 404", console, method, path, rr.Code)
					}
				}
			}
			if p.purged.Load() != 0 || p.deleted.Load() != nil {
				t.Fatal("removed route reached cache mutation")
			}
			// The supported replacement is still reachable and executes exactly once.
			rr := postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{Selector: adminapi.CacheSelector{Kind: "all", All: true}, ConfirmBroad: true})
			if rr.Code != 200 || p.purged.Load() != 1 {
				t.Fatalf("versioned replacement failed: %d %s", rr.Code, rr.Body.String())
			}
		}
	}
}
