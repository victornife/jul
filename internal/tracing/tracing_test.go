// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package tracing

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// TestActiveDefaultsToNoop verifies Active never returns nil and the no-op span
// tolerates every method when no tracer is installed (the lean default build).
func TestActiveDefaultsToNoop(t *testing.T) {
	Set(nil) // ensure a clean process-wide state
	tr := Active()
	if tr == nil {
		t.Fatal("Active returned nil; it must always be non-nil")
	}
	ctx, span := tr.Start(context.Background(), "proxy.roundtrip")
	if ctx == nil {
		t.Fatal("Start returned a nil context")
	}
	if span == nil {
		t.Fatal("Start returned a nil span")
	}
	// None of these must panic on the no-op span.
	span.SetString("upstream.backend", "10.0.0.1:80")
	span.SetStatus(http.StatusBadGateway)
	span.RecordError(errors.New("boom"))
	span.End()

	// Inject on the no-op tracer must leave headers untouched.
	h := http.Header{}
	tr.Inject(ctx, h)
	if len(h) != 0 {
		t.Errorf("no-op Inject wrote headers: %v", h)
	}
}

type fakeTracer struct {
	started  []string
	injected int
}

func (f *fakeTracer) Start(ctx context.Context, name string) (context.Context, Span) {
	f.started = append(f.started, name)
	return ctx, &fakeSpan{}
}

func (f *fakeTracer) Inject(context.Context, http.Header) { f.injected++ }

type fakeSpan struct {
	ended   bool
	status  int
	err     error
	strings map[string]string
}

func (s *fakeSpan) End()                { s.ended = true }
func (s *fakeSpan) RecordError(e error) { s.err = e }
func (s *fakeSpan) SetStatus(c int)     { s.status = c }
func (s *fakeSpan) SetString(k, v string) {
	if s.strings == nil {
		s.strings = map[string]string{}
	}
	s.strings[k] = v
}

// TestSetActiveSwap verifies Set installs a tracer that Active delegates to, and
// that Set(nil) restores the no-op.
func TestSetActiveSwap(t *testing.T) {
	defer Set(nil)
	f := &fakeTracer{}
	Set(f)

	tr := Active()
	ctx, span := tr.Start(context.Background(), "proxy.roundtrip")
	span.SetString("upstream.backend", "10.0.0.1:80")
	span.SetStatus(http.StatusOK)
	span.End()
	tr.Inject(ctx, http.Header{})

	if len(f.started) != 1 || f.started[0] != "proxy.roundtrip" {
		t.Errorf("started = %v, want [proxy.roundtrip]", f.started)
	}
	if f.injected != 1 {
		t.Errorf("injected = %d, want 1", f.injected)
	}
	fs, ok := span.(*fakeSpan)
	if !ok {
		t.Fatalf("span type = %T, want *fakeSpan", span)
	}
	if !fs.ended {
		t.Error("span.End was not delegated")
	}
	if fs.status != http.StatusOK {
		t.Errorf("status = %d, want 200", fs.status)
	}
	if fs.strings["upstream.backend"] != "10.0.0.1:80" {
		t.Errorf("attribute not delegated: %v", fs.strings)
	}

	Set(nil)
	if _, ok := Active().(noop); !ok {
		t.Errorf("after Set(nil), Active = %T, want noop", Active())
	}
}
