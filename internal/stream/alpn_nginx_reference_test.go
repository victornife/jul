// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	"jul/internal/config"
)

// The required pinned-reference script supplies all four loopback endpoints.
// Ordinary package tests explicitly skip this external-runtime lane.
func TestALPNNGINXReference(t *testing.T) {
	reference := os.Getenv("JUL_ALPN_NGINX_ADDR")
	if reference == "" {
		t.Skip("not executed: run scripts/nginx-migration-e2e.sh for the pinned NGINX ALPN lane")
	}
	a, b, fallback := os.Getenv("JUL_ALPN_BACKEND_A"), os.Getenv("JUL_ALPN_BACKEND_B"), os.Getenv("JUL_ALPN_BACKEND_F")
	if a == "" || b == "" || fallback == "" {
		t.Fatal("incomplete required NGINX endpoints")
	}
	s := newTestServer(t, Hooks{})
	addr := freeTCPAddr(t)
	if err := s.Reload([]config.StreamServer{{Listen: addr, ProxyPass: fallback, ALPNRoutes: []config.StreamALPNRoute{
		{ServerName: "*", Protocols: []string{"h2"}, ProxyPass: a},
		{ServerName: "*", Protocols: []string{"http/1.1"}, ProxyPass: b},
	}}}, nil); err != nil {
		t.Fatal(err)
	}
	request := func(addr string, wire []byte) byte {
		t.Helper()
		c, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := c.Write(wire); err != nil {
			t.Fatal(err)
		}
		var marker [1]byte
		if _, err := io.ReadFull(c, marker[:]); err != nil {
			t.Fatal(err)
		}
		return marker[0]
	}
	for _, tt := range []struct {
		name       string
		offers     []string
		nginx, jul byte
	}{
		{"h2", []string{"h2"}, 'A', 'A'},
		{"http1", []string{"http/1.1"}, 'B', 'B'},
		{"multiple", []string{"h2", "http/1.1"}, 'A', 'A'},
		{"reversed", []string{"http/1.1", "h2"}, 'A', 'A'},
		{"absent", nil, 'F', 'F'},
		{"unknown", []string{"unknown"}, 'F', 'F'},
		{"case sensitive", []string{"H2"}, 'F', 'F'},
		{"whole token", []string{"xh2"}, 'F', 'F'},
		{"comma ambiguity is an expected difference", []string{"x,h2"}, 'A', 'F'},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var extensions [][]byte
			if tt.offers != nil {
				extensions = append(extensions, helloExtension(16, alpnExtension(tt.offers...)))
			}
			wire := alpnHello("shared.example", extensions...)
			if got := request(reference, wire); got != tt.nginx {
				t.Fatalf("NGINX=%q want %q", got, tt.nginx)
			}
			if got := request(addr, wire); got != tt.jul {
				t.Fatalf("Jul=%q want %q", got, tt.jul)
			}
		})
	}
}
