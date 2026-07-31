// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"context"
	"net"
	"net/http"
	"time"
)

// DialContext returns a dial function that enforces the guard's policy on top of
// base. When the guard is nil (disabled policy) the base dialer's DialContext is
// returned unchanged, so a disabled policy adds no behaviour and no overhead.
func (g *Guard) DialContext(base *net.Dialer) DialFunc {
	if base == nil {
		base = defaultDialer()
	}
	return g.DialContextWith(base.DialContext)
}

// DialContextWith returns a guarded dial that uses baseDial for the actual
// connection instead of a *net.Dialer. It lets a caller compose an additional
// dial-time guard beneath the policy — for example the WASM plugin's SSRF check —
// while the policy still evaluates the requested host and every resolved IP.
// When the guard is nil, baseDial is returned unchanged.
func (g *Guard) DialContextWith(baseDial DialFunc) DialFunc {
	if !g.Enabled() {
		return baseDial
	}
	subsystem := g.subsystem
	policy := g.policy
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, &BlockError{Subsystem: subsystem, Host: addr, Reason: ReasonInvalidAddress}
		}
		return policy.dial(ctx, subsystem, baseDial, network, host, port)
	}
}

// Transport returns an *http.Transport whose DialContext enforces the guard's
// policy and whose Proxy is nil, so a guarded client ignores HTTP_PROXY,
// HTTPS_PROXY, and NO_PROXY and a proxy address can never hide the real target
// from the allow-list. When base is nil a clone of http.DefaultTransport is used
// so idle pooling and TLS defaults are preserved; when the guard is nil base is
// returned with only Proxy pinned to nil.
func (g *Guard) Transport(base *http.Transport) *http.Transport {
	if base == nil {
		base = http.DefaultTransport.(*http.Transport).Clone()
	}
	base.Proxy = nil
	if !g.Enabled() {
		return base
	}
	base.DialContext = g.DialContext(defaultDialer())
	return base
}

// Client returns an *http.Client with the given timeout whose transport enforces
// the guard's policy at dial time and whose RoundTripper additionally validates
// the request URL before dispatch (and so re-checks every redirect target). A
// nil guard yields an ordinary client with proxying disabled.
func (g *Guard) Client(timeout time.Duration) *http.Client {
	transport := g.Transport(nil)
	if !g.Enabled() {
		return &http.Client{Timeout: timeout, Transport: transport}
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: &guardedRoundTripper{subsystem: g.subsystem, policy: g.policy, next: transport},
	}
}

// guardedRoundTripper validates req.URL.Hostname() against the policy before
// dispatch. Because net/http calls RoundTrip again for each redirect, this
// re-checks redirect targets at the HTTP layer in addition to the dial-time
// enforcement in the wrapped transport.
type guardedRoundTripper struct {
	subsystem string
	policy    *Policy
	next      http.RoundTripper
}

func (rt *guardedRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if be := rt.policy.staticBlock(rt.subsystem, req.URL.Hostname()); be != nil {
		rt.policy.emit(Decision{
			Subsystem: rt.subsystem,
			Result:    ResultBlock,
			Reason:    be.Reason,
			Host:      be.Host,
			IP:        be.IP,
		})
		return nil, be
	}
	return rt.next.RoundTrip(req)
}
