// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !console

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestConsoleConfiguredTrueRemainsFallbackInLeanBuild(t *testing.T) {
	on := true
	off := false
	cfg := config.AdminConfig{Enabled: true, Console: &on}
	srv := New(cfg, testLogger(t), Deps{})
	h := srv.handleConsoleOrRoot()

	serve := func() string {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/ui", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("/ui status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		return rr.Body.String()
	}

	configuredOn := serve()
	cfg.Console = &off
	srv.UpdateLiveAdminConfig(cfg)
	configuredOff := serve()
	if configuredOn != configuredOff {
		t.Fatal("lean build changed UI behavior for a Console flag it cannot serve")
	}

	status := srv.adminRuntimeStatus(nil)
	if status == nil || status.ConsoleCompiled || status.ConsoleEffective {
		t.Fatalf("lean runtime status is not truthful: %#v", status)
	}
}
