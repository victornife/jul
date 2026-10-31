// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
)

func TestVersionedPurgeDurabilityOutcomes(t *testing.T) {
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
	doPurge := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		selector := adminapi.CacheSelector{Kind: "all", All: true}
		if key != "" {
			selector = adminapi.CacheSelector{Kind: "key", Key: key}
		}
		return postV1JSON(t, s, "/api/v1/cache/purge", token, "", adminapi.CachePurgeRequest{Selector: selector, ConfirmBroad: true})
	}

	// 1. Clean whole-cache and single-key purge succeed with 200 OK with structured effective success.
	seed("/clean-1")
	rrClean := doPurge("")
	if rrClean.Code != http.StatusOK {
		t.Fatalf("clean purge status=%d body=%s", rrClean.Code, rrClean.Body.String())
	}
	cleanBody := decodeInto[adminapi.CachePurgeResponse](t, rrClean)
	if cleanBody.State != "succeeded" || cleanBody.InvalidationEffective != "effective" {
		t.Fatalf("unexpected clean purge: %+v", cleanBody)
	}

	seed("/clean-key")
	keyParam := "GET\nportal.example.com\n/clean-key"
	rrKey := doPurge(keyParam)
	if rrKey.Code != http.StatusOK {
		t.Fatalf("clean key purge status=%d body=%s", rrKey.Code, rrKey.Body.String())
	}

	// 2. Marker-only failure (.jul-cache-gen is a directory so atomic rename fails with EISDIR,
	// while cache file unlinks in diskDir succeed):
	// - Returns a terminal 200 response with effectiveness="uncertain", failed_entries=0
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
	if rrMarker.Code != http.StatusOK {
		t.Fatalf("marker-only completed failure: expected 200, got %d body=%s", rrMarker.Code, rrMarker.Body.String())
	}
	markerBody := decodeInto[adminapi.CachePurgeResponse](t, rrMarker)
	if markerBody.InvalidationEffective != "uncertain" || markerBody.State == "succeeded" || markerBody.RecoveryGuidance == "" {
		t.Fatalf("unexpected marker-only body: %+v", markerBody)
	}
	if got := s.audit.snapshot("cache.purge", "failure", 0); len(got) != 1 {
		t.Fatalf("uncertain result did not audit failure: %+v", got)
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
	// Versioned key purge reports a structured incomplete/uncertain result, never success.
	tombPath := filepath.Join(diskDir, ".jul-cache-tombstones")
	_ = os.Remove(tombPath)
	if err := os.Mkdir(tombPath, 0o700); err != nil {
		t.Fatalf("Mkdir(.jul-cache-tombstones): %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tombPath) })

	rrKeyFail := doPurge("GET\nportal.example.com\n/still-healthy")
	if rrKeyFail.Code != http.StatusOK {
		t.Fatalf("expected terminal structured response, got %d body=%s", rrKeyFail.Code, rrKeyFail.Body.String())
	}
	keyFail := decodeInto[adminapi.CachePurgeResponse](t, rrKeyFail)
	if keyFail.State == "succeeded" || keyFail.InvalidationEffective == "effective" || keyFail.RecoveryGuidance == "" {
		t.Fatalf("key fault fabricated success: %+v", keyFail)
	}
}
