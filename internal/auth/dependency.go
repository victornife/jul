// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"net/http"
	"time"

	"jul/internal/upstream"
)

// dependency is an outbound authentication dependency — the forward-auth
// service or the JWKS endpoint — reached through the shared upstream
// primitives rather than through a bare http.Client.
//
// Both dependencies are on the request path of every authenticated request, so
// an unbounded number of subrequests to a struggling auth service is the same
// amplification the rest of this programme bounds for backends. They get the
// same admission, balancing, passive health and retry, with one rule that
// overrides all of them: **every failure denies**. A resilience control may
// never become an authentication bypass.
type dependency struct {
	// pool is nil when the URL names no configured upstream and resolution was
	// not requested, in which case the client is used directly and behaviour is
	// exactly what it was before this type existed.
	pool   *upstream.Pool
	client *http.Client
}

// do sends req through the dependency.
//
// The returned error is the fail-closed signal: admission rejection, a spent
// retry budget, an exhausted pool and — when the breaker lands — an open
// circuit all arrive here as errors, and every caller denies on error. That is
// why the fail-closed property is a consequence of the shape rather than a rule
// each call site has to remember.
func (d *dependency) do(req *http.Request) (*http.Response, error) {
	if d.pool == nil {
		return d.client.Do(req)
	}

	// Admission first, as everywhere else: the subrequest is upstream work and
	// is counted as such. The slot covers the exchange; both callers read the
	// bounded response body immediately after this returns.
	release, err := d.pool.Admission().Admit(req.Context(), nil)
	if err != nil {
		return nil, err
	}
	defer release()

	var resp *http.Response
	_, err = d.pool.Do(req.Context(),
		d.pool.RetryRequestFor(upstream.RetryOverride{}, upstream.RetrySafeMethod(req.Method)),
		func(ctx context.Context, b upstream.Attempt, n int) upstream.AttemptResult {
			out := req.Clone(ctx)
			out.URL.Host = b.Address
			r, aerr := d.client.Do(out)
			if aerr != nil {
				d.pool.MarkFailure(b)
				return upstream.AttemptResult{Err: aerr}
			}
			// A received response ends the attempt whatever its status. Only
			// the transport is in scope here: a 500 from an auth service is an
			// answer, and treating it as a backend failure would let one
			// misbehaving service take a healthy replica out of rotation.
			d.pool.MarkSuccess(b)
			resp = r
			return upstream.AttemptResult{}
		})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// DefaultDependencyTimeout bounds an auth dependency call when no timeout is
// configured. It is the value both clients hardcoded before it was
// configurable, so an unset field changes nothing.
const DefaultDependencyTimeout = 10 * time.Second
