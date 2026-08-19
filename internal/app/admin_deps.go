// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"sort"
	"sync/atomic"
	"time"

	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/server"
	"jul/internal/upstream"
)

// Subsystems carries the pointer-sized runtime components initialized in serve()
// that are needed to build admin.Deps.  It is a pure data carrier — no lifecycle
// ownership, no initialization order semantics.
type Subsystems struct {
	ResponseCache    *cache.Cache
	Metrics          *observability.Metrics
	PoolReg          *upstream.Registry
	LogTail          *observability.LogTail
	PluginsCompiled  bool
	StreamCompiled   bool
	WAFCompiled      bool
	LastStreamReload *atomic.Pointer[string]
}

// BuildAdminDeps wires an admin.Deps from the subsystems and configuration.
// It builds the universally-wired fields; file-dependent fields (ReadConfigRaw,
// WriteConfigRaw, SaveConfig) and lifecycle hooks (Reload, Ready) are set by
// the caller because they depend on local channels and closures.
func BuildAdminDeps(productName, version string, src config.Source, subsystems Subsystems) admin.Deps {
	deps := admin.Deps{
		Product:    productName,
		Version:    version,
		ConfigPath: src.Name(),
		Metrics:    subsystems.Metrics.Handler(),
		Stats:      subsystems.Metrics.Snapshot,
		Cache:      AdminCache(subsystems.ResponseCache),
	}

	deps.TrafficSources = subsystems.Metrics.TrafficSnapshot
	deps.RequestSamples = subsystems.Metrics.RequestSamples
	deps.FailingRoutes = subsystems.Metrics.FailingRoutes
	deps.UpstreamHealthHistory = subsystems.Metrics.UpstreamHealthHistory
	deps.CertRenewalHistory = subsystems.Metrics.CertRenewalHistory
	deps.ObserveManagedApplyLookup = subsystems.Metrics.ObserveManagedApplyLookup
	deps.EgressBlocked = subsystems.Metrics.EgressBlocked
	deps.RecentLogs = subsystems.LogTail.Snapshot
	deps.SubscribeLogs = subsystems.LogTail.Subscribe

	deps.PluginsCompiled = subsystems.PluginsCompiled
	deps.StreamCompiled = subsystems.StreamCompiled
	deps.WAFCompiled = subsystems.WAFCompiled

	deps.Upstreams = func() []admin.UpstreamStatus {
		return AdaptUpstreams(subsystems.PoolReg.Snapshot())
	}
	deps.UpstreamResilience = func(name string) []admin.PoolResilience {
		return AdaptResilience(subsystems.PoolReg.Resilience(name))
	}
	deps.Certs = func() []admin.CertStatus {
		c, err := src.Load()
		if err != nil {
			return nil
		}
		return AdaptCerts(server.InspectCerts(c.Servers))
	}
	deps.StreamStatus = func() string {
		if p := subsystems.LastStreamReload.Load(); p != nil {
			return *p
		}
		return ""
	}

	return deps
}

// AdminCache adapts the response cache to the admin Purger interface, returning
// a nil interface (not a typed nil) when caching is disabled so the admin
// server can detect the absence reliably.
func AdminCache(c *cache.Cache) admin.Purger {
	if c == nil {
		return nil
	}
	return c
}

