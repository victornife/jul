// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
	"jul/internal/quota"
)

type quotaRuntime struct {
	mu       sync.Mutex
	policies map[string]*quotaPolicyState
}

type quotaPolicyState struct {
	enforce   *quota.Coordinator
	observe   *quota.Coordinator
	decisions *quotaDecisionCounters
	security  [sha256.Size]byte
	mode      string
	active    bool
}

type quotaBinding struct {
	state      *quotaPolicyState
	version    atomic.Uint64
	mode       string
	name       string
	maxCallers int
	policy     quota.Policy
	principals map[quotaPrincipalKey]string
	pools      []quotaPool
}

type quotaPrincipalKey struct {
	method  string
	domain  string
	issuer  string
	subject string
}

type quotaPool struct {
	prefix netip.Prefix
	class  string
}

type quotaCandidate struct {
	runtime  *quotaRuntime
	next     map[string]*quotaPolicyState
	bindings map[string]*quotaBinding
	changes  map[string]bool
	done     bool
}

// The registry holds eight active policies plus one retired identity so one
// replacement can publish before old depleted state becomes safely evictable.
const maxQuotaPolicyStates = 9

func newQuotaRuntime() *quotaRuntime {
	return &quotaRuntime{policies: make(map[string]*quotaPolicyState)}
}

func (r *quotaRuntime) prepare(c *config.Config, gen *Generation) (*quotaCandidate, error) {
	if r == nil && len(c.QuotaPolicies) == 0 {
		return nil, nil
	}
	if r == nil {
		return nil, fmt.Errorf("quota policies are configured but the process quota runtime is unavailable")
	}

	r.mu.Lock()
	if r.policies == nil {
		r.policies = make(map[string]*quotaPolicyState)
	}
	r.pruneRefilledPolicies()
	if len(c.QuotaPolicies) == 0 && len(r.policies) == 0 {
		r.mu.Unlock()
		return nil, nil
	}
	additions := 0
	for name := range c.QuotaPolicies {
		if r.policies[name] == nil {
			additions++
		}
	}
	if len(r.policies)+additions > maxQuotaPolicyStates {
		err := r.failOnPolicyCapacity(additions)
		r.mu.Unlock()
		return nil, err
	}
	candidate := &quotaCandidate{
		runtime:  r,
		next:     make(map[string]*quotaPolicyState, len(c.QuotaPolicies)),
		bindings: make(map[string]*quotaBinding, len(c.QuotaPolicies)),
		changes:  make(map[string]bool, len(c.QuotaPolicies)),
	}
	fail := func(err error) (*quotaCandidate, error) {
		r.mu.Unlock()
		return nil, err
	}

	for name, cfg := range c.QuotaPolicies {
		policy := quotaPolicy(cfg)
		security, err := quotaSecurityFingerprint(c, name, cfg, gen)
		if err != nil {
			return fail(fmt.Errorf("quota_policies.%s security fingerprint: %w", name, err))
		}
		current := r.policies[name]
		changed := current != nil && current.security != security
		var state *quotaPolicyState
		if current == nil {
			enforce, err := quota.NewCoordinator(policy, cfg.MaxCallers)
			if err != nil {
				return fail(fmt.Errorf("quota_policies.%s: %w", name, err))
			}
			observe, err := quota.NewCoordinator(policy, cfg.MaxCallers)
			if err != nil {
				return fail(fmt.Errorf("quota_policies.%s observe state: %w", name, err))
			}
			state = &quotaPolicyState{
				enforce:   enforce,
				observe:   observe,
				decisions: &quotaDecisionCounters{},
			}
		} else {
			if err := current.enforce.CheckReconfigure(policy, cfg.MaxCallers); err != nil {
				return fail(fmt.Errorf("quota_policies.%s enforcement state: %w", name, err))
			}
			if err := current.observe.CheckReconfigure(policy, cfg.MaxCallers); err != nil {
				return fail(fmt.Errorf("quota_policies.%s observation state: %w", name, err))
			}
			if changed && (current.enforce.AuthorizationVersion() == ^uint64(0) ||
				current.observe.AuthorizationVersion() == ^uint64(0)) {
				return fail(fmt.Errorf("quota_policies.%s authorization version exhausted", name))
			}
			state = &quotaPolicyState{
				enforce:   current.enforce,
				observe:   current.observe,
				decisions: current.decisions,
			}
		}
		state.security = security
		state.mode = cfg.Mode
		candidate.next[name] = state
		candidate.changes[name] = changed
		binding, err := newQuotaBinding(name, cfg, policy, state)
		if err != nil {
			return fail(fmt.Errorf("quota_policies.%s: %w", name, err))
		}
		candidate.bindings[name] = binding
	}

	for name, current := range r.policies {
		if _, retained := candidate.next[name]; retained {
			continue
		}
		if !current.active {
			continue
		}
		if current.enforce.AuthorizationVersion() == ^uint64(0) ||
			current.observe.AuthorizationVersion() == ^uint64(0) {
			return fail(fmt.Errorf("removed quota policy %q authorization version exhausted", name))
		}
	}
	return candidate, nil
}

