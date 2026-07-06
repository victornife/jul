// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchFileNotifiesOnChange(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := WatchFile(ctx, path, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("WatchFile: %v", err)
	}

	// Trigger a change.
	if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
		t.Fatalf("update temp config: %v", err)
	}

	select {
	case <-ch:
		// Good.
	case <-ctx.Done():
		t.Fatal("timed out waiting for watch notification")
	}
}

func TestWatchFileDebounces(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := WatchFile(ctx, path, 200*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("WatchFile: %v", err)
	}

	// Trigger multiple rapid changes.
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(path, []byte(string(rune('b'+i))), 0644); err != nil {
			t.Fatalf("update temp config: %v", err)
		}
	}

	// Should still receive exactly one event.
	select {
	case <-ch:
		// Good. Wait briefly to ensure no second event is queued.
		select {
		case <-ch:
			t.Fatal("expected only one debounced event, got a second")
		case <-time.After(300 * time.Millisecond):
			// No duplicate within debounce window.
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for watch notification")
	}
}

func TestWatchFileDefaultDebounce(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Pass zero debounce — should adopt the 200 ms default.
	ch, err := WatchFile(ctx, path, 0, nil)
	if err != nil {
		t.Fatalf("WatchFile: %v", err)
	}

	if err := os.WriteFile(path, []byte("b"), 0644); err != nil {
		t.Fatalf("update temp config: %v", err)
	}

	select {
	case <-ch:
		// Good.
	case <-ctx.Done():
		t.Fatal("timed out waiting for watch notification")
	}
}

func TestWatchFileIgnoresOtherFiles(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	other := filepath.Join(tmp, "other.toml")
	if err := os.WriteFile(path, []byte("a"), 0644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := WatchFile(ctx, path, 50*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("WatchFile: %v", err)
	}

	// Write a different file in the same directory.
	if err := os.WriteFile(other, []byte("noise"), 0644); err != nil {
		t.Fatalf("write other: %v", err)
	}

	select {
	case <-ch:
		t.Fatal("should not have received event for unrelated file")
	case <-time.After(300 * time.Millisecond):
		// Good — no spurious event.
	}
}
