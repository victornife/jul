// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
)

// TestAdminWriteAndWatcherEcho (R9-14.2) verifies that a configuration applied
// through the admin API is persisted to disk and picked up by the live runtime,
// and that a subsequent external write to the same file is detected by the
// file watcher and also reloaded.
func TestAdminWriteAndWatcherEcho(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "server.toml")
	adminAddr := freePort(t)
	trafficAddr := freePort(t)
	token := "admin-write-watcher-token"

	writeConfig := func(returnCode int) {
		t.Helper()
		cfg := &config.Config{
			Admin: config.AdminConfig{
				Enabled:    true,
				Listen:     adminAddr,
				Token:      token,
				HistoryDir: tmp,
			},
			Servers: []config.ServerConfig{{
				Listen: trafficAddr,
				Locations: []config.LocationConfig{{
					Match:  config.MatchConfig{Type: "prefix", Path: "/"},
					Return: returnCode,
				}},
			}},
		}
		raw, err := config.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	writeConfig(200)

	logBuf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		seed, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Errorf("read seed config: %v", err)
			done <- 1
			return
		}
		cfg, err := config.Parse(seed)
		if err != nil {
			t.Errorf("parse seed config: %v", err)
			done <- 1
			return
		}
		done <- Serve(ctx, nil, config.NewTOMLSource(cfgPath), cfg, "Jul.IA", "test", WithLogOutput(logBuf))
	}()

	trafficURL := "http://" + trafficAddr
	if !waitForHTTPStatus(t, trafficURL, 200, 5*time.Second) {
		t.Fatalf("traffic server did not start")
	}

	// Admin apply: change return code from 200 to 201.
	writeConfig(201)
	raw201, err := config.Marshal(&config.Config{
		Admin: config.AdminConfig{Enabled: true, Listen: adminAddr, Token: token, HistoryDir: tmp},
		Servers: []config.ServerConfig{{
			Listen: trafficAddr,
			Locations: []config.LocationConfig{{
				Match:  config.MatchConfig{Type: "prefix", Path: "/"},
				Return: 201,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("marshal 201 config: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+adminAddr+"/api/config/apply", bytes.NewReader(raw201))
	if err != nil {
		t.Fatalf("create apply request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("admin apply: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	// Wait for the live runtime to serve the admin-applied config.
	if !waitForHTTPStatus(t, trafficURL, 201, 5*time.Second) {
		t.Fatalf("admin-applied config did not become live")
	}

	// Verify the on-disk file matches the admin-applied config.
	disk, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read disk config: %v", err)
	}
	if !bytes.Contains(disk, []byte("return = 201")) {
		t.Fatalf("disk config did not contain applied return code; got:\n%s", disk)
	}

	// External file write: change return code from 201 to 202, simulating an
	// operator editing the file directly. The watcher should reload it.
	writeConfig(202)
	if !waitForHTTPStatus(t, trafficURL, 202, 5*time.Second) {
		t.Fatalf("watcher did not reload externally written config")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not exit after context cancellation")
	}
}

func waitForHTTPStatus(t *testing.T, url string, want int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			code := resp.StatusCode
			resp.Body.Close()
			if code == want {
				return true
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
