// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSensitiveVaryUnrecoverableCleanupStillRejectsAfterRestart(t *testing.T) {
	dir := t.TempDir()
	req, _ := seedSensitiveVaryDisk(t, dir, http.MethodGet, "Cookie")
	cfg := largeCacheCfg(dir, 1<<20, 1<<20, 8<<20)
	c := newTestCache(t, cfg)
	c.disk.testRemoveFile = func(string) error { return errors.New("fixture removal failure") }
	c.disk.testPersistTombstoneErr = errors.New("fixture manifest failure")
	c.disk.testTruncateFile = func(string, int64) error { return errors.New("fixture truncate failure") }
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("fresh-origin"))
	})
	first := httptest.NewRecorder()
	c.Handler(origin).ServeHTTP(first, req)
	if first.Header().Get("X-Cache") != stateMiss || first.Body.String() != "fresh-origin" {
		t.Fatal("legacy representation survived a failed cleanup")
	}
	failures, _, ok := c.DiskWriteHealth()
	if !ok || failures == 0 || !c.disk.unrecoverableInvalidation.Load() {
		t.Fatal("uncertain physical cleanup was not reported")
	}
	restarted := newTestCache(t, cfg)
	after := httptest.NewRecorder()
	restarted.Handler(origin).ServeHTTP(after, req)
	if after.Header().Get("X-Cache") != stateMiss || after.Body.String() != "fresh-origin" {
		t.Fatal("legacy representation was reused after failed cleanup and restart")
	}
}
