// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/handler"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func TestH2CEnabledForAddr(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{
		{Listen: "127.0.0.1:80", H2C: true},
		{Listen: "127.0.0.1:81"},
	}}
	s := New(cfg, nil, lifecycle.Fingerprint{}, quietLogger(), nil, nil, nil)

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

func TestH2CConformanceSecurityBoundaries(t *testing.T) {
	var backendCalls, middlewareCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		for _, name := range []string{"Connection", "Keep-Alive", "Proxy-Connection", "Upgrade", "TE", "X-Hop"} {
			if len(r.Header.Values(name)) != 0 {
				t.Errorf("backend received %s", name)
			}
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	proxy, err := handler.NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{ProxyPass: backend.URL}, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	addr := freePort(t)
	cfg := cfgWith(addr)
	cfg.Servers[0].H2C = true
	cfg.Servers[0].MaxHeaderBytes = 1024
	factory := func(context.Context, *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			middlewareCalls.Add(1)
			proxy.ServeHTTP(w, r)
		})}, 1, func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(cfg, nil, lifecycle.Fingerprint{}, quietLogger(), factory, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, make(chan ReloadRequest), redact.EmptyState()) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	waitForServe(t, "http://"+addr+"/", "ok")

	t.Run("invalid preface does not reach handlers", func(t *testing.T) {
		before := middlewareCalls.Load()
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, "INVALID CONNECTION PREFACE\r\n\r\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := io.ReadAll(io.LimitReader(conn, 4096)); err != nil {
			t.Fatal(err)
		}
		if middlewareCalls.Load() != before {
			t.Fatal("invalid preface reached Jul's handler")
		}
	})

	connect := func(t *testing.T) (net.Conn, *http2.Framer, uint32) {
		t.Helper()
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(conn, http2.ClientPreface); err != nil {
			t.Fatal(err)
		}
		framer := http2.NewFramer(conn, conn)
		if err := framer.WriteSettings(); err != nil {
			t.Fatal(err)
		}
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if settings, ok := frame.(*http2.SettingsFrame); ok && !settings.IsAck() {
				maximum := uint32(16384)
				if err := settings.ForeachSetting(func(setting http2.Setting) error {
					if setting.ID == http2.SettingMaxFrameSize {
						maximum = setting.Val
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := framer.WriteSettingsAck(); err != nil {
					t.Fatal(err)
				}
				return conn, framer, maximum
			}
		}
	}
	for _, tc := range []struct{ name, value, status string }{
		{"connection", "x-hop", "400"}, {"keep-alive", "timeout=5", "400"},
		{"proxy-connection", "keep-alive", "400"}, {"upgrade", "websocket", "400"},
		{"te", "gzip", "400"}, {"x-large", strings.Repeat("x", 4096), "rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, framer, _ := connect(t)
			beforeBackend, beforeMiddleware := backendCalls.Load(), middlewareCalls.Load()
			var encoded bytes.Buffer
			encoder := hpack.NewEncoder(&encoded)
			for _, field := range []hpack.HeaderField{{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "http"}, {Name: ":authority", Value: "localhost"}, {Name: ":path", Value: "/"}, {Name: tc.name, Value: tc.value}} {
				if err := encoder.WriteField(field); err != nil {
					t.Fatal(err)
				}
			}
			if err := framer.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, EndStream: true, EndHeaders: true, BlockFragment: encoded.Bytes()}); err != nil {
				t.Fatal(err)
			}
			framer.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
			var status string
			for status == "" {
				frame, err := framer.ReadFrame()
				if err != nil {
					if tc.status == "rejected" && errors.Is(err, io.EOF) {
						status = "rejected"
						break
					}
					t.Fatal(err)
				}
				if goaway, ok := frame.(*http2.GoAwayFrame); ok && tc.status == "rejected" {
					if goaway.ErrCode != http2.ErrCodeCompression && goaway.ErrCode != http2.ErrCodeProtocol {
						t.Fatalf("oversized header GOAWAY=%v", goaway.ErrCode)
					}
					status = "rejected"
				}
				if headers, ok := frame.(*http2.MetaHeadersFrame); ok {
					for _, field := range headers.Fields {
						if field.Name == ":status" {
							status = field.Value
						}
					}
				}
			}
			if status != tc.status {
				t.Fatalf("status=%s want=%s", status, tc.status)
			}
			if backendCalls.Load() != beforeBackend || middlewareCalls.Load() != beforeMiddleware {
				t.Fatal("malformed H2 request reached Jul or its backend")
			}
		})
	}
	t.Run("genuinely oversized frame", func(t *testing.T) {
		_, framer, maximum := connect(t)
		_ = framer.WriteRawFrame(http2.FrameHeaders, http2.FlagHeadersEndHeaders|http2.FlagHeadersEndStream, 1, make([]byte, int(maximum)+1))
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			if goaway, ok := frame.(*http2.GoAwayFrame); ok {
				if goaway.ErrCode != http2.ErrCodeFrameSize {
					t.Fatalf("GOAWAY=%v", goaway.ErrCode)
				}
				break
			}
		}
	})
	t.Run("duplicate settings peer isolation", func(t *testing.T) {
		for attempt := 0; attempt < 8; attempt++ {
			conn, framer, _ := connect(t)
			if err := framer.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0}, http2.Setting{ID: http2.SettingInitialWindowSize, Val: 65535}); err != nil {
				t.Fatal(err)
			}
			for {
				frame, err := framer.ReadFrame()
				if err != nil {
					t.Fatal(err)
				}
				if goaway, ok := frame.(*http2.GoAwayFrame); ok {
					if goaway.ErrCode != http2.ErrCodeProtocol {
						t.Fatalf("GOAWAY=%v", goaway.ErrCode)
					}
					break
				}
			}
			_ = conn.Close()
		}
		var protocols http.Protocols
		protocols.SetUnencryptedHTTP2(true)
		transport := &http.Transport{Protocols: &protocols}
		defer transport.CloseIdleConnections()
		response, err := (&http.Client{Transport: transport, Timeout: 3 * time.Second}).Get("http://" + addr + "/")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != 200 || response.ProtoMajor != 2 || string(body) != "ok" {
			t.Fatalf("fresh peer affected: status=%d body=%q err=%v", response.StatusCode, body, err)
		}
	})
}
