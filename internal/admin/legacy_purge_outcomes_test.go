// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/cache"
	"jul/internal/config"
)

func TestLegacyPurgeDurabilityOutcomesAndCompatibility(t *testing.T) {
	t.Parallel()

	const token = "legacy-purge-token-32-chars-668-a"
	diskDir := t.TempDir()
	cacheCfg := config.CacheConfig{
		Enabled:       true,
		MemoryMaxSize: config.Size(2 << 20),
		DiskPath:      diskDir,
		DiskMaxSize:   config.Size(16 << 20),
		DefaultTTL:    config.Duration(10 * time.Minute),
	}
	c, err := cache.New(cacheCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}

	origin := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = io.WriteString(w, "body-"+r.URL.Path)
	}))

	seed := func(uri string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "http://portal.example.com"+uri, nil)
		req.Host = "portal.example.com"
		rec := httptest.NewRecorder()
		origin.ServeHTTP(rec, req)
		if rec.Header().Get("X-Cache") != "MISS" {
			t.Fatalf("seed %s: expected MISS, got %s", uri, rec.Header().Get("X-Cache"))
		}
	}

	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{
		Cache: c,
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{Cache: cacheCfg}, nil
		},
	})
	handler := s.routes()

	doPurge := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/cache/purge"+query, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	// 1. Clean whole-cache and single-key purge succeed with 200 OK {"status":"purged"}.
	seed("/clean-1")
	rrClean := doPurge("")
	if rrClean.Code != http.StatusOK {
		t.Fatalf("clean purge status=%d body=%s", rrClean.Code, rrClean.Body.String())
	}
	var cleanBody map[string]any
	if err := json.Unmarshal(rrClean.Body.Bytes(), &cleanBody); err != nil || cleanBody["status"] != "purged" {
		t.Fatalf("unexpected clean purge body: %s", rrClean.Body.String())
	}

	seed("/clean-key")
	keyParam := "?key=" + "GET%0Aportal.example.com%0A/clean-key"
	rrKey := doPurge(keyParam)
	if rrKey.Code != http.StatusOK {
		t.Fatalf("clean key purge status=%d body=%s", rrKey.Code, rrKey.Body.String())
	}

	// 2. Marker-only failure (.jul-cache-gen is a directory so atomic rename fails with EISDIR,
	// while cache file unlinks in diskDir succeed):
	// - Returns 503 Service Unavailable with status="uncertain", failed_entries=0
	// - Records DiskWriteHealth failure
	// - Old disk content is removed and does NOT reappear after restart
	// - Healthy disk tier remains operational for new writes and hits!
	seed("/marker-only")
	markerPath := filepath.Join(diskDir, ".jul-cache-gen")
	_ = os.Remove(markerPath)
	if err := os.Mkdir(markerPath, 0o700); err != nil {
		t.Fatalf("Mkdir(.jul-cache-gen): %v", err)
	}
	rrMarker := doPurge("")
	if rrMarker.Code != http.StatusServiceUnavailable {
		t.Fatalf("marker-only failure: expected 503, got %d body=%s", rrMarker.Code, rrMarker.Body.String())
	}
	var markerBody map[string]any
	if err := json.Unmarshal(rrMarker.Body.Bytes(), &markerBody); err != nil {
		t.Fatal(err)
	}
	if markerBody["status"] != "uncertain" || markerBody["invalidation_effective"] != "uncertain" {
		t.Fatalf("unexpected marker-only body: %+v", markerBody)
	}
	failures, failing, ok := c.DiskWriteHealth()
	if !ok || failures == 0 || !failing {
		t.Fatalf("expected DiskWriteHealth failure after marker-only error, got failures=%d failing=%v ok=%v", failures, failing, ok)
	}

	// Repair .jul-cache-gen directory and verify old content does NOT reappear on restart,
	// and that c's disk tier was NOT disabled by the marker-only failure.
	_ = os.RemoveAll(markerPath)
	reopened, err := cache.New(cacheCfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	reopenedOrigin := reopened.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=600")
		_, _ = io.WriteString(w, "fresh-after-marker-failure")
	}))
	reqCheck := httptest.NewRequest(http.MethodGet, "http://portal.example.com/marker-only", nil)
	reqCheck.Host = "portal.example.com"
	recCheck := httptest.NewRecorder()
	reopenedOrigin.ServeHTTP(recCheck, reqCheck)
	if recCheck.Header().Get("X-Cache") != "MISS" || recCheck.Body.String() != "fresh-after-marker-failure" {
		t.Fatalf("old content reappeared after marker-only failure: state=%s body=%q",
			recCheck.Header().Get("X-Cache"), recCheck.Body.String())
	}

	// Verify c's own disk tier is still operational (not disabled by unrecoverableInvalidation).
	seed("/still-healthy")
	reqHealthy := httptest.NewRequest(http.MethodGet, "http://portal.example.com/still-healthy", nil)
	reqHealthy.Host = "portal.example.com"
	recHealthy := httptest.NewRecorder()
	origin.ServeHTTP(recHealthy, reqHealthy)
	if recHealthy.Header().Get("X-Cache") != "HIT" {
		t.Fatalf("expected HIT on healthy cache after marker-only error, got %s", recHealthy.Header().Get("X-Cache"))
	}

	// 3. Combined tombstone manifest failure (.jul-cache-tombstones is a directory):
	// POST /cache/purge?key=... returns 503 Service Unavailable and does not fabricate success.
	tombPath := filepath.Join(diskDir, ".jul-cache-tombstones")
	_ = os.Remove(tombPath)
	if err := os.Mkdir(tombPath, 0o700); err != nil {
		t.Fatalf("Mkdir(.jul-cache-tombstones): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tombPath) })

	rrKeyFail := doPurge("?key=" + "GET%0Aportal.example.com%0A/still-healthy")
	if rrKeyFail.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on single-key tombstone failure, got %d body=%s", rrKeyFail.Code, rrKeyFail.Body.String())
	}
}
