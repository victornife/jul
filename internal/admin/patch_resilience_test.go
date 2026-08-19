// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

func TestApplyPatchUpstreamSetResilience(t *testing.T) {
	c := patchTestConfig()
	probes := 3
	summary, err := applyPatch(c, patchRequest{
		Op:       "upstream_set_resilience",
		Upstream: "pool",
		Resilience: &upstreamResilience{
			MaxFails:              5,
			FailTimeout:           "45s",
			MaxActiveRequests:     100,
			MaxPendingRequests:    20,
			PendingTimeout:        "2s",
			RetryAttempts:         2,
			RetryBudgetPercent:    10,
			CircuitHalfOpenProbes: &probes,
		},
	})
	if err != nil {
		t.Fatalf("set resilience: %v", err)
	}
	r := c.Upstreams[0].Resilience
	if r == nil {
		t.Fatal("resilience block not applied")
	}
	if r.MaxFails != 5 || r.FailTimeout.Std().String() != "45s" {
		t.Errorf("circuit limits = %d/%v", r.MaxFails, r.FailTimeout.Std())
	}
	if r.MaxActiveRequests != 100 || r.MaxPendingRequests != 20 {
		t.Errorf("admission limits = %d/%d", r.MaxActiveRequests, r.MaxPendingRequests)
	}
	if r.CircuitHalfOpenProbes == nil || *r.CircuitHalfOpenProbes != 3 {
		t.Errorf("half-open probes = %v", r.CircuitHalfOpenProbes)
	}
	// The audit line has to name what changed; "resilience updated" tells an
	// operator reading the log nothing about what they are looking at.
	for _, want := range []string{"max_fails=5", "fail_timeout=45s", "retry_budget_percent=10"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q: %s", want, summary)
		}
	}
}

// An explicit 0 is the documented way to ask for the old unbounded half-open
// behaviour. It must survive the round trip as a set value, not be dropped as
// an absent one — the pointer in both the payload and the config exists for
// exactly this distinction.
func TestApplyPatchResilienceKeepsAnExplicitZeroProbeCount(t *testing.T) {
	c := patchTestConfig()
	zero := 0
	if _, err := applyPatch(c, patchRequest{
		Op:         "upstream_set_resilience",
		Upstream:   "pool",
		Resilience: &upstreamResilience{CircuitHalfOpenProbes: &zero},
	}); err != nil {
		t.Fatalf("set resilience: %v", err)
	}
	got := c.Upstreams[0].Resilience.CircuitHalfOpenProbes
	if got == nil {
		t.Fatal("explicit 0 was dropped; it reads as the default 1 instead of unbounded")
	}
	if *got != 0 {
		t.Errorf("half-open probes = %d, want 0", *got)
	}
}

// Omitting the payload removes the block, mirroring how a disabled health-check
// payload removes that one.
func TestApplyPatchResilienceWithNoPayloadClearsTheBlock(t *testing.T) {
	c := patchTestConfig()
	c.Upstreams[0].Resilience = &config.ResilienceConfig{MaxFails: 9}
	summary, err := applyPatch(c, patchRequest{Op: "upstream_set_resilience", Upstream: "pool"})
	if err != nil {
		t.Fatalf("clear resilience: %v", err)
	}
	if c.Upstreams[0].Resilience != nil {
		t.Error("resilience block was not cleared")
	}
	if !strings.Contains(summary, "cleared") {
		t.Errorf("summary = %q, want it to say cleared", summary)
	}
}

// max_fails and fail_timeout have a deprecated top-level spelling, and a config
// carrying both is rejected by validation. Leaving the old keys in place would
// make this operation produce a configuration that cannot be loaded, so it
// migrates them — and says so, rather than dropping configuration silently.
func TestApplyPatchResilienceMigratesTheDeprecatedSpelling(t *testing.T) {
	c := patchTestConfig()
	c.Upstreams[0].MaxFails = 3
	c.Upstreams[0].FailTimeout = config.Duration(10)

	summary, err := applyPatch(c, patchRequest{
		Op:         "upstream_set_resilience",
		Upstream:   "pool",
		Resilience: &upstreamResilience{MaxFails: 5, FailTimeout: "45s"},
	})
	if err != nil {
		t.Fatalf("set resilience: %v", err)
	}
	up := c.Upstreams[0]
	if up.MaxFails != 0 || up.FailTimeout != 0 {
		t.Errorf("deprecated keys survived: max_fails=%d fail_timeout=%v", up.MaxFails, up.FailTimeout)
	}
	if !strings.Contains(summary, "moved max_fails") || !strings.Contains(summary, "fail_timeout") {
		t.Errorf("the migration was not reported: %s", summary)
	}
	// The both-spellings conflict is the thing the migration exists to avoid, so
	// that specific rejection must be gone. The fixture has unrelated validation
	// errors of its own, which asserting on Validate as a whole would pick up.
	if err := config.Validate(c); err != nil && strings.Contains(err.Error(), "max_fails is set both") {
		t.Errorf("the both-spellings conflict survived the migration: %v", err)
	}
}

