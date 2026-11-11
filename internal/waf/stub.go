// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !waf

package waf

import (
	"context"
	"errors"
	"sync/atomic"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/middleware"
)

// Compiled reports whether this binary includes WAF support. It is false in the
// default build, which excludes the Coraza engine and the embedded rule set
// entirely.
const Compiled = false

// Firewall is the no-WAF stub. It holds no engine and inspects nothing.
type Firewall struct {
	opts   Options
	closed *atomic.Bool
}

// New rejects construction in a build without the "waf" tag. The startup-time
// safety net is Check; this guards any direct construction path.
func New(_ context.Context, _ config.WAFConfig, _ Options) (*Firewall, error) {
	return nil, errors.New("waf requires a build with -tags waf")
}

// Middleware is a no-op in the stub: it returns nil so the caller skips WAF
// wrapping entirely.
func (f *Firewall) Middleware() middleware.Middleware { return nil }

// RouteMiddleware is a no-op in the stub.
func (f *Firewall) RouteMiddleware(_ RouteAttribution, _ func() uint64) middleware.Middleware {
	return nil
}

// Close is a no-op for the stub.
func (f *Firewall) Close() error {
	if f != nil && f.closed != nil {
		f.closed.Store(true)
	}
	return nil
}

// IsClosed reports whether Close has been called on f.
func (f *Firewall) IsClosed() bool {
	if f == nil || f.closed == nil {
		return true
	}
	return f.closed.Load()
}

// Info is empty in the stub: there is no compiled policy to describe.
func (f *Firewall) Info() PolicyInfo { return PolicyInfo{} }

// InspectionFacts is empty in the stub: a lean build constructs no Firewall.
func (f *Firewall) InspectionFacts() inspection.WAFFacts { return inspection.WAFFacts{} }

// SourceChanged reports whether two stub Firewalls differ.
func (f *Firewall) SourceChanged(other *Firewall) bool {
	if f == nil || other == nil {
		return f != other
	}
	return false
}

// SourceDigest returns "" in the no-WAF stub.
func (f *Firewall) SourceDigest() string { return "" }

// RawPolicyDigest returns "" in the no-WAF stub.
func (f *Firewall) RawPolicyDigest() string { return "" }

// ExternalAssets returns nil in the no-WAF stub.
func (f *Firewall) ExternalAssets() []ExternalAssetRecord { return nil }

// EmbeddedCRSVersion is "" in a build without the "waf" tag: no rule set is
// embedded.
func EmbeddedCRSVersion() string { return "" }

// EngineVersion is "" in a build without the "waf" tag: no engine is linked.
func EngineVersion() string { return "" }
