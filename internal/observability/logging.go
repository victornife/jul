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
// default error log destination.
func NewLogger(w io.Writer, level, format string) *slog.Logger {
	if w == nil {
		w = os.Stderr
	}

	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	switch strings.ToLower(format) {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		h = slog.NewTextHandler(w, opts)
	}
	return slog.New(h)
}

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
