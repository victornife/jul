// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
)

// Manager owns the process-lifetime pointer to the currently published egress
// generation. Candidate generations are compiled before Publish and remain
// unreachable through Current until Publish performs the single atomic swap.
//
// A generation owns its HTTP connection pools. That is the security boundary
// #94 needs: after Publish, newly admitted work resolves the new Generation and
// therefore cannot reuse an HTTP/1.1 keep-alive or HTTP/2 connection created by
// the previous policy. Old work may continue using the Generation it already
// captured; CloseIdleConnections is used when that generation retires.
type Manager struct {
	opts    []Option
	nextID  atomic.Uint64
	current atomic.Pointer[Generation]
}

// NewManager compiles and publishes the startup egress generation.
func NewManager(cfg config.EgressConfig, opts ...Option) (*Manager, error) {
	m := &Manager{opts: append([]Option(nil), opts...)}
	policy, err := New(cfg, m.opts...)
	if err != nil {
		return nil, err
	}
	m.current.Store(newGeneration(m.nextID.Add(1), policy))
	return m, nil
}

// Current returns the immutable generation currently used by newly admitted
// process-lifetime consumers such as ACME and OCSP.
func (m *Manager) Current() *Generation {
	if m == nil {
		return nil
	}
	return m.current.Load()
}

// Prepare compiles cfg without mutating live state. changed is false when cfg is
// semantically identical to the published policy; in that case the current
// immutable generation is returned so discovery workers and pools do not churn
// on unrelated reloads.
func (m *Manager) Prepare(cfg config.EgressConfig) (generation *Generation, changed bool, err error) {
	if m == nil {
		return nil, false, errors.New("egress: nil generation manager")
	}
	policy, err := New(cfg, m.opts...)
	if err != nil {
		return nil, false, err
	}
	sig := policySignature(policy)
	if cur := m.current.Load(); cur != nil && cur.signature == sig {
		return cur, false, nil
	}
	return newGeneration(m.nextID.Add(1), policy), true, nil
}

// Publish atomically makes generation authoritative for newly admitted work and
// returns the generation it replaced. It performs no validation, dialing,
// teardown, filesystem access, or other fallible work.
func (m *Manager) Publish(generation *Generation) *Generation {
	if m == nil || generation == nil {
		return nil
	}
	return m.current.Swap(generation)
}

// Client returns a stable process-lifetime HTTP client that dispatches each
// RoundTrip through the egress generation current when that HTTP exchange is
// admitted. It is used for long-lived PKI owners whose identity is intentionally
// process-lifetime even while their outbound policy changes.
//
// Redirects are intentionally re-evaluated against the then-current generation.
// If Publish lands between redirect hops, the later hop can become stricter; it
// can never keep using an old-policy pool. This is conservative for an operation
// that began before Publish and preserves the RoundTripper contract by never
// mutating the caller's *http.Request.
func (m *Manager) Client(subsystem string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &dynamicRoundTripper{manager: m, subsystem: subsystem},
	}
}

// Generation is one immutable egress policy generation plus the HTTP pools it
// owns for process-lifetime consumers. The policy itself never mutates.
type Generation struct {
	id        uint64
	policy    *Policy
	signature string

	mu   sync.Mutex
	http map[string]*generationHTTP
}

type generationHTTP struct {
	transport *http.Transport
	rt        http.RoundTripper
}

func newGeneration(id uint64, policy *Policy) *Generation {
	return &Generation{
		id:        id,
		policy:    policy,
		signature: policySignature(policy),
		http:      make(map[string]*generationHTTP),
	}
}

// ID is a monotonic, process-local generation identity suitable for bounded
// diagnostics. It carries no allow-list or destination information.
func (g *Generation) ID() uint64 {
	if g == nil {
		return 0
	}
	return g.id
}

// Enabled reports whether this generation enforces an allow-list.
func (g *Generation) Enabled() bool {
	return g != nil && g.policy != nil
}

// For returns this generation's immutable subsystem-scoped policy guard.
func (g *Generation) For(subsystem string) *Guard {
	if g == nil {
		return nil
	}
	return g.policy.For(subsystem)
}

// CloseIdleConnections retires all idle HTTP/1.1 and HTTP/2 connections owned
// by this generation. Active requests are deliberately not cancelled; they were
// admitted under this generation and may complete under its policy.
func (g *Generation) CloseIdleConnections() {
	if g == nil {
		return
	}
	g.mu.Lock()
	transports := make([]*http.Transport, 0, len(g.http))
	for _, owned := range g.http {
		transports = append(transports, owned.transport)
	}
	g.mu.Unlock()
	for _, transport := range transports {
		transport.CloseIdleConnections()
	}
}

// Close lets a Generation participate in the repository's existing retirement
// machinery. Closing a generation is intentionally non-destructive to active
// exchanges; it only retires idle pools.
func (g *Generation) Close() error {
	g.CloseIdleConnections()
	return nil
}

func (g *Generation) roundTripper(subsystem string) http.RoundTripper {
	g.mu.Lock()
	defer g.mu.Unlock()
	if owned := g.http[subsystem]; owned != nil {
		return owned.rt
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	var rt http.RoundTripper = transport
	if g.policy != nil {
		guard := g.policy.For(subsystem)
		transport = guard.Transport(transport)
		rt = &guardedRoundTripper{subsystem: subsystem, policy: g.policy, next: transport}
	}
	// A disabled generation deliberately keeps the cloned default transport's
	// ProxyFromEnvironment behavior. An enabled generation uses Guard.Transport,
	// which pins Proxy=nil so a proxy cannot conceal the real destination.
	g.http[subsystem] = &generationHTTP{transport: transport, rt: rt}
	return rt
}

type dynamicRoundTripper struct {
	manager   *Manager
	subsystem string
}

func (rt *dynamicRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, errors.New("egress: nil HTTP request")
	}
	generation := rt.manager.Current()
	if generation == nil {
		return nil, errors.New("egress: no published generation")
	}
	return generation.roundTripper(rt.subsystem).RoundTrip(req)
}

func (rt *dynamicRoundTripper) CloseIdleConnections() {
	if rt == nil || rt.manager == nil {
		return
	}
	if generation := rt.manager.Current(); generation != nil {
		generation.CloseIdleConnections()
	}
}

func policySignature(policy *Policy) string {
	if policy == nil {
		return "disabled"
	}
	parts := make([]string, 0, len(policy.hosts)+len(policy.cidrs))
	for _, rule := range policy.hosts {
		prefix := "host:"
		if rule.suffix {
			prefix = "suffix:"
		}
		parts = append(parts, prefix+rule.value)
	}
	for _, cidr := range policy.cidrs {
		parts = append(parts, "cidr:"+cidr.String())
	}
	sort.Strings(parts)
	return "enabled\x00" + strings.Join(parts, "\x00")
}
