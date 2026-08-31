// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"jul/internal/config"
	"jul/internal/middleware"

	"gopkg.in/natefinch/lumberjack.v2"
)

// BuildAccessSinks assembles the access-log sink set from configuration. base is
// the server's structured logger, reused for the "stdout" sink so the standard
// access line keeps honoring [global].log_format. The "file" and "syslog" sinks
// write a dedicated copy encoded per cfg.Format ("text" or "json").
//
// The returned closers (the rotating file and the syslog connection) must be
// closed once the caller no longer needs this sink generation; the base logger
// is never closed here. Called once per handler generation — at startup and on
// every reload — so config changes hot-apply (#98): the caller stages the
// returned closers for generational teardown rather than closing them on
// shutdown directly. On any error every resource opened so far is closed
// before returning, and the real file/syslog target is never touched destructively:
// see probeWritableDir.
func BuildAccessSinks(cfg config.AccessLogConfig, base *slog.Logger) (sinks []middleware.AccessSink, closers []io.Closer, err error) {
	if !cfg.IsEnabled() {
		return nil, nil, nil
	}
	defer func() {
		if err != nil {
			for _, c := range closers {
				_ = c.Close()
			}
			sinks, closers = nil, nil
		}
	}()

	names := cfg.Sinks
	if names == nil {
		// Defaults are normally applied by config.Parse; guard direct callers so
		// an omitted set still produces the standard access line.
		names = []string{"stdout"}
	} else if len(names) == 0 {
		return nil, nil, fmt.Errorf("access_log: enabled configuration requires at least one sink")
	}

	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true

		switch name {
		case "stdout":
			sinks = append(sinks, middleware.NewSlogSink(base))
		case "file":
			if werr := probeWritableDir(cfg.File); werr != nil {
				err = fmt.Errorf("access_log file sink: %w", werr)
				return
			}
			// A fresh *lumberjack.Logger is built for every call, even when the
			// path is unchanged from the previous generation (#98): mutating a
			// live writer's exported fields (e.g. on a rotation-setting change)
			// while the previous, still-draining generation might concurrently
			// write to it would be a data race. This is safe for a changed path
			// (different generations then own different files) and safe for a
			// same-path change that does not alter rotation settings. The one
			// documented residual: a same-path rotation-setting change whose old
			// generation happens to rotate during the brief drain overlap can
			// leave the new generation's writer appending to the just-rotated
			// backup file rather than the live path — see docs/known-limitations.md.
			lj := &lumberjack.Logger{
				Filename:   cfg.File,
				MaxSize:    cfg.RotateMaxMB,
				MaxBackups: cfg.RotateKeep,
				LocalTime:  true,
			}
			sinks = append(sinks, middleware.NewSlogSink(slog.New(accessHandler(lj, cfg.Format))))
			closers = append(closers, lj)
		case "syslog":
			w, serr := newSyslogWriter()
			if serr != nil {
				err = fmt.Errorf("access_log syslog sink: %w", serr)
				return
			}
			sinks = append(sinks, middleware.NewSlogSink(slog.New(accessHandler(w, cfg.Format))))
			closers = append(closers, w)
		default:
			err = fmt.Errorf("access_log: unknown sink %q", name)
			return
		}
	}
	return sinks, closers, nil
}

// accessHandler builds an slog handler for a dedicated access-log sink. Access
// lines are always emitted at info level regardless of [global].log_level, so a
// quieter global level never suppresses a sink's own file or syslog output.
func accessHandler(w io.Writer, format string) slog.Handler {
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if format == "json" {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// probeCloseFile and probeRemoveFile are indirected so tests can force the
// rare close/remove failure paths in probeWritableDir deterministically —
// a real close or remove failure on a just-created temp file is impractical
// to trigger portably across platforms without this seam.
var (
	probeCloseFile  = (*os.File).Close
	probeRemoveFile = os.Remove
)

// probeWritableDir proves path's parent directory is writable by creating and
// immediately removing a temporary sentinel file, creating the directory first
// if it does not exist. It never touches path itself: a candidate access-sink
// build (#98) must be fully reversible on Abort, and the real file is created
// only by the writer's own first live write, which cannot happen before the
// candidate generation is committed. Rejects an empty path outright rather
// than probing the current working directory, and treats a failure to close
// or remove the sentinel as an error rather than leaving it behind silently.
func probeWritableDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("no path configured")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create directory %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".access-log-probe-*")
	if err != nil {
		return fmt.Errorf("directory %q not writable: %w", dir, err)
	}
	name := tmp.Name()
	if err := probeCloseFile(tmp); err != nil {
		_ = probeRemoveFile(name)
		return fmt.Errorf("directory %q: closing writability probe: %w", dir, err)
	}
	if err := probeRemoveFile(name); err != nil {
		return fmt.Errorf("directory %q: removing writability probe: %w", dir, err)
	}
	return nil
}
