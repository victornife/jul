// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package quota provides process-local admission accounting for an application.
package quota

import (
	"container/list"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"sync"
	"sync/atomic"
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
	Operations  map[string]Operation
}

// Operation adds a shared and per-caller ceiling to one named route operation.
type Operation struct {
	Shared    Limit
	PerCaller Limit
}

// Reason identifies the first budget preventing admission.
type Reason string

const (
	ReasonApplication        Reason = "application"
	ReasonClass              Reason = "class"
	ReasonCaller             Reason = "caller"
	ReasonCallerCapacity     Reason = "caller_capacity"
	ReasonOperation          Reason = "operation"
	ReasonOperationCaller    Reason = "operation_caller"
	ReasonStaleAuthorization Reason = "stale_authorization"
)

// Decision is an admission result. RetryAfter is zero when no time estimate is
// available, including when the caller state store is full.
type Decision struct {
	Allowed    bool
	Reason     Reason
	RetryAfter time.Duration
}

const (
	maxRateOrBurst      = 1_000_000_000
	maxCallerBytes      = 4096
	maxCallerSlots      = 16_384
	maxActiveClasses    = 8
	maxActiveOperations = 8
	maxStoredClasses    = 16
	maxStoredOperations = 16
)

type classState struct {
	mu        sync.Mutex
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
	mu                   sync.Mutex
	reconfigureMu        sync.Mutex
	application          *rate.Limiter
	classes              map[string]*classState
	retiredClasses       map[string]*classState
	operations           map[string]*classState
	retiredOperations    map[string]*classState
	maxCallers           int
	now                  func() time.Time
	authorizationVersion atomic.Uint64
}

