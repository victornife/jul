// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestManagedConfigReadUsesStartupSizeBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.toml")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((16 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfigFile(path); err == nil {
		t.Fatal("managed raw read accepted a config over startup's limit")
	}
}

func TestStateMarkerRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxStateMarkerBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readStateMarker(path); err == nil {
		t.Fatal("oversized state marker accepted")
	}
}

func TestConfigWatcherKeepsLatestDigestWhenReceiverIsSlow(t *testing.T) {
	ch := make(chan [32]byte, 1)
	ch <- [32]byte{1}
	if !sendLatestConfigDigest(context.Background(), ch, [32]byte{2}) {
		t.Fatal("watcher failed to publish latest digest")
	}
	if got := <-ch; got[0] != 2 {
		t.Fatalf("watcher retained stale digest %v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sendLatestConfigDigest(ctx, ch, [32]byte{3}) {
		t.Fatal("watcher continued after cancellation")
	}
}

// Cancellation can arrive between the initial Err check and either send.
// Simulate that exact edge so a watcher does not block without a receiver.
type cancelAfterErrCheck struct {
	context.Context
	cancel context.CancelFunc
}

func (c cancelAfterErrCheck) Err() error {
	c.cancel()
	return nil
}

func TestConfigDigestSendStopsWhenCancellationRacesInitialCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := make(chan [32]byte) // no receiver: only cancellation can release the send
	if sendLatestConfigDigest(cancelAfterErrCheck{Context: ctx, cancel: cancel}, out, [32]byte{1}) {
		t.Fatal("digest was published after watcher cancellation")
	}
}

func TestStopConfigWatcherWaitsForShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		notifications := make(chan [32]byte, 1)
		notifications <- [32]byte{1}
		done := make(chan struct{})
		go func() {
			defer close(done)
			stopConfigWatcher(cancel, notifications)
		}()
		synctest.Wait()
		if ctx.Err() != context.Canceled {
			t.Fatal("watcher context was not canceled")
		}
		select {
		case <-done:
			t.Fatal("watcher stop returned before shutdown completed")
		default:
		}
		close(notifications)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("watcher stop did not return after shutdown completed")
		}
	})
}

func TestStopConfigWatcherWithoutWatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopConfigWatcher(cancel, nil)
	if ctx.Err() != context.Canceled {
		t.Fatal("watcher context was not canceled")
	}
}

func TestWatchConfigCancellationDrainsNotificationsBeforeClosingDigests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		notifications := make(chan struct{})
		digests := make(chan [32]byte, 1)
		go watchConfigEvents(ctx, "server.toml", nil, notifications, digests)
		synctest.Wait()
		select {
		case <-digests:
			t.Fatal("digest channel closed before watcher shutdown completed")
		default:
		}
		notifications <- struct{}{}
		synctest.Wait()
		select {
		case <-digests:
			t.Fatal("digest channel closed after draining but before watcher shutdown completed")
		default:
		}
		close(notifications)
		synctest.Wait()
		if _, open := <-digests; open {
			t.Fatal("digest channel stayed open after watcher shutdown completed")
		}
	})
}

func TestWatchConfigEventsClosesAfterSourceStops(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		notify        bool
		readable      bool
		logErrors     bool
		cancelPublish bool
	}{
		{name: "closed source"},
		{name: "unreadable file", notify: true},
		{name: "unreadable file logs error", notify: true, logErrors: true},
		{name: "updated file", notify: true, readable: true},
		{name: "canceled publish", notify: true, readable: true, cancelPublish: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "server.toml")
			data := []byte("updated config")
			if testCase.readable {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			notifications := make(chan struct{}, 1)
			if testCase.notify {
				notifications <- struct{}{}
			}
			close(notifications)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			watcherContext := context.Context(ctx)
			capacity := 1
			if testCase.cancelPublish {
				watcherContext = cancelAfterErrCheck{Context: ctx, cancel: cancel}
				capacity = 0
			}
			digests := make(chan [32]byte, capacity)
			var logOutput bytes.Buffer
			var logger *slog.Logger
			if testCase.logErrors {
				logger = slog.New(slog.NewTextHandler(&logOutput, nil))
			}
			watchConfigEvents(watcherContext, path, logger, notifications, digests)
			if testCase.readable && !testCase.cancelPublish {
				if digest, open := <-digests; !open || digest != sha256.Sum256(data) {
					t.Fatalf("unexpected config digest: %v, open=%v", digest, open)
				}
			}
			if _, open := <-digests; open {
				t.Fatal("digest channel stayed open or emitted an unexpected digest")
			}
			if testCase.logErrors && !strings.Contains(logOutput.String(), "failed to read file for digest") {
				t.Fatalf("read failure was not logged: %q", logOutput.String())
			}
			if testCase.cancelPublish && ctx.Err() != context.Canceled {
				t.Fatal("publish did not observe cancellation")
			}
		})
	}
}

func TestWatchConfigCancellationClosesDigests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.toml")
	if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	digests := watchConfig(ctx, path, nil)
	if digests == nil {
		t.Fatal("config watcher did not start")
	}
	cancel()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		select {
		case _, open := <-digests:
			if !open {
				return
			}
		case <-timeout.C:
			t.Fatal("config watcher digests did not close after cancellation")
		}
	}
}
