// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
)

// TestNewCandidateClonesRaw (R8-13) verifies that NewCandidate stores a deep
// clone of the raw config, so a caller that mutates the original pointer after
// constructing the candidate cannot corrupt Candidate.Raw or Candidate.Effective.
func TestNewCandidateClonesRaw(t *testing.T) {
	raw := ProxyTarget(":3000", ":8080")
	raw.Global.WorkerThreads = "4"

	cand, err := NewCandidate(raw)
	if err != nil {
		t.Fatalf("NewCandidate failed: %v", err)
	}
	if cand == nil || cand.Raw == nil || cand.Effective == nil {
		t.Fatal("NewCandidate returned nil candidate or fields")
	}

	if cand.Raw.Global.WorkerThreads != "4" {
		t.Fatalf("candidate.Raw initial worker_threads = %q, want 4", cand.Raw.Global.WorkerThreads)
	}

	// Mutate the caller's original config.
	raw.Global.WorkerThreads = "8"
	raw.Servers[0].Listen = ":9090"

	if cand.Raw.Global.WorkerThreads != "4" {
		t.Errorf("candidate.Raw was aliased: worker_threads = %q after caller mutation", cand.Raw.Global.WorkerThreads)
	}
	if cand.Effective.Global.WorkerThreads != "4" {
		t.Errorf("candidate.Effective was aliased: worker_threads = %q after caller mutation", cand.Effective.Global.WorkerThreads)
	}
	if cand.Raw.Servers[0].Listen != ":8080" {
		t.Errorf("candidate.Raw server listen was aliased: %q", cand.Raw.Servers[0].Listen)
	}
}

// TestNewCandidateRawIndependentOfEffective (R8-13) verifies that Raw and
// Effective are independent clones; mutating one through the candidate does
// not affect the other. (Candidates are immutable by contract, but this test
// exercises the clone boundary.)
func TestNewCandidateRawIndependentOfEffective(t *testing.T) {
	raw := ProxyTarget(":8080", ":3000")
	cand, err := NewCandidate(raw)
	if err != nil {
		t.Fatalf("NewCandidate failed: %v", err)
	}

	if &cand.Raw.Global == &cand.Effective.Global {
		t.Fatal("candidate.Raw and candidate.Effective share a pointer")
	}
}

// TestNewCandidateRedactionCoversResolvedSecret (R8-13/R8-15) verifies that a
// file-backed secret consumed while building Effective is present in the
// candidate's redaction state.
func TestNewCandidateRedactionCoversResolvedSecret(t *testing.T) {
	secret := "candidate-secret-value"
	// Use an env reference so no filesystem setup is required.
	t.Setenv("CANDIDATE_SECRET", secret)
	raw := &Config{
		Global: GlobalConfig{WorkerThreads: "${env:CANDIDATE_SECRET}"},
		Servers: []ServerConfig{{
			Listen:    ":8080",
			Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
		}},
	}

	cand, err := NewCandidate(raw)
	if err != nil {
		t.Fatalf("NewCandidate failed: %v", err)
	}

	masked := cand.Redaction.Apply("value=" + secret)
	if !strings.Contains(masked, "***") {
		t.Errorf("candidate redaction did not mask resolved secret: %q", masked)
	}
}
