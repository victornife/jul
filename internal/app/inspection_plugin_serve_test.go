// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && waf

package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// TestServeInspectionDeadlineThroughFullStack runs the production
// composition (server deadline owner, metrics, access log, compression,
// plugin, WAF) and checks that the plugin request path's body deadline is
// delivered as 408 through every wrapper, while WAF decisions and ordinary
// traffic keep their own outcomes.
func TestServeInspectionDeadlineThroughFullStack(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "origin saw %d bytes", len(b))
	}))
	defer backend.Close()
	guest, err := filepath.Abs(v2GuestPath())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	addr := freePort(t)
	raw := fmt.Sprintf(`
[global]
access_log = %q

[global.inspection]
mode = "enforce"
max_buffered_bytes = "1g"
body_timeout = "300ms"

[compression]
enabled = true
encoders = ["gzip"]

[plugins.p]
path = %q
abi = "jul-abi/v2"

[[servers]]
listen = %q

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  proxy_pass = %q
  plugins = ["p"]

    [servers.locations.waf]
    enabled = true
    mode = "block"
    inline_rules = 'SecRule ARGS:probe "@streq attack" "id:9701,phase:1,deny,status:403"'
`, filepath.Join(dir, "access.log"), guest, addr, backend.URL)
	path := filepath.Join(dir, "jul.toml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	logs := &safeBuffer{}
	go func() {
		done <- Serve(ctx, nil, config.NewTOMLSource(path), cfg, "Jul.IA", "test", WithLogOutput(logs))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not exit")
		}
	})
	base := "http://" + addr
	client := &http.Client{Timeout: 10 * time.Second}
	ready := time.Now().Add(30 * time.Second) // module compilation is slow under -race
	for {
		resp, err := client.Post(base+"/", "text/plain", strings.NewReader("hello"))
		if err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK || string(b) != "origin saw 5 bytes" {
				t.Fatalf("ordinary POST: %d %q", resp.StatusCode, b)
			}
			break
		}
		if time.Now().After(ready) {
			t.Fatalf("server not ready: %v\n%s", err, logs.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	resp, err := client.Get(base + "/?probe=attack")
	if err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("WAF decision: %v %v", err, resp)
	}
	resp.Body.Close()

	pr, pw := io.Pipe()
	defer pw.Close()
	req, _ := http.NewRequest(http.MethodPost, base+"/", pr)
	go func() { _, _ = pw.Write([]byte("partial")) }()
	start := time.Now()
	resp, err = client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("stalled upload: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout || !resp.Close {
		t.Fatalf("stalled upload: %d close=%v %q", resp.StatusCode, resp.Close, body)
	}
	if elapsed < 300*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("408 after %s", elapsed)
	}
}
