// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/versionid"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileOwnedEffectiveVersionFollowsActualPublication(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jul.toml")
	reference := filepath.Join(dir, "ref")
	os.WriteFile(reference, []byte("a"), 0600)
	cfg := config.ProxyTarget("http://127.0.0.1:9000", freePort(t))
	cfg.Global.ConfigAuthority = "file_owned"
	cfg.Servers[0].Name = "before-${file:" + reference + "}-after"
	cfg.Servers[0].Locations[0].ProxyPass = ""
	cfg.Servers[0].Locations[0].Return = 200
	cfg.Admin = config.AdminConfig{Enabled: true, Listen: freePort(t), Token: "effective-version-http-synthetic-token"}
	raw, _ := config.Marshal(cfg)
	os.WriteFile(path, raw, 0600)
	versions, _ := versionid.New(bytes.Repeat([]byte{64}, 32))
	expected, _ := config.NewCandidate(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	signals := make(chan struct{}, 1)
	done := make(chan int, 1)
	go func() {
		done <- Serve(ctx, signals, config.NewTOMLSource(path), cfg, "Jul.IA", "version-test", WithEffectiveVersions(versions), WithLogOutput(io.Discard))
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("Serve exit %d", code)
			}
		case <-time.After(10 * time.Second):
			t.Error("shutdown timed out")
		}
	})
	client := &http.Client{Timeout: time.Second}
	status := func() adminapi.StatusResponse {
		req, _ := http.NewRequest("GET", "http://"+cfg.Admin.Listen+"/api/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+cfg.Admin.Token)
		res, err := client.Do(req)
		if err != nil {
			return adminapi.StatusResponse{}
		}
		defer res.Body.Close()
		var s adminapi.StatusResponse
		_ = json.NewDecoder(res.Body).Decode(&s)
		return s
	}
	waitVersion := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if s := status(); s.Ready && s.ServingVersion == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("serving version did not follow publication")
	}
	original := versions.Config(expected.Effective)
	waitVersion(original)
	os.WriteFile(reference, []byte("b"), 0600)
	changed, _ := config.NewCandidate(cfg)
	signals <- struct{}{}
	rotated := versions.Config(changed.Effective)
	waitVersion(rotated)
	if rotated == original {
		t.Fatal("reference rotation undetected")
	}
	// A failed resolution leaves the previously published runtime unchanged.
	os.Remove(reference)
	signals <- struct{}{}
	time.Sleep(80 * time.Millisecond)
	if status().ServingVersion != rotated {
		t.Fatal("failed reload changed serving version")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, raw) {
		t.Fatal("file-owned reload changed raw configuration")
	}
	os.WriteFile(reference, []byte("a"), 0600)
	signals <- struct{}{}
	waitVersion(original)
}

func TestServeMissingVersionKeyDoesNotStartListeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-key")
	t.Setenv(versionid.Environment, path)
	cfg := config.ProxyTarget("http://127.0.0.1:9000", freePort(t))
	var output bytes.Buffer
	if code := Serve(context.Background(), nil, config.NewTOMLSource(filepath.Join(t.TempDir(), "jul.toml")), cfg, "Jul.IA", "test", WithLogOutput(&output)); code == 0 {
		t.Fatal("missing established key accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("startup initialized missing key")
	}
}

func TestServeExplicitKeyPathOverridesEnvironment(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "private", "key")
	if _, err := versionid.Init(keyPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv(versionid.Environment, filepath.Join(t.TempDir(), "absent"))
	cfg := config.ProxyTarget("http://127.0.0.1:9000", freePort(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	// A cancelled runtime may finish either cleanly or during preparation; key
	// selection itself must succeed and proceed past the startup load boundary.
	_ = Serve(ctx, nil, config.NewTOMLSource(filepath.Join(t.TempDir(), "jul.toml")), cfg, "Jul.IA", "test", WithEffectiveVersionKeyFile(keyPath), WithLogOutput(&out))
	if strings.Contains(out.String(), "effective version key unavailable") {
		t.Fatal("explicit file did not override environment")
	}
}
