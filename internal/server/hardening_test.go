// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestServerTimeoutDefaults(t *testing.T) {
	// A server block with no explicit timeouts should get safe defaults.
	cfg := &config.Config{Servers: []config.ServerConfig{{Listen: "127.0.0.1:80"}}}
	s := New(cfg, nil, lifecycle.Fingerprint{}, quietLogger(), nil, nil, nil)

	if got := s.readHeaderTimeout("127.0.0.1:80"); got != 10*time.Second {
		t.Errorf("readHeaderTimeout default = %v, want 10s", got)
	}
	if got := s.idleTimeout("127.0.0.1:80"); got != 60*time.Second {
		t.Errorf("idleTimeout default = %v, want 60s", got)
	}
	if got := s.maxHeaderBytes("127.0.0.1:80"); got != 1<<20 {
		t.Errorf("maxHeaderBytes default = %d, want 1048576", got)
	}
	// ReadTimeout/WriteTimeout default off so streaming is not severed.
	if got := s.readTimeout("127.0.0.1:80"); got != 0 {
		t.Errorf("readTimeout default = %v, want 0", got)
	}
	if got := s.writeTimeout("127.0.0.1:80"); got != 0 {
		t.Errorf("writeTimeout default = %v, want 0", got)
	}
}

func TestServerTimeoutOverrides(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen:            "127.0.0.1:80",
		ReadHeaderTimeout: config.Duration(3 * time.Second),
		ReadTimeout:       config.Duration(15 * time.Second),
		WriteTimeout:      config.Duration(20 * time.Second),
		IdleTimeout:       config.Duration(90 * time.Second),
		MaxHeaderBytes:    config.Size(4096),
	}}}
	s := New(cfg, nil, lifecycle.Fingerprint{}, quietLogger(), nil, nil, nil)

	if got := s.readHeaderTimeout("127.0.0.1:80"); got != 3*time.Second {
		t.Errorf("readHeaderTimeout = %v, want 3s", got)
	}
	if got := s.readTimeout("127.0.0.1:80"); got != 15*time.Second {
		t.Errorf("readTimeout = %v, want 15s", got)
	}
	if got := s.writeTimeout("127.0.0.1:80"); got != 20*time.Second {
		t.Errorf("writeTimeout = %v, want 20s", got)
	}
	if got := s.idleTimeout("127.0.0.1:80"); got != 90*time.Second {
		t.Errorf("idleTimeout = %v, want 90s", got)
	}
	if got := s.maxHeaderBytes("127.0.0.1:80"); got != 4096 {
		t.Errorf("maxHeaderBytes = %d, want 4096", got)
	}
}
