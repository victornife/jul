// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import "fmt"

// lintResilience reports admission-sizing findings that are legal but almost
// certainly not what the operator meant.
//
// Both are warnings rather than errors: each configuration is coherent and
// enforceable, and each has a legitimate — if unusual — reading. What they
// cannot be is silent, because the symptom in both cases is rejected traffic
// with an idle queue, which looks like a bug in Jul rather than a sizing choice.
func lintResilience(c *Config) []Diagnostic {
	var diags []Diagnostic
	for i, up := range c.Upstreams {
		r := up.Resilience
		if r == nil {
			continue
		}
		where := fmt.Sprintf("upstreams[%d].resilience", i)

		// The per-backend limit is fail-fast by design: a saturated backend is
		// filtered out of selection rather than queued behind. If the per-backend
		// capacity cannot add up to the pool limit, the pool limit is unreachable
		// and requests are rejected with backend_at_capacity while the pending
		// queue sits empty.
		//
		// Only static server lists are checked. Under discovery the backend count
		// is a runtime property, so the check is necessarily soft and the metric,
		// not the configuration, is the authority.
		if r.MaxActivePerBackend > 0 && r.MaxActiveRequests > 0 && !discoveryEnabled(up.Discovery) {
			if capacity := r.MaxActivePerBackend * len(up.Servers); capacity < r.MaxActiveRequests {
				diags = append(diags, Diagnostic{
					Severity: SeverityWarning,
					Field:    where + ".max_active_per_backend",
					Message:  fmt.Sprintf("%d backends x %d = %d, below max_active_requests = %d, so the pool limit is unreachable and requests are rejected while the pending queue is empty", len(up.Servers), r.MaxActivePerBackend, capacity, r.MaxActiveRequests),
					Hint:     fmt.Sprintf("raise max_active_per_backend to at least %d, lower max_active_requests to %d, or add backends", (r.MaxActiveRequests+len(up.Servers)-1)/max(len(up.Servers), 1), capacity),
				})
			}
		}

		// A queue with no timeout is bounded only by each request's own context.
		// Clients that set none (or a very long one) then occupy queue slots for
		// as long as they are willing to wait, which is the shape that turns a
		// brief overload into a persistent one.
		if r.MaxPendingRequests > 0 && r.PendingTimeout.Std() == 0 {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    where + ".pending_timeout",
				Message:  "a pending queue with no timeout is bounded only by each client's own request context, so slow clients can hold queue slots indefinitely",
				Hint:     "set pending_timeout to the longest wait that is still useful to a client, typically well under global.shutdown_timeout",
			})
		}

		// A socket bound cannot bound concurrency where one HTTP/2 connection
		// carries every stream, so on a gRPC or transcoding route the setting
		// does nothing at all. Silence would leave an operator believing a limit
		// is in force.
		if r.MaxConnectionsPerBackend > 0 && multiplexedConsumers(c, up.Name) {
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    where + ".max_connections_per_backend",
				Message:  fmt.Sprintf("upstream %q is used by a native gRPC or gRPC-transcoding route, where one HTTP/2 connection carries every stream, so a socket bound does not limit concurrency there", up.Name),
				Hint:     "use max_active_requests to bound concurrency on multiplexed routes; keep max_connections_per_backend for HTTP/1.1 traffic",
			})
		}
	}

	for i, srv := range c.Servers {
		for j, loc := range srv.Locations {
			if loc.Resilience == nil || loc.Resilience.MaxConnectionsPerBackend == 0 {
				continue
			}
			if !multiplexedLocation(loc) {
				continue
			}
			diags = append(diags, Diagnostic{
				Severity: SeverityWarning,
				Field:    fmt.Sprintf("servers[%d].locations[%d].resilience.max_connections_per_backend", i, j),
				Message:  "this route is native gRPC or gRPC transcoding, where one HTTP/2 connection carries every stream, so a socket bound does not limit its concurrency",
				Hint:     "use the pool's max_active_requests to bound concurrency on a multiplexed route",
			})
		}
	}
	return diags
}

// multiplexedLocation reports whether a route reaches its backend over a single
// multiplexed HTTP/2 connection.
func multiplexedLocation(loc LocationConfig) bool {
	return loc.GRPC || loc.GRPCTranscode != nil
}

// multiplexedConsumers reports whether any multiplexed route targets the named
// upstream.
func multiplexedConsumers(c *Config, name string) bool {
	for _, srv := range c.Servers {
		for _, loc := range srv.Locations {
			if !multiplexedLocation(loc) {
				continue
			}
			if loc.GRPCTranscode != nil && loc.GRPCTranscode.Target == name {
				return true
			}
			if loc.GRPC {
				if ref, ok := upstreamRefOf(loc.ProxyPass); ok && ref == name {
					return true
				}
			}
		}
	}
	return false
}
