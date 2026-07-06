// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// TestConsoleApplyRollbackFlowE2E drives the Console's primary operator flow —
// load, apply a structured edit, then roll it back — over real HTTP against the
// live admin router, exactly as the frontend API client does. It is the
// backend↔Console end-to-end smoke the audit flagged as missing (Finding UI-1):
// a browser is not required to prove the request/response contract the Console
// depends on stays intact.
func TestConsoleApplyRollbackFlowE2E(t *testing.T) {
	s, _ := concurrentWriteServer(t)
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	client := ts.Client()

	getConfig := func() (rawTOML, baseVersion string) {
		t.Helper()
		resp, err := client.Get(ts.URL + "/api/config")
		if err != nil {
			t.Fatalf("GET /api/config: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET /api/config: status %d", resp.StatusCode)
		}
		var out struct {
			Raw         string `json:"raw"`
			BaseVersion string `json:"base_version"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode /api/config: %v", err)
		}
		return out.Raw, out.BaseVersion
	}

	target := func(rawTOML string) string {
		t.Helper()
		c, err := config.Parse([]byte(rawTOML))
		if err != nil {
			t.Fatalf("parse config: %v", err)
		}
		if len(c.Servers) == 0 || len(c.Servers[0].Locations) == 0 {
			t.Fatalf("unexpected config shape:\n%s", rawTOML)
		}
		return c.Servers[0].Locations[0].ProxyPass
	}

	// 1. Load the live config (base version + current target).
	raw0, base0 := getConfig()
	orig := target(raw0)
	if orig == "" || base0 == "" {
		t.Fatalf("expected a proxy target and base_version; target=%q base=%q", orig, base0)
	}

	// 2. Apply a structured edit carrying the base version (optimistic concurrency).
	const newTarget = "http://127.0.0.1:9999"
	applyBody, _ := json.Marshal(patchApplyRequest{
		BaseVersion: base0,
		Ops:         []patchRequest{{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: newTarget}},
	})
	applyResp, err := client.Post(ts.URL+"/api/config/patch/apply", "application/json", bytes.NewReader(applyBody))
	if err != nil {
		t.Fatalf("POST apply: %v", err)
	}
	body, _ := io.ReadAll(applyResp.Body)
	applyResp.Body.Close()
	if applyResp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d; body: %s", applyResp.StatusCode, body)
	}

	// 3. The edit is persisted and the base version advanced.
	raw1, base1 := getConfig()
	if got := target(raw1); got != newTarget {
		t.Fatalf("after apply target = %q, want %q", got, newTarget)
	}
	if base1 == base0 {
		t.Fatal("base_version did not change after apply")
	}

	// 4. The pre-apply config is available as a history snapshot.
	histResp, err := client.Get(ts.URL + "/api/history")
	if err != nil {
		t.Fatalf("GET /api/history: %v", err)
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(histResp.Body).Decode(&entries); err != nil {
		histResp.Body.Close()
		t.Fatalf("decode history: %v", err)
	}
	histResp.Body.Close()
	if len(entries) == 0 {
		t.Fatal("expected a history snapshot after apply")
	}

	// 5. Roll back to the most recent snapshot (the pre-apply config).
	rbBody, _ := json.Marshal(map[string]string{"id": entries[0].ID})
	rbResp, err := client.Post(ts.URL+"/api/history/rollback", "application/json", bytes.NewReader(rbBody))
	if err != nil {
		t.Fatalf("POST rollback: %v", err)
	}
	rbOut, _ := io.ReadAll(rbResp.Body)
	rbResp.Body.Close()
	if rbResp.StatusCode != http.StatusOK {
		t.Fatalf("rollback status = %d; body: %s", rbResp.StatusCode, rbOut)
	}

	// 6. The target is restored to its original value.
	raw2, _ := getConfig()
	if got := target(raw2); got != orig {
		t.Fatalf("after rollback target = %q, want original %q", got, orig)
	}
}
