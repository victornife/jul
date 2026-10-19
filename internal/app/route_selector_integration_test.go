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
	"reflect"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
)

// This starts the production composition root, HTTP API, coordinator, disk CAS
// and live reload pipeline. No mock persistence or publication callbacks.
func TestPublishedSelectorEditsOnlyShadowedBlock(t *testing.T) {
	for _, authority := range []string{"managed", "file_owned"} {
		t.Run(authority, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "jul.toml")
			adminAddr, trafficAddr := freePort(t), freePort(t)
			cfg := config.ProxyTarget("http://127.0.0.1:9000", trafficAddr)
			cfg.Global.ConfigAuthority = authority
			cfg.Admin = config.AdminConfig{Enabled: true, Listen: adminAddr, Token: "selector-integration-token", HistoryDir: filepath.Join(dir, "history")}
			cfg.Servers[0].ServerNames = []string{"a.test"}
			cfg.Servers[0].Locations[0].ProxyPass = ""
			cfg.Servers[0].Locations[0].Return = 200
			cfg.Servers = append(cfg.Servers, cfg.Servers[0])
			cfg.Servers[1].Locations = append([]config.LocationConfig(nil), cfg.Servers[0].Locations...)
			cfg.Servers[1].Locations[0].Return = 201
			raw, err := config.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if authority == "managed" {
				if err := NewManagedBaselineStore(path).CommitMark(raw, ""); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan int, 1)
			go func() {
				done <- Serve(ctx, nil, config.NewTOMLSource(path), cfg, "Jul.IA", "selector-test", WithLogOutput(io.Discard))
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case code := <-done:
					if code != 0 {
						t.Errorf("Serve exit %d", code)
					}
				case <-time.After(10 * time.Second):
					t.Error("Serve did not stop")
				}
			})
			if !waitForURL(t, "http://"+adminAddr+"/healthz", 5*time.Second) {
				t.Fatal("admin not ready")
			}
			client := &http.Client{Timeout: 5 * time.Second}
			request := func(method, endpoint string, body any) (int, []byte) {
				t.Helper()
				data, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequest(method, "http://"+adminAddr+endpoint, bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer selector-integration-token")
				req.Header.Set("Content-Type", "application/json")
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer res.Body.Close()
				content, err := io.ReadAll(res.Body)
				if err != nil {
					t.Fatal(err)
				}
				return res.StatusCode, content
			}
			code, content := request(http.MethodGet, "/api/routes", nil)
			if code != 200 {
				t.Fatalf("routes %d: %s", code, content)
			}
			var routes []admin.RouteProjection
			if err := json.Unmarshal(content, &routes); err != nil {
				t.Fatal(err)
			}
			if len(routes) != 2 || routes[1].Locations[0].MatchOrdinal != 1 {
				t.Fatalf("wrong projections: %s", content)
			}
			loc := routes[1].Locations[0]
			op := map[string]any{"op": "location_set_action", "listen": routes[1].Listen, "server_names": routes[1].ServerNames, "match_type": loc.Type, "path": loc.Match, "match_ordinal": loc.MatchOrdinal, "selector_version": loc.SelectorVersion, "action": map[string]any{"kind": "return", "status": 202}}
			authOp := map[string]any{}
			for key, value := range op {
				authOp[key] = value
			}
			delete(authOp, "action")
			authOp["op"] = "location_set_auth"
			authOp["auth"] = map[string]any{"method": "cidr", "allow": []string{"192.0.2.0/24"}}
			body := map[string]any{"base_version": routes[1].BaseVersion, "ops": []any{op, authOp}}
			code, content = request(http.MethodPost, "/api/config/patch/preview", body)
			if code != 200 {
				t.Fatalf("preview %d: %s", code, content)
			}
			code, content = request(http.MethodPost, "/api/config/patch/apply", body)
			if authority == "file_owned" {
				if code != 409 {
					t.Fatalf("file-owned apply %d: %s", code, content)
				}
			} else if code != 200 {
				t.Fatalf("apply %d: %s", code, content)
			}
			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := config.Parse(onDisk)
			if err != nil {
				t.Fatal(err)
			}
			original, err := config.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(parsed.Servers[0], original.Servers[0]) {
				t.Fatal("live block configuration was changed")
			}
			want := 202
			if authority == "file_owned" {
				want = 201
				if !bytes.Equal(onDisk, raw) {
					t.Fatal("file-owned refusal persisted")
				}
			}
			if authority == "managed" && parsed.Servers[1].Locations[0].Auth == nil {
				t.Fatal("shadowed access edit was not applied")
			}
			if parsed.Servers[1].Locations[0].Return != want {
				t.Fatalf("shadowed return=%d, want %d", parsed.Servers[1].Locations[0].Return, want)
			}
			req, _ := http.NewRequest(http.MethodGet, "http://"+trafficAddr+"/", nil)
			req.Host = "a.test"
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatalf("live route now returns %d", res.StatusCode)
			}
		})
	}
}
