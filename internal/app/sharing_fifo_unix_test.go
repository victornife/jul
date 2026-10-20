// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build unix

package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/observability"
	"jul/internal/redact"
	"jul/internal/server"
	"jul/internal/upstream"
)

func TestSixth653SnapshotMustNotReopenPreparedAuthFile(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("s3cret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	validHtpasswd := []byte("alice:" + string(hash) + "\n")

	runCase := func(t *testing.T, replaceWithFIFO bool) {
		t.Helper()
		dir := t.TempDir()
		htFile := filepath.Join(dir, "users")
		if err := os.WriteFile(htFile, validHtpasswd, 0o600); err != nil {
			t.Fatal(err)
		}

		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()

		routeID := "admin-only"
		makeCfg := func(withdrawn bool) *config.Config {
			return &config.Config{
				Servers: []config.ServerConfig{{
					Listen: addr,
					Locations: []config.LocationConfig{{
						RouteID:   &routeID,
						Match:     config.MatchConfig{Type: "prefix", Path: "/"},
						Return:    200,
						Withdrawn: withdrawn,
						Auth: &config.AuthConfig{
							Basic: &config.BasicAuthConfig{File: htFile},
						},
					}},
				}},
			}
		}

		cfg1 := makeCfg(false)
		f, cleanup := minimalFactory(t)
		defer cleanup()

		swapped := make(chan struct{}, 1)
		prepareFn := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
			handlers, genID, commit, abort, pErr := f.Prepare(ctx, c)
			if pErr == nil && c.Servers[0].Locations[0].Withdrawn {
				replacement := filepath.Join(dir, "replacement")
				if replaceWithFIFO {
					if err := syscall.Mkfifo(replacement, 0o600); err != nil {
						t.Fatalf("mkfifo: %v", err)
					}
				} else {
					if err := os.WriteFile(replacement, validHtpasswd, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Rename(replacement, htFile); err != nil {
					t.Fatal(err)
				}
				swapped <- struct{}{}
			}
			return handlers, genID, commit, abort, pErr
		}

		srv := server.New(
			cfg1,
			cfg1,
			lifecycle.ComputeFingerprint(cfg1),
			observability.NewLogger(io.Discard, "error", "text"),
			prepareFn,
			config.NewTOMLSource(""),
			func(context.Context, *config.Config) error { return nil },
		)
		srv.WAFSourceDigests = f.WAFSourceDigests
		srv.PluginModuleChanges = f.PluginModuleChanges

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		reloadCh := make(chan server.ReloadRequest, 2)
		runDone := make(chan error, 1)
		go func() {
			runDone <- srv.Run(ctx, reloadCh, redact.EmptyState())
		}()
		defer func() {
			cancel()
			<-runDone
		}()

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
			req.SetBasicAuth("alice", "s3cret")
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
		}

		cfg2 := makeCfg(true)
		cand2, err := config.NewCandidate(cfg2)
		if err != nil {
			t.Fatal(err)
		}

		resCh := make(chan server.ReloadResult, 1)
		reloadCh <- server.ReloadRequest{
			ID:        "withdraw",
			Source:    server.ReloadSourceAdmin,
			Candidate: cand2,
			Deadline:  time.Now().Add(300 * time.Millisecond),
			Result:    resCh,
		}
		<-swapped

		select {
		case res := <-resCh:
			if res.Outcome != server.ReloadAppliedLive {
				t.Fatalf("replaceWithFIFO=%v: expected ReloadAppliedLive, got %+v", replaceWithFIFO, res)
			}
		case <-time.After(1 * time.Second):
			t.Fatalf("replaceWithFIFO=%v: reload blocked past 1s", replaceWithFIFO)
		}

		reqAfter, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/", nil)
		reqAfter.SetBasicAuth("alice", "s3cret")
		respAfter, err := http.DefaultClient.Do(reqAfter)
		if err != nil {
			t.Fatal(err)
		}
		_ = respAfter.Body.Close()
		if respAfter.StatusCode != http.StatusForbidden {
			t.Fatalf("replaceWithFIFO=%v: expected 403 after withdrawal, got %d", replaceWithFIFO, respAfter.StatusCode)
		}
	}

	t.Run("regular-file-control", func(t *testing.T) {
		runCase(t, false)
	})
	t.Run("fifo-after-prepare", func(t *testing.T) {
		runCase(t, true)
	})
}
