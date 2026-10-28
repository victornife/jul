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

func TestResponseHookBypassInspectionIsBoundedAndRedacted(t *testing.T) {
	c := newTestCache(t, config.CacheConfig{MemoryMaxSize: 1 << 20, DecisionSamples: true})
	t.Cleanup(func() { c.setDecisionSampling(false) })
	h, _ := scopedHandler(t, c, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=60")
		_, _ = w.Write([]byte("origin"))
	}))
	req := httptest.NewRequest(http.MethodGet, "http://example.com/item?token=fixture-query-secret", nil)
	req.Header.Set("Cookie", "session=fixture-cookie-secret")
	h.ServeHTTP(httptest.NewRecorder(), WithResponseHookBypass(req))
	inspection, err := c.InspectScope(context.Background(), inspectSelector(t, SelectorExactURL, "/item?token=fixture-query-secret"))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decision := range inspection.Decisions {
		if decision.Reason == "response_hook_bypass" && decision.Summary != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("response-hook bypass was not reported by its server owner")
	}
	raw, err := json.Marshal(inspection)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"fixture-query-secret", "fixture-cookie-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("inspection exposed fixture request data")
		}
	}
}
