// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build console

package admin

import (
	"net/http"
	"net/http/httptest"
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
	cfg.Console = &off
	srv.UpdateLiveAdminConfig(cfg)
	offResponse := serve()
	if onFirst.Body.String() == offResponse.Body.String() {
		t.Fatal("Console disable did not switch the stable root handler to fallback UI")
	}

	cfg.Console = &on
	srv.UpdateLiveAdminConfig(cfg)
	onAgain := serve()
	if onFirst.Body.String() != onAgain.Body.String() {
		t.Fatal("Console re-enable did not restore the same embedded Console response")
	}
}
