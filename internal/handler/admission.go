// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"sync"

	"jul/internal/config"
	"jul/internal/upstream"
)

// maxConnsPerBackend resolves the effective socket bound for a route.
//
// The control is stateless — a transport is built per location — so a location
// may override the pool. A location value of 0 inherits rather than meaning
// "unlimited": every other zero in this configuration means "not set, use the
// default", and one field reading its zero the other way would be a trap. The
// consequence is that a route cannot be made explicitly unbounded under a
// bounded pool, which is documented rather than hidden.
func maxConnsPerBackend(loc config.LocationConfig, pool *upstream.Pool) int {
	if loc.Resilience != nil && loc.Resilience.MaxConnectionsPerBackend > 0 {
		return loc.Resilience.MaxConnectionsPerBackend
	}
	if pool == nil {
		return 0
	}
	return pool.Policy().MaxConnectionsPerBackend()
}

// admittedHandler is a protocol handler that acquires pool admission before it
// runs and releases it exactly once when it returns.
//
// It exists because native gRPC passthrough has no other seam: NewGRPCProxy
// returns an *httputil.ReverseProxy directly, and admission cannot live in its
// RoundTrip for the same reason it does not live in the HTTP proxy's — that is
// where retry and per-attempt work happen, so it would count attempts rather
// than requests.
//
// The slot spans the whole call because ServeHTTP returns only once the response
// body has been copied. For a gRPC stream — server, client or bidirectional —
// that is the stream's real lifetime, which is exactly what the accounting model
// requires and what makes a long-lived stream visible to least-conn balancing.
type admittedHandler struct {
	next      http.Handler
	admission *upstream.Admission
	closer    func() error

	// retire is closed when this generation is torn down, so a request parked in
	// the pending queue is rejected instead of being admitted onto a transport
	// that is about to close.
	retire     chan struct{}
	retireOnce sync.Once
}

func newAdmittedHandler(next http.Handler, adm *upstream.Admission, closer func() error) *admittedHandler {
	return &admittedHandler{next: next, admission: adm, closer: closer, retire: make(chan struct{})}
}

func (h *admittedHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	release, err := h.admission.Admit(r.Context(), h.retire)
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	defer release()
	h.next.ServeHTTP(w, r)
}

// Close wakes this generation's parked requests, then releases the handler's own
// resources. The order matters: a request granted a slot after its transport
// closed would dial nothing.
func (h *admittedHandler) Close() error {
	h.retireOnce.Do(func() { close(h.retire) })
	if h.closer == nil {
		return nil
	}
	return h.closer()
}
