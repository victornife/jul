// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

func TestAltSvcValue(t *testing.T) {
	cases := map[string]string{
		"0.0.0.0:443":         `h3=":443"; ma=86400`,
		":8443":               `h3=":8443"; ma=86400`,
		"example.com:443":     `h3=":443"; ma=86400`,
		"127.0.0.1:443":       `h3=":443"; ma=86400`,
		"[::1]:8443":          `h3=":8443"; ma=86400`,
		"addr-without-a-port": `h3=":443"; ma=86400`, // SplitHostPort fails -> default 443
	}
	for addr, want := range cases {
		if got := altSvcValue(addr, 86400); got != want {
			t.Errorf("altSvcValue(%q) = %q, want %q", addr, got, want)
		}
	}
	if got := altSvcValue(":443", 0); got != `h3=":443"; ma=86400` {
		t.Errorf("altSvcValue with non-positive maxAge = %q, want default ma=86400", got)
	}
	if got := altSvcValue(":443", 3600); got != `h3=":443"; ma=3600` {
		t.Errorf("altSvcValue ma not honored: %q", got)
	}
}

func TestWithAltSvc(t *testing.T) {
	const value = `h3=":443"; ma=86400`
	h := withAltSvc(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), value)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Alt-Svc"); got != value {
		t.Errorf("Alt-Svc header = %q, want %q", got, value)
	}
	if rec.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204 (wrapper must pass through)", rec.Code)
	}
}

func TestHTTP3EnabledForAddr(t *testing.T) {
	s := &Server{cfg: &config.Config{Servers: []config.ServerConfig{
		{Listen: "0.0.0.0:443", HTTP3: &config.HTTP3Config{Enabled: true, AltSvcMaxAge: 7200}},
		{Listen: "0.0.0.0:8443", HTTP3: &config.HTTP3Config{Enabled: true}},
		{Listen: "0.0.0.0:80"},
	}}}

	if !s.http3EnabledForAddr("0.0.0.0:443") {
		t.Error(":443 should be HTTP/3-enabled")
	}
	if s.http3EnabledForAddr("0.0.0.0:80") {
		t.Error(":80 should not be HTTP/3-enabled")
	}
	if got := s.http3MaxAgeForAddr("0.0.0.0:443"); got != 7200 {
		t.Errorf("max age for :443 = %d, want 7200", got)
	}
	if got := s.http3MaxAgeForAddr("0.0.0.0:8443"); got != 86400 {
		t.Errorf("max age for :8443 (unset) = %d, want 86400 default", got)
	}
	if got := s.http3MaxAgeForAddr("0.0.0.0:80"); got != 86400 {
		t.Errorf("max age for non-http3 addr = %d, want 86400 default", got)
	}
}

func TestHandlerForAddrAltSvc(t *testing.T) {
	s := &Server{cfg: &config.Config{Servers: []config.ServerConfig{{Listen: ":443"}}}}
	handlers := map[string]http.Handler{":443": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})}
	s.handlers.Store(newHandlerGen(handlers, nil, 1))

	// With an Alt-Svc value, the wrapper advertises h3.
	rec := httptest.NewRecorder()
	s.handlerForAddr(":443", `h3=":443"; ma=86400`).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Header().Get("Alt-Svc") == "" {
		t.Error("expected Alt-Svc header when value is non-empty")
	}

	// Without one (HTTP/3 off), no Alt-Svc header is added.
	rec = httptest.NewRecorder()
	s.handlerForAddr(":443", "").ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if got := rec.Header().Get("Alt-Svc"); got != "" {
		t.Errorf("unexpected Alt-Svc header %q when HTTP/3 is off", got)
	}
}

func TestCheckHTTP3(t *testing.T) {
	enabled := []config.ServerConfig{{Listen: ":443", HTTP3: &config.HTTP3Config{Enabled: true}}}
	err := CheckHTTP3(enabled)
	if http3Compiled {
		if err != nil {
			t.Errorf("http3 build must accept an enabled config: %v", err)
		}
	} else {
		if err == nil {
			t.Error("non-http3 build must reject an enabled HTTP/3 config")
		}
	}

	// No server enables HTTP/3: always accepted regardless of build.
	if err := CheckHTTP3([]config.ServerConfig{{Listen: ":80"}}); err != nil {
		t.Errorf("config without HTTP/3 must be accepted: %v", err)
	}
	if err := CheckHTTP3([]config.ServerConfig{{Listen: ":443", HTTP3: &config.HTTP3Config{Enabled: false}}}); err != nil {
		t.Errorf("disabled HTTP/3 block must be accepted: %v", err)
	}
}