// NewCoordinator creates an independent set of budgets. Class allocations must
// fit within the application rate and burst. Each class has its own caller
// capacity. A full caller store scans at most maxCallers entries for the least
// recently admitted caller that has fully refilled; otherwise it rejects new callers.
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
	if len(policy.Classes) > maxActiveClasses || len(policy.Operations) > maxActiveOperations {
		return nil, fmt.Errorf("active quota state exceeds %d classes or %d operations", maxActiveClasses, maxActiveOperations)
	}
	if maxCallers > maxCallerSlots/(len(policy.Classes)+len(policy.Operations)) {
		return nil, fmt.Errorf("active class and operation caller stores exceed the %d-entry registry bound", maxCallerSlots)
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
	operations := make(map[string]*classState, len(policy.Operations))
	for name, operation := range policy.Operations {
		if name == "" {
			return nil, fmt.Errorf("operation name must not be empty")
		}
		if err := validateLimit(operation.Shared); err != nil {
			return nil, fmt.Errorf("operation %q shared limit: %w", name, err)
		}
		if err := validateLimit(operation.PerCaller); err != nil {
			return nil, fmt.Errorf("operation %q caller limit: %w", name, err)
		}
		operations[name] = &classState{
			shared:    newLimiter(operation.Shared),
			perCaller: operation.PerCaller,
			callers:   make(map[[sha256.Size]byte]*callerState),
		}
	}
	c := &Coordinator{
		application:       newLimiter(policy.Application),
		classes:           classes,
		retiredClasses:    make(map[string]*classState),
		operations:        operations,
		retiredOperations: make(map[string]*classState),
		maxCallers:        maxCallers,
		now:               time.Now,
	}
	c.authorizationVersion.Store(1)
	return c, nil
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
// request path; this package does not classify or authenticate it. Admit does
// not fence authorization changes; serving paths that rely on the fence must
// use AdmitForAuthorizationVersion.
func (c *Coordinator) Admit(class, caller string) (Decision, error) {
	return c.admit(class, "", caller, 0, false)
}

// AdmitOperation charges the named operation together with the application and
// selected class budgets.
func (c *Coordinator) AdmitOperation(class, operation, caller string) (Decision, error) {
	return c.admit(class, operation, caller, 0, false)
}

// AuthorizationVersion identifies the currently published authorization.
func (c *Coordinator) AuthorizationVersion() uint64 {
	return c.authorizationVersion.Load()
}

// AdvanceAuthorization fences admissions prepared against earlier versions.
// Publication must call this only for an authorization change, including a
// rollback to an older policy. Equivalent policies retain their version.
func (c *Coordinator) AdvanceAuthorization() (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	version := c.authorizationVersion.Load()
	if version == ^uint64(0) {
		return 0, fmt.Errorf("authorization version exhausted")
	}
	version++
	c.authorizationVersion.Store(version)
	return version, nil
}

// CheckReconfigure validates a future policy update and reclaims only retired,
// fully refilled class and operation state. It does not change active budgets.
func (c *Coordinator) CheckReconfigure(policy Policy, maxCallers int) error {
	_, err := c.reconfigure(policy, maxCallers, false, false)
	return err
}

// Reconfigure updates fixed limits in place, preserving accumulated tokens and
// caller state. Removed classes and operations remain as bounded tombstones
// until all of their buckets are full. A security change advances the
// authorization version in the same critical publication transaction.
func (c *Coordinator) Reconfigure(policy Policy, maxCallers int, securityChanged bool) (uint64, error) {
	return c.reconfigure(policy, maxCallers, true, securityChanged)
}

func (c *Coordinator) reconfigure(policy Policy, maxCallers int, apply, securityChanged bool) (uint64, error) {
	c.reconfigureMu.Lock()
	defer c.reconfigureMu.Unlock()

	prepared, err := NewCoordinator(policy, maxCallers)
	if err != nil {
		return 0, err
	}

	c.mu.Lock()
	oldClasses := namedStates(c.classes, c.retiredClasses)
	oldOperations := namedStates(c.operations, c.retiredOperations)
	c.mu.Unlock()
	for _, entry := range oldClasses {
		entry.state.mu.Lock()
	}
	for _, entry := range oldOperations {
		entry.state.mu.Lock()
	}
	defer func() {
		for i := len(oldOperations) - 1; i >= 0; i-- {
			oldOperations[i].state.mu.Unlock()
		}
		for i := len(oldClasses) - 1; i >= 0; i-- {
			oldClasses[i].state.mu.Unlock()
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()
	version := c.authorizationVersion.Load()
	if securityChanged && version == ^uint64(0) {
		return 0, fmt.Errorf("authorization version exhausted")
	}
	now := c.now()
	for name, state := range c.retiredClasses {
		if _, restored := prepared.classes[name]; !restored && state.fullyRefilled(now) {
			delete(c.retiredClasses, name)
		}
	}
	for name, state := range c.retiredOperations {
		if _, restored := prepared.operations[name]; !restored && state.fullyRefilled(now) {
			delete(c.retiredOperations, name)
		}
	}
	retiredClasses := make(map[string]*classState, len(c.retiredClasses)+len(c.classes))
	for name, state := range c.retiredClasses {
		if _, restored := prepared.classes[name]; restored {
			continue
		}
		if state.fullyRefilled(now) {
			continue
		}
		retiredClasses[name] = state
	}
	for name, state := range c.classes {
		if _, retained := prepared.classes[name]; !retained {
			retiredClasses[name] = state
		}
	}
	retiredOperations := make(map[string]*classState, len(c.retiredOperations)+len(c.operations))
	for name, state := range c.retiredOperations {
		if _, restored := prepared.operations[name]; restored {
			continue
		}
		if state.fullyRefilled(now) {
			continue
		}
		retiredOperations[name] = state
	}
	for name, state := range c.operations {
		if _, retained := prepared.operations[name]; !retained {
			retiredOperations[name] = state
		}
	}

	if len(prepared.classes)+len(retiredClasses) > maxStoredClasses {
		name, wait := firstRefillBlocker(retiredClasses, now)
		return 0, fmt.Errorf("quota class-state registry is full (%d states); retired class %q is not fully refilled (estimated safe eviction in %s)", maxStoredClasses, name, wait)
	}
	if len(prepared.operations)+len(retiredOperations) > maxStoredOperations {
		name, wait := firstRefillBlocker(retiredOperations, now)
		return 0, fmt.Errorf("quota operation-state registry is full (%d states); retired operation %q is not fully refilled (estimated safe eviction in %s)", maxStoredOperations, name, wait)
	}

	activeCallerSlots := 0
	for name := range prepared.classes {
		state := c.classes[name]
		if state == nil {
			state = c.retiredClasses[name]
		}
		activeCallerSlots += callerReservation(state, maxCallers)
	}
	for name := range prepared.operations {
		state := c.operations[name]
		if state == nil {
			state = c.retiredOperations[name]
		}
		activeCallerSlots += callerReservation(state, maxCallers)
	}
	retiredCallerSlots := 0
	for name, state := range retiredClasses {
		if _, wasActive := c.classes[name]; wasActive {
			retiredCallerSlots += callerReservation(state, c.maxCallers)
		} else {
			retiredCallerSlots += len(state.callers)
		}
	}
	for name, state := range retiredOperations {
		if _, wasActive := c.operations[name]; wasActive {
			retiredCallerSlots += callerReservation(state, c.maxCallers)
		} else {
			retiredCallerSlots += len(state.callers)
		}
	}
	if activeCallerSlots+retiredCallerSlots > maxCallerSlots {
		needed := activeCallerSlots + retiredCallerSlots - maxCallerSlots
		name, kind, wait, reclaimable := earliestCallerEviction(retiredClasses, retiredOperations, c.classes, c.operations, now, needed)
		if reclaimable {
			return 0, fmt.Errorf("quota caller-state registry needs %d entries but is bounded at %d; retired %s %q can be safely evicted in approximately %s", activeCallerSlots+retiredCallerSlots, maxCallerSlots, kind, name, wait.Round(time.Millisecond))
		}
		return 0, fmt.Errorf("quota caller-state registry needs %d entries but is bounded at %d; no already-retired class or operation can free enough space safely", activeCallerSlots+retiredCallerSlots, maxCallerSlots)
	}

	if !apply {
		for name, state := range c.retiredClasses {
			if _, restored := prepared.classes[name]; restored {
				continue
			}
			if state.fullyRefilled(now) {
				delete(c.retiredClasses, name)
			}
		}
		for name, state := range c.retiredOperations {
			if _, restored := prepared.operations[name]; restored {
				continue
			}
			if state.fullyRefilled(now) {
				delete(c.retiredOperations, name)
			}
		}
		return version, nil
	}
	c.application.SetLimitAt(now, rate.Limit(policy.Application.Rate))
	c.application.SetBurstAt(now, policy.Application.Burst)
	for name, next := range prepared.classes {
		if current := c.classes[name]; current != nil {
			updateStateLimit(current, next.shared, next.perCaller, now)
			prepared.classes[name] = current
		} else if current := c.retiredClasses[name]; current != nil {
			updateStateLimit(current, next.shared, next.perCaller, now)
			prepared.classes[name] = current
		}
	}
	for name, next := range prepared.operations {
		if current := c.operations[name]; current != nil {
			updateStateLimit(current, next.shared, next.perCaller, now)
			prepared.operations[name] = current
		} else if current := c.retiredOperations[name]; current != nil {
			updateStateLimit(current, next.shared, next.perCaller, now)
			prepared.operations[name] = current
		}
	}
	c.classes = prepared.classes
	c.retiredClasses = retiredClasses
	c.operations = prepared.operations
	c.retiredOperations = retiredOperations
	c.maxCallers = prepared.maxCallers
	if securityChanged {
		version++
		c.authorizationVersion.Store(version)
	}
	return version, nil
}

type namedClassState struct {
	name  string
	state *classState
}

func namedStates(active, retired map[string]*classState) []namedClassState {
	out := make([]namedClassState, 0, len(active)+len(retired))
	for name, state := range active {
		out = append(out, namedClassState{name: name, state: state})
	}
	for name, state := range retired {
		out = append(out, namedClassState{name: name, state: state})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func callerReservation(state *classState, maxCallers int) int {
	if state == nil || len(state.callers) >= maxCallers {
		if state == nil {
			return maxCallers
		}
		return len(state.callers)
	}
	return maxCallers
}

func firstRefillBlocker(states map[string]*classState, now time.Time) (string, time.Duration) {
	var (
		first string
		wait  time.Duration
	)
	for name, state := range states {
		if state.fullyRefilled(now) {
			continue
		}
		candidateWait := state.refillWait(now)
		if first == "" || candidateWait < wait || (candidateWait == wait && name < first) {
			first, wait = name, candidateWait
		}
	}
	return first, wait
}

func earliestCallerEviction(classes, operations, activeClasses, activeOperations map[string]*classState, now time.Time, needed int) (string, string, time.Duration, bool) {
	type candidate struct {
		name  string
		kind  string
		count int
		wait  time.Duration
	}
	candidates := make([]candidate, 0, len(classes)+len(operations))
	for name, state := range classes {
		if activeClasses[name] != nil || len(state.callers) == 0 || state.fullyRefilled(now) {
			continue
		}
		candidates = append(candidates, candidate{name: name, kind: "class", count: len(state.callers), wait: state.refillWait(now)})
	}
	for name, state := range operations {
		if activeOperations[name] != nil || len(state.callers) == 0 || state.fullyRefilled(now) {
			continue
		}
		candidates = append(candidates, candidate{name: name, kind: "operation", count: len(state.callers), wait: state.refillWait(now)})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].wait != candidates[j].wait {
			return candidates[i].wait < candidates[j].wait
		}
		if candidates[i].kind != candidates[j].kind {
			return candidates[i].kind < candidates[j].kind
		}
		return candidates[i].name < candidates[j].name
	})
	freed := 0
	for _, entry := range candidates {
		freed += entry.count
		if freed >= needed {
			return entry.name, entry.kind, entry.wait, true
		}
	}
	return "", "", 0, false
}

func (s *classState) fullyRefilled(now time.Time) bool {
	if !limiterFull(s.shared, now) {
		return false
	}
	for _, caller := range s.callers {
		if !limiterFull(caller.limiter, now) {
			return false
		}
	}
	return true
}

func (s *classState) refillWait(now time.Time) time.Duration {
	wait := limiterRefillWait(s.shared, now)
	for _, caller := range s.callers {
		if callerWait := limiterRefillWait(caller.limiter, now); callerWait > wait {
			wait = callerWait
		}
	}
	return wait
}

func limiterFull(limiter *rate.Limiter, now time.Time) bool {
	return limiter.TokensAt(now) >= float64(limiter.Burst())
}

func limiterRefillWait(limiter *rate.Limiter, now time.Time) time.Duration {
	deficit := float64(limiter.Burst()) - limiter.TokensAt(now)
	if deficit <= 0 {
		return 0
	}
	limit := float64(limiter.Limit())
	if limit <= 0 {
		return 0
	}
	return time.Duration(math.Ceil(deficit / limit * float64(time.Second)))
}

// RefillStatus reports whether every budget held by the coordinator is full
// and the maximum estimated time remaining until that is true. It is intended
// for safe eviction of a coordinator whose authorization version is already
// fenced and which has no active serving binding.
func (c *Coordinator) RefillStatus() (bool, time.Duration) {
	c.reconfigureMu.Lock()
	defer c.reconfigureMu.Unlock()

	c.mu.Lock()
	classes := namedStates(c.classes, c.retiredClasses)
	operations := namedStates(c.operations, c.retiredOperations)
	c.mu.Unlock()
	for _, entry := range classes {
		entry.state.mu.Lock()
	}
	for _, entry := range operations {
		entry.state.mu.Lock()
	}
	defer func() {
		for i := len(operations) - 1; i >= 0; i-- {
			operations[i].state.mu.Unlock()
		}
		for i := len(classes) - 1; i >= 0; i-- {
			classes[i].state.mu.Unlock()
		}
	}()

	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	wait := limiterRefillWait(c.application, now)
	full := limiterFull(c.application, now)
	for _, entry := range classes {
		if !entry.state.fullyRefilled(now) {
			full = false
		}
		if stateWait := entry.state.refillWait(now); stateWait > wait {
			wait = stateWait
		}
	}
	for _, entry := range operations {
		if !entry.state.fullyRefilled(now) {
			full = false
		}
		if stateWait := entry.state.refillWait(now); stateWait > wait {
			wait = stateWait
		}
	}
	return full, wait
}

func updateStateLimit(current *classState, prepared *rate.Limiter, perCaller Limit, now time.Time) {
	current.shared.SetLimitAt(now, prepared.Limit())
	current.shared.SetBurstAt(now, prepared.Burst())
	current.perCaller = perCaller
	for _, caller := range current.callers {
		caller.limiter.SetLimitAt(now, rate.Limit(perCaller.Rate))
		caller.limiter.SetBurstAt(now, perCaller.Burst)
	}
}

// AdmitForAuthorizationVersion atomically checks the authorization version
// and charges budgets. It does not itself authenticate or authorize the caller.
func (c *Coordinator) AdmitForAuthorizationVersion(class, caller string, version uint64) (Decision, error) {
	return c.admit(class, "", caller, version, true)
}

// AdmitOperationForAuthorizationVersion atomically fences and charges an
// operation admission against its published authorization version.
func (c *Coordinator) AdmitOperationForAuthorizationVersion(class, operation, caller string, version uint64) (Decision, error) {
	return c.admit(class, operation, caller, version, true)
}

func (c *Coordinator) admit(class, operation, caller string, version uint64, fenced bool) (Decision, error) {
	stale := func() bool {
		return fenced && c.authorizationVersion.Load() != version
	}
	if stale() {
		return Decision{Reason: ReasonStaleAuthorization}, nil
	}
	if len(caller) == 0 || len(caller) > maxCallerBytes {
		return Decision{}, fmt.Errorf("caller identity must be between 1 and %d bytes", maxCallerBytes)
	}
	key := sha256.Sum256([]byte(caller))

	c.mu.Lock()
	if stale() {
		c.mu.Unlock()
		return Decision{Reason: ReasonStaleAuthorization}, nil
	}
	state, ok := c.classes[class]
	if !ok {
		c.mu.Unlock()
		return Decision{}, fmt.Errorf("unknown quota class %q", class)
	}
	var operationState *classState
	maxCallers := c.maxCallers
	if operation != "" {
		operationState, ok = c.operations[operation]
		if !ok {
			c.mu.Unlock()
			return Decision{}, fmt.Errorf("unknown quota operation %q", operation)
		}
	}
	c.mu.Unlock()
	// Always acquire the class lock before the application lock.
	state.mu.Lock()
	defer state.mu.Unlock()
	if operationState != nil {
		operationState.mu.Lock()
		defer operationState.mu.Unlock()
	}

	entry := state.callers[key]
	newEntry := entry == nil
	var evict []*callerState
	var operationEntry *callerState
	var operationEvict []*callerState
	newOperationEntry := false
	var scanTime time.Time
	if newEntry {
		evictionsNeeded := len(state.callers) - maxCallers + 1
		if evictionsNeeded > 0 {
			scanTime = c.now()
			evict = state.refilledCallers(scanTime, evictionsNeeded)
			if len(evict) != evictionsNeeded {
				if stale() {
					return Decision{Reason: ReasonStaleAuthorization}, nil
				}
				return Decision{Reason: ReasonCallerCapacity}, nil
			}
		}
		entry = &callerState{key: key, limiter: newLimiter(state.perCaller)}
	}
	if operationState != nil {
		operationEntry = operationState.callers[key]
		newOperationEntry = operationEntry == nil
		if newOperationEntry {
			evictionsNeeded := len(operationState.callers) - maxCallers + 1
			if evictionsNeeded > 0 {
				if scanTime.IsZero() {
					scanTime = c.now()
				}
				operationEvict = operationState.refilledCallers(scanTime, evictionsNeeded)
				if len(operationEvict) != evictionsNeeded {
					if stale() {
						return Decision{Reason: ReasonStaleAuthorization}, nil
					}
					return Decision{Reason: ReasonCallerCapacity}, nil
				}
			}
			operationEntry = &callerState{key: key, limiter: newLimiter(operationState.perCaller)}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if stale() {
		return Decision{Reason: ReasonStaleAuthorization}, nil
	}
	now := c.now()
	// Recheck capacity under the publication lock: a concurrent reconfigure
	// may have lowered the bound after the initial snapshot.
	if newEntry {
		evictionsNeeded := len(state.callers) - c.maxCallers + 1
		if evictionsNeeded > 0 {
			if len(evict) != evictionsNeeded || now.Before(scanTime) {
				evict = state.refilledCallers(now, evictionsNeeded)
			}
			if len(evict) != evictionsNeeded {
				return Decision{Reason: ReasonCallerCapacity}, nil
			}
		} else {
			evict = nil
		}
	}
	if operationState != nil && newOperationEntry {
		evictionsNeeded := len(operationState.callers) - c.maxCallers + 1
		if evictionsNeeded > 0 {
			if len(operationEvict) != evictionsNeeded || now.Before(scanTime) {
				operationEvict = operationState.refilledCallers(now, evictionsNeeded)
			}
			if len(operationEvict) != evictionsNeeded {
				return Decision{Reason: ReasonCallerCapacity}, nil
			}
		} else {
			operationEvict = nil
		}
	}

	var limiters [5]struct {
		limiter *rate.Limiter
		reason  Reason
	}
	limiters[0] = struct {
		limiter *rate.Limiter
		reason  Reason
	}{c.application, ReasonApplication}
	limiters[1] = struct {
		limiter *rate.Limiter
		reason  Reason
	}{state.shared, ReasonClass}
	limiters[2] = struct {
		limiter *rate.Limiter
		reason  Reason
	}{entry.limiter, ReasonCaller}
	limitCount := 3
	if operationState != nil {
		limiters[3] = struct {
			limiter *rate.Limiter
			reason  Reason
		}{operationState.shared, ReasonOperation}
		limiters[4] = struct {
			limiter *rate.Limiter
			reason  Reason
		}{operationEntry.limiter, ReasonOperationCaller}
		limitCount = 5
	}
	var decision Decision
	for _, limit := range limiters[:limitCount] {
		tokens := limit.limiter.TokensAt(now)
		if tokens < 1 {
			delay := time.Duration(math.Ceil((1 - tokens) / float64(limit.limiter.Limit()) * float64(time.Second)))
			if decision.Reason == "" {
				decision.Reason = limit.reason
			}
			if delay > decision.RetryAfter {
				decision.RetryAfter = delay
			}
		}
	}
	if decision.Reason != "" {
		return decision, nil
	}
	// Class state and the application budget remain locked through all charges.
	for _, limit := range limiters[:limitCount] {
		limit.limiter.AllowN(now, 1)
	}
	for _, caller := range evict {
		delete(state.callers, caller.key)
		state.order.Remove(caller.element)
	}
	if entry.element == nil {
		entry.element = state.order.PushBack(entry)
		state.callers[key] = entry
	} else {
		state.order.MoveToBack(entry.element)
	}
	if operationState != nil {
		for _, caller := range operationEvict {
			delete(operationState.callers, caller.key)
			operationState.order.Remove(caller.element)
		}
		if operationEntry.element == nil {
			operationEntry.element = operationState.order.PushBack(operationEntry)
			operationState.callers[key] = operationEntry
		} else {
			operationState.order.MoveToBack(operationEntry.element)
		}
	}
	return Decision{Allowed: true}, nil
}

func (s *classState) refilledCallers(now time.Time, count int) []*callerState {
	candidates := make([]*callerState, 0, count)
	for element := s.order.Front(); element != nil; element = element.Next() {
		candidate := element.Value.(*callerState)
		if candidate.limiter.TokensAt(now) >= float64(candidate.limiter.Burst()) {
			candidates = append(candidates, candidate)
			if len(candidates) == count {
				return candidates
			}
		}
	}
	return nil
}