// A pool that never used the deprecated spelling must not have its summary
// cluttered with a migration that did not happen.
func TestApplyPatchResilienceReportsNoMigrationWhenThereIsNone(t *testing.T) {
	c := patchTestConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:         "upstream_set_resilience",
		Upstream:   "pool",
		Resilience: &upstreamResilience{MaxFails: 5},
	})
	if err != nil {
		t.Fatalf("set resilience: %v", err)
	}
	if strings.Contains(summary, "moved") {
		t.Errorf("reported a migration that did not happen: %s", summary)
	}
}

// The bounds are the runtime's. This op must not be a way around them: a value
// the config file would reject cannot be smuggled in through a patch.
func TestApplyPatchResilienceRejectsWhatTheConfigWouldReject(t *testing.T) {
	cases := []struct {
		name string
		in   upstreamResilience
	}{
		{"pending queue with no admission limit", upstreamResilience{MaxPendingRequests: 10}},
		{"backoff max with no initial", upstreamResilience{RetryBackoffMax: "5s"}},
		{"initial backoff above the max", upstreamResilience{RetryBackoffInitial: "10s", RetryBackoffMax: "1s"}},
		{"retry budget above the ceiling", upstreamResilience{RetryBudgetPercent: 1001}},
		{"negative max_fails", upstreamResilience{MaxFails: -1}},
		{"unparseable duration", upstreamResilience{FailTimeout: "soon"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := patchTestConfig()
			if _, err := applyPatch(c, patchRequest{Op: "upstream_set_resilience", Upstream: "pool", Resilience: &tc.in}); err == nil {
				t.Error("accepted a value the configuration file would reject")
			}
			if c.Upstreams[0].Resilience != nil {
				t.Error("a rejected patch still mutated the config")
			}
		})
	}
}

func TestApplyPatchResilienceRejectsAnUnknownUpstream(t *testing.T) {
	c := patchTestConfig()
	if _, err := applyPatch(c, patchRequest{Op: "upstream_set_resilience", Upstream: "nope", Resilience: &upstreamResilience{MaxFails: 1}}); err == nil {
		t.Error("accepted an unknown upstream")
	}
}

// A change nobody can see in the preview is a change an operator applies blind.
//
// Left to the registry completeness pass this produced a row for every
// resilience path whether or not it changed, with empty before/after values and
// the schema path as the name: thirteen rows saying nothing for a four-field
// change.
func TestResilienceChangesAppearInTheDiff(t *testing.T) {
	before := patchTestConfig()
	after := patchTestConfig()
	if _, err := applyPatch(after, patchRequest{
		Op:         "upstream_set_resilience",
		Upstream:   "pool",
		Resilience: &upstreamResilience{MaxActiveRequests: 100, RetryAttempts: 2},
	}); err != nil {
		t.Fatalf("set resilience: %v", err)
	}
	d := diffConfigs(before, after)
	rows := map[string]DiffEntry{}
	for _, e := range append(append([]DiffEntry{}, d.Additions...), d.Modifications...) {
		if e.Kind == "resilience" {
			rows[e.Detail] = e
		}
	}
	if len(rows) != 2 {
		t.Fatalf("got %d resilience rows for a 2-field change: %+v", len(rows), rows)
	}
	for _, want := range []string{"Change max_active_requests of pool", "Change retry_attempts of pool"} {
		e, ok := rows[want]
		if !ok {
			t.Errorf("missing row %q", want)
			continue
		}
		if e.Name != "pool" {
			t.Errorf("row %q names %q, not the pool", want, e.Name)
		}
		if e.After == "" {
			t.Errorf("row %q carries no new value, so the preview shows nothing", want)
		}
	}
}

// An explicit 0 asks for unbounded half-open probing. It must not render the
// same as an omitted key, and it is worth a warning: it is the behaviour the
// circuit breaker was introduced to remove.
func TestDiffDistinguishesAnExplicitZeroProbeCount(t *testing.T) {
	before := patchTestConfig()
	after := patchTestConfig()
	zero := 0
	if _, err := applyPatch(after, patchRequest{
		Op:         "upstream_set_resilience",
		Upstream:   "pool",
		Resilience: &upstreamResilience{CircuitHalfOpenProbes: &zero},
	}); err != nil {
		t.Fatalf("set resilience: %v", err)
	}
	d := diffConfigs(before, after)
	found := false
	for _, e := range append(append([]DiffEntry{}, d.Additions...), d.Modifications...) {
		if strings.Contains(e.Detail, "circuit_half_open_probes") {
			found = true
			if e.Before == e.After {
				t.Errorf("an explicit 0 renders identically to the default: before=%q after=%q", e.Before, e.After)
			}
		}
	}
	if !found {
		t.Error("setting circuit_half_open_probes produced no diff row")
	}
	if len(d.Warnings) == 0 {
		t.Error("unbounded half-open probing was applied with no warning")
	}
}