func (r *quotaRuntime) pruneRefilledPolicies() {
	for name, state := range r.policies {
		if state.active {
			continue
		}
		enforceFull, _ := state.enforce.RefillStatus()
		if !enforceFull {
			continue
		}
		observeFull, _ := state.observe.RefillStatus()
		if observeFull {
			delete(r.policies, name)
		}
	}
}

func (r *quotaRuntime) failOnPolicyCapacity(additions int) error {
	var (
		blocker string
		wait    time.Duration
	)
	names := make([]string, 0, len(r.policies))
	for name := range r.policies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		state := r.policies[name]
		if state.active {
			continue
		}
		_, enforceWait := state.enforce.RefillStatus()
		_, observeWait := state.observe.RefillStatus()
		candidateWait := enforceWait
		if observeWait > candidateWait {
			candidateWait = observeWait
		}
		if blocker == "" || candidateWait < wait {
			blocker, wait = name, candidateWait
		}
	}
	if blocker == "" {
		return fmt.Errorf("quota policy registry is bounded at %d remembered policies; cannot reserve %d additional identities while all entries are active", maxQuotaPolicyStates, additions)
	}
	return fmt.Errorf("quota policy registry is bounded at %d remembered policies; retired policy %q is still depleted and may be safely evicted in approximately %s", maxQuotaPolicyStates, blocker, wait.Round(time.Millisecond))
}

func (c *quotaCandidate) Binding(name string) *quotaBinding {
	if c == nil {
		return nil
	}
	return c.bindings[name]
}

// Commit publishes the candidate while the handler factory's build lock is
// held. Policy validation and authorization-version exhaustion are checked in
// prepare, so this transaction cannot fail.
func (c *quotaCandidate) Commit() {
	if c == nil || c.done {
		return
	}
	for name, state := range c.next {
		if old := c.runtime.policies[name]; old != nil {
			binding := c.bindings[name]
			if _, err := old.enforce.Reconfigure(binding.policy, binding.maxCallers, c.changes[name]); err != nil {
				panic(fmt.Sprintf("prepared quota enforcement update failed: %v", err))
			}
			if _, err := old.observe.Reconfigure(binding.policy, binding.maxCallers, c.changes[name]); err != nil {
				panic(fmt.Sprintf("prepared quota observation update failed: %v", err))
			}
			state.enforce = old.enforce
			state.observe = old.observe
		}
		state.active = true
		c.runtime.policies[name] = state
		active := state.enforce
		if state.mode == "observe" {
			active = state.observe
		}
		c.bindings[name].version.Store(active.AuthorizationVersion())
	}
	for name, state := range c.runtime.policies {
		if _, retained := c.next[name]; retained {
			continue
		}
		if !state.active {
			continue
		}
		if _, err := state.enforce.AdvanceAuthorization(); err != nil {
			panic(fmt.Sprintf("prepared removed quota policy fence failed: %v", err))
		}
		if _, err := state.observe.AdvanceAuthorization(); err != nil {
			panic(fmt.Sprintf("prepared removed quota policy fence failed: %v", err))
		}
		state.active = false
	}
	c.done = true
	c.runtime.mu.Unlock()
}

