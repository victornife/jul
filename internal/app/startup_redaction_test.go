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
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
)

// safeBuffer is a concurrency-safe bytes.Buffer for capturing log output.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestCompositionRootStartupRedactionIsolation (R9-14.1) boots the full
// composition root and verifies that secret values resolved from ${env:} and
// ${file:} references never appear in startup logs or the admin runtime
// overview response.
func TestCompositionRootStartupRedactionIsolation(t *testing.T) {
	t.Setenv("JUL_ADMIN_TOKEN", "super-secret-admin-token-7d3f")
	t.Setenv("JUL_PROXY_HEADER", "super-secret-header-value-9a2e")

	tmp := t.TempDir()
	secretFile := filepath.Join(tmp, "api-key.txt")
	const fileSecret = "super-secret-file-key-b8c1"
	if err := os.WriteFile(secretFile, []byte(fileSecret), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	adminAddr := freePort(t)
	trafficAddr := freePort(t)
	cfgPath := filepath.Join(tmp, "server.toml")
	cfg := &config.Config{
		Global: config.GlobalConfig{
			LogLevel:  "debug",
			LogFormat: "text",
		},
		Admin: config.AdminConfig{
			Enabled:    true,
			Listen:     adminAddr,
			Token:      "${env:JUL_ADMIN_TOKEN}",
			HistoryDir: tmp,
		},
		Servers: []config.ServerConfig{{
			Listen: trafficAddr,
			Locations: []config.LocationConfig{{
				Match:  config.MatchConfig{Type: "prefix", Path: "/"},
				Return: 200,
				Headers: map[string]string{
					"X-Api-Key": "${file:" + secretFile + "}",
				},
			}},
		}},
	}
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	logBuf := &safeBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan int, 1)
	go func() {
		done <- Serve(ctx, nil, config.NewTOMLSource(cfgPath), cfg, "Jul.IA", "test", WithLogOutput(logBuf))
	}()

	// Wait for the admin endpoint to become ready.
	adminURL := "http://" + adminAddr + "/api/runtime/overview"
	if !waitForURL(t, adminURL, 5*time.Second) {
		t.Fatalf("admin server did not become ready")
	}

	// Fetch the runtime overview using the admin token.
	req, err := http.NewRequest(http.MethodGet, adminURL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer super-secret-admin-token-7d3f")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch overview: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read overview: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("overview status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not exit after context cancellation")
	}

	secrets := []string{
		"super-secret-admin-token-7d3f",
		"super-secret-header-value-9a2e",
		fileSecret,
	}
	logs := logBuf.String()
	overview := string(body)
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("secret %q leaked into startup logs", s)
		}
		if strings.Contains(overview, s) {
			t.Errorf("secret %q leaked into runtime overview", s)
		}
	}
}

func waitForURL(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
