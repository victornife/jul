// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package waf implements the web application firewall (WAF) that inspects HTTP
// requests (and, optionally, responses) against ModSecurity-compatible SecLang
// rules — including the embedded OWASP Core Rule Set — and either blocks or
// merely records the requests that trip a rule. It is built on the pure-Go
// Coraza engine and applied as a per-location middleware composed just inside
// authentication and outside the location's action.
//
// The feature is compiled only into builds with the "waf" build tag. A lean
// build (without the tag) provides a stub whose Firewall is a no-op and whose
// Check rejects any configuration that enables the WAF, so such a build fails
// fast with a clear, actionable message instead of silently ignoring the
// configuration. This mirrors the stream, http3, and wasmplugins seams.
package waf

import (
	"errors"
	"log/slog"

	"jul/internal/config"
)

// Hooks carries optional observation callbacks supplied by the composition root
// so this package stays decoupled from the metrics implementation. Each may be
// nil.
type Hooks struct {
	// OnEvent is invoked once per matched rule that drives a decision, labeled
	// by action ("block" or "detect") and the matched rule's ID (as a string).
	OnEvent func(action, ruleID string)
}

// Options configures a Firewall.
type Options struct {
	Logger *slog.Logger
	Hooks  Hooks
}

// Enabled reports whether any WAF policy is turned on in the configuration —
// the global [waf] block or any per-location override. It is used by Check and
// by the console Status panel.
func Enabled(c *config.Config) bool {
	if c.WAF.Enabled {
		return true
	}
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			if w := c.Servers[i].Locations[j].WAF; w != nil && w.Enabled {
				return true
			}
		}
	}
	return false
}

// Check reports whether the configuration can be served by this binary with
// respect to the WAF. WAF support is a build-time choice (the "waf" tag); a
// binary without it cannot enforce rules. When such a binary is given a
// configuration that enables the WAF anywhere, this returns an error so startup
// fails fast with a clear message instead of silently dropping the protection.
// It is a no-op (returns nil) in WAF-enabled builds or when no WAF is configured.
func Check(c *config.Config) error {
	if Compiled {
		return nil
	}
	if Enabled(c) {
		return errors.New("[waf] (web application firewall) is configured but this binary was built without WAF support; rebuild with -tags waf")
	}
	return nil
}
