// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"errors"
	"math"
	"net/http"

	"github.com/tetratelabs/wazero/api"

	"jul/internal/inspection"
)

// This file holds the plugin side of ADR 0032 slice I2: the request lease
// taken by the outermost inspecting layer, and the host allocations charged to
// it before they are made.

// errNoLease is an internal invariant failure: a plugin invocation reached
// the runtime without the request lease its HTTP layer must acquire.
var errNoLease = errors.New("plugin: no inspection lease for the request")

// errChargeOverflow rejects a capacity whose charge cannot be represented.
var errChargeOverflow = &inspection.AdmissionError{Outcome: inspection.InvalidAccounting}

// byteCharge is one live reservation: inspection.Charge in production, a
// controllable fake in tests.
type byteCharge interface {
	Resize(uint64) error
	Release() error
}

// chargeFunc obtains a new reservation of n bytes before the represented
// allocation is made.
type chargeFunc func(n uint64) (byteCharge, error)

// leaseCharger returns a chargeFunc on lease's buffer class.
func leaseCharger(lease *inspection.Lease) chargeFunc {
	return func(n uint64) (byteCharge, error) {
		c, err := lease.Charge(inspection.BufferBytes, n)
		if err != nil {
			return nil, err
		}
		return c, nil
	}
}

// chargedBuf is a host byte buffer whose whole capacity is charged for as
// long as the buffer is reachable through it. Capacity, not length, is
// charged: shortening the slice refunds nothing.
type chargedBuf struct {
	b []byte
	c byteCharge
}

// newChargedBuf charges capacity bytes and then allocates them.
func newChargedBuf(charge chargeFunc, capacity int) (*chargedBuf, error) {
	if capacity < 0 {
		return nil, errChargeOverflow
	}
	c, err := charge(uint64(capacity))
	if err != nil {
		return nil, err
	}
	return &chargedBuf{b: make([]byte, 0, capacity), c: c}, nil
}

// grow moves the contents to a new array of newCap bytes. The new array is
// charged before it is allocated, both arrays stay charged during the copy,
// and the old array's charge is refunded only after the buffer's reference
// to it is dropped. On failure the buffer is unchanged and still charged.
func (cb *chargedBuf) grow(newCap int) error {
	old := cap(cb.b)
	if newCap <= old {
		return nil
	}
	both, ok := addCap(old, newCap)
	if !ok {
		return errChargeOverflow
	}
	if err := cb.c.Resize(both); err != nil {
		return err
	}
	nb := make([]byte, len(cb.b), newCap)
	copy(nb, cb.b)
	cb.b = nb
	return cb.c.Resize(uint64(newCap))
}

// release drops the buffer and then refunds its charge. It is idempotent.
func (cb *chargedBuf) release() {
	if cb == nil || cb.c == nil {
		return
	}
	cb.b = nil
	_ = cb.c.Release()
	cb.c = nil
}

// addCap adds two capacities without overflow.
func addCap(a, b int) (uint64, bool) {
	if a < 0 || b < 0 {
		return 0, false
	}
	x, y := uint64(a), uint64(b)
	if x > math.MaxUint64-y {
		return 0, false
	}
	return x + y, true
}

// copyGuest copies n bytes of guest memory at ptr into a new host array that
// is charged before it is allocated. view is wazero's borrowed view of guest
// memory; it is not an allocation.
func copyGuest(charge chargeFunc, view []byte) (*chargedBuf, error) {
	cb, err := newChargedBuf(charge, len(view))
	if err != nil {
		return nil, err
	}
	cb.b = append(cb.b, view...)
	return cb, nil
}

// requestLease acquires the request's inspection lease, or reuses the one an
// outer inspecting layer holds. A refused admission is answered here and
// reported as ok=false; owner reports whether the caller must close it.
func (p *plugin) requestLease(w http.ResponseWriter, r *http.Request) (lease *inspection.Lease, rr *http.Request, owner, ok bool) {
	if p.gov == nil {
		writePluginError(w, r, errNoGovernor)
		return nil, r, false, false
	}
	lease, ctx, owner, err := p.gov.AcquireLease(r.Context())
	if err != nil {
		writePluginError(w, r, err)
		return nil, r, false, false
	}
	if owner {
		r = r.WithContext(ctx)
	}
	return lease, r, owner, true
}

