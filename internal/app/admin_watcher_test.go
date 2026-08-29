// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/config"
)

// TestAdminWriteAndWatcherEcho (R9-14.2) verifies that a configuration applied
// through the admin API is persisted to disk and picked up by the live
// runtime, and that a subsequent external write to the same file is treated
// as drift rather than adopted (ADR 0019 §11: managed mode's file watcher is
// a drift detector, not a reload trigger). config_authority is declared
// explicitly as managed so the admin write path stays enabled.
func TestAdminWriteAndWatcherEcho(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "server.toml")
	adminAddr := freePort(t)
	trafficAddr := freePort(t)
	token := "admin-write-watcher-token"

	writeConfig := func(returnCode int) {
		t.Helper()
		cfg := &config.Config{
			Global: config.GlobalConfig{ConfigAuthority: "managed"},
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
	// Simulate a prior explicit adoption (ADR 0019 §14): a fresh managed boot
	// with no persisted baseline refuses every write until one occurs. Tests
	// that only exercise ordinary managed writes pre-seed the baseline
	// directly instead of driving the adopt-external HTTP flow.
	seedRaw, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seed for pre-adoption: %v", err)
	}
	if err := NewManagedBaselineStore(cfgPath).CommitMark(seedRaw, ""); err != nil {
		t.Fatalf("pre-adopt managed baseline: %v", err)
	}

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
	// Wait for the admin listener as well before sending the apply request; the
	// admin goroutine starts just before the traffic server, and under loaded
	// parallel test runs the listener may not be accepting yet (or may fail to
	// bind) when the first request is issued.
	adminHealthURL := "http://" + adminAddr + "/healthz"
	if !waitForURL(t, adminHealthURL, 5*time.Second) {
		t.Fatalf("admin server did not become ready")
	}
	// Admin apply: change return code from 200 to 201. Do not write the file
	// first: the apply endpoint persists the candidate itself, and a competing
	// file-watcher reload from a manual write races with the managed reload and
	// can increment the monotonic admin auth generation between authorization
	// and validation (flaky auth_cas 409).
	raw201, err := config.Marshal(&config.Config{
		Global: config.GlobalConfig{ConfigAuthority: "managed"},
		Admin:  config.AdminConfig{Enabled: true, Listen: adminAddr, Token: token, HistoryDir: tmp},
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
	// operator editing the file directly. Managed mode's watcher must treat
	// this as drift and must NOT reload it — the runtime keeps serving 201
	// (ADR 0019 §11 points 3-4). Give the debounced watcher time to fire and
	// assert the traffic server never moves off the last managed version.
	writeConfig(202)
	if waitForHTTPStatus(t, trafficURL, 202, 1500*time.Millisecond) {
		t.Fatal("managed mode must not adopt an external edit through the watcher")
	}
	resp2, err := http.Get(trafficURL)
	if err != nil {
		t.Fatalf("get traffic url: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 201 {
		t.Fatalf("runtime status = %d, want 201 (still the last managed version)", resp2.StatusCode)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not exit after context cancellation")
	}
}

// TestExternalDivergenceBlocksHotApply verifies that an external edit which
// introduces a restart-required change is detected as external divergence and
// blocks subsequent admin hot applies until the divergence is resolved (F-04).
func TestExternalDivergenceBlocksHotApply(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "server.toml")
	adminAddr := freePort(t)
	trafficAddr := freePort(t)
	token := "admin-external-divergence-token"

	baseCfg := func() *config.Config {
		return &config.Config{
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
					Return: 200,
				}},
			}},
		}
	}

	writeCfg := func(cfg *config.Config) {
		t.Helper()
		raw, err := config.Marshal(cfg)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if err := os.WriteFile(cfgPath, raw, 0o600); err != nil {
			t.Fatalf("write config: %v", err)
		}
	}

	writeCfg(baseCfg())

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
	adminHealthURL := "http://" + adminAddr + "/healthz"
	if !waitForURL(t, adminHealthURL, 5*time.Second) {
		t.Fatalf("admin server did not become ready")
	}

	// Externally edit the file with a restart-required change (log_format).
	// The watcher will attempt to reload it, fail because it is restart-required,
	// and leave the runtime serving the previous config while disk diverges.
	diverged := baseCfg()
	diverged.Global.LogFormat = "json"
	writeCfg(diverged)

	// Poll the pending-restart endpoint until the divergence is detected.
	var pendingOK bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		req, err := http.NewRequest(http.MethodGet, "http://"+adminAddr+"/api/config/pending-restart", nil)
		if err != nil {
			t.Fatalf("create pending-restart request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("pending-restart request: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("pending-restart status = %d, want 200; body: %s", resp.StatusCode, body)
		}
		var out struct {
			Pending bool `json:"pending"`
			Status  struct {
				State      string   `json:"state"`
				External   bool     `json:"external"`
				Managed    bool     `json:"managed"`
				Subsystems []string `json:"subsystems"`
			} `json:"status"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode pending-restart: %v", err)
		}
		if out.Pending && out.Status.State == "external_divergence" && out.Status.External && !out.Status.Managed {
			pendingOK = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !pendingOK {
		t.Fatalf("external divergence was not detected; last logs:\n%s", logBuf.String())
	}

	// A hot apply while external divergence is present must be rejected.
	hotRaw, err := config.Marshal(baseCfg())
	if err != nil {
		t.Fatalf("marshal hot candidate: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+adminAddr+"/api/config/apply", bytes.NewReader(hotRaw))
	if err != nil {
		t.Fatalf("create apply request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("hot apply request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("hot apply status = %d, want 409; body: %s", resp.StatusCode, body)
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
