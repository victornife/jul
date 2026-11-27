// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package quota provides process-local admission accounting for an application.
package quota

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limit describes a sustained request rate and a burst in requests.
type Limit struct {
	Rate  int
	Burst int
}

// Class describes the shared and per-caller limits for one traffic class.
type Class struct {
	Shared    Limit
	PerCaller Limit
}

// Policy describes the aggregate application and its fixed class allocations.
type Policy struct {
	Application Limit
	Classes     map[string]Class
}

// Reason identifies the first budget preventing admission.
type Reason string

const (
	ReasonApplication    Reason = "application"
	ReasonClass          Reason = "class"
	ReasonCaller         Reason = "caller"
	ReasonCallerCapacity Reason = "caller_capacity"
)

// Decision is an admission result. RetryAfter is zero when no time estimate is
// available, including when the caller state store is full.
type Decision struct {
	Allowed    bool
	Reason     Reason
	RetryAfter time.Duration
}

const (
	maxRateOrBurst = 1_000_000_000
	maxCallerBytes = 4096
)

type classState struct {
	shared    *rate.Limiter
	perCaller Limit
	callers   map[[sha256.Size]byte]*callerState
	order     list.List
}

type callerState struct {
	key     [sha256.Size]byte
	limiter *rate.Limiter
	element *list.Element
}

// Coordinator serializes admissions against an application and its classes.
// It is not connected to serving or configuration publication.
type Coordinator struct {
	mu          sync.Mutex
	application *rate.Limiter
	classes     map[string]*classState
	maxCallers  int
	now         func() time.Time
}

// NewCoordinator creates an independent set of budgets. Class allocations must
// fit within the application rate and burst. Each class has its own caller
// capacity. A full caller store rejects new callers unless its least recently
// admitted caller has fully refilled.
func NewCoordinator(policy Policy, maxCallers int) (*Coordinator, error) {
	if err := validateLimit(policy.Application); err != nil {
		return nil, fmt.Errorf("application limit: %w", err)
	}
	if maxCallers <= 0 {
		return nil, fmt.Errorf("max callers must be positive")
	}
	if len(policy.Classes) == 0 {
		return nil, fmt.Errorf("at least one class is required")
	}

	classes := make(map[string]*classState, len(policy.Classes))
	totalRate, totalBurst := 0, 0
	for name, class := range policy.Classes {
		if name == "" {
			return nil, fmt.Errorf("class name must not be empty")
		}
		if err := validateLimit(class.Shared); err != nil {
			return nil, fmt.Errorf("class %q shared limit: %w", name, err)
		}
		if err := validateLimit(class.PerCaller); err != nil {
			return nil, fmt.Errorf("class %q caller limit: %w", name, err)
		}
		if class.Shared.Rate > policy.Application.Rate-totalRate ||
			class.Shared.Burst > policy.Application.Burst-totalBurst {
			return nil, fmt.Errorf("class allocations exceed application limit")
		}
		totalRate += class.Shared.Rate
		totalBurst += class.Shared.Burst
		classes[name] = &classState{
			shared:    newLimiter(class.Shared),
			perCaller: class.PerCaller,
			callers:   make(map[[sha256.Size]byte]*callerState),
		}
	}
	return &Coordinator{
		application: newLimiter(policy.Application),
		classes:     classes,
		maxCallers:  maxCallers,
		now:         time.Now,
	}, nil
}

func validateLimit(limit Limit) error {
	if limit.Rate <= 0 || limit.Burst <= 0 ||
		limit.Rate > maxRateOrBurst || limit.Burst > maxRateOrBurst {
		return fmt.Errorf("rate and burst must be between 1 and %d", maxRateOrBurst)
	}
	return nil
}

func newLimiter(limit Limit) *rate.Limiter {
	return rate.NewLimiter(rate.Limit(limit.Rate), limit.Burst)
}

// Admit charges one request to all three budgets or none of them. The caller
// string must already be a verified or canonical identity selected by the
// request path; this package does not classify or authenticate it.
func (c *Coordinator) Admit(class, caller string) (Decision, error) {
	if len(caller) == 0 || len(caller) > maxCallerBytes {
		return Decision{}, fmt.Errorf("caller identity must be between 1 and %d bytes", maxCallerBytes)
	}
	key := sha256.Sum256([]byte(caller))

	c.mu.Lock()
	defer c.mu.Unlock()

	state, ok := c.classes[class]
	if !ok {
		return Decision{}, fmt.Errorf("unknown quota class %q", class)
	}
	now := c.now()
	entry := state.callers[key]
	if entry == nil {
		if len(state.callers) >= c.maxCallers {
			oldest := state.order.Front().Value.(*callerState)
			if oldest.limiter.TokensAt(now) < float64(oldest.limiter.Burst()) {
				return Decision{Reason: ReasonCallerCapacity}, nil
			}
			delete(state.callers, oldest.key)
			state.order.Remove(oldest.element)
		}
		entry = &callerState{key: key, limiter: newLimiter(state.perCaller)}
	}

	limiters := [...]struct {
		limiter *rate.Limiter
		reason  Reason
	}{
		{c.application, ReasonApplication},
		{state.shared, ReasonClass},
		{entry.limiter, ReasonCaller},
	}
	var reservations [3]*rate.Reservation
	var decision Decision
	for i, limit := range limiters {
		unavailable := limit.limiter.TokensAt(now) < 1
		reservations[i] = limit.limiter.ReserveN(now, 1)
		delay := reservations[i].DelayFrom(now)
		if unavailable || delay > 0 {
			if delay == 0 {
				delay = time.Nanosecond
			}
			if decision.Reason == "" {
				decision.Reason = limit.reason
			}
			if delay > decision.RetryAfter {
				decision.RetryAfter = delay
			}
		}
	}
	if decision.Reason != "" {
		for _, reservation := range reservations {
			reservation.CancelAt(now)
		}
		return decision, nil
	}
	if entry.element == nil {
		entry.element = state.order.PushBack(entry)
		state.callers[key] = entry
	} else {
		state.order.MoveToBack(entry.element)
	}
	return Decision{Allowed: true}, nil
}