// leaseFrom returns the request lease carried by ctx, or nil.
func leaseFrom(ctx context.Context) *inspection.Lease {
	return inspection.LeaseFrom(ctx)
}

// chargeFn returns the invocation's charger on its request lease.
func (inv *invocation) chargeFn() chargeFunc { return leaseCharger(inv.lease) }

// failCharge retains a refused or invalid charge in the invocation, so the
// call fails with it even when the guest ignores the host function's error
// return.
func (inv *invocation) failCharge(err error) {
	if inv.err == nil || (inspection.IsOverload(err) && !inspection.IsOverload(inv.err)) {
		inv.err = err
	}
}

// guestCopy copies the guest range [ptr, ptr+n) into a new charged host
// array. ok is false when the range is outside guest memory (inv is not
// failed; the caller applies its ABI's convention) or when the charge was
// refused (inv is failed with the refusal).
func (inv *invocation) guestCopy(m api.Module, ptr, n uint32) (cb *chargedBuf, inBounds, ok bool) {
	view, inBounds := m.Memory().Read(ptr, n)
	if !inBounds {
		return nil, false, false
	}
	cb, err := copyGuest(inv.chargeFn(), view)
	if err != nil {
		inv.failCharge(err)
		return nil, true, false
	}
	return cb, true, true
}

// appendResponse appends a guest range to the generated response body. The
// caller has checked the total against max_response_body. Growth is charged
// before allocation and the bytes are copied straight from guest memory, so
// at most the old and new arrays coexist.
func (inv *invocation) appendResponse(m api.Module, ptr, n uint32) (inBounds bool) {
	view, inBounds := m.Memory().Read(ptr, n)
	if !inBounds {
		return false
	}
	if len(view) == 0 {
		return true
	}
	need := len(inv.respBody) + len(view)
	if need > cap(inv.respBody) {
		newCap := min(max(need, 2*cap(inv.respBody)), max(inv.maxRespBody, need))
		if inv.respCharge == nil {
			cb, err := newChargedBuf(inv.chargeFn(), newCap)
			if err != nil {
				inv.failCharge(err)
				return true
			}
			inv.respBody, inv.respCharge = cb.b, cb.c
		} else {
			cb := chargedBuf{b: inv.respBody, c: inv.respCharge}
			if err := cb.grow(newCap); err != nil {
				inv.failCharge(err)
				return true
			}
			inv.respBody = cb.b
		}
	}
	inv.respBody = append(inv.respBody, view...)
	return true
}

// setState replaces the per-request state with a charged copy of a guest
// range. The old state is dropped and refunded first: before handle_request
// returns nothing else can reference it.
func (inv *invocation) setState(m api.Module, ptr, n uint32) (inBounds bool) {
	if _, ok := m.Memory().Read(ptr, n); !ok {
		return false
	}
	inv.releaseState()
	cb, _, ok := inv.guestCopy(m, ptr, n)
	if !ok {
		return true
	}
	inv.state, inv.stateCharge = cb.b, cb.c
	return true
}

// releaseResponse drops the generated response body and refunds it.
func (inv *invocation) releaseResponse() {
	inv.respBody = nil
	releaseCharge(&inv.respCharge)
}

// releaseState drops the per-request state and refunds it. A subscription
// that captured the state must have ended first.
func (inv *invocation) releaseState() {
	inv.state = nil
	releaseCharge(&inv.stateCharge)
}

// releaseFetch drops the retained fetch response and refunds it.
func (inv *invocation) releaseFetch() {
	inv.lastFetch = nil
	inv.lastFetchTruncated = false
	releaseCharge(&inv.fetchCharge)
}

// endCall releases what only the guest call could reach: the retained fetch
// response and an unused response replacement.
func (inv *invocation) endCall() {
	if inv == nil {
		return
	}
	inv.releaseFetch()
}

// releaseAll drops every array the invocation owns. The response-phase
// invocation borrows its state from the subscription and does not own it.
func (inv *invocation) releaseAll() {
	if inv == nil {
		return
	}
	inv.releaseFetch()
	inv.releaseResponse()
	if inv.phase == phaseRequest {
		inv.releaseState()
	}
}

func releaseCharge(c *byteCharge) {
	if *c != nil {
		_ = (*c).Release()
		*c = nil
	}
}
