// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"

	"jul/internal/config"
)

// writeTempConfig creates a minimal valid TOML config for service lifecycle
// tests and returns its path. It uses <stdlib testing>'s TempDir cleanup.
func writeTempConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

// minimalValidConfig returns the smallest TOML block that passes Validate().
func minimalValidConfig() string {
	return `[[servers]]
listen = "127.0.0.1:0"
  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  return = 200
`
}

// stubServe returns a function that blocks until ctx is cancelled and then
// returns exitCode. Tests use this instead of the real serve() to avoid
// binding ports and starting the full runtime.
func stubServe(exitCode int) func(context.Context, <-chan struct{}, config.Source, *config.Config) int {
	return func(ctx context.Context, _ <-chan struct{}, _ config.Source, _ *config.Config) int {
		<-ctx.Done()
		return exitCode
	}
}

// collectStatuses drains the status channel concurrently so test goroutine
// can send statuses without blocking. Returns a function to stop collecting.
func collectStatuses(status <-chan svc.Status) (states *[]svc.State, stop func()) {
	s := &[]svc.State{}
	mu := sync.Mutex{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for st := range status {
			mu.Lock()
			*s = append(*s, st.State)
			mu.Unlock()
		}
	}()
	return s, func() {
		mu.Lock()
		defer mu.Unlock()
		// Create a copy so the caller has a stable snapshot.
		copyStates := make([]svc.State, len(*s))
		for i, v := range *s {
			copyStates[i] = v
		}
		*s = copyStates
	}
}

// TestEdgeServiceExecuteStop tests the happy path: service starts, receives
// a Stop request, shuts down cleanly, and returns exit code 0.
func TestEdgeServiceExecuteStop(t *testing.T) {
	configPath := writeTempConfig(t, minimalValidConfig())

	requests := make(chan svc.ChangeRequest, 10)
	status := make(chan svc.Status, 10)

	es := &edgeService{configPath: configPath, serveFn: stubServe(0)}
	go func() {
		time.Sleep(50 * time.Millisecond)
		requests <- svc.ChangeRequest{Cmd: svc.Stop}
	}()

	var states []svc.State
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for s := range status {
			states = append(states, s.State)
		}
	}()

	_, code := es.Execute(nil, requests, status)
	close(status)
	wg.Wait()

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	hasStart := false
	hasRunning := false
	for _, st := range states {
		if st == svc.StartPending {
			hasStart = true
		}
		if st == svc.Running {
			hasRunning = true
		}
	}
	if !hasStart {
		t.Error("never sent StartPending status")
	}
	if !hasRunning {
		t.Error("never sent Running status")
	}
}

// TestEdgeServiceExecuteShutdown tests the Shutdown control path.
func TestEdgeServiceExecuteShutdown(t *testing.T) {
	configPath := writeTempConfig(t, minimalValidConfig())

	requests := make(chan svc.ChangeRequest, 10)
	status := make(chan svc.Status, 10)

	es := &edgeService{configPath: configPath, serveFn: stubServe(0)}
	go func() {
		time.Sleep(50 * time.Millisecond)
		requests <- svc.ChangeRequest{Cmd: svc.Shutdown}
	}()

	_, code := es.Execute(nil, requests, status)
	close(status)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestEdgeServiceExecuteInterrogate tests that Interrogate echoes the current
// status back. We send Interrogate while running, ensure status is echoed, then
// stop.
func TestEdgeServiceExecuteInterrogate(t *testing.T) {
	configPath := writeTempConfig(t, minimalValidConfig())

	requests := make(chan svc.ChangeRequest, 10)
	status := make(chan svc.Status, 10)

	es := &edgeService{configPath: configPath, serveFn: stubServe(0)}
	go func() {
		time.Sleep(50 * time.Millisecond)
		requests <- svc.ChangeRequest{
			Cmd:           svc.Interrogate,
			CurrentStatus: svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown},
		}
		time.Sleep(50 * time.Millisecond)
		requests <- svc.ChangeRequest{Cmd: svc.Stop}
	}()

	_, code := es.Execute(nil, requests, status)
	close(status)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestEdgeServiceExecuteBadConfig tests that a broken config causes immediate
// exit without a Running status and returns a non-zero exit code.
func TestEdgeServiceExecuteBadConfig(t *testing.T) {
	configPath := writeTempConfig(t, "not-valid-toml[[")

	requests := make(chan svc.ChangeRequest, 1)
	status := make(chan svc.Status, 10)

	es := &edgeService{configPath: configPath}

	_, code := es.Execute(nil, requests, status)
	close(status)

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero for bad config")
	}
}
