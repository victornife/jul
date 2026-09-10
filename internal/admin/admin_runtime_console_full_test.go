// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build console

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestConsoleModeHotReloadOnOffOnFullBuild(t *testing.T) {
	on := true
	off := false
	cfg := config.AdminConfig{Enabled: true, Console: &on}
	srv := New(cfg, testLogger(t), Deps{})
	h := srv.handleConsoleOrRoot()

	serve := func() *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ui", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("/ui status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		return rr
	}

	onFirst := serve()
	if !strings.Contains(onFirst.Body.String(), "<title>Jul.IA Console</title>") {
		t.Fatalf("enabled Console did not serve embedded SPA: %s", onFirst.Body.String())
	}

	cfg.Console = &off
	srv.UpdateLiveAdminConfig(cfg)
	offResponse := serve()
	if strings.Contains(offResponse.Body.String(), "<title>Jul.IA Console</title>") {
		t.Fatal("Console disable did not switch the stable root handler to fallback UI")
	}

	cfg.Console = &on
	srv.UpdateLiveAdminConfig(cfg)
	onAgain := serve()
	if !strings.Contains(onAgain.Body.String(), "<title>Jul.IA Console</title>") {
		t.Fatalf("Console re-enable did not restore embedded SPA: %s", onAgain.Body.String())
	}
}
