// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// PoolResilience is the runtime resilience view of one upstream pool.
//
// It is the surface the per-backend metric label was withdrawn in favour of.
// An address costs one JSON field here, queried on demand; the same address as
// a Prometheus label cost a time series per pod for the life of the process.
type PoolResilience struct {
	Name   string `json:"name"`
	Scheme string `json:"scheme,omitempty"`

	// Verdict is the pool rolled up to one word, computed here so the Console
	// and any API client cannot disagree during an incident.
	Verdict string `json:"verdict"`

	Limits ResilienceLimits `json:"limits"`

	Active      int64 `json:"active"`
	Pending     int64 `json:"pending"`
	Connections int64 `json:"connections"`
	Eligible    int   `json:"eligible"`

	// ByState always carries every state, including zeroes. A missing key reads
	// as "no data" to a consumer rather than "none in that state".
	ByState map[string]int `json:"by_state"`

	Backends []BackendResilience `json:"backends"`
	Budget   RetryBudgetStatus   `json:"retry_budget"`
}

// ResilienceLimits are the bounds in force right now, which is not necessarily
// what the file on disk says: a reload or a patch applied by someone else
// changes these and nothing else tells the operator.
//
// Each limit is paired with the configuration key that supplied it, because
// `max_fails` can be written in two places and "the value is 3" does not tell
// an operator which of the two spellings is winning — the question they
// actually have when a change they made appears to have done nothing.
type ResilienceLimits struct {
	MaxActiveRequests        int64  `json:"max_active_requests"`
	MaxActivePerBackend      int64  `json:"max_active_per_backend"`
	MaxPendingRequests       int    `json:"max_pending_requests"`
	PendingTimeout           string `json:"pending_timeout,omitempty"`
	MaxConnectionsPerBackend int    `json:"max_connections_per_backend"`

	RetryAttempts       int    `json:"retry_attempts"`
	RetryDeadline       string `json:"retry_deadline,omitempty"`
	RetryBackoffInitial string `json:"retry_backoff_initial,omitempty"`
	RetryBackoffMax     string `json:"retry_backoff_max,omitempty"`
	RetryBudgetPercent  int    `json:"retry_budget_percent"`

	CircuitMaxFails       int    `json:"circuit_max_fails"`
	CircuitFailTimeout    string `json:"circuit_fail_timeout,omitempty"`
	CircuitHalfOpenProbes int    `json:"circuit_half_open_probes"`

	// Sources maps a limit's JSON name to where its value came from:
	// "resilience" (the [upstreams.resilience] block), "upstream" (the
	// deprecated top-level spelling) or "default".
	Sources map[string]string `json:"sources,omitempty"`
}

// BackendResilience is one backend's live state.
type BackendResilience struct {
	Address  string `json:"address"`
	Weight   int    `json:"weight"`
	Inflight int64  `json:"inflight"`
	State    string `json:"state"`

	// Fails is the consecutive-failure count against circuit_max_fails. It is
	// what distinguishes a backend about to trip from one merely unlucky.
	Fails int `json:"fails"`
	// OpenUntil is when the cooldown ends, RFC 3339. Absent unless the circuit
	// is open.
	OpenUntil string `json:"open_until,omitempty"`
	// ProbesRemaining is how many more half-open probes may be admitted.
	// Absent unless the circuit is half-open.
	ProbesRemaining int `json:"probes_remaining,omitempty"`
}

// RetryBudgetStatus is the pool's retry allowance over its trailing window.
type RetryBudgetStatus struct {
	Percent   int   `json:"percent"`
	Primaries int64 `json:"primaries"`
	Retries   int64 `json:"retries"`
	// Remaining can be zero while Percent is not, which is the whole point of
	// the budget and exactly what an operator needs to see when retries are
	// being denied.
	Remaining int64 `json:"remaining"`
}
