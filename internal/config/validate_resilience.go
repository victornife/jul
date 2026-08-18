// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"strings"
	"time"

	"jul/internal/resilience"
)

// validateResilience checks one [upstreams.resilience] block. Range checks are
// delegated to resilience.Resolve so the accepted bounds live with the type
// that enforces them and cannot drift from what the data path actually accepts.
//
// grace is the global shutdown timeout, which is also the handler-generation
// retirement grace. pending_timeout must not exceed it: a request parked longer
// than the grace outlives the transport it was queued for.
func validateResilience(r *ResilienceConfig, where string, grace time.Duration) []error {
	if r == nil {
		return nil
	}
	var errs []error
	if _, err := resilience.Resolve(r.Options()); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", where, err))
	}
	if pt := r.PendingTimeout.Std(); pt > 0 && grace > 0 && pt > grace {
		errs = append(errs, fmt.Errorf("%s: pending_timeout (%s) must not exceed global.shutdown_timeout (%s), which bounds handler-generation retirement: a request parked longer than the grace outlives the transport it is queued for", where, pt, grace))
	}
	return errs
}

// validateLocationResilience checks a location's stateless resilience block.
//
// proxyRetries is the location's deprecated proxy_retries value. Setting both
// spellings is an error rather than a precedence rule: two names for one
// control that quietly disagree is how a configuration comes to mean something
// its author did not intend, and the migration is one line.
func validateLocationResilience(r *LocationResilienceConfig, proxyRetries int, where string) []error {
	if r == nil {
		return nil
	}
	var errs []error
	if _, err := resilience.Resolve(r.Options()); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", where, err))
	}
	if r.RetryAttempts > 0 && proxyRetries > 0 {
		errs = append(errs, fmt.Errorf("%s: retry_attempts and the deprecated proxy_retries are the same control and must not both be set; keep retry_attempts", where))
	}
	return errs
}

// validateStreamResilience rejects max_active_requests on a UDP-only stream
// route.
//
// UDP session admission already exists and is different in kind:
// max_udp_sessions is a per-listener cap with LRU eviction of an idle victim,
// because UDP has no client to park and no way to signal a rejection. Adding a
// pool-scoped concurrency limit on top would be the overlapping mechanism this
// programme rejects elsewhere, and the two would disagree about what a "session"
// is. The asymmetry is deliberate: the UDP cap is per listener, the TCP cap is
// per pool.
func validateStreamResilience(c *Config) []error {
	bounded := map[string]bool{}
	for _, up := range c.Upstreams {
		if up.Resilience != nil && up.Resilience.MaxActiveRequests > 0 {
			bounded[up.Name] = true
		}
	}
	if len(bounded) == 0 {
		return nil
	}

	// An upstream is UDP-only when every stream route naming it is UDP and no
	// HTTP location uses it: a pool shared with TCP or HTTP traffic keeps its
	// limit, which is what makes the rule about the route rather than the pool.
	var errs []error
	for i, st := range c.Streams {
		if strings.ToLower(strings.TrimSpace(st.Protocol)) != "udp" {
			continue
		}
		for _, target := range streamTargets(st) {
			if bounded[target] {
				errs = append(errs, fmt.Errorf("stream[%d]: upstream %q sets resilience.max_active_requests, which does not apply to a UDP route; use max_udp_sessions on the stream block, which bounds per-listener sessions with idle eviction", i, target))
			}
		}
	}
	return errs
}

// streamTargets lists the upstream references a stream block makes.
func streamTargets(st StreamServer) []string {
	targets := make([]string, 0, 1+len(st.SNIRoutes))
	if t := strings.TrimSpace(st.ProxyPass); t != "" {
		targets = append(targets, t)
	}
	for _, t := range st.SNIRoutes {
		if t = strings.TrimSpace(t); t != "" {
			targets = append(targets, t)
		}
	}
	return targets
}
