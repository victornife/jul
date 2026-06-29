// Package upstream implements named backend pools with pluggable load
// balancing and passive health checking for the reverse proxy.
package upstream

import (
	"net/url"
	"sync/atomic"
	"time"
)

// Backend is a single server within a pool, carrying its address, weight, and
// runtime state (in-flight requests and passive health).
type Backend struct {
	Address string
	Weight  int
	URL     *url.URL

	inflight  atomic.Int64
	fails     atomic.Int32
	downUntil atomic.Int64 // unix nano; 0 means healthy

	// activeHealthy is the verdict of the active health checker (Y1-05). It is
	// true unless an active checker has taken the backend out of rotation after
	// consecutive failed probes. Backends without active checks stay true for
	// their whole lifetime. It composes with the passive cooldown in available.
	activeHealthy atomic.Bool

	// currentWeight is smooth-weighted-round-robin state, guarded by the
	// weightedRR balancer's mutex.
	currentWeight int
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
func (b *Backend) release() { b.inflight.Add(-1) }

// Inflight returns the current number of in-flight requests.
func (b *Backend) Inflight() int64 { return b.inflight.Load() }

// FailCount returns the current number of consecutive passive failures recorded
// for this backend. It resets to zero when MarkSuccess is called.
func (b *Backend) FailCount() int32 { return b.fails.Load() }

// Available reports whether the backend may currently receive traffic,
// combining the active health verdict with passive cooldown. It is exported for
// operational inspection (the admin console upstream panel).
func (b *Backend) Available() bool { return b.available(time.Now().UnixNano()) }
