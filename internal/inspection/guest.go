// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"fmt"
	"math"

	"jul/internal/config"
)

// Bound newly admitted work when repeated real teardown failures accumulate.
const maxRetainedGuestFailures = 64

// GuestState describes one reserved guest instance's lifetime.
type GuestState uint8

const (
	GuestStarting GuestState = iota
	GuestIdle
	GuestBusy
	GuestClosing
	GuestFailedRetained
	GuestClosed
)

type guestCapacity struct {
	reserved  uint64
	failed    uint64
	instances [GuestClosed]uint64
}

// GuestCapacitySnapshot reports Jul-managed reservations, not committed guest memory.
type GuestCapacitySnapshot struct {
	ReservedBytes uint64
	Instances     [GuestClosed]uint64
	Draining      bool
}

func (g *Governor) guestSnapshotLocked() GuestCapacitySnapshot {
	return GuestCapacitySnapshot{
		ReservedBytes: g.guest.reserved,
		Instances:     g.guest.instances,
		Draining:      g.limits.Mode == Enforce && g.limits.MaxGuestCapacityBytes > 0 && g.guest.reserved > g.limits.MaxGuestCapacityBytes,
	}
}

// GuestStateNames returns the stable bounded states suitable for metrics.
func GuestStateNames() []string {
	return []string{"starting", "idle", "busy", "closing", "failed_retained"}
}

// CheckGuestCapacityMinimum checks that one eager instance of each plugin can
// fit the proposed cap. A shortfall is a warning in observe mode.
func CheckGuestCapacityMinimum(limits Limits, plugins map[string]config.PluginConfig) (warning, err error) {
	var minimum uint64
	for name, plugin := range plugins {
		pages, err := config.EffectivePluginMemoryPages(plugin.MemoryLimit)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", name, err)
		}
		amount := uint64(pages) * config.PluginMemoryPageSize
		if amount > math.MaxUint64-minimum {
			return nil, fmt.Errorf("inspection: eager guest capacity overflows uint64")
		}
		minimum += amount
	}
	if limits.MaxGuestCapacityBytes == 0 || minimum <= limits.MaxGuestCapacityBytes {
		return nil, nil
	}
	shortfall := fmt.Errorf("inspection: max_guest_capacity_bytes is %d B; one eager instance of each plugin needs at least %d B",
		limits.MaxGuestCapacityBytes, minimum)
	if limits.Mode == Observe {
		return fmt.Errorf("inspection (observe): %w", shortfall), nil
	}
	return nil, shortfall
}

// GuestTicket belongs to a plugin generation rather than a request lease.
type GuestTicket struct {
	g          *Governor
	generation uint64
	amount     uint64
	state      GuestState
	failed     bool
}

// Generation identifies the generation that owns the reservation.
func (t *GuestTicket) Generation() uint64 { return t.generation }

