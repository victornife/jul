// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"time"

	"jul/internal/config"
)

// DialFunc matches net.Dialer.DialContext. When non-nil it guards the outbound
// connections of the HTTP-based discoverers (Consul, Kubernetes) against the
// [egress] allow-list. It is an alias so a value from internal/egress passes
// through without this package importing it.
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// discoveryTimeout bounds a single resolve call so a hung provider cannot stall
// the refresher.
const discoveryTimeout = 5 * time.Second

// Target is a discovered backend: an address ("host:port") with an optional
// weight (0 means the default weight of 1).
type Target struct {
	Address string
	Weight  int
	// ID is the provider's own identity for this backend — a Kubernetes pod
	// UID or a Consul ServiceID. It is empty for DNS, DNS SRV and static
	// servers, which have nothing to offer beyond the address.
	//
	// It exists because an address is not an identity. Kubernetes reuses pod IPs
	// within seconds, so without it a replacement pod inherits the dead one's
	// failure history and arrives already partway to being taken out of
	// rotation — for failures it never caused.
	ID string
}

// Discoverer resolves the current backend set for a pool from an external
// source (DNS, Consul, Kubernetes). Resolve is called from a single per-pool
// goroutine, so implementations need not be safe for concurrent use.
type Discoverer interface {
	// Resolve returns the current backend targets. A non-nil error (or an empty
	// result) keeps the pool's last-good backend set in place.
	Resolve(ctx context.Context) ([]Target, error)
	// Describe returns a short identifier for logs and diagnostics.
	Describe() string
}

// DiscoveryHooks observe a pool's discovery refresher for metrics. Either field
// may be nil.
type DiscoveryHooks struct {
	// OnBackends is called with the new backend count after a successful resolve.
	OnBackends func(pool string, n int)
	// OnError is called after a failed or empty resolve.
	OnError func(pool string)
}

// loggingDiscoverer lets a Discoverer accept a logger for detailed diagnostics.
// It is optional; only the Kubernetes discoverer implements it today.
type loggingDiscoverer interface {
	SetLogger(*slog.Logger)
}

// discoveryEnabled reports whether a discovery config selects a dynamic provider
// (as opposed to the static Servers list).
func discoveryEnabled(d *config.DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "", "static":
		return false
	default:
		return true
	}
}

// discoveryUsesEgress reports whether the provider owns an HTTP client guarded
// by Boundary C. DNS and DNS-SRV use the system resolver and must not churn
// merely because the auxiliary HTTP egress policy changed.
func discoveryUsesEgress(d *config.DiscoveryConfig) bool {
	if d == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(d.Type)) {
	case "consul", "kubernetes":
		return true
	default:
		return false
	}
}

// newDiscoverer builds the Discoverer for a discovery config. The "consul" and
// "kubernetes" providers are compiled only into builds with the matching build
// tag; other builds return a clear error here, failing the startup or reload
// that referenced them — the same model as other gated features. A non-nil dial
// guards the Consul/Kubernetes HTTP clients with the egress allow-list.
func newDiscoverer(cfg config.DiscoveryConfig, dial DialFunc) (Discoverer, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "dns":
		return newDNSDiscoverer(cfg)
	case "dns_srv":
		return newDNSSRVDiscoverer(cfg)
	case "consul":
		return newConsulDiscoverer(cfg, dial)
	case "kubernetes":
		return newKubernetesDiscoverer(cfg, dial)
	default:
		return nil, fmt.Errorf("unknown discovery type %q", cfg.Type)
	}
}

// StartDiscovery installs one discovery-worker generation. Installing a new
// generation cancels and fences the previous one without closing the backend
// pool. That distinction is required by #94: policy B must stop policy-A
// Consul/Kubernetes refreshes at Publish while old handler work may still use
// the same pool until it drains. A stale resolve result is ignored even if its
// provider returns after cancellation.
func (p *Pool) StartDiscovery(d Discoverer, refresh time.Duration, hooks DiscoveryHooks, log *slog.Logger) {
	if refresh <= 0 {
		refresh = 30 * time.Second
	}
	if log != nil {
		log.Warn("starting discovery refresher", "upstream", p.name, "discoverer", d.Describe(), "refresh", refresh.String())
	}
	if ld, ok := d.(loggingDiscoverer); ok && log != nil {
		ld.SetLogger(log)
	}
	workerCtx, epoch := p.beginDiscoveryGeneration()
	go func() {
		defer closeDiscoverer(d)
		p.refreshOnce(workerCtx, epoch, d, hooks, log)
		timer := time.NewTimer(jitter(refresh))
		defer timer.Stop()
		for {
			select {
			case <-workerCtx.Done():
				if log != nil {
					log.Warn("stopping discovery refresher", "upstream", p.name)
				}
				return
			case <-p.Done():
				return
			case <-timer.C:
				p.refreshOnce(workerCtx, epoch, d, hooks, log)
				timer.Reset(refresh)
			}
		}
	}()
}

// refreshOnce performs a single resolve and applies it via UpdateBackends, which
// preserves the runtime state (in-flight count, passive cooldown) of surviving
// backends. Errors and empty results are logged and skip the update (keep
// last-good) so transient provider issues do not drop all backends at once.
func (p *Pool) refreshOnce(workerCtx context.Context, epoch uint64, d Discoverer, hooks DiscoveryHooks, log *slog.Logger) {
	if log != nil {
		log.Warn("discovery refresh starting", "upstream", p.name, "discoverer", d.Describe())
	}
	ctx, cancel := context.WithTimeout(workerCtx, discoveryTimeout)
	defer cancel()

	targets, err := d.Resolve(ctx)
	// A policy-generation change may cancel an in-flight provider request. Even
	// if the provider ignores cancellation and returns later, its A-generation
	// result must never overwrite the B-generation backend view.
	if !p.discoveryGenerationCurrent(epoch) {
		return
	}
	if log != nil {
		log.Warn("discovery refresh completed", "upstream", p.name, "targets", len(targets), "error", err)
	}
	if err != nil {
		if hooks.OnError != nil {
			hooks.OnError(p.name)
		}
		if log != nil {
			log.Warn("discovery resolve failed; keeping last-good backends",
				"upstream", p.name, "discoverer", d.Describe(), "error", err)
		}
		return
	}
	if len(targets) == 0 {
		if hooks.OnError != nil {
			hooks.OnError(p.name)
		}
		if log != nil {
			log.Warn("discovery returned no targets; keeping last-good backends",
				"upstream", p.name, "discoverer", d.Describe())
		}
		return
	}

	if !p.applyDiscoveryTargets(epoch, targets) {
		return
	}
	if hooks.OnBackends != nil {
		hooks.OnBackends(p.name, len(targets))
	}
}

// targetsToServers converts discovered targets to upstream server configs,
// normalizing weights to at least 1.
func closeDiscoverer(d Discoverer) {
	if closer, ok := d.(io.Closer); ok {
		_ = closer.Close()
	}
}

func targetsToServers(targets []Target) []config.UpstreamServer {
	out := make([]config.UpstreamServer, 0, len(targets))
	for _, t := range targets {
		w := t.Weight
		if w < 1 {
			w = 1
		}
		out = append(out, config.UpstreamServer{Address: t.Address, Weight: w})
	}
	return out
}