// AdaptUpstreams maps the upstream registry snapshot onto the admin console's
// decoupled view types so the admin package needs no upstream import. Pools
// that differ only by scheme are merged under the upstream name so the console
// shows one logical app with the combined backends.
func AdaptUpstreams(in []upstream.PoolStatus) []admin.UpstreamStatus {
	byName := make(map[string]admin.UpstreamStatus, len(in))
	for _, p := range in {
		ps, ok := byName[p.Name]
		if !ok {
			ps = admin.UpstreamStatus{Name: p.Name, Strategy: p.Strategy}
		}
		for _, b := range p.Backends {
			ps.Backends = append(ps.Backends, admin.BackendStatus{
				Address:  b.Address,
				Weight:   b.Weight,
				State:    string(b.State),
				Inflight: b.Inflight,
			})
		}
		byName[p.Name] = ps
	}
	out := make([]admin.UpstreamStatus, 0, len(byName))
	for _, ps := range byName {
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// AdaptCerts maps the server certificate summaries onto the admin console's view
// types. It carries no key material.
func AdaptCerts(in []server.CertSummary) []admin.CertStatus {
	out := make([]admin.CertStatus, 0, len(in))
	for _, c := range in {
		out = append(out, admin.CertStatus{
			ServerNames: c.ServerNames,
			Source:      c.Source,
			Subject:     c.Subject,
			Issuer:      c.Issuer,
			DNSNames:    c.DNSNames,
			NotBefore:   c.NotBefore,
			NotAfter:    c.NotAfter,
			Error:       c.Error,
		})
	}
	return out
}

// AdaptResilience maps the runtime's resilience view onto the admin package's
// decoupled types. Durations become strings and states become plain strings for
// the same reason as AdaptUpstreams: internal/admin must not import the runtime.
func AdaptResilience(in []upstream.PoolResilience) []admin.PoolResilience {
	if len(in) == 0 {
		return nil
	}
	out := make([]admin.PoolResilience, 0, len(in))
	for _, p := range in {
		byState := make(map[string]int, len(p.ByState))
		for st, n := range p.ByState {
			byState[string(st)] = n
		}
		ap := admin.PoolResilience{
			Name:        p.Name,
			Scheme:      p.Scheme,
			Active:      p.Active,
			Pending:     p.Pending,
			Connections: p.Connections,
			Eligible:    p.Eligible,
			ByState:     byState,
			Limits: admin.ResilienceLimits{
				MaxActiveRequests:        p.Limits.MaxActiveRequests,
				MaxActivePerBackend:      p.Limits.MaxActivePerBackend,
				MaxPendingRequests:       p.Limits.MaxPendingRequests,
				PendingTimeout:           durationString(p.Limits.PendingTimeout),
				MaxConnectionsPerBackend: p.Limits.MaxConnectionsPerBackend,
				RetryAttempts:            p.Limits.RetryAttempts,
				RetryDeadline:            durationString(p.Limits.RetryDeadline),
				RetryBackoffInitial:      durationString(p.Limits.RetryBackoffInitial),
				RetryBackoffMax:          durationString(p.Limits.RetryBackoffMax),
				RetryBudgetPercent:       p.Limits.RetryBudgetPercent,
				CircuitMaxFails:          p.Limits.CircuitMaxFails,
				CircuitFailTimeout:       durationString(p.Limits.CircuitFailTimeout),
				CircuitHalfOpenProbes:    p.Limits.CircuitHalfOpenProbes,
			},
			Budget: admin.RetryBudgetStatus{
				Percent:   p.Budget.Percent,
				Primaries: p.Budget.Primaries,
				Retries:   p.Budget.Retries,
				Remaining: p.Budget.Remaining,
			},
		}
		for _, b := range p.Backends {
			ab := admin.BackendResilience{
				Address:         b.Address,
				Weight:          b.Weight,
				Inflight:        b.Inflight,
				State:           string(b.State),
				Fails:           b.Fails,
				ProbesRemaining: b.ProbesRemaining,
			}
			if !b.OpenUntil.IsZero() {
				ab.OpenUntil = b.OpenUntil.UTC().Format(time.RFC3339)
			}
			ap.Backends = append(ap.Backends, ab)
		}
		out = append(out, ap)
	}
	return out
}

// durationString renders a limit for the API. Zero means unset throughout the
// resilience configuration, and an explicit "0s" would read as a deliberate
// zero rather than an absent key.
func durationString(d time.Duration) string {
	if d == 0 {
		return ""
	}
	return d.String()
}
