// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"net"
	"net/http"

	"jul/internal/affinity"
)

// newBalancer builds a fresh balancer for this pool's strategy. Snapshots
// call it so each generation advances its own round-robin state; for
// consistent_hash that state is only the fallback's.
func (p *Pool) newBalancer() Balancer { return newBalancer(p.strategy, p.hashFallback) }

// Hashing reports whether the pool places requests by an affinity key.
func (p *Pool) Hashing() bool { return p != nil && p.hash.Kind() != "" }

// AffinityKey extracts the request's affinity key when the pool hashes, and
// returns the zero Key otherwise, so adapters may call it unconditionally.
// Callers extract once per logical request and pass the key into every
// attempt; re-extracting per retry would count one request's outcome twice.
func (p *Pool) AffinityKey(r *http.Request) affinity.Key {
	if !p.Hashing() {
		return affinity.Key{}
	}
	k := p.hash.FromRequest(r)
	p.observeKey(k)
	return k
}

// AffinityKeyForAddr extracts a stream client's key: its canonical address
// for a client_ip pool, the zero Key otherwise.
func (p *Pool) AffinityKeyForAddr(a net.Addr) affinity.Key {
	if !p.Hashing() {
		return affinity.Key{}
	}
	k := p.hash.FromAddr(a)
	p.observeKey(k)
	return k
}

func (p *Pool) observeKey(k affinity.Key) {
	if p.affinityHook != nil {
		p.affinityHook(p.name, k.Status().String())
	}
}

// SetAffinityHook wires the key-outcome counter. It is set once by the
// registry, before the pool serves.
func (p *Pool) SetAffinityHook(h func(pool, status string)) { p.affinityHook = h }

// HashStatus is a consistent_hash pool's effective configuration for the admin
// API and Console. It carries configuration only: no key, no per-key state.
type HashStatus struct {
	Key       string
	Name      string
	Fallback  string
	Algorithm string
}

// HashStatus returns the effective hash configuration, or nil when the pool
// does not hash.
func (p *Pool) HashStatus() *HashStatus {
	if !p.Hashing() {
		return nil
	}
	fb := p.hashFallback
	if fb == "" {
		fb = "round_robin"
	}
	return &HashStatus{Key: p.hash.Kind(), Name: p.hash.Name(), Fallback: fb, Algorithm: affinity.Algorithm}
}
