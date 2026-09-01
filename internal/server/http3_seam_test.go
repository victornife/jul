// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"

	"jul/internal/config"
)

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
