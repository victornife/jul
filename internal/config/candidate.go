// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"jul/internal/redact"
)

// LifecycleChangeSet mirrors lifecycle.ChangeSet without forcing config to
// import lifecycle. Preflight.Apply assigns this slice to Candidate.Lifecycle
// so the coordinator can build the pending-restart marker from the same
// resolved candidate used for validation (M-02).
type LifecycleChangeSet = []LifecycleDiffEntry

// LifecycleDiffEntry mirrors lifecycle.DiffEntry. It describes one
// configuration field whose effective value changed between two resolved
// configs, according to the lifecycle registry.
type LifecycleDiffEntry struct {
	Path      string
	Class     string
	Subsystem string
	Reason    string
	Before    any
	After     any
}

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

	// Lifecycle is an optional change-set attached by Preflight.Apply in
	// stage-restart mode. It lets the coordinator build the pending-restart
	// marker from the same resolved candidate used for validation, avoiding a
	// second resolution (M-02). The concrete lifecycle types live in
	// internal/lifecycle; config defines local mirrors to avoid an import cycle.
	Lifecycle LifecycleChangeSet
}

// NewCandidate builds a Candidate from raw. It resolves secret references
// exactly once and returns an immutable view. The raw config is cloned so the
// candidate cannot be mutated through the caller's pointer.
func NewCandidate(raw *Config) (*Candidate, error) {
	clone, err := raw.Clone()
	if err != nil {
		return nil, err
	}
	effective, state, digests, err := Resolve(clone)
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
