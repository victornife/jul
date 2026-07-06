// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// WatchFile watches the configuration file for changes and emits a debounced
// signal on the returned channel. It watches the parent directory (not the file
// directly) so that atomic-rename saves common in editors are detected. The
// watcher stops when ctx is cancelled.
//
// Events are coalesced within debounce so a burst of writes triggers a single
// reload.
func WatchFile(ctx context.Context, path string, debounce time.Duration, log *slog.Logger) (<-chan struct{}, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	dir := filepath.Dir(abs)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, err
	}

	out := make(chan struct{}, 1)
	if debounce <= 0 {
		debounce = 200 * time.Millisecond
	}

	go func() {
		defer w.Close()
		var timer *time.Timer
		var timerC <-chan time.Time

		emit := func() {
			select {
			case out <- struct{}{}:
			default:
			}
		}

		for {
			select {
			case <-ctx.Done():
				if timer != nil {
					timer.Stop()
				}
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				// Only react to changes affecting our config file.
				if filepath.Clean(ev.Name) != abs {
					continue
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
					continue
				}
				if timer == nil {
					timer = time.NewTimer(debounce)
					timerC = timer.C
				} else {
					timer.Reset(debounce)
				}
			case <-timerC:
				emit()
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				if log != nil {
					log.Warn("config watcher error", "error", err)
				}
			}
		}
	}()

	return out, nil
}
