// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	const debounce = 50 * time.Millisecond
	events := make(chan fsnotify.Event)
	ch := make(chan struct{}, 1)
	go watchFileEvents(ctx, path, debounce, nil, events, make(chan error), ch)

	// The unbuffered event stream ensures each change reaches the debounce loop
	// before the next is sent; OS filesystem notification latency is irrelevant.
	for i := 0; i < 3; i++ {
		events <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	}

	// Should still receive exactly one event.
	select {
	case <-ch:
		// Good. Wait to ensure no second event is queued, comfortably past
		// the debounce window to tolerate CI scheduling jitter.
		select {
		case <-ch:
			t.Fatal("expected only one debounced event, got a second")
		case <-time.After(2 * debounce):
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