// ReserveGuestCapacity records the effective guest-memory ceiling before
// instantiation. An unconfigured ceiling tracks usage without limiting it.
func (g *Governor) ReserveGuestCapacity(generation, amount uint64) (*GuestTicket, error) {
	if g == nil || generation == 0 || amount == 0 {
		return nil, &AdmissionError{Outcome: InvalidAccounting}
	}
	g.mu.Lock()
	if g.guest.failed >= maxRetainedGuestFailures {
		g.noteRejectionForModeLocked(GuestCapacitySaturated, Enforce)
		hook := g.hooks.OnRejection
		g.mu.Unlock()
		if hook != nil {
			hook(GuestCapacitySaturated, Enforce)
		}
		return nil, fmt.Errorf("inspection: unresolved guest teardown prevents admission: %w",
			&AdmissionError{Outcome: GuestCapacitySaturated})
	}
	wouldExceed := g.limits.MaxGuestCapacityBytes > 0 &&
		(g.guest.reserved > g.limits.MaxGuestCapacityBytes ||
			amount > g.limits.MaxGuestCapacityBytes-g.guest.reserved)
	mode := g.limits.Mode
	var hook func(Outcome, Mode)
	if wouldExceed {
		g.noteRejectionLocked(GuestCapacitySaturated)
		hook = g.hooks.OnRejection
		if mode == Enforce {
			g.mu.Unlock()
			if hook != nil {
				hook(GuestCapacitySaturated, mode)
			}
			return nil, &AdmissionError{Outcome: GuestCapacitySaturated}
		}
	}
	if amount > math.MaxUint64-g.guest.reserved || g.guest.instances[GuestStarting] == math.MaxUint64 {
		g.mu.Unlock()
		if hook != nil {
			hook(GuestCapacitySaturated, mode)
		}
		return nil, &AdmissionError{Outcome: InvalidAccounting}
	}
	g.guest.reserved += amount
	g.guest.instances[GuestStarting]++
	ticket := &GuestTicket{g: g, generation: generation, amount: amount, state: GuestStarting}
	g.mu.Unlock()
	if hook != nil {
		hook(GuestCapacitySaturated, mode)
	}
	return ticket, nil
}

// Transition moves a ticket through the Jul-managed instance lifecycle.
func (t *GuestTicket) Transition(next GuestState) error {
	if t == nil || t.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := t.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if next > GuestClosed {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	if t.state == GuestClosed && next == GuestClosed {
		return nil
	}
	valid := false
	switch t.state {
	case GuestStarting:
		valid = next == GuestIdle || next == GuestBusy || next == GuestClosing
	case GuestIdle:
		valid = next == GuestBusy || next == GuestClosing
	case GuestBusy:
		valid = next == GuestIdle || next == GuestClosing
	case GuestClosing:
		valid = next == GuestClosed || next == GuestFailedRetained
	case GuestFailedRetained:
		valid = next == GuestClosing
	}
	if !valid || g.guest.instances[t.state] == 0 ||
		(next != GuestClosed && g.guest.instances[next] == math.MaxUint64) ||
		(next == GuestClosed && (g.guest.reserved < t.amount || (t.failed && g.guest.failed == 0))) {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g.guest.instances[t.state]--
	if next == GuestClosed {
		g.guest.reserved -= t.amount
		if t.failed {
			g.guest.failed--
			t.failed = false
		}
	} else {
		g.guest.instances[next]++
	}
	t.state = next
	return nil
}

// ResolveTeardown releases a ticket after Jul-managed cleanup succeeds. A
// close failure retains the reservation until its managed owner completes.
func (t *GuestTicket) ResolveTeardown(closeErr error) error {
	if t == nil || t.g == nil {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g := t.g
	g.mu.Lock()
	defer g.mu.Unlock()
	if t.state == GuestClosed {
		if closeErr == nil {
			return nil
		}
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	if t.state != GuestClosing && (closeErr == nil || t.state != GuestFailedRetained) {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	next := GuestClosed
	if closeErr != nil {
		next = GuestFailedRetained
	}
	if t.state >= GuestClosed || g.guest.instances[t.state] == 0 ||
		(next == GuestFailedRetained && t.state != GuestFailedRetained &&
			g.guest.instances[GuestFailedRetained] == math.MaxUint64) ||
		(closeErr != nil && !t.failed && g.guest.failed == math.MaxUint64) ||
		(next == GuestClosed && (g.guest.reserved < t.amount || (t.failed && g.guest.failed == 0))) {
		return &AdmissionError{Outcome: InvalidAccounting}
	}
	g.guest.instances[t.state]--
	if next == GuestClosed {
		g.guest.reserved -= t.amount
		if t.failed {
			g.guest.failed--
			t.failed = false
		}
	} else {
		g.guest.instances[next]++
		if !t.failed {
			g.guest.failed++
			t.failed = true
		}
	}
	t.state = next
	return nil
}
