// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package observability provides logging, access logging, and metrics wiring.
package observability

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
)

// NewLogger builds a slog.Logger from a level ("debug"|"info"|"warn"|"error")
// and a format ("text"|"json"). Output goes to w; pass os.Stderr for the
// default error log destination. For hot-reload support use NewDynamicLogger.
func NewLogger(w io.Writer, level, format string) *slog.Logger {
	l, _, _ := NewDynamicLogger(w, level, format)
	return l
}

// NewDynamicLogger builds a slog.Logger backed by a mutable level var and a
// swappable format handler. The returned setLevel function atomically updates
// the log level; setFormat atomically swaps the active text/JSON handler
// (#91). Both hot-reload without rebuilding the *slog.Logger: every package
// that already holds this logger, or a derived one from With/WithGroup,
// observes the change on its very next call.
func NewDynamicLogger(w io.Writer, level, format string) (logger *slog.Logger, setLevel func(string), setFormat func(string)) {
	if w == nil {
		w = os.Stderr
	}
	lv := &slog.LevelVar{}
	lv.Set(parseLevel(level))
	opts := &slog.HandlerOptions{Level: lv}
	root := newDynamicHandler(newFormatHandler(w, opts, format))
	setLevel = func(l string) { lv.Set(parseLevel(l)) }
	setFormat = func(f string) { root.root.store(newFormatHandler(w, opts, f)) }
	return slog.New(root), setLevel, setFormat
}

// newFormatHandler builds a plain (non-dynamic) text or JSON handler for the
// given format. It never fails: canonical format values are validated by
// configuration validation before this is called.
func newFormatHandler(w io.Writer, opts *slog.HandlerOptions, format string) slog.Handler {
	switch strings.ToLower(format) {
	case "json":
		return slog.NewJSONHandler(w, opts)
	default:
		return slog.NewTextHandler(w, opts)
	}
}

// dynamicRoot holds the process's single active log-handler generation. It is
// swapped exactly once per successful [global].log_format hot reload (#91).
// Every DynamicHandler derived from the same root — via WithAttrs/WithGroup —
// shares this pointer, so a swap is observed by every one of them on their
// very next Handle or Enabled call; no logger reference goes stale.
type dynamicRoot struct {
	delegate atomic.Pointer[slog.Handler]
}

func (r *dynamicRoot) load() slog.Handler   { return *r.delegate.Load() }
func (r *dynamicRoot) store(h slog.Handler) { r.delegate.Store(&h) }

// logOp records one WithAttrs or WithGroup call in the exact order the caller
// made it. slog's documented contract nests groups and attributes in call
// order, so the two kinds cannot be tracked as independent slices — a
// WithGroup("a").WithAttrs(x).WithGroup("b") chain must replay as exactly
// that sequence over whatever the current root delegate is.
type logOp struct {
	attrs []slog.Attr // non-nil for a WithAttrs op
	group string      // non-empty for a WithGroup op; attrs is nil
}

// DynamicHandler is a stable slog.Handler facade over an atomically
// swappable root delegate. It satisfies the plain slog.Handler contract, so
// it composes with WithAttrs/WithGroup exactly like any other handler, while
// every derived instance keeps following future root swaps (#91).
type DynamicHandler struct {
	root *dynamicRoot
	ops  []logOp
}

// newDynamicHandler builds the root DynamicHandler wrapping the initial
// text/JSON handler.
func newDynamicHandler(initial slog.Handler) *DynamicHandler {
	root := &dynamicRoot{}
	root.store(initial)
	return &DynamicHandler{root: root}
}

// resolve rebuilds this instance's fully-qualified handler by replaying its
// recorded ops, in order, over the currently active root delegate.
func (h *DynamicHandler) resolve() slog.Handler {
	cur := h.root.load()
	for _, op := range h.ops {
		if op.attrs != nil {
			cur = cur.WithAttrs(op.attrs)
		} else {
			cur = cur.WithGroup(op.group)
		}
	}
	return cur
}

// Enabled reports whether the currently active delegate would log at level.
func (h *DynamicHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.resolve().Enabled(ctx, level)
}

// Handle resolves the current delegate (root swap included) and hands it the
// record. A record is always fully handled by exactly one delegate — never
// split or duplicated across an in-progress format swap.
func (h *DynamicHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.resolve().Handle(ctx, r)
}

// WithAttrs returns a derived handler carrying attrs in addition to this
// instance's own recorded ops, still following future root swaps.
func (h *DynamicHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := make([]logOp, len(h.ops), len(h.ops)+1)
	copy(next, h.ops)
	next = append(next, logOp{attrs: attrs})
	return &DynamicHandler{root: h.root, ops: next}
}

// WithGroup returns a derived handler nesting future attributes/records under
// name, still following future root swaps.
func (h *DynamicHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := make([]logOp, len(h.ops), len(h.ops)+1)
	copy(next, h.ops)
	next = append(next, logOp{group: name})
	return &DynamicHandler{root: h.root, ops: next}
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
