// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package observability provides logging, access logging, and metrics wiring.
package observability

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

// NewLogger builds a slog.Logger from a level ("debug"|"info"|"warn"|"error")
// and a format ("text"|"json"). Output goes to w; pass os.Stderr for the
// default error log destination. For hot-reload support use NewDynamicLogger.
func NewLogger(w io.Writer, level, format string) *slog.Logger {
	l, _ := NewDynamicLogger(w, level, format)
	return l
}

// NewDynamicLogger builds a slog.Logger backed by a mutable level var. The
// returned set function atomically updates the log level without rebuilding
// the handler, enabling hot-reload of [global].log_level. Format changes
// (text ↔ json) require a restart because they change the handler type.
func NewDynamicLogger(w io.Writer, level, format string) (*slog.Logger, func(string)) {
	if w == nil {
		w = os.Stderr
	}
	lv := &slog.LevelVar{}
	lv.Set(parseLevel(level))
	opts := &slog.HandlerOptions{Level: lv}
	var h slog.Handler
	switch strings.ToLower(format) {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h), func(l string) { lv.Set(parseLevel(l)) }
}

// parseLevel retains an internal fallback to info as defense in depth for
// direct library callers. Supported configuration paths validate
// [global].log_level before constructing or updating the logger, so public
// configuration never relies on this fallback.
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
