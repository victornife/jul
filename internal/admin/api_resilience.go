// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strings"

	"jul/internal/config"
)

// upstreamNameFromPath extracts and bounds the {name} path parameter.
func upstreamNameFromPath(r *http.Request) (string, bool) {
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" || len(name) > 255 || strings.ContainsAny(name, "/\\ \t\r\n") {
		return "", false
	}
	return name, true
}

func (s *Server) handleUpstreamResilience(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	name, ok := upstreamNameFromPath(r)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "upstream name is required"})
		return
	}
	if s.deps.UpstreamResilience == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "upstream resilience is not available"})
		return
	}
	pools := s.deps.UpstreamResilience(name)
	if len(pools) == 0 {
		// A name with no live pool is a 404, which is not the same answer as a
		// live pool with every backend down. Collapsing the two would tell an
		// operator their pool had vanished during an outage.
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no live upstream named " + name})
		return
	}
	for i := range pools {
		pools[i].Verdict = poolVerdictFromStates(pools[i].Backends)
	}
	if s.deps.LoadConfig != nil {
		if cfg, err := s.deps.LoadConfig(); err == nil {
			if up, err := findUpstream(cfg, name); err == nil {
				for i := range pools {
					pools[i].Limits.Sources = circuitLimitSources(up)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, pools)
}

// poolVerdictFromStates rolls backends up the same way poolVerdict does for the
// apps projection. Both call the same classifier so the two endpoints cannot
// report different verdicts for the same pool.
func poolVerdictFromStates(backends []BackendResilience) string {
	bp := make([]BackendProjection, 0, len(backends))
	for _, b := range backends {
		bp = append(bp, BackendProjection{Address: b.Address, State: b.State})
	}
	return poolVerdict(bp)
}

// circuitLimitSources reports which configuration key supplied each circuit
// limit. Only the circuit limits have two spellings, so only they are ambiguous
// enough to be worth reporting.
func circuitLimitSources(up *config.UpstreamConfig) map[string]string {
	res := up.Resilience
	out := map[string]string{
		"circuit_max_fails":        "default",
		"circuit_fail_timeout":     "default",
		"circuit_half_open_probes": "default",
	}
	switch {
	case res != nil && res.MaxFails > 0:
		out["circuit_max_fails"] = "resilience"
	case up.MaxFails > 0:
		out["circuit_max_fails"] = "upstream"
	}
	switch {
	case res != nil && res.FailTimeout > 0:
		out["circuit_fail_timeout"] = "resilience"
	case up.FailTimeout > 0:
		out["circuit_fail_timeout"] = "upstream"
	}
	if res != nil && res.CircuitHalfOpenProbes != nil {
		out["circuit_half_open_probes"] = "resilience"
	}
	return out
}
