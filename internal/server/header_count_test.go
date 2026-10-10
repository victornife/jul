// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

// Go 1.27's header-value count cap is independent of max_header_bytes. Exercise
// real Jul listeners so a small, highly compressible list cannot reach a handler
// merely because its byte size remains well below the configured ceiling.
func TestHeaderValueCountLimitBeforeHandler(t *testing.T) {
	for _, h2c := range []bool{false, true} {
		name := "http1"
		if h2c {
			name = "h2c"
		}
		t.Run(name, func(t *testing.T) {
			addr := freePort(t)
			cfg := cfgWith(addr)
			cfg.Servers[0].H2C = h2c
			cfg.Servers[0].MaxHeaderBytes = 1 << 20
			var calls atomic.Int32
			factory := func(context.Context, *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
				return map[string]http.Handler{addr: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					calls.Add(1)
					_, _ = w.Write([]byte("ok"))
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
			client := &http.Client{Timeout: 5 * time.Second}
			transport := &http.Transport{DisableKeepAlives: true}
			if h2c {
				var protocols http.Protocols
				protocols.SetUnencryptedHTTP2(true)
				transport.Protocols = &protocols
			}
			client.Transport = transport
			t.Cleanup(transport.CloseIdleConnections)
			request := func(count int) (*http.Response, error) {
				req, err := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < count; i++ {
					req.Header.Add("X-Test", "v")
				}
				return client.Do(req)
			}
			before := calls.Load()
			resp, err := request(http.DefaultMaxHeaderValueCount + 1)
			if resp != nil {
				resp.Body.Close()
				if resp.StatusCode < 400 {
					t.Fatalf("over-count request accepted: status = %d", resp.StatusCode)
				}
			} else if err == nil {
				t.Fatal("over-count request had neither a rejection nor an error")
			}
			if calls.Load() != before {
				t.Fatal("over-count request reached Jul's handler")
			}
			resp, err = request(10)
			if err != nil {
				t.Fatalf("healthy request after rejection: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || calls.Load() != before+1 {
				t.Fatalf("healthy request: status = %d, handler calls = %d", resp.StatusCode, calls.Load()-before)
			}
		})
	}
}