func (c *quotaCandidate) Abort() {
	if c == nil || c.done {
		return
	}
	c.done = true
	c.runtime.mu.Unlock()
}

func quotaPolicy(cfg config.QuotaPolicyConfig) quota.Policy {
	policy := quota.Policy{
		Application: quota.Limit{Rate: cfg.Rate, Burst: cfg.Burst},
		Classes:     make(map[string]quota.Class, len(cfg.Classes)),
		Operations:  make(map[string]quota.Operation, len(cfg.Operations)),
	}
	for name, class := range cfg.Classes {
		policy.Classes[name] = quota.Class{
			Shared:    quota.Limit{Rate: class.Shared.Rate, Burst: class.Shared.Burst},
			PerCaller: quota.Limit{Rate: class.PerCaller.Rate, Burst: class.PerCaller.Burst},
		}
	}
	for name, operation := range cfg.Operations {
		policy.Operations[name] = quota.Operation{
			Shared:    quota.Limit{Rate: operation.Shared.Rate, Burst: operation.Shared.Burst},
			PerCaller: quota.Limit{Rate: operation.PerCaller.Rate, Burst: operation.PerCaller.Burst},
		}
	}
	return policy
}

func newQuotaBinding(name string, cfg config.QuotaPolicyConfig, policy quota.Policy, state *quotaPolicyState) (*quotaBinding, error) {
	binding := &quotaBinding{
		state:      state,
		mode:       cfg.Mode,
		name:       name,
		maxCallers: cfg.MaxCallers,
		policy:     policy,
		principals: make(map[quotaPrincipalKey]string),
	}
	for className, class := range cfg.Classes {
		for _, principal := range class.Principals {
			key := quotaPrincipalKey{
				method:  principal.Method,
				domain:  principal.Domain,
				issuer:  principal.Issuer,
				subject: principal.Subject,
			}
			binding.principals[key] = className
		}
	}
	for _, pool := range cfg.Pools {
		for _, raw := range pool.CIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return nil, err
			}
			if prefix.Addr().Is4In6() {
				if prefix.Bits() < 96 {
					return nil, fmt.Errorf("IPv4-mapped prefix %q is broader than /96", raw)
				}
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			binding.pools = append(binding.pools, quotaPool{prefix: prefix.Masked(), class: pool.Class})
		}
	}
	sort.Slice(binding.pools, func(i, j int) bool {
		if binding.pools[i].prefix.Bits() != binding.pools[j].prefix.Bits() {
			return binding.pools[i].prefix.Bits() > binding.pools[j].prefix.Bits()
		}
		return binding.pools[i].prefix.String() < binding.pools[j].prefix.String()
	})
	return binding, nil
}

type quotaDecisionCounters struct {
	enforce [7]atomic.Uint64
	observe [7]atomic.Uint64
}

var quotaDecisionReasons = [...]string{
	string(quota.ReasonApplication),
	string(quota.ReasonClass),
	string(quota.ReasonCaller),
	string(quota.ReasonCallerCapacity),
	string(quota.ReasonOperation),
	string(quota.ReasonOperationCaller),
	string(quota.ReasonStaleAuthorization),
}

func (c *quotaDecisionCounters) record(mode, reason string) {
	if c == nil {
		return
	}
	for i, candidate := range quotaDecisionReasons {
		if reason != candidate {
			continue
		}
		switch mode {
		case "enforce":
			c.enforce[i].Add(1)
		case "observe":
			c.observe[i].Add(1)
		}
		return
	}
}

