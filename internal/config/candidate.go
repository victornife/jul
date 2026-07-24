// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"context"

	"jul/internal/redact"
)

// Candidate is the single immutable configuration object for a startup,
// preflight, or reload transaction. It carries the raw (on-disk/admin-facing)
// configuration, the secret-expanded effective configuration, the redaction
// state covering every resolved value, and a digest map for file-backed secret
// rotation detection. Once created it is never mutated; consumers that need a
// different effective config build a new Candidate.
type Candidate struct {
	// Raw is the pre-expansion configuration loaded from the source. It
	// preserves ${env:...} and ${file:...} references for the on-disk view.
	Raw *Config

	// Effective is a deep clone of Raw with all secret references resolved.
	// This is the only config value that process-lifetime consumers and the
	// handler factory should use.
	Effective *Config

	// Redaction is the self-contained redaction state covering every secret
	// value consumed while building Effective.
	Redaction redact.State

	// Digests maps each secret reference string to a digest of the bytes
	// actually consumed, so file-content rotation can be detected even when
	// the configured path is unchanged.
	Digests map[string]string
}

// NewCandidate builds a Candidate from raw. It resolves secret references
// exactly once and returns an immutable view. The raw config is cloned so the
// candidate cannot be mutated through the caller's pointer.
//
// NewCandidate is the context.Background() wrapper around NewCandidateContext;
// managed apply paths that must be bounded by reload_timeout should call
// NewCandidateContext directly so secret resolution is cancellable.
func NewCandidate(raw *Config) (*Candidate, error) {
	return NewCandidateContext(context.Background(), raw)
}

// NewCandidateContext builds a Candidate from raw, resolving secret references
// under ctx so that a slow or blocked secret provider (e.g. a file read on a
// stalled mount) is bounded by the caller's deadline. When ctx is cancelled or
// its deadline expires before resolution completes, it returns ctx.Err()
// (context.DeadlineExceeded or context.Canceled) without persisting anything.
func NewCandidateContext(ctx context.Context, raw *Config) (*Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clone, err := raw.Clone()
	if err != nil {
		return nil, err
	}
	effective, state, digests, err := ResolveContext(ctx, clone)
	if err != nil {
		return nil, err
	}
	rawClone, err := raw.Clone()
	if err != nil {
		return nil, err
	}
	return &Candidate{
		Raw:       rawClone,
		Effective: effective,
		Redaction: state,
		Digests:   digests,
	}, nil
}
