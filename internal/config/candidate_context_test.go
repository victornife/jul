// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestNewCandidateContextCancelledBeforeResolve verifies that a context already
// cancelled before resolution starts short-circuits without building a
// candidate, returning the context error. This is the pre-persistence
// cancellation contract for AC-08: a request cancelled before any secret
// provider is consulted aborts cleanly.
func TestNewCandidateContextCancelledBeforeResolve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	raw := &Config{}
	raw.Admin.Token = "${env:TEST_CANDIDATE_CTX_MISSING}"

	got, err := NewCandidateContext(ctx, raw)
	if got != nil {
		t.Fatalf("expected nil candidate on cancelled context, got %+v", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// TestResolveContextDeadlineExceeded verifies that a deadline that has already
// elapsed is reported as context.DeadlineExceeded rather than surfacing a
// provider error, and that no usable config is returned.
func TestResolveContextDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	raw := &Config{}
	raw.Admin.Token = "${file:/definitely/not/here}"

	cfg, _, _, err := ResolveContext(ctx, raw)
	if cfg != nil {
		t.Fatalf("expected nil config on expired deadline, got %+v", cfg)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context.DeadlineExceeded, got %v", err)
	}
}

// TestNewCandidateBackgroundStillResolves verifies that the context.Background()
// wrapper NewCandidate keeps resolving secrets eagerly for callers that do not
// need a deadline, preserving existing behaviour.
func TestNewCandidateBackgroundStillResolves(t *testing.T) {
	t.Setenv("TEST_CANDIDATE_CTX_TOKEN", "resolved-value")

	raw := &Config{}
	raw.Admin.Token = "${env:TEST_CANDIDATE_CTX_TOKEN}"

	cand, err := NewCandidate(raw)
	if err != nil {
		t.Fatalf("NewCandidate: %v", err)
	}
	if cand.Effective.Admin.Token != "resolved-value" {
		t.Fatalf("expected resolved token, got %q", cand.Effective.Admin.Token)
	}
	// Raw retains the unresolved reference.
	if raw.Admin.Token != "${env:TEST_CANDIDATE_CTX_TOKEN}" {
		t.Fatalf("NewCandidate mutated caller raw: %q", raw.Admin.Token)
	}
}
