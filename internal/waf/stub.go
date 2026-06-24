//go:build !waf

package waf

import (
	"errors"

	"jul/internal/config"
	"jul/internal/middleware"
)

// Compiled reports whether this binary includes WAF support. It is false in the
// default build, which excludes the Coraza engine and the embedded rule set
// entirely.
const Compiled = false

// Firewall is the no-WAF stub. It holds no engine and inspects nothing.
type Firewall struct{}

// New rejects construction in a build without the "waf" tag. The startup-time
// safety net is Check; this guards any direct construction path.
func New(_ config.WAFConfig, _ Options) (*Firewall, error) {
	return nil, errors.New("waf requires a build with -tags waf")
}

// Middleware is a no-op in the stub: it returns nil so the caller skips WAF
// wrapping entirely.
func (f *Firewall) Middleware() middleware.Middleware { return nil }

// Close is a no-op for the stub.
func (f *Firewall) Close() error { return nil }
