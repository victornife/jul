// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"net/http"
	"testing"

	"jul/internal/config"
)

func TestH2CEnabledForAddr(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{Listen: "127.0.0.1:80", H2C: true},
		{Listen: "127.0.0.1:81"},
	}}
	s := New(cfg, quietLogger(), nil, nil, nil)

	if !s.h2cEnabledForAddr("127.0.0.1:80") {
		t.Error("addr :80 should report h2c enabled")
	}
	if s.h2cEnabledForAddr("127.0.0.1:81") {
		t.Error("addr :81 should report h2c disabled")
	}
}

func TestEnableH2C(t *testing.T) {
	httpd := &http.Server{}
	enableH2C(httpd)
	if httpd.Protocols == nil {
		t.Fatal("enableH2C should set Protocols")
	}
	if !httpd.Protocols.UnencryptedHTTP2() {
		t.Error("enableH2C should enable unencrypted HTTP/2 (h2c)")
	}
	if !httpd.Protocols.HTTP1() {
		t.Error("enableH2C should keep HTTP/1.1 enabled")
	}
}
