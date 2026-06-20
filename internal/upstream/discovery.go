package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"jul/internal/config"
)

// discoveryTimeout bounds a single resolve call so a hung provider cannot stall
// the refresher.
const discoveryTimeout = 5 * time.Second

// Target is a discovered backend: an address ("host:port") with an optional
// weight (0 means the default weight of 1).
type Target struct {
	Address string
	Weight  int
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

// newDiscoverer builds the Discoverer for a discovery config. The "consul" and
// "kubernetes" providers are compiled only into builds with the matching build
// tag; other builds return a clear error here, failing the startup or reload
// that referenced them — the same model as other gated features.
func newDiscoverer(cfg config.DiscoveryConfig) (Discoverer, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "dns":
		return newDNSDiscoverer(cfg)
	case "dns_srv":
		return newDNSSRVDiscoverer(cfg)
	case "consul":
		return newConsulDiscoverer(cfg)
	case "kubernetes":
		return newKubernetesDiscoverer(cfg)
	default:
		return nil, fmt.Errorf("unknown discovery type %q", cfg.Type)
	}
}

// StartDiscovery launches the pool's discovery refresher goroutine. It performs
// an immediate first resolve, then re-resolves every refresh interval until the
// pool is Closed (via Done). A failed or empty resolve keeps the last-good
// backend set in place, so a provider blip or a transient empty response does
// not black-hole traffic. It must be called at most once per pool.
func (p *Pool) StartDiscovery(d Discoverer, refresh time.Duration, hooks DiscoveryHooks, log *slog.Logger) {
	if refresh <= 0 {
		refresh = 30 * time.Second
	}
	go func() {
		p.refreshOnce(d, hooks, log)
		timer := time.NewTimer(jitter(refresh))
		defer timer.Stop()
		for {
			select {
			case <-p.Done():
				return
			case <-timer.C:
				p.refreshOnce(d, hooks, log)
				timer.Reset(refresh)
			}
		}
	}()
}

// refreshOnce performs a single resolve and applies it via UpdateBackends, which
// preserves the runtime state (in-flight count, passive cooldown) of surviving
// backends. Errors and empty results are logged and skip the update (keep
// last-good) so transient provider issues do not drop all backends at once.
func (p *Pool) refreshOnce(d Discoverer, hooks DiscoveryHooks, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
	defer cancel()

	targets, err := d.Resolve(ctx)
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

	servers := targetsToServers(targets)
	p.UpdateBackends(servers)
	if hooks.OnBackends != nil {
		hooks.OnBackends(p.name, len(servers))
	}
}

// targetsToServers converts discovered targets to upstream server configs,
// normalizing weights to at least 1.
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
