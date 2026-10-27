// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/cachemeta"
)

func TestRemoteCacheInspectionCLIAndClient(t *testing.T) {
	var got adminapi.CacheInspectRequest
	fail := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/v1/cache/inspect" {
			t.Errorf("wrong operation %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"code":"storage_unavailable","message":"inspection unavailable"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(adminapi.CacheInspectResponse{APIVersion: "v1", NextCursor: "opaque.next", ExpiresAt: "2026-10-27T13:00:00Z", Inspection: cachemeta.Inspection{State: "partial", CountAccuracy: "exact", EvidenceState: "disabled", MatchedResources: 1, Resources: []cachemeta.SafeResourceSummary{{Host: "example.com", Path: "/a", FreshnessState: "fresh", BodyBytes: 1, Details: []cachemeta.RepresentationMetadata{}}}, Decisions: []cachemeta.CacheDecision{{Reason: "fresh_hit", Host: "example.com", Path: "/a"}}}})
	}))
	defer ts.Close()
	file := filepath.Join(t.TempDir(), "admin.token")
	if err := os.WriteFile(file, []byte("test-inspect-token-32-chars-000000"), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"inspect", "--endpoint", ts.URL, "--token-file", file}
	for _, tc := range []struct {
		flags []string
		kind  string
	}{{[]string{"--host", "example.com"}, "host"}, {[]string{"--url", "https://example.com/a?secret=TOKEN"}, "exact_url"}, {[]string{"--host", "example.com", "--uri", "/a"}, "exact_url"}, {[]string{"--host", "example.com", "--prefix", "/a"}, "prefix"}, {[]string{"--host", "example.com", "--glob", "/a/*"}, "glob"}, {[]string{"--host", "example.com", "--tag", "article:42"}, "tag"}, {[]string{"--key", "GET\nexample.com\n/a"}, "key"}} {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			args := append(append([]string{}, base...), tc.flags...)
			if code := cmdRemoteCache(args); code != 0 {
				t.Fatal(code, errOut.String())
			}
			if got.Selector.Kind != tc.kind || !strings.Contains(out.String(), "Metadata only") || strings.Contains(out.String(), "TOKEN") {
				t.Fatal(got, out.String())
			}
		})
	}
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		args := append(append([]string{}, base...), "--host", "example.com", "--json", "--cursor", "bound.cursor", "--page-size", "2")
		if code := cmdRemoteCache(args); code != 0 {
			t.Fatal(code, errOut.String())
		}
		var v adminapi.CacheInspectResponse
		if err := json.Unmarshal(out.Bytes(), &v); err != nil || v.Inspection.State != "partial" || got.Cursor != "bound.cursor" {
			t.Fatal(err, out.String(), got)
		}
	})
	for _, flags := range [][]string{{"unexpected"}, {"--uri", "/a", "--prefix", "/b"}, {"--unknown"}} {
		withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
			if code := cmdRemoteCache(append(append([]string{}, base...), flags...)); code == 0 {
				t.Fatal("invalid args accepted", flags)
			}
		})
	}
	fail = true
	withRemoteOutput(t, func(out, errOut *bytes.Buffer) {
		if code := cmdRemoteCache(append(append([]string{}, base...), "--host", "example.com")); code == 0 {
			t.Fatal("server error hidden")
		}
	})
}
