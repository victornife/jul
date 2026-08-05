// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

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
// closed on shutdown; the base logger is never closed here. Sinks are built once
// at startup — the file sink owns a rotating file handle and the syslog sink a
// system-log connection — so changing access-log settings requires a restart. On
// any error every resource opened so far is closed before returning.
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
			if werr := ensureWritable(cfg.File); werr != nil {
				err = fmt.Errorf("access_log file sink: %w", werr)
				return
			}
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

// ensureWritable verifies the access-log file can be created and appended to,
// creating its parent directory if needed, so a bad path fails fast at startup
// instead of silently dropping records on the first request.
func ensureWritable(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}
