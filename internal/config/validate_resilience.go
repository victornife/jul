// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"time"

	"jul/internal/resilience"
)

// validateResilience checks one [upstreams.resilience] block. Range checks are
// delegated to resilience.Resolve so the accepted bounds live with the type
// that enforces them and cannot drift from what the data path actually accepts.
//
// grace is the global shutdown timeout, which is also the handler-generation
// retirement grace. pending_timeout must not exceed it: a request parked longer
// than the grace outlives the transport it was queued for.
func validateResilience(r *ResilienceConfig, where string, grace time.Duration) []error {
	if r == nil {
		return nil
	}
	var errs []error
	if _, err := resilience.Resolve(r.Options()); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", where, err))
	}
	if pt := r.PendingTimeout.Std(); pt > 0 && grace > 0 && pt > grace {
		errs = append(errs, fmt.Errorf("%s: pending_timeout (%s) must not exceed global.shutdown_timeout (%s), which bounds handler-generation retirement: a request parked longer than the grace outlives the transport it is queued for", where, pt, grace))
	}
	return errs
}