func (r *quotaRuntime) decisionSnapshot() map[string]map[string]float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]map[string]float64, len(r.policies))
	for name, policy := range r.policies {
		if !policy.active {
			continue
		}
		decisions := make(map[string]float64)
		for i, reason := range quotaDecisionReasons {
			if count := policy.decisions.enforce[i].Load(); count > 0 {
				decisions["enforce:"+reason] = float64(count)
			}
			if count := policy.decisions.observe[i].Load(); count > 0 {
				decisions["observe:"+reason] = float64(count)
			}
		}
		if len(decisions) != 0 {
			out[name] = decisions
		}
	}
	return out
}

type quotaSecurityRoute struct {
	Listen            string
	ServerNames       []string
	Match             config.MatchConfig
	Operation         string
	Auth              *config.AuthConfig
	ClientAddress     *config.ClientAddressConfig
	TLS               *config.TLSConfig
	RequireClientCert bool
	BasicDigest       [sha256.Size]byte
}

type quotaSecurityPool struct {
	Class string
	CIDRs []string
}

type quotaSecurityPolicy struct {
	Mode    string
	Classes map[string][]config.QuotaPrincipalMatch
	Pools   map[string]quotaSecurityPool
	Routes  []quotaSecurityRoute
}

func quotaSecurityFingerprint(c *config.Config, policyName string, cfg config.QuotaPolicyConfig, gen *Generation) ([sha256.Size]byte, error) {
	projection := quotaSecurityPolicy{
		Mode:    cfg.Mode,
		Classes: make(map[string][]config.QuotaPrincipalMatch, len(cfg.Classes)),
		Pools:   make(map[string]quotaSecurityPool, len(cfg.Pools)),
	}
	for name, class := range cfg.Classes {
		projection.Classes[name] = append([]config.QuotaPrincipalMatch(nil), class.Principals...)
		sort.Slice(projection.Classes[name], func(i, j int) bool {
			a, b := projection.Classes[name][i], projection.Classes[name][j]
			return a.Method+"\x00"+a.Domain+"\x00"+a.Issuer+"\x00"+a.Subject <
				b.Method+"\x00"+b.Domain+"\x00"+b.Issuer+"\x00"+b.Subject
		})
	}
	for name, pool := range cfg.Pools {
		canonical := quotaSecurityPool{Class: pool.Class, CIDRs: make([]string, 0, len(pool.CIDRs))}
		for _, raw := range pool.CIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil {
				return [sha256.Size]byte{}, fmt.Errorf("invalid pool prefix %q: %w", raw, err)
			}
			if prefix.Addr().Is4In6() {
				if prefix.Bits() < 96 {
					return [sha256.Size]byte{}, fmt.Errorf("IPv4-mapped prefix %q is broader than /96", raw)
				}
				prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
			}
			canonical.CIDRs = append(canonical.CIDRs, prefix.Masked().String())
		}
		sort.Strings(canonical.CIDRs)
		projection.Pools[name] = canonical
	}
	for i, server := range c.Servers {
		for j, location := range server.Locations {
			if location.QuotaPolicy != policyName {
				continue
			}
			var basicDigest [sha256.Size]byte
			if raw := gen.basicByLoc[[2]int{i, j}]; len(raw) != 0 {
				basicDigest = sha256.Sum256(raw)
			}
			serverNames := append([]string(nil), server.ServerNames...)
			sort.Strings(serverNames)
			projection.Routes = append(projection.Routes, quotaSecurityRoute{
				Listen:            server.Listen,
				ServerNames:       serverNames,
				Match:             location.Match,
				Operation:         location.QuotaOperation,
				Auth:              location.Auth,
				ClientAddress:     server.ClientAddress,
				TLS:               server.TLS,
				RequireClientCert: location.RequireClientCert,
				BasicDigest:       basicDigest,
			})
		}
	}
	sort.Slice(projection.Routes, func(i, j int) bool {
		a, _ := json.Marshal(projection.Routes[i])
		b, _ := json.Marshal(projection.Routes[j])
		return string(a) < string(b)
	})
	raw, err := json.Marshal(projection)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(raw), nil
}
