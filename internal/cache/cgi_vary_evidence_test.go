// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestCGIVaryReasonIsBoundedAndRedacted(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DecisionSamples: true})
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		w.Header().Set("Vary", "Client-Cert")
		_, _ = w.Write([]byte("origin"))
	})
	_, scope := scopedHandler(t, c, origin)
	h := cgiScopedHandler(t, c, scope, origin)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item", nil)
	req.Header.Set("Client-Cert", "fixture-secret-marker")
	h.ServeHTTP(httptest.NewRecorder(), req)
	result, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range result.Decisions {
		found = found || decision.Reason == "vary_cgi_rewrite_rejected"
	}
	if !found {
		t.Fatal("bounded CGI Vary rejection was not recorded")
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "fixture-secret-marker") {
		t.Fatal("decision sample exposed the request header value")
	}
}
