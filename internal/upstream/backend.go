// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package upstream implements named backend pools with pluggable load
// balancing and passive health checking for the reverse proxy.
package upstream

import (
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// Backend networks. A backend is reached over TCP or over a unix domain socket;
// the network is part of its identity because the same string can name either.
const (
	NetworkTCP  = "tcp"
	NetworkUnix = "unix"
)

// ParseSocketAddress interprets a backend target that may name a unix socket:
//
//	unix:/run/php/php-fpm.sock   -> ("unix", "/run/php/php-fpm.sock")
//	tcp://127.0.0.1:9000         -> ("tcp",  "127.0.0.1:9000")
//	127.0.0.1:9000               -> ("tcp",  "127.0.0.1:9000")
//
// It is exported because FastCGI and uWSGI targets, upstream server addresses
// and health probes must all agree on what an address means; a second parser
// would be a second answer.
func ParseSocketAddress(pass string) (network, address string) {
	switch {
	case strings.HasPrefix(pass, "unix:"):
		return NetworkUnix, strings.TrimPrefix(pass, "unix:")
	case strings.HasPrefix(pass, "tcp://"):
		return NetworkTCP, strings.TrimPrefix(pass, "tcp://")
	default:
		return NetworkTCP, pass
	}
}

// BackendIdentity is a stable identity for a backend that survives discovery
// churn and config reloads. It is used by the proxy retry loop to exclude
// already-attempted backends without depending on *Backend pointer identity
// (R9-08).
//
// It carries Network because this identity concerns *dialing*: two entries that
// differ only by network are two different places to connect.
type BackendIdentity struct {
	Scheme  string
	Network string
	Address string
}

// Backend is a single server within a pool, carrying its address, weight, and
// runtime state (in-flight requests and passive health).
type Backend struct {
	Address string
	// Network is "tcp" or "unix", derived from the address form.
	Network string
	// URL is the dial target for HTTP-shaped backends. It is nil for a unix
	// socket, which has no meaningful URL, so consumers use Scheme(), Network
	// and Address rather than reaching through it.
	URL *url.URL

	// scheme is stored rather than read back from URL, because a unix backend
	// has no URL to read it from.
	scheme string

	// id is the provider's logical identity (Kubernetes pod UID, Consul
	// ServiceID), empty when the provider has none. It is the reuse key across
	// a discovery refresh, so per-backend state follows the workload rather than
	// the address it happens to hold.
	id string

	// weight is atomic so a discovery weight change is applied in place. Reusing
	// the backend across that change is the point: the reuse key is the address
	// alone, so retuning a weight no longer resets in-flight accounting or
	// (from #143) the breaker, which is the moment an operator is most likely to
	// be watching them.
	weight atomic.Int64

	inflight  atomic.Int64
	fails     atomic.Int32
	downUntil atomic.Int64 // unix nano; 0 means healthy

	// activeHealthy is the verdict of the active health checker (Y1-05). It is
	// true unless an active checker has taken the backend out of rotation after
	// consecutive failed probes. Backends without active checks stay true for
	// their whole lifetime. It composes with the passive cooldown in available.
	activeHealthy atomic.Bool
}

// Scheme returns the backend's scheme: "http", "https", or empty for a non-HTTP
// backend such as a FastCGI or uWSGI socket.
func (b *Backend) Scheme() string { return b.scheme }

// Weight returns the backend's load-balancing weight.
func (b *Backend) Weight() int { return int(b.weight.Load()) }

// setWeight applies a new weight in place.
func (b *Backend) setWeight(w int) { b.weight.Store(int64(w)) }

// LogicalID returns the provider's identity for this backend, or "" when the
// provider has none.
//
// It is deliberately not part of BackendIdentity: that identity answers "where
// do I dial", and two workloads at one address are still one place to connect.
// This one answers "whose state is this", which is a different question with a
// different answer.
func (b *Backend) LogicalID() string { return b.id }

// Identity returns the stable (scheme, network, address) identity of this
// backend.
func (b *Backend) Identity() BackendIdentity {
	return BackendIdentity{Scheme: b.scheme, Network: b.Network, Address: b.Address}
}

// available reports whether the backend may receive traffic now. It combines
// the active health verdict with passive cooldown: a backend ejected by active
// checks is unavailable regardless of passive state, and a passively
// cooled-down backend becomes available again once the cooldown elapses
// (half-open): the next failure re-trips it.
func (b *Backend) available(nowNano int64) bool {
	if !b.activeHealthy.Load() {
		return false
	}
	du := b.downUntil.Load()
	return du == 0 || nowNano > du
}

// setActiveHealthy records the active health checker's verdict for this backend.
func (b *Backend) setActiveHealthy(healthy bool) { b.activeHealthy.Store(healthy) }

func (b *Backend) acquire() { b.inflight.Add(1) }

// Release decrements the backend's in-flight counter. It is exported so build-
// time snapshot consumers (e.g. gRPC reflection) can balance the acquire made
// by PoolSnapshot.Pick.
func (b *Backend) Release() { b.inflight.Add(-1) }

// Inflight returns the current number of in-flight requests.
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// FailCount returns the current number of consecutive passive failures recorded
// for this backend. It resets to zero when MarkSuccess is called.
func (b *Backend) FailCount() int32 { return b.fails.Load() }

// Available reports whether the backend may currently receive traffic,
// combining the active health verdict with passive cooldown. It is exported for
// operational inspection (the admin console upstream panel).
func (b *Backend) Available() bool { return b.available(time.Now().UnixNano()) }
