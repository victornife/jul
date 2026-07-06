// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"sync/atomic"

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
	deps.RecentLogs = subsystems.LogTail.Snapshot
	deps.SubscribeLogs = subsystems.LogTail.Subscribe

	deps.PluginsCompiled = subsystems.PluginsCompiled
	deps.StreamCompiled = subsystems.StreamCompiled
	deps.WAFCompiled = subsystems.WAFCompiled

	deps.Upstreams = func() []admin.UpstreamStatus {
		return AdaptUpstreams(subsystems.PoolReg.Snapshot())
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
// decoupled view types so the admin package needs no upstream import.
func AdaptUpstreams(in []upstream.PoolStatus) []admin.UpstreamStatus {
	out := make([]admin.UpstreamStatus, 0, len(in))
	for _, p := range in {
		ps := admin.UpstreamStatus{Name: p.Name, Strategy: p.Strategy}
		for _, b := range p.Backends {
			ps.Backends = append(ps.Backends, admin.BackendStatus{
				Address:  b.Address,
				Weight:   b.Weight,
				Healthy:  b.Healthy,
				Inflight: b.Inflight,
			})
		}
		out = append(out, ps)
	}
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
